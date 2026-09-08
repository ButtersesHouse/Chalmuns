package review

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// A parse failure is an error, never an empty finding list. A tool whose
// output shape changed would otherwise capture cleanly, mine to zero rules,
// and look like "this review said nothing" — the failure mode the PR path's
// comment_sources warning exists to prevent.

// --- Claude Code /code-review (ReportFindings) ---

type rcFinding struct {
	File            string `json:"file"`
	Line            int    `json:"line"`
	Category        string `json:"category"`
	Summary         string `json:"summary"`
	ShortSummary    string `json:"short_summary"`
	FailureScenario string `json:"failure_scenario"`
	Verdict         string `json:"verdict"`
	Severity        string `json:"severity"`
	Suggestion      string `json:"suggestion"`
}

func parseFindings(data []byte) ([]Finding, error) {
	// Both shapes are real: the tool call carries {"findings": [...]}, while a
	// saved report is often just the array. Which one this is decides which
	// error to report — falling through to the array attempt for an object
	// input blamed the container shape for what was actually a bad field
	// inside one finding, pointing the reader at the wrong thing entirely.
	var list []rcFinding
	if bytes.HasPrefix(bytes.TrimLeft(data, " \t\r\n\ufeff"), []byte("{")) {
		var wrapper struct {
			Findings []rcFinding `json:"findings"`
		}
		if err := json.Unmarshal(data, &wrapper); err != nil {
			return nil, fmt.Errorf("parse findings JSON: %w", err)
		}
		// A null or absent list is a clean review, not a malformed one.
		list = wrapper.Findings
	} else if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parse findings JSON: %w", err)
	}

	out := make([]Finding, 0, len(list))
	for _, f := range list {
		// summary is the claim, failure_scenario the evidence for it, and a
		// rule may quote either — so both are kept, in *separate* fields.
		// Concatenating them into one body welded the end of one field to the
		// start of the other, and grounding checks containment: a quote
		// spanning that join passed verification as the reviewer's verbatim
		// words when the reviewer never wrote that sentence.
		title := strings.TrimSpace(f.ShortSummary)
		if title == "" {
			title = firstSentence(f.Summary)
		}
		out = append(out, Finding{
			File:      strings.TrimSpace(f.File),
			Line:      f.Line,
			Category:  strings.TrimSpace(f.Category),
			Severity:  strings.TrimSpace(f.Severity),
			Title:     title,
			Body:      strings.TrimSpace(f.Summary),
			Evidence:  strings.TrimSpace(f.FailureScenario),
			CodeAfter: strings.TrimSpace(f.Suggestion),
			Verdict:   strings.TrimSpace(f.Verdict),
		})
	}
	return out, nil
}

// hasContent reports whether a parsed finding carries any of the reviewer's
// words. A list of findings with none means the input used a `findings` key
// with a different vocabulary (Snyk, Trivy, a bespoke report) and was only
// guessed to be this format — see Capture, which falls back rather than
// recording a review whose text never reaches the extraction step.
func hasContent(fs []Finding) bool {
	for _, f := range fs {
		if f.Title != "" || f.Body != "" || f.Evidence != "" {
			return true
		}
	}
	return len(fs) == 0
}

// --- SARIF 2.1.0 ---

type sarifText struct {
	Text string `json:"text"`
}

type sarifDoc struct {
	Runs []struct {
		Tool struct {
			Driver struct {
				Name  string `json:"name"`
				Rules []struct {
					ID               string    `json:"id"`
					Name             string    `json:"name"`
					ShortDescription sarifText `json:"shortDescription"`
					FullDescription  sarifText `json:"fullDescription"`
					Help             sarifText `json:"help"`
				} `json:"rules"`
			} `json:"driver"`
		} `json:"tool"`
		Results []struct {
			RuleID    string    `json:"ruleId"`
			Level     string    `json:"level"`
			Message   sarifText `json:"message"`
			Locations []struct {
				PhysicalLocation struct {
					ArtifactLocation struct {
						URI string `json:"uri"`
					} `json:"artifactLocation"`
					Region struct {
						StartLine int       `json:"startLine"`
						Snippet   sarifText `json:"snippet"`
					} `json:"region"`
				} `json:"physicalLocation"`
			} `json:"locations"`
			Fixes []struct {
				ArtifactChanges []struct {
					Replacements []struct {
						InsertedContent sarifText `json:"insertedContent"`
					} `json:"replacements"`
				} `json:"artifactChanges"`
			} `json:"fixes"`
		} `json:"results"`
	} `json:"runs"`
}

