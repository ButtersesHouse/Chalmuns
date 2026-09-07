// Package review captures and normalizes the output of a code-review skill
// or tool the user designated for pattern-learner to keep eyes on, so that
// output can feed the same signal → grounding → classify → triage → approval
// spine that PR review comments feed.
//
// Why a package of its own. The PR path has one input shape (the GitHub API)
// and one parser (internal/pipeline.ExtractLean). A designated reviewer can be
// Claude Code's own /code-review skill, a SARIF-emitting analyzer, ESLint,
// semgrep, or a person's markdown write-up, so the input shapes are plural and
// keep growing. Normalizing them here — deterministically, in the binary —
// keeps that variety out of the pipeline stages and out of the model's hands:
// per the SKILL's tooling policy, parsing is never something a run reimplements
// in an ad-hoc script.
//
// The normalized Artifact is also the grounding corpus. verify-grounding
// checks a signal's verbatim snippet against the stored artifact exactly as it
// checks a PR signal against pr-N.json, so an artifact keeps the source text
// byte-for-byte in RawText alongside the parsed findings. A rule can therefore
// never be attributed to a review that does not contain the words it cites.
package review

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Recognised artifact formats. FormatAuto asks Capture to sniff the shape,
// which is the default: a watcher's output format is a property of the tool,
// not something the user should have to spell out, and a tool that changes
// its output shape between versions should not silently produce zero findings.
const (
	FormatAuto     = "auto"
	FormatFindings = "findings" // Claude Code /code-review ReportFindings JSON
	FormatSARIF    = "sarif"    // SARIF 2.1.0 — the static-analysis interchange format
	FormatESLint   = "eslint"   // `eslint --format json`
	FormatSemgrep  = "semgrep"  // `semgrep --json`
	FormatMarkdown = "markdown" // a prose review write-up, or any unrecognised text
)

// Formats lists every value Format may take, for flag validation and error
// messages. FormatAuto is included: it is a legal request, not a parsed shape.
var Formats = []string{FormatAuto, FormatFindings, FormatSARIF, FormatESLint, FormatSemgrep, FormatMarkdown}

// ValidFormat reports whether f names a format the capture path understands.
func ValidFormat(f string) bool {
	for _, known := range Formats {
		if f == known {
			return true
		}
	}
	return false
}

// maxArtifactBytes caps one captured review. The cap exists because RawText is
// the grounding corpus: truncating it would let a signal quoting the dropped
// tail fail grounding for a reason no message could explain. Refusing an
// oversized capture outright is the honest failure — the user can point the
// capture at a narrower review instead.
const maxArtifactBytes = 4 << 20 // 4 MiB

// Artifact is the normalized on-disk form of one captured code review. It is
// written to <cache-dir>/review-<ReviewID>.json.
type Artifact struct {
	// ReviewID is content-addressed over what the reviewer said — see
	// artifactID. The same review captured twice yields the same ID and
	// therefore the same file, which is what makes capture idempotent. That
	// matters because the confidence model counts how many reviews a
	// convention recurs across: a hook firing twice for one review, or a
	// linter re-run over a finding nobody has fixed, must not read as two
	// independent reviews agreeing.
	ReviewID string `json:"review_id"`
	// Source is the designated watcher's name ("code-review", "semgrep"), and
	// becomes the signal's reviewer, exactly as a human's login does on the PR
	// path.
	Source string `json:"source"`
	// Format is the resolved shape, never "auto" — what the parse actually used.
	Format     string `json:"format"`
	CapturedAt string `json:"captured_at"`
	// Label is optional free text naming what was reviewed ("PR #42",
	// "pre-merge sweep"), carried into the approval display.
	Label string `json:"label,omitempty"`
	// Findings is the parsed view. Empty for a format that carries no
	// structure (a prose review) — RawText still holds the review in full, and
	// the extraction subagent reads that instead.
	Findings []Finding `json:"findings"`
	// RawText is the captured output verbatim. It is the grounding corpus and
	// must never be rewritten, reflowed, or truncated.
	RawText string `json:"raw_text"`
}

