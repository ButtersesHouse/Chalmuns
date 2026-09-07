package review

import (
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
	var list []rcFinding
	// Both shapes are real: the tool call carries {"findings": [...]}, while a
	// saved report is often just the array.
	var wrapper struct {
		Findings []rcFinding `json:"findings"`
	}
	if err := json.Unmarshal(data, &wrapper); err == nil && wrapper.Findings != nil {
		list = wrapper.Findings
	} else if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parse findings JSON: %w", err)
	}

	out := make([]Finding, 0, len(list))
	for _, f := range list {
		// summary is the claim; failure_scenario is the evidence for it. Both
		// are the reviewer's own words, and a rule may legitimately quote
		// either, so both go in the body a signal can be grounded against.
		body := strings.TrimSpace(f.Summary)
		if fs := strings.TrimSpace(f.FailureScenario); fs != "" {
			if body != "" {
				body += "\n\n"
			}
			body += fs
		}
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
			Body:      body,
			CodeAfter: strings.TrimSpace(f.Suggestion),
			Verdict:   strings.TrimSpace(f.Verdict),
		})
	}
	return out, nil
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
			if d := desc[res.RuleID]; d != "" && d != body {
				if body != "" {
					body += "\n\n"
				}
				body += d
			}
			f := Finding{
				Title:    strings.TrimSpace(res.RuleID),
				Severity: strings.TrimSpace(res.Level),
				Body:     body,
				Verdict:  strings.TrimSpace(res.Level),
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
	// carry an extension so ordinary prose containing a colon is not read as
	// a location.
	reMDFileRef = regexp.MustCompile(`([A-Za-z0-9_./\\-]+\.[A-Za-z0-9]+):L?(\d+)`)
	reMDFence   = regexp.MustCompile("(?s)```[^\n]*\n(.*?)```")
	// Words that label the fence that follows them. Kept narrow on purpose:
	// guessing wrong swaps a rule's do and don't examples, which teaches the
	// opposite of the convention.
	reBeforeCue = regexp.MustCompile(`(?i)\b(before|current|currently|don't|do not|avoid|instead of|problem|found|flagged)\b`)
	reAfterCue  = regexp.MustCompile(`(?i)\b(after|instead|prefer|should be|suggested|suggestion|fix|fixed|do this|correct)\b`)
)

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
		f.CodeBefore, f.CodeAfter = labelledFences(section)
		out = append(out, f)
	}
	return out
}

// labelledFences pulls the before/after code out of a section, but only when
// the text right above a fence says which it is. An unlabelled fence is left
// out rather than assigned by position: a review that shows only the offending
// code would otherwise have it recorded as the example to imitate.
func labelledFences(section string) (before, after string) {
	for _, loc := range reMDFence.FindAllStringSubmatchIndex(section, -1) {
		code := strings.TrimRight(section[loc[2]:loc[3]], "\n")
		lead := section[:loc[0]]
		// Only the run of text since the previous fence labels this one.
		if cut := strings.LastIndex(lead, "```"); cut >= 0 {
			lead = lead[cut+3:]
		}
		switch {
		case reAfterCue.MatchString(lead) && after == "":
			after = code
		case reBeforeCue.MatchString(lead) && before == "":
			before = code
		}
	}
	return before, after
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
func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, ".\n"); i > 0 {
		s = s[:i]
	}
	const maxTitle = 80
	if len([]rune(s)) > maxTitle {
		s = string([]rune(s)[:maxTitle])
	}
	return strings.TrimSpace(s)
}
