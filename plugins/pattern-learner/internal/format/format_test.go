package format

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
