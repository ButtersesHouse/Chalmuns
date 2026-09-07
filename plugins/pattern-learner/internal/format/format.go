// Package format implements deterministic SKILL.md frontmatter/size checks
// for pattern-learner's own generated output, so the check can run as a
// sanctioned $BIN subcommand instead of an ad-hoc script (which the
// learn-patterns guard blocks during a run — see internal/guard).
//
// Scope, and why it is narrow. output.Write already keeps generated skills
// within the documented body-line budget itself: it renders rules inline
// while the body fits under maxSkillBodyLines (BodyLineWarn) and chunks the
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
	// The fences are whole lines. `\s` would match a newline, so `\s*\n?`
	// let the closing fence match at any line merely *starting* with "---"
	// ("----", "--- x"), cutting the block short of where a real YAML
	// loader ends it and grading the truncated remainder clean.
	reFrontmatter = regexp.MustCompile(`(?s)\A---[ \t]*\r?\n(.*?)\r?\n---[ \t]*(?:\r?\n|\z)`)
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
	res := Result{Path: path, FrontmatterIssues: []string{}}
	data, err := os.ReadFile(path)
	if err != nil {
		res.Error = err.Error()
		return res
	}

	text := string(data)
	fields, body, parseIssues := ParseFrontmatter(text)
	res.Name = fields["name"]
	res.BodyLines = countLines(body)
	res.OverBudget = res.BodyLines > BodyLineLimit
	res.ApproachingBudget = !res.OverBudget && res.BodyLines >= BodyLineWarn
	res.FrontmatterIssues = append(parseIssues, checkFrontmatter(fields, reported(parseIssues))...)
	return res
}

// ParseFrontmatter splits a SKILL.md's text into its frontmatter fields and
// the body that follows the closing "---". It is the one frontmatter reader
// in the module: the audit uses it to check generated files, and the
// generator uses it to recognise its own earlier output. The block is parsed
// as YAML, since that is how the consuming agent reads it; a block that does
// not parse is reported as an issue, and the fields are then recovered
// line-by-line so callers (and the reported name) still have something to
// work with — which is also what lets the generator recognise skills it
// wrote before it quoted its frontmatter.
//
// Values are flattened to strings using the scalar's source text (so
// `name: 007` is reported as "007", the literal the file carries, not the
// integer 7 it decodes to); a list (the .claude/rules form of `paths`) is
// joined with commas. Anything else is reported as an issue, since no
// documented field takes a nested value. A repeated key is reported too:
// yaml.v3 tolerates it when decoding into a Node, but strict loaders refuse
// the file.
func ParseFrontmatter(text string) (fields map[string]string, body string, issues []string) {
	fields = map[string]string{}
	issues = []string{}
	// A UTF-8 BOM sits before the opening fence, and the pattern is anchored
	// at byte 0, so leaving it in place hides the frontmatter completely:
	// the audit then reports name and description missing from a file that
	// plainly has both, and the generator fails to recognise a skill it
	// wrote itself and refuses the run over it.
	text = strings.TrimPrefix(text, "\ufeff")
	m := reFrontmatter.FindStringSubmatchIndex(text)
	if m == nil {
		return fields, text, issues
	}
	fmText := text[m[2]:m[3]]
	body = text[m[1]:]

	var root yaml.Node
	if err := yaml.Unmarshal([]byte(fmText), &root); err != nil {
		issues = append(issues, "frontmatter is not valid YAML (the skill will fail to load): "+
			strings.Join(strings.Fields(err.Error()), " "))
		return lenientFields(fmText), body, issues
	}
	// A parsed document wraps one top-level node, which must be a mapping;
	// valid YAML that is a bare scalar or a list is a different problem from
	// a parse error and is reported as such.
	var top *yaml.Node
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		top = root.Content[0]
	}
	if top == nil {
		return fields, body, issues
	}
	if top.Kind != yaml.MappingNode {
		issues = append(issues, "frontmatter must be a key/value mapping (name:, description:, ...), not a bare value or list")
		return fields, body, issues
	}
	seen := map[string]bool{}
	// Keys reported malformed here are deliberately left out of fields, so
	// checkFrontmatter must not then also report them absent: one defect
	// handed to the agent as two findings with different remedies sends it
	// after the wrong one.
	malformed := map[string]bool{}
	for i := 0; i+1 < len(top.Content); i += 2 {
		k, node := top.Content[i].Value, top.Content[i+1]
		if seen[k] {
			issues = append(issues, fmt.Sprintf("frontmatter key '%s' appears more than once; strict YAML loaders reject the file", k))
		}
		seen[k] = true
		switch {
		case node.Kind == yaml.ScalarNode:
			if node.Tag == "!!null" {
				fields[k] = ""
			} else {
				fields[k] = node.Value
			}
		case node.Kind == yaml.SequenceNode && listValued[k]:
			// Only paths is documented as a list (the .claude/rules form).
			// Flattening every key's list turned `name:` and `description:`
			// written as one-item lists into plain strings that then passed
			// every check — a false clean on a file Claude Code would hand a
			// list where it wants a string.
			parts := make([]string, 0, len(node.Content))
			nested := false
			for _, item := range node.Content {
				if item.Kind != yaml.ScalarNode {
					nested = true
					break
				}
				parts = append(parts, item.Value)
			}
			if nested {
				// Report the nested item only; storing a partial join would
				// trigger a second, spurious empty-glob finding.
				issues = append(issues, fmt.Sprintf("frontmatter field '%s' has a nested value; expected a string", k))
				malformed[k] = true
				continue
			}
			fields[k] = strings.Join(parts, ",")
		case node.Kind == yaml.SequenceNode:
			issues = append(issues, fmt.Sprintf("frontmatter field '%s' is a list; expected a string", k))
			malformed[k] = true
		default:
			issues = append(issues, fmt.Sprintf("frontmatter field '%s' has a nested value; expected a string", k))
			malformed[k] = true
		}
	}
	for k := range malformed {
		delete(fields, k)
	}
	return fields, body, issues
}

