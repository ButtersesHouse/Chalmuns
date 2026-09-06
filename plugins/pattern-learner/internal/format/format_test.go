package format

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ButtersesHouse/Chalmuns/internal/output"
	"github.com/ButtersesHouse/Chalmuns/internal/state"
)

func writeSkill(t *testing.T, dir, name, frontmatter, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	content := "---\n" + frontmatter + "\n---\n" + body
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAuditFile_underBudget_noIssues(t *testing.T) {
	dir := t.TempDir()
	path := writeSkill(t, dir, "SKILL.md",
		"name: api\ndescription: API conventions.",
		"# API\n\nSome rules.\n")

	res := AuditFile(path)
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if res.OverBudget || res.ApproachingBudget {
		t.Errorf("small body should not be flagged: %+v", res)
	}
	if len(res.FrontmatterIssues) != 0 {
		t.Errorf("expected no frontmatter issues, got %v", res.FrontmatterIssues)
	}
	if res.Name != "api" {
		t.Errorf("expected name 'api', got %q", res.Name)
	}
}

func TestAuditFile_overBudget(t *testing.T) {
	dir := t.TempDir()
	body := "# API\n\n" + strings.Repeat("A rule line.\n", 501)
	path := writeSkill(t, dir, "SKILL.md", "name: api\ndescription: API conventions.", body)

	res := AuditFile(path)
	if !res.OverBudget {
		t.Errorf("expected over_budget, got body_lines=%d", res.BodyLines)
	}
	if res.ApproachingBudget {
		t.Errorf("over-budget file should not also be 'approaching'")
	}
}

func TestAuditFile_approachingBudget(t *testing.T) {
	dir := t.TempDir()
	body := "# API\n\n" + strings.Repeat("A rule line.\n", BodyLineWarn)
	path := writeSkill(t, dir, "SKILL.md", "name: api\ndescription: API conventions.", body)

	res := AuditFile(path)
	if res.OverBudget {
		t.Errorf("should not be over budget yet: body_lines=%d", res.BodyLines)
	}
	if !res.ApproachingBudget {
		t.Errorf("expected approaching_budget at body_lines=%d (warn=%d)", res.BodyLines, BodyLineWarn)
	}
}

func TestAuditFile_frontmatterIssues(t *testing.T) {
	cases := []struct {
		name        string
		frontmatter string
		wantSubstr  string
	}{
		{"missing name", "description: x.", "missing required 'name'"},
		{"missing description", "name: api", "missing required 'description'"},
		{"uppercase name", "name: API\ndescription: x.", "lowercase"},
		{"reserved word", "name: claude-helper\ndescription: x.", "reserved word"},
		{"name too long", "name: " + strings.Repeat("a", 65) + "\ndescription: x.", "exceeds 64 chars"},
		{"description too long", "name: api\ndescription: " + strings.Repeat("a", 1025), "exceeds 1024 chars"},
	}
	// Limits are characters, not bytes: 1000 two-byte runes is within budget.
	t.Run("multibyte description within limit", func(t *testing.T) {
		path := writeSkill(t, t.TempDir(), "SKILL.md", "name: api\ndescription: "+strings.Repeat("é", 1000), "# API\n\nbody\n")
		if res := AuditFile(path); len(res.FrontmatterIssues) != 0 {
			t.Errorf("1000-rune description should pass, got %v", res.FrontmatterIssues)
		}
	})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeSkill(t, dir, "SKILL.md", tc.frontmatter, "# API\n\nbody\n")
			res := AuditFile(path)
			found := false
			for _, issue := range res.FrontmatterIssues {
				if strings.Contains(issue, tc.wantSubstr) {
					found = true
				}
			}
			if !found {
				t.Errorf("expected an issue containing %q, got %v", tc.wantSubstr, res.FrontmatterIssues)
			}
		})
	}
}

// The block is parsed as YAML, as Claude Code reads it. The two unquoted
// shapes below used to slip past the old line-based reader and then fail at
// load time; they must be reported here instead. Quoted forms must pass, and
// the .claude/rules list form of paths is accepted.
func TestAuditFile_yamlValidity(t *testing.T) {
	cases := []struct {
		name        string
		frontmatter string
		wantIssue   string // "" means no YAML issue expected
	}{
		{"unquoted colon in description", "name: api\ndescription: API endpoints: errors, auth.", "not valid YAML"},
		{"unquoted star glob", "name: ui\ndescription: x.\npaths: *.tsx,src/**/*.ts", "not valid YAML"},
		{"quoted colon and star", "name: \"ui\"\ndescription: \"API endpoints: errors, auth.\"\npaths: \"*.tsx,src/**/*.ts\"", ""},
		{"paths as list", "name: ui\ndescription: x.\npaths:\n  - \"*.tsx\"\n  - \"src/**/*.ts\"", ""},
		{"empty glob", "name: ui\ndescription: x.\npaths: \"*.tsx,,src/**/*.ts\"", "empty glob"},
		{"nested value", "name: ui\ndescription:\n  text: x\n", "nested value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeSkill(t, t.TempDir(), "SKILL.md", tc.frontmatter, "# body\n")
			res := AuditFile(path)
			if res.FrontmatterIssues == nil {
				t.Fatal("frontmatter_issues must be a list, not null")
			}
			var matched []string
			for _, issue := range res.FrontmatterIssues {
				if tc.wantIssue != "" && strings.Contains(issue, tc.wantIssue) {
					matched = append(matched, issue)
				}
			}
			switch {
			case tc.wantIssue == "" && len(res.FrontmatterIssues) != 0:
				t.Errorf("expected clean, got %v", res.FrontmatterIssues)
			case tc.wantIssue != "" && len(matched) == 0:
				t.Errorf("expected an issue containing %q, got %v", tc.wantIssue, res.FrontmatterIssues)
			}
		})
	}
}