func parseSARIF(data []byte) ([]Finding, error) {
	var doc sarifDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse SARIF: %w", err)
	}

	var out []Finding
	for _, run := range doc.Runs {
		// A SARIF result's message is often one terse line ("Unused variable
		// 'x'"). The rule's own description is where the convention is stated,
		// and it is what a rule about the convention should be able to quote,
		// so it is folded into the body of every result that rule produced.
		desc := map[string]string{}
		for _, r := range run.Tool.Driver.Rules {
			text := firstNonEmpty(r.FullDescription.Text, r.Help.Text, r.ShortDescription.Text)
			if id := firstNonEmpty(r.ID, r.Name); id != "" && text != "" {
				desc[id] = strings.TrimSpace(text)
			}
		}
		for _, res := range run.Results {
			body := strings.TrimSpace(res.Message.Text)
			f := Finding{
				Title:    strings.TrimSpace(res.RuleID),
				Severity: strings.TrimSpace(res.Level),
				Body:     body,
				Verdict:  strings.TrimSpace(res.Level),
			}
			// The rule's own description is where the convention is stated,
			// and a rule may quote it — but it is separate prose from the
			// result message, so it is kept in its own field. Joining them
			// would let a snippet span the seam and still ground.
			if d := desc[res.RuleID]; d != "" && d != body {
				f.Evidence = d
			}
			if len(res.Locations) > 0 {
				phys := res.Locations[0].PhysicalLocation
				f.File = strings.TrimPrefix(strings.TrimSpace(phys.ArtifactLocation.URI), "file://")
				f.Line = phys.Region.StartLine
				f.CodeBefore = strings.TrimSpace(phys.Region.Snippet.Text)
			}
			for _, fix := range res.Fixes {
				for _, ch := range fix.ArtifactChanges {
					for _, rep := range ch.Replacements {
						if c := strings.TrimSpace(rep.InsertedContent.Text); c != "" && f.CodeAfter == "" {
							f.CodeAfter = c
						}
					}
				}
			}
			out = append(out, f)
		}
	}
	return out, nil
}

// --- ESLint (`eslint --format json`) ---

func parseESLint(data []byte) ([]Finding, error) {
	var files []struct {
		FilePath string `json:"filePath"`
		Messages []struct {
			RuleID   string `json:"ruleId"`
			Severity int    `json:"severity"`
			Message  string `json:"message"`
			Line     int    `json:"line"`
		} `json:"messages"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal(data, &files); err != nil {
		return nil, fmt.Errorf("parse ESLint JSON: %w", err)
	}

	var out []Finding
	for _, file := range files {
		for _, m := range file.Messages {
			out = append(out, Finding{
				File:     strings.TrimSpace(file.FilePath),
				Line:     m.Line,
				Title:    strings.TrimSpace(m.RuleID),
				Severity: eslintSeverity(m.Severity),
				Body:     strings.TrimSpace(m.Message),
			})
		}
	}
	return out, nil
}

func eslintSeverity(n int) string {
	switch n {
	case 2:
		return "error"
	case 1:
		return "warning"
	default:
		return ""
	}
}

// --- semgrep (`semgrep --json`) ---

func parseSemgrep(data []byte) ([]Finding, error) {
	var doc struct {
		Results []struct {
			CheckID string `json:"check_id"`
			Path    string `json:"path"`
			Start   struct {
				Line int `json:"line"`
			} `json:"start"`
			Extra struct {
				Message  string `json:"message"`
				Severity string `json:"severity"`
				Lines    string `json:"lines"`
				Fix      string `json:"fix"`
			} `json:"extra"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse semgrep JSON: %w", err)
	}

	out := make([]Finding, 0, len(doc.Results))
	for _, r := range doc.Results {
		out = append(out, Finding{
			File:       strings.TrimSpace(r.Path),
			Line:       r.Start.Line,
			Title:      strings.TrimSpace(r.CheckID),
			Severity:   strings.TrimSpace(r.Extra.Severity),
			Body:       strings.TrimSpace(r.Extra.Message),
			CodeBefore: strings.TrimSpace(r.Extra.Lines),
			CodeAfter:  strings.TrimSpace(r.Extra.Fix),
		})
	}
	return out, nil
}

// --- markdown / free-form prose ---