// listValued names the frontmatter keys a YAML list is a legitimate form
// for. paths is the only one: .claude/rules files write their globs as a
// list, and the audit reads those too.
var listValued = map[string]bool{"paths": true}

// reported maps the keys ParseFrontmatter named in its own issues, so
// checkFrontmatter can stay quiet about them. The issue text is the one
// carrier between the two: the alternative is a third return value on an
// exported function whose only caller in the module is AuditFile.
func reported(issues []string) map[string]bool {
	out := map[string]bool{}
	for _, s := range issues {
		if i := strings.Index(s, "frontmatter field '"); i >= 0 {
			rest := s[i+len("frontmatter field '"):]
			if j := strings.Index(rest, "'"); j > 0 {
				out[rest[:j]] = true
			}
		}
	}
	return out
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
func checkFrontmatter(fields map[string]string, malformed map[string]bool) []string {
	// Non-nil so the JSON output reads as an empty list, not null.
	issues := []string{}
	name := fields["name"]
	desc := fields["description"]

	if name == "" && malformed["name"] {
		// Already reported as malformed by ParseFrontmatter; "missing" would
		// be a second, contradictory finding for the one defect.
	} else if name == "" {
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

	if desc == "" && malformed["description"] {
		// See the name branch above.
	} else if desc == "" {
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
	var unread []string
	for _, path := range args {
		res := AuditFile(path)
		if res.Error != "" {
			unread = append(unread, path)
		}
		results = append(results, res)
	}
	if err := encodeResults(os.Stdout, results); err != nil {
		return err
	}
	// A file that could not be read was not audited, and the documented
	// decision procedure reads only frontmatter_issues and the budget flags
	// — all of which are empty here. Exiting 0 made a missing or misspelled
	// path indistinguishable from a clean skill, so the caller was told
	// "frontmatter valid for all N domain skills" about files nothing looked
	// at. Fail instead, naming them.
	if len(unread) > 0 {
		return fmt.Errorf("could not read %d of %d file(s), so they were not audited: %s",
			len(unread), len(args), strings.Join(unread, ", "))
	}
	return nil
}

func encodeResults(w io.Writer, results []Result) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(results)
}
