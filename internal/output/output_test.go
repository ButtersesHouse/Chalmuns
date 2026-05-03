package output

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ButtersesHouse/Chalmuns/internal/state"
)

// helpers

func approvedRule(title, rule, location, confidence string, prNums ...int) state.Rule {
	var sources []state.Signal
	for _, n := range prNums {
		sources = append(sources, state.Signal{PRNumber: n, Reviewer: "alice", Snippet: "quote"})
	}
	return state.Rule{
		ID: "rule_test", Title: title, Rule: rule,
		Status: "approved", Confidence: confidence,
		Target:      state.Target{Location: location},
		Sources:     sources,
		SignalCount: len(sources),
	}
}

func stateWith(rules ...state.Rule) state.State {
	s := state.Empty()
	s.Rules = rules
	return s
}

// CLAUDE.md tests

func TestWriteCLAUDEMDBasicContent(t *testing.T) {
	dir := t.TempDir()
	s := stateWith(approvedRule("Use errors.As", "Always use errors.As", "CLAUDE.md", "established", 1, 2))

	if err := Write(s, dir); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, filepath.Join(dir, "CLAUDE.md"))
	if !strings.Contains(content, "# Coding Conventions") {
		t.Error("missing heading")
	}
	if !strings.Contains(content, "Use errors.As") {
		t.Error("missing rule title")
	}
	if !strings.Contains(content, "Always use errors.As") {
		t.Error("missing rule text")
	}
	if !strings.Contains(content, "#1") || !strings.Contains(content, "#2") {
		t.Error("missing PR source citations")
	}
}

func TestWriteCLAUDEMDOnlyApproved(t *testing.T) {
	dir := t.TempDir()
	s := stateWith(
		approvedRule("Approved rule", "do this", "CLAUDE.md", "established", 1),
		func() state.Rule {
			r := approvedRule("Proposed rule", "maybe this", "CLAUDE.md", "emerging", 2)
			r.Status = "proposed"
			return r
		}(),
		func() state.Rule {
			r := approvedRule("Rejected rule", "not this", "CLAUDE.md", "established", 3)
			r.Status = "rejected"
			return r
		}(),
	)

	if err := Write(s, dir); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, filepath.Join(dir, "CLAUDE.md"))
	if !strings.Contains(content, "Approved rule") {
		t.Error("approved rule should be present")
	}
	if strings.Contains(content, "Proposed rule") {
		t.Error("proposed rule should not appear in CLAUDE.md")
	}
	if strings.Contains(content, "Rejected rule") {
		t.Error("rejected rule should not appear in CLAUDE.md")
	}
}

func TestWriteCLAUDEMDEstablishedBeforeEmerging(t *testing.T) {
	dir := t.TempDir()
	s := stateWith(
		approvedRule("Emerging rule", "emerging", "CLAUDE.md", "emerging", 1),
		approvedRule("Established rule", "established", "CLAUDE.md", "established", 2),
	)

	if err := Write(s, dir); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, filepath.Join(dir, "CLAUDE.md"))
	estPos := strings.Index(content, "Established rule")
	emgPos := strings.Index(content, "Emerging rule")
	if estPos == -1 || emgPos == -1 {
		t.Fatal("both rules should be present")
	}
	if estPos > emgPos {
		t.Error("established rule should appear before emerging rule")
	}
}

func TestWriteCLAUDEMDMaxThirtyRules(t *testing.T) {
	dir := t.TempDir()
	var rules []state.Rule
	for i := 0; i < 35; i++ {
		rules = append(rules, approvedRule("Rule", "text", "CLAUDE.md", "established", i+1))
	}
	if err := Write(stateWith(rules...), dir); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, filepath.Join(dir, "CLAUDE.md"))
	// count "## Rule" headings
	count := strings.Count(content, "## Rule")
	if count != 30 {
		t.Errorf("expected 30 rules in CLAUDE.md, got %d", count)
	}
}

func TestWriteCLAUDEMDNotCreatedWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	// only a skill-domain rule — no CLAUDE.md rules
	s := stateWith(approvedRule("API rule", "use handler", "api", "established", 1))

	if err := Write(s, dir); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Error("CLAUDE.md should not be created when there are no CLAUDE.md-targeted rules")
	}
}

func TestWriteCLAUDEMDWithExamples(t *testing.T) {
	dir := t.TempDir()
	r := approvedRule("Use errors.As", "Use errors.As", "CLAUDE.md", "established", 1)
	r.DoExample = &state.Example{Code: "errors.As(err, &target)", Language: "go"}
	r.DontExample = &state.Example{Code: "err.(*MyErr)", Language: "go"}
	if err := Write(stateWith(r), dir); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, filepath.Join(dir, "CLAUDE.md"))
	if !strings.Contains(content, "errors.As(err, &target)") {
		t.Error("do example missing")
	}
	if !strings.Contains(content, "err.(*MyErr)") {
		t.Error("dont example missing")
	}
	if !strings.Contains(content, "**Do:**") {
		t.Error("Do label missing")
	}
	if !strings.Contains(content, "**Don't:**") {
		t.Error("Don't label missing")
	}
}

// Skill file tests