// A YAML failure still reports the name (recovered leniently) so the caller
// can say which domain is broken.
func TestAuditFile_yamlFailureStillReportsName(t *testing.T) {
	path := writeSkill(t, t.TempDir(), "SKILL.md", "name: api\ndescription: A: b", "# body\n")
	res := AuditFile(path)
	if res.Name != "api" {
		t.Errorf("name should be recovered on parse failure, got %q", res.Name)
	}
}

func TestAuditFile_missingFile(t *testing.T) {
	res := AuditFile("/nonexistent/SKILL.md")
	if res.Error == "" {
		t.Error("expected an error for a missing file")
	}
}

func TestCountLines(t *testing.T) {
	// Expected values mirror Python's len(body.splitlines()), which the
	// sibling audit_format.py uses — verified against `python3 -c
	// "print(len(s.splitlines()))"` for each case, including trailing blank
	// lines (a naive strings.TrimRight(body, "\n") would collapse those away
	// and undercount).
	cases := []struct {
		body string
		want int
	}{
		{"", 0},
		{"one line\n", 1},
		{"one line", 1},
		{"line1\nline2\nline3\n", 3},
		{"line1\nline2\nline3", 3},
		{"line1\n\n\n\n", 4},
		{"line1\n\n", 2},
		{"\n", 1},
		{"\n\n", 2},
	}
	for _, tc := range cases {
		if got := countLines(tc.body); got != tc.want {
			t.Errorf("countLines(%q) = %d, want %d", tc.body, got, tc.want)
		}
	}
}

// --- integration with the generator ---
//
// These lock in the division of labor documented in the package comment:
// output.Write manages the body-line budget itself (inline vs. chunked), and
// audit-format's residual job is the frontmatter validity that output.Write
// never checks. If either half changes, one of these should fail rather than
// the subcommand quietly becoming redundant or wrong.

func genSkill(t *testing.T, domain string, nRules int) string {
	t.Helper()
	dir := t.TempDir()
	var rules []state.Rule
	for i := 0; i < nRules; i++ {
		rules = append(rules, state.Rule{
			ID: fmt.Sprintf("rule_%d", i), Status: "approved", Confidence: "established",
			Title:        fmt.Sprintf("Rule number %d about things", i),
			Rule:         strings.Repeat("Do the thing carefully and consistently. ", 5),
			Target:       state.Target{Location: domain, FileGlob: []string{"**/*.go", "src/**/*.go"}},
			DoExamples:   []state.Example{{Code: "good()", Language: "go"}},
			DontExamples: []state.Example{{Code: "bad()", Language: "go"}},
		})
	}
	s := state.Empty()
	s.Rules = rules
	// The description shape Step 11 asks for, colon included, and a glob
	// starting with "*": the generator must quote both so this parses.
	s.DomainDescriptions = map[string]string{domain: "Conventions for " + domain + ": errors, naming. Use when editing src/."}
	skillsDir := filepath.Join(dir, ".claude", "skills")
	if err := output.Write(s, dir, output.Options{SkillsDir: skillsDir}); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(skillsDir, domain, "SKILL.md")
}

// output.Write keeps generated skills within budget on its own, including a
// rule count far past what would fit inline — so audit-format should never
// need to report a size problem for generator output.
func TestGeneratedSkill_staysWithinBudget(t *testing.T) {
	for _, n := range []int{3, 200} {
		res := AuditFile(genSkill(t, "api", n))
		if res.Error != "" {
			t.Fatalf("n=%d: %s", n, res.Error)
		}
		if len(res.FrontmatterIssues) != 0 {
			t.Errorf("n=%d: generated frontmatter should be clean, got %v", n, res.FrontmatterIssues)
		}
		if res.OverBudget || res.ApproachingBudget {
			t.Errorf("n=%d rules: generator emitted a skill at %d body lines (over=%v approaching=%v); "+
				"output.Write's chunking should have prevented this",
				n, res.BodyLines, res.OverBudget, res.ApproachingBudget)
		}
	}
}

// The gap audit-format actually exists to close: output.Write writes the
// domain name to `name:` verbatim, so an invalid domain reaches the file and
// only this check catches it.
func TestGeneratedSkill_invalidDomainNameIsCaught(t *testing.T) {
	res := AuditFile(genSkill(t, "Legacy_API", 3))
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	found := false
	for _, issue := range res.FrontmatterIssues {
		if strings.Contains(issue, "lowercase") {
			found = true
		}
	}
	if !found {
		t.Errorf("domain %q should be flagged for the charset rule, got %v", "Legacy_API", res.FrontmatterIssues)
	}
}
