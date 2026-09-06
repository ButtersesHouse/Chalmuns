// Package format implements deterministic SKILL.md frontmatter/size checks
// for pattern-learner's own generated output, so the check can run as a
// sanctioned $BIN subcommand instead of an ad-hoc script (which the
// learn-patterns guard blocks during a run — see internal/guard).
//
// Scope, and why it is narrow. output.Write already keeps generated skills
// within the documented body-line budget itself: it renders rules inline
// while the result fits under maxSkillLines (450) and otherwise chunks the
// domain into rules/<slug>.md plus an index, and it emits examples/<slug>.md
// companion files. Those companions are one link-hop from SKILL.md by
// construction, so the reference-nesting and TOC checks in the sibling
// Python audit (plugins/skill-right-sizing/skills/right-format-skills/
// scripts/audit_format.py, rubric in that skill's references/rubric.md)
// have nothing to catch here and are deliberately not ported.
//
// What is left is the gap output.Write does *not* close:
//
//   - Frontmatter validity. The domain name is written to `name:` verbatim
//     (renderSkillHeader), and the description comes from the model-authored
//     domain_descriptions entry. Neither is validated or sanitized, so a
//     domain canonicalized to e.g. "Legacy_API" yields a SKILL.md whose
//     name breaks the documented charset rule. This is the check with real
//     residual value. The block is parsed as real YAML (the way Claude Code
//     reads it), so a file whose frontmatter does not parse is reported as
//     such rather than passing on a line-by-line regex read.
//   - Body lines, as a regression assertion on the chunking above rather
//     than a budget the model is expected to act on: if a generated skill
//     ever reports over budget, output.Write's chunking failed to do its
//     job, which is a bug in the generator, not something to hand-fix in
//     the emitted file (write-outputs rewrites it wholesale next run).
package format

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Size/frontmatter thresholds, sourced from Anthropic's Skill-authoring
// guidance — see the rubric cited in the package doc above. The character
// limits are counted in runes, matching the generator's own rune-based cap.
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
	fields, body, parseIssues := parseFrontmatter(text)
	res.Name = fields["name"]
	res.BodyLines = countLines(body)
	res.OverBudget = res.BodyLines > BodyLineLimit
	res.ApproachingBudget = !res.OverBudget && res.BodyLines >= BodyLineWarn
	res.FrontmatterIssues = append(parseIssues, checkFrontmatter(fields)...)
	return res
}

// parseFrontmatter splits text into the frontmatter fields and the body that
// follows the closing "---". The block is parsed as YAML, since that is how
// the consuming agent reads it; a block that does not parse is reported as
// an issue, and the fields are then recovered line-by-line so the remaining
// checks (and the reported name) still have something to work with.
//
// Values are flattened to strings: scalars via their string form, a list
// (the .claude/rules form of `paths`) via a comma join. Anything else is
// reported as an issue, since no documented field takes a nested value.
func parseFrontmatter(text string) (map[string]string, string, []string) {
	fields := map[string]string{}
	issues := []string{}
	m := reFrontmatter.FindStringSubmatchIndex(text)
	if m == nil {
		return fields, text, issues
	}
	fmText := text[m[2]:m[3]]
	body := text[m[1]:]

	var doc map[string]any
	if err := yaml.Unmarshal([]byte(fmText), &doc); err != nil {
		issues = append(issues, "frontmatter is not valid YAML (the skill will fail to load): "+
			strings.Join(strings.Fields(err.Error()), " "))
		return lenientFields(fmText), body, issues
	}
	for k, v := range doc {
		switch val := v.(type) {
		case nil:
			fields[k] = ""
		case string:
			fields[k] = val
		case []any:
			parts := make([]string, 0, len(val))
			for _, item := range val {
				parts = append(parts, fmt.Sprint(item))
			}
			fields[k] = strings.Join(parts, ",")
		case map[string]any:
			issues = append(issues, fmt.Sprintf("frontmatter field '%s' has a nested value; expected a string", k))
		default:
			fields[k] = fmt.Sprint(val)
		}
	}
	return fields, body, issues
}

// lenientFields is the pre-YAML line reader, kept only as the fallback for a
// block that failed to parse.
func lenientFields(fmText string) map[string]string {
	fields := map[string]string{}
	for _, line := range strings.Split(fmText, "\n") {
		if fm := reFMField.FindStringSubmatch(line); fm != nil {
			fields[strings.TrimSpace(fm[1])] = strings.Trim(strings.TrimSpace(fm[2]), `"'`)
		}
	}
	return fields
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
	// Non-nil so the JSON output reads as an empty list, not null.
	issues := []string{}
	name := fields["name"]
	desc := fields["description"]

	if name == "" {
		issues = append(issues, "frontmatter missing required 'name'")
	} else {
		if n := utf8.RuneCountInString(name); n > NameMaxChars {
			issues = append(issues, fmt.Sprintf("name exceeds %d chars (%d)", NameMaxChars, n))
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
	} else if n := utf8.RuneCountInString(desc); n > DescMaxChars {
		issues = append(issues, fmt.Sprintf("description exceeds %d chars (%d)", DescMaxChars, n))
	}

	// paths is optional; when present it gates auto-loading, so an empty
	// entry (a stray comma) would silently widen or break the gate.
	if paths, ok := fields["paths"]; ok {
		for _, g := range strings.Split(paths, ",") {
			if strings.TrimSpace(g) == "" {
				issues = append(issues, "paths contains an empty glob")
				break
			}
		}
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