var (
	// A heading starts a finding. Any level is accepted because review
	// write-ups nest inconsistently.
	reMDHeading = regexp.MustCompile(`(?m)^(#{1,6})[ \t]+(.+?)[ \t]*$`)
	// A file reference: "internal/api/handler.go:42" or ":L42". The path must
	// carry an extension so ordinary prose containing a colon is not read as a
	// location. The character class excludes only separators and punctuation
	// that cannot appear in a path — an ASCII-only class silently truncated
	// any path containing a non-ASCII byte ("café/naïve.go:12" matched as
	// "ve.go"), attributing the finding to a file that does not exist. The
	// backtick and "#" are excluded for the same reason from the other side:
	// reviewers normally write a path in a code span, and capturing the
	// delimiter into the path names a file just as absent.
	reMDFileRef = regexp.MustCompile("([^\\s:;,()\\[\\]{}'\"<>|*?`#]+\\.[A-Za-z0-9]+):L?(\\d+)")
	// Words that label the fence that follows them. Kept narrow on purpose:
	// guessing wrong swaps a rule's do and don't examples, which teaches the
	// exact opposite of the convention. "instead" is deliberately absent from
	// both lists: "instead of X" labels the code to avoid and "use X instead"
	// labels the code to write, so the word alone decides nothing.
	reBeforeCue = regexp.MustCompile(`(?i)\b(before|current|currently|don'?t|do not|avoid|instead of|problem|issue|flagged|bad|wrong|anti-?pattern)\b`)
	reAfterCue  = regexp.MustCompile(`(?i)\b(after|prefer|preferred|should be|suggested|suggestion|fix|fixed|do this|correct|better|good|use this)\b`)
)

// fenceBlock is one closed fenced code block and where it sits in the section.
// A block dropped as nested is still reported, with keep false: its extent has
// to advance the caller's cursor even though its code is unusable, or the text
// it spans becomes part of the *next* fence's label and a cue word inside it
// decides that fence.
type fenceBlock struct {
	start int // byte offset of the opening fence line
	end   int // byte offset just past the closing fence line
	code  string
	keep  bool
}

// fenceBlocks finds the closed code fences in s, scanning line by line rather
// than by regex because fences are stateful. A pattern pairing "```…" with the
// next "```" cannot tell an opener from a closer, so a review with an unclosed
// fence had that opener paired with the *next block's* opener and stored the
// reviewer's prose in between as code. Here an opener runs until a line that
// is nothing but backticks, and a fence left unclosed at the end of the
// section is dropped: its extent is unknown, and guessing it is how prose ends
// up recorded as an example.
func fenceBlocks(s string) []fenceBlock {
	var out []fenceBlock
	var open, nested bool
	var marker string
	var block fenceBlock
	var buf strings.Builder
	offset := 0
	for _, line := range strings.SplitAfter(s, "\n") {
		bare := strings.TrimLeft(strings.TrimRight(line, "\r\n"), " \t")
		// Both fence syntaxes are legal markdown and reviewers use both; a
		// fence must be closed by its own character, so a ``` inside a ~~~
		// block is content.
		fenceChar := ""
		if strings.HasPrefix(bare, "```") {
			fenceChar = "`"
		} else if strings.HasPrefix(bare, "~~~") {
			fenceChar = "~"
		}
		isFence := fenceChar != ""
		switch {
		case !open && isFence:
			open, nested, marker = true, false, fenceChar
			block = fenceBlock{start: offset}
			buf.Reset()
		case open && fenceChar == marker && strings.Trim(bare, marker) == "":
			block.end = offset + len(line)
			// A CRLF document would otherwise carry a stray carriage return
			// into every extracted example, and from there into the generated
			// skill's code block.
			block.code = strings.ReplaceAll(buf.String(), "\r\n", "\n")
			// A block that swallowed another opening fence means the writer
			// forgot a closing one: by the letter of the syntax everything up
			// to the next bare ``` is code, but in a review it is the
			// reviewer's prose plus the next example. Dropping the block loses
			// an example; keeping it files prose as the code to imitate.
			block.keep = !nested
			out = append(out, block)
			open = false
		case open:
			// Only a fence of the *same* character could have been meant as a
			// closer, so only that one signals a forgotten one. A ``` line
			// inside a ~~~ block is content, as the closing check above
			// already says.
			if fenceChar == marker {
				nested = true
			}
			buf.WriteString(line)
		}
		offset += len(line)
	}
	return out
}

