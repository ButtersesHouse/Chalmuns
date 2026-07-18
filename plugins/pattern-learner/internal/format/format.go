// Package format implements deterministic SKILL.md size/frontmatter checks
// for pattern-learner's own generated output, so a format-budget check can
// run as a sanctioned $BIN subcommand instead of an ad-hoc script (which the
// learn-patterns guard blocks during a run — see internal/guard).
//
// This is a narrower port of the skill-right-sizing plugin's
// right-format-skills audit (plugins/skill-right-sizing/skills/
// right-format-skills/scripts/audit_format.py, rubric in that skill's
// references/rubric.md): it covers only the checks meaningful for
// write-outputs' shape — a single flat SKILL.md per domain, never a
// multi-file skill with bundled references — so reference-nesting, TOC, and
// path-style checks are intentionally omitted here. If write-outputs ever
// starts bundling reference files, port those checks over too.
package format

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// Size/frontmatter thresholds, sourced from Anthropic's Skill-authoring
// guidance — see the rubric cited in the package doc above.
const (
	BodyLineLimit = 500
	BodyLineWarn  = 400
	NameMaxChars  = 64
	DescMaxChars  = 1024
)

var (
	reservedWords = []string{"claude", "anthropic"}
	reNameChars   = regexp.MustCompile(`^[a-z0-9-]+$`)
	reFrontmatter = regexp.MustCompile(`(?s)^---\s*\n(.*?)\n---\s*\n?`)
	reFMField     = regexp.MustCompile(`^([A-Za-z_-]+):\s*(.*)$`)
)

// Result is the audit finding for one SKILL.md file.
type Result struct {
	Path              string   `json:"path"`
	Name              string   `json:"name"`
	BodyLines         int      `json:"body_lines"`
	OverBudget        bool     `json:"over_budget"`
	ApproachingBudget bool     `json:"approaching_budget"`
	FrontmatterIssues []string `json:"frontmatter_issues"`
	Error             string   `json:"error,omitempty"`
}

// AuditFile reads path and checks it against the body-line budget and
// frontmatter validity rules.
func AuditFile(path string) Result {
	res := Result{Path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		res.Error = err.Error()
		return res
	}

	text := string(data)
	fields, body := parseFrontmatter(text)
	res.Name = fields["name"]
	res.BodyLines = countLines(body)
	res.OverBudget = res.BodyLines > BodyLineLimit
	res.ApproachingBudget = !res.OverBudget && res.BodyLines >= BodyLineWarn
	res.FrontmatterIssues = checkFrontmatter(fields)
	return res
}

// parseFrontmatter splits text into the YAML frontmatter fields (flat
// key: value pairs only — sufficient for name/description) and the body
// that follows the closing "---".
func parseFrontmatter(text string) (map[string]string, string) {
	fields := map[string]string{}
	m := reFrontmatter.FindStringSubmatchIndex(text)
	if m == nil {
		return fields, text
	}
	fmText := text[m[2]:m[3]]
	body := text[m[1]:]
	for _, line := range strings.Split(fmText, "\n") {
		if fm := reFMField.FindStringSubmatch(line); fm != nil {
			fields[strings.TrimSpace(fm[1])] = strings.Trim(strings.TrimSpace(fm[2]), `"'`)
		}
	}
	return fields, body
}

// countLines mirrors Python's len(s.splitlines()): a trailing newline does
// not add an extra (empty) line, but interior/multiple trailing blank lines
// each count — unlike strings.TrimRight(body, "\n"), which collapses every
// trailing blank line away and would undercount a padded body.
func countLines(body string) int {
	if body == "" {
		return 0
	}
	n := strings.Count(body, "\n")
	if strings.HasSuffix(body, "\n") {
		return n
	}
	return n + 1
}

// checkFrontmatter validates name/description against Anthropic's documented
// hard limits (max lengths, character set, reserved words, non-empty
// description). Gerund-naming and vagueness are advisory-only in the
// right-format-skills rubric and are deliberately not re-flagged here —
// pattern-learner's domain names are dictated by codebase structure
// (api, auth, models, ...), not chosen for gerund style.
func checkFrontmatter(fields map[string]string) []string {
	var issues []string
	name := fields["name"]
	desc := fields["description"]

	if name == "" {
		issues = append(issues, "frontmatter missing required 'name'")
	} else {
		if len(name) > NameMaxChars {
			issues = append(issues, fmt.Sprintf("name exceeds %d chars (%d)", NameMaxChars, len(name)))
		}
		if !reNameChars.MatchString(name) {
			issues = append(issues, "name must be lowercase letters, numbers, hyphens only")
		}
		lower := strings.ToLower(name)
		for _, w := range reservedWords {
			if strings.Contains(lower, w) {
				issues = append(issues, fmt.Sprintf("name contains reserved word '%s'", w))
			}
		}
	}

	if desc == "" {
		issues = append(issues, "frontmatter missing required 'description'")
	} else if len(desc) > DescMaxChars {
		issues = append(issues, fmt.Sprintf("description exceeds %d chars (%d)", DescMaxChars, len(desc)))
	}

	return issues
}

// RunAuditFormat is the `audit-format` subcommand entry point. It reads one
// or more SKILL.md paths from args and writes a JSON array of Results to
// stdout.
//
// Usage: audit-format <path> [<path> ...]
func RunAuditFormat(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: audit-format <path> [<path> ...]")
	}
	results := make([]Result, 0, len(args))
	for _, path := range args {
		results = append(results, AuditFile(path))
	}
	return encodeResults(os.Stdout, results)
}

func encodeResults(w io.Writer, results []Result) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(results)
}