// Finding is one normalized review remark. Fields absent from a given input
// shape stay empty rather than being invented: an unset File is "the tool did
// not say where", which the extraction step must be able to tell from "the
// tool said the repository root".
type Finding struct {
	Index    int    `json:"index"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Category string `json:"category,omitempty"`
	Severity string `json:"severity,omitempty"`
	// Title is the finding's short handle — a rule ID, a check name, a
	// one-line summary. It is what a recurring finding is recognised by.
	Title string `json:"title,omitempty"`
	// Body is the reviewer's prose. This is the text a signal quotes, so it is
	// stored as the tool wrote it.
	Body string `json:"body"`
	// Evidence is a second piece of the reviewer's prose that supports Body —
	// a /code-review failure scenario, a SARIF rule description. It is a field
	// of its own rather than being appended to Body because grounding checks
	// containment: concatenating two separate statements creates a sentence
	// spanning the join that the reviewer never wrote, and a quote of it would
	// verify as verbatim.
	Evidence string `json:"evidence,omitempty"`
	// CodeBefore and CodeAfter are the flagged code and the suggested
	// replacement when the tool provides them. They become a rule's
	// dont_example and do_example, which is the highest-fidelity example
	// source there is — the same role diff_hunk plays on the PR path.
	CodeBefore string `json:"code_before,omitempty"`
	CodeAfter  string `json:"code_after,omitempty"`
	// Verdict carries a tool's own confidence marker (SARIF level, a
	// /code-review CONFIRMED/PLAUSIBLE verdict) unchanged.
	Verdict string `json:"verdict,omitempty"`
}

// Input is one capture request.
type Input struct {
	Data   []byte
	Source string
	Format string
	Label  string
	Now    time.Time
}

// Capture normalizes one review output into an Artifact. It does not write
// anything; see WriteArtifact.
func Capture(in Input) (Artifact, error) {
	if len(bytes.TrimSpace(in.Data)) == 0 {
		return Artifact{}, fmt.Errorf("captured review is empty")
	}
	if len(in.Data) > maxArtifactBytes {
		return Artifact{}, fmt.Errorf("captured review is %d bytes, over the %d-byte cap; "+
			"the whole text is kept as the grounding corpus and cannot be truncated — capture a narrower review",
			len(in.Data), maxArtifactBytes)
	}
	source := strings.TrimSpace(in.Source)
	if source == "" {
		return Artifact{}, fmt.Errorf("--source required: a captured review is attributed to the tool that produced it")
	}
	format := in.Format
	sniffed := format == "" || format == FormatAuto
	if sniffed {
		format = Detect(in.Data)
	}
	if !ValidFormat(format) || format == FormatAuto {
		return Artifact{}, fmt.Errorf("unknown --format %q; accepts %s", in.Format, strings.Join(Formats, ", "))
	}

	findings, err := parse(format, in.Data)
	if err != nil {
		return Artifact{}, err
	}
	// A parse that yields findings carrying none of the reviewer's words means
	// the shape was guessed wrong — another tool's report that happens to use
	// the same container key. Recording it would be the worst outcome
	// available: capture reports N findings and succeeds, while the review's
	// actual text reaches nobody, because the lean view sends RawText only
	// when there are no findings at all. A sniffed format falls back to
	// treating the input as prose, which always carries the text through; an
	// explicitly-requested one is an error, because the caller asserted a
	// shape the data does not have.
	if !hasContent(findings) {
		if !sniffed {
			return Artifact{}, fmt.Errorf(
				"parsed %d %s finding(s) but none carry any text; the input does not have the %s shape",
				len(findings), format, format)
		}
		format = FormatMarkdown
		if findings, err = parse(format, in.Data); err != nil {
			return Artifact{}, err
		}
	}
	for i := range findings {
		findings[i].Index = i
	}

	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	return Artifact{
		ReviewID:   artifactID(source, findings, in.Data),
		Source:     source,
		Format:     format,
		CapturedAt: now.UTC().Format(time.RFC3339Nano),
		Label:      strings.TrimSpace(in.Label),
		Findings:   findings,
		RawText:    string(in.Data),
	}, nil
}

// artifactID derives an artifact's content-addressed ID from what the reviewer
// actually said, not from the bytes it said it in.
//
// This distinction is the difference between counting evidence and inventing
// it. The confidence model treats each artifact a rule cites as one
// independent review, so two artifacts saying the same thing read as two
// reviewers agreeing. Hashing raw bytes made that trivially false: re-running
// a linter over a finding nobody has fixed yet emits a byte-different report
// every time an unrelated edit shifts a line number, so an agent iterating on
// a fix and re-linting five times manufactured a five-signal "established"
// rule out of one unaddressed finding. Hashing the findings — without the line
// numbers, which are exactly what shifts — makes a repeat of the same review
// the same artifact, and re-capture the no-op it claims to be.
//
// Source stays in the digest: two *different* tools reporting the same thing
// is real corroboration and must remain two artifacts.
func artifactID(source string, findings []Finding, data []byte) string {
	h := sha256.New()
	h.Write([]byte(source))
	h.Write([]byte{0})
	if len(findings) == 0 {
		// Nothing was parsed (prose, or a shape with no findings): the bytes
		// are all the identity there is.
		h.Write(data)
	} else {
		for _, f := range findings {
			// Deliberately excludes Line, Index, Severity and Verdict —
			// everything that can differ between two runs reporting the same
			// unchanged problem.
			for _, part := range []string{f.File, f.Title, f.Body, f.Evidence, f.CodeBefore, f.CodeAfter} {
				h.Write([]byte(part))
				h.Write([]byte{0})
			}
			h.Write([]byte{'\n'})
		}
	}
	return fmt.Sprintf("rev-%x", h.Sum(nil)[:6])
}

// reReviewID is the exact shape artifactID mints. Grounding validates a
// model-supplied review_id against it before joining it into a path.
var reReviewID = regexp.MustCompile(`^rev-[0-9a-f]{12}$`)

// ValidReviewID reports whether s is a well-formed artifact ID.
//
// This is a containment check, not a formatting nicety. A signal's review_id
// arrives from the extraction subagent and names the file its snippet is
// verified against; joined into a path unchecked, "../state" or
// "../../../signals" points grounding at some other JSON file on disk, and
// every fabricated quote in the batch verifies against it. The PR arm cannot
// do this because a PR number is an int.
func ValidReviewID(s string) bool {
	return reReviewID.MatchString(s)
}

// Detect sniffs the shape of a captured review. It is deliberately
// conservative: anything it cannot positively identify as a known JSON shape
// is markdown, which parses to no findings but keeps the text in full, so an
// unrecognised tool degrades to "the subagent reads the review" rather than to
// a parse error or, worse, a silent empty capture.
func Detect(data []byte) string {
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")))
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return FormatMarkdown
	}

	var doc interface{}
	if err := json.Unmarshal(trimmed, &doc); err != nil {
		return FormatMarkdown
	}

	switch t := doc.(type) {
	case map[string]interface{}:
		if _, ok := t["runs"].([]interface{}); ok {
			return FormatSARIF
		}
		// "findings" is a popular container key (Snyk, Trivy, Security Hub all
		// use it with their own vocabulary), so claim this shape only when an
		// element actually looks like a ReportFindings entry. Guessing on the
		// key alone parsed every foreign report into content-free findings.
		if fs, ok := t["findings"].([]interface{}); ok && looksLikeFindings(fs) {
			return FormatFindings
		}
		if results, ok := t["results"].([]interface{}); ok {
			// semgrep and SARIF both say "results"; SARIF's live under runs[]
			// and were matched above, so a top-level one is semgrep's. Confirm
			// on a key semgrep always emits before claiming the shape.
			if len(results) == 0 {
				return FormatSemgrep
			}
			if first, ok := results[0].(map[string]interface{}); ok {
				if _, ok := first["check_id"]; ok {
					return FormatSemgrep
				}
				if _, ok := first["extra"]; ok {
					return FormatSemgrep
				}
			}
		}
	case []interface{}:
		if len(t) == 0 {
			// An empty array is a clean run of either shape. Findings is the
			// house format, so read it as that.
			return FormatFindings
		}
		if first, ok := t[0].(map[string]interface{}); ok {
			if _, ok := first["filePath"]; ok {
				if _, ok := first["messages"]; ok {
					return FormatESLint
				}
			}
			if looksLikeFinding(first) {
				return FormatFindings
			}
		}
	}
	return FormatMarkdown
}

// looksLikeFindings reports whether a decoded "findings" array carries the
// ReportFindings vocabulary. An empty array is accepted — a clean review is a
// legitimate capture, and there is nothing to misread.
func looksLikeFindings(fs []interface{}) bool {
	if len(fs) == 0 {
		return true
	}
	first, ok := fs[0].(map[string]interface{})
	return ok && looksLikeFinding(first)
}

// looksLikeFinding reports whether one decoded object carries at least one
// field only a ReportFindings entry would have. "file" alone is not enough:
// most report formats name a file.
func looksLikeFinding(m map[string]interface{}) bool {
	for _, key := range []string{"summary", "short_summary", "failure_scenario"} {
		if _, ok := m[key]; ok {
			return true
		}
	}
	return false
}

func parse(format string, data []byte) ([]Finding, error) {
	switch format {
	case FormatFindings:
		return parseFindings(data)
	case FormatSARIF:
		return parseSARIF(data)
	case FormatESLint:
		return parseESLint(data)
	case FormatSemgrep:
		return parseSemgrep(data)
	case FormatMarkdown:
		return parseMarkdown(string(data)), nil
	}
	return nil, fmt.Errorf("unknown format %q", format)
}