func TestWriteSkillFileCreated(t *testing.T) {
	dir := t.TempDir()
	s := stateWith(approvedRule("Wrap errors", "use writeError", "api", "established", 1))

	if err := Write(s, dir); err != nil {
		t.Fatal(err)
	}

	skillPath := filepath.Join(dir, ".claude", "skills", "api", "SKILL.md")
	if _, err := os.Stat(skillPath); os.IsNotExist(err) {
		t.Fatalf("skill file not created at %s", skillPath)
	}
}

func TestWriteSkillFileFrontmatter(t *testing.T) {
	dir := t.TempDir()
	r := approvedRule("Wrap errors", "use writeError", "api", "established", 1)
	r.Target.FileGlob = []string{"internal/api/**/*.go"}
	if err := Write(stateWith(r), dir); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, filepath.Join(dir, ".claude", "skills", "api", "SKILL.md"))
	if !strings.HasPrefix(content, "---\n") {
		t.Error("skill file should start with YAML frontmatter")
	}
	if !strings.Contains(content, "name: api") {
		t.Error("frontmatter missing name field")
	}
	if !strings.Contains(content, "internal/api/**/*.go") {
		t.Error("file glob should appear in description")
	}
}

func TestWriteSkillFileDoesNotContainCLAUDEMDRules(t *testing.T) {
	dir := t.TempDir()
	s := stateWith(
		approvedRule("General rule", "general", "CLAUDE.md", "established", 1),
		approvedRule("API rule", "api specific", "api", "established", 2),
	)

	if err := Write(s, dir); err != nil {
		t.Fatal(err)
	}

	skillContent := readFile(t, filepath.Join(dir, ".claude", "skills", "api", "SKILL.md"))
	if strings.Contains(skillContent, "General rule") {
		t.Error("CLAUDE.md-targeted rule should not appear in skill file")
	}
	if !strings.Contains(skillContent, "API rule") {
		t.Error("domain rule should appear in skill file")
	}
}

func TestWriteMultipleDomains(t *testing.T) {
	dir := t.TempDir()
	s := stateWith(
		approvedRule("API rule", "api thing", "api", "established", 1),
		approvedRule("Auth rule", "auth thing", "auth", "established", 2),
	)

	if err := Write(s, dir); err != nil {
		t.Fatal(err)
	}

	for _, domain := range []string{"api", "auth"} {
		path := filepath.Join(dir, ".claude", "skills", domain, "SKILL.md")
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("skill file for domain %q not created", domain)
		}
	}
}

func TestWriteSkillFileEstablishedBeforeEmerging(t *testing.T) {
	dir := t.TempDir()
	s := stateWith(
		approvedRule("Emerging", "emerging text", "api", "emerging", 1),
		approvedRule("Established", "established text", "api", "established", 2),
	)

	if err := Write(s, dir); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, filepath.Join(dir, ".claude", "skills", "api", "SKILL.md"))
	estPos := strings.Index(content, "Established")
	emgPos := strings.Index(content, "Emerging")
	if estPos > emgPos {
		t.Error("established rule should appear before emerging in skill file")
	}
}

func TestWriteSkillFileNoFile(t *testing.T) {
	dir := t.TempDir()
	// only CLAUDE.md rules — no skill files should be written
	s := stateWith(approvedRule("General", "general", "CLAUDE.md", "established", 1))

	if err := Write(s, dir); err != nil {
		t.Fatal(err)
	}

	skillsDir := filepath.Join(dir, ".claude", "skills")
	if _, err := os.Stat(skillsDir); !os.IsNotExist(err) {
		t.Error(".claude/skills should not be created when there are no domain rules")
	}
}

// description truncation

func TestBuildDescriptionTruncation(t *testing.T) {
	globs := []string{strings.Repeat("a", 200)}
	desc := buildDescription("api", globs)
	if len(desc) > 200 {
		t.Errorf("description should be capped at 200 chars, got %d", len(desc))
	}
	if !strings.HasSuffix(desc, "...") {
		t.Error("truncated description should end with ...")
	}
}

// PR list deduplication (tested via output content)

func TestPRListDeduplicatesInOutput(t *testing.T) {
	dir := t.TempDir()
	r := approvedRule("Rule", "text", "CLAUDE.md", "established")
	// same PR number appears twice in sources
	r.Sources = []state.Signal{
		{PRNumber: 5, Reviewer: "alice", Snippet: "a"},
		{PRNumber: 5, Reviewer: "bob", Snippet: "b"},
		{PRNumber: 3, Reviewer: "carol", Snippet: "c"},
	}
	if err := Write(stateWith(r), dir); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, filepath.Join(dir, "CLAUDE.md"))
	// #5 should appear exactly once; #3 should appear; no duplicate
	if strings.Count(content, "#5") != 1 {
		t.Errorf("PR #5 should appear exactly once, content:\n%s", content)
	}
	if !strings.Contains(content, "#3") {
		t.Error("PR #3 should be present")
	}
}

// atomic write

func TestAtomicWriteNoTmpLeft(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.md")
	if err := atomicWrite(path, "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("tmp file should not exist after atomicWrite")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readFile %s: %v", path, err)
	}
	return string(data)
}
