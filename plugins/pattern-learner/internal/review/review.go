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
	// ReviewID is content-addressed: the same output captured twice yields the
	// same ID and therefore the same file. That makes capture idempotent, which
	// matters because the confidence model counts how many times a convention
	// recurs — a hook that fires twice for one review must not look like two
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
	if format == "" || format == FormatAuto {
		format = Detect(in.Data)
	}
	if !ValidFormat(format) || format == FormatAuto {
		return Artifact{}, fmt.Errorf("unknown --format %q; accepts %s", in.Format, strings.Join(Formats, ", "))
	}

	findings, err := parse(format, in.Data)
	if err != nil {
		return Artifact{}, err
	}
	for i := range findings {
		findings[i].Index = i
	}

	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	return Artifact{
		ReviewID:   ReviewID(source, in.Data),
		Source:     source,
		Format:     format,
		CapturedAt: now.UTC().Format(time.RFC3339),
		Label:      strings.TrimSpace(in.Label),
		Findings:   findings,
		RawText:    string(in.Data),
	}, nil
}

// ReviewID derives an artifact's content-addressed ID. Source is part of the
// digest so the same finding list arriving from two different watched tools
// stays two artifacts — two tools agreeing is real corroboration and must not
// collapse into one.
func ReviewID(source string, data []byte) string {
	h := sha256.New()
	h.Write([]byte(source))
	h.Write([]byte{0})
	h.Write(data)
	return fmt.Sprintf("rev-%x", h.Sum(nil)[:6])
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
		if _, ok := t["findings"].([]interface{}); ok {
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
			_, hasFile := first["file"]
			_, hasSummary := first["summary"]
			_, hasFailure := first["failure_scenario"]
			if hasFile && (hasSummary || hasFailure) {
				return FormatFindings
			}
		}
	}
	return FormatMarkdown
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