// parseMarkdown splits a prose review into one finding per heading. A document
// with no headings yields no findings: the review is still captured whole in
// RawText, and the extraction step reads that. Inventing one finding per
// paragraph would fabricate structure the reviewer did not write.
func parseMarkdown(text string) []Finding {
	locs := reMDHeading.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		return nil
	}

	var out []Finding
	for i, loc := range locs {
		title := strings.TrimSpace(text[loc[4]:loc[5]])
		level := loc[3] - loc[2]
		bodyStart := loc[1]
		bodyEnd := len(text)
		nextLevel := 0
		if i+1 < len(locs) {
			bodyEnd = locs[i+1][0]
			nextLevel = locs[i+1][3] - locs[i+1][2]
		}
		section := text[bodyStart:bodyEnd]
		if strings.TrimSpace(section) == "" {
			// A heading with nothing under it that introduces deeper headings
			// is the document's title or a section divider, not a remark. One
			// with no deeper heading after it is a finding stated entirely in
			// its heading ("## Never panic in handlers"), which is real.
			if title == "" || nextLevel > level {
				continue
			}
		}

		f := Finding{Title: title, Body: strings.TrimSpace(section)}
		if m := reMDFileRef.FindStringSubmatch(title + "\n" + section); m != nil {
			f.File = m[1]
			if n, err := strconv.Atoi(m[2]); err == nil {
				f.Line = n
			}
		}
		// The heading labels the section's first fence: a review written as
		// "### Before" / fence / "### After" / fence splits into one finding
		// per heading, and each one's only label is its own title.
		f.CodeBefore, f.CodeAfter = labelledFences(title + "\n" + section)
		out = append(out, f)
	}
	return out
}

// labelledFences pulls the before/after code out of a section, but only when
// the text right above a fence says which it is. An unlabelled fence is left
// out rather than assigned by position: a review that shows only the offending
// code would otherwise have it recorded as the example to imitate.
//
// The label is the cue *nearest* the fence, not the first cue found anywhere
// above it. Testing one list before the other made any stray cue word in the
// paragraph outrank the label immediately above the code — "…the fix is to
// wrap them.\n\nBefore:\n```go" put the flagged code under "do", which teaches
// the reverse of what the reviewer wrote.
func labelledFences(section string) (before, after string) {
	prevEnd := 0
	for _, block := range fenceBlocks(section) {
		// Only the run of text since the previous fence closed can label this
		// one; anything earlier belongs to the previous example. The cursor
		// advances past every block, kept or not — a dropped block's code
		// would otherwise sit in the next fence's lead and label it.
		lead := section[prevEnd:block.start]
		prevEnd = block.end
		if !block.keep {
			continue
		}
		code := strings.TrimRight(block.code, "\n")
		switch nearestCue(lead) {
		case cueAfter:
			if after == "" {
				after = code
			}
		case cueBefore:
			if before == "" {
				before = code
			}
		}
	}
	return before, after
}

type cue int

const (
	cueNone cue = iota
	cueBefore
	cueAfter
)

// nearestCue reports which kind of label sits closest to the end of lead —
// i.e. closest to the fence it introduces. An equal position (no match of
// either kind) leaves the fence unlabelled, which drops it rather than
// guessing.
func nearestCue(lead string) cue {
	b := lastMatch(reBeforeCue, lead)
	a := lastMatch(reAfterCue, lead)
	switch {
	case a > b:
		return cueAfter
	case b > a:
		return cueBefore
	default:
		return cueNone
	}
}

// lastMatch returns the start offset of re's last match in s, or -1.
func lastMatch(re *regexp.Regexp, s string) int {
	locs := re.FindAllStringIndex(s, -1)
	if len(locs) == 0 {
		return -1
	}
	return locs[len(locs)-1][0]
}

// --- shared helpers ---

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// firstSentence is the fallback title for a finding whose tool gave none.
// A period only ends a sentence when whitespace follows it: cutting at any
// period turned "Don't call os.Exit in library code" into "Don't call os",
// and Title is what the recurrence check recognises a repeated finding by.
func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "\n"); i > 0 {
		s = s[:i]
	}
	for i := 0; i < len(s)-1; i++ {
		if s[i] == '.' && (s[i+1] == ' ' || s[i+1] == '\t') {
			s = s[:i]
			break
		}
	}
	s = strings.TrimSuffix(s, ".")
	const maxTitle = 80
	if len([]rune(s)) > maxTitle {
		s = string([]rune(s)[:maxTitle])
	}
	return strings.TrimSpace(s)
}
