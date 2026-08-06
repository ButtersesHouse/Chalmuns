package output

import (
	"fmt"
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

	if err := Write(s, dir, Options{}); err != nil {
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

	if err := Write(s, dir, Options{}); err != nil {
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

	if err := Write(s, dir, Options{}); err != nil {
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

func TestWriteCLAUDEMDStatedBeforeEstablishedBeforeEmerging(t *testing.T) {
	dir := t.TempDir()
	s := stateWith(
		approvedRule("Emerging rule", "emerging text", "CLAUDE.md", "emerging", 1),
		approvedRule("Established rule", "established text", "CLAUDE.md", "established", 2),
		approvedRule("Stated rule", "stated text", "CLAUDE.md", "stated", 3),
	)

	if err := Write(s, dir, Options{}); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, filepath.Join(dir, "CLAUDE.md"))
	statedPos := strings.Index(content, "Stated rule")
	estPos := strings.Index(content, "Established rule")
	emgPos := strings.Index(content, "Emerging rule")
	if statedPos == -1 || estPos == -1 || emgPos == -1 {
		t.Fatal("all three rules should be present")
	}
	if statedPos > estPos {
		t.Error("stated rule should appear before established rule")
	}
	if estPos > emgPos {
		t.Error("established rule should appear before emerging rule")
	}
}

func TestWriteSkillFileStatedFirst(t *testing.T) {
	dir := t.TempDir()
	s := stateWith(
		approvedRule("Emerging api", "emerging", "api", "emerging", 1),
		approvedRule("Stated api", "stated", "api", "stated", 2),
		approvedRule("Established api", "established", "api", "established", 3),
	)

	if err := Write(s, dir, Options{}); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, filepath.Join(dir, ".claude", "skills", "api", "SKILL.md"))
	statedPos := strings.Index(content, "Stated api")
	estPos := strings.Index(content, "Established api")
	emgPos := strings.Index(content, "Emerging api")
	if statedPos == -1 || estPos == -1 || emgPos == -1 {
		t.Fatal("all three rules should be present in skill file")
	}
	if statedPos > estPos {
		t.Error("stated rule should appear before established in skill file")
	}
	if estPos > emgPos {
		t.Error("established rule should appear before emerging in skill file")
	}
}

func TestWriteCLAUDEMDMaxThirtyRules(t *testing.T) {
	dir := t.TempDir()
	var rules []state.Rule
	for i := 0; i < 35; i++ {
		rules = append(rules, approvedRule("Rule", "text", "CLAUDE.md", "established", i+1))
	}
	if err := Write(stateWith(rules...), dir, Options{}); err != nil {
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

	if err := Write(s, dir, Options{}); err != nil {
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
	if err := Write(stateWith(r), dir, Options{}); err != nil {
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

func TestWriteCLAUDEMDExamplesBeforeRuleProse(t *testing.T) {
	dir := t.TempDir()
	r := approvedRule("Use errors.As", "Always use errors.As for type checking", "CLAUDE.md", "established", 1)
	r.DoExample = &state.Example{Code: "errors.As(err, &target)", Language: "go"}
	if err := Write(stateWith(r), dir, Options{}); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, filepath.Join(dir, "CLAUDE.md"))
	examplePos := strings.Index(content, "errors.As(err, &target)")
	rulePos := strings.Index(content, "Always use errors.As for type checking")
	if examplePos == -1 || rulePos == -1 {
		t.Fatal("both example and rule prose should be present")
	}
	if examplePos > rulePos {
		t.Error("examples should appear before rule prose")
	}
}

func TestWriteCLAUDEMDPluralExamples(t *testing.T) {
	dir := t.TempDir()
	r := approvedRule("Use errors.As", "Use errors.As", "CLAUDE.md", "established", 1)
	r.DoExamples = []state.Example{
		{Code: "errors.As(err, &target)", Language: "go"},
		{Code: "errors.As(err, &myErr)", Language: "go"},
	}
	r.DontExamples = []state.Example{
		{Code: "err.(*MyErr)", Language: "go"},
	}
	if err := Write(stateWith(r), dir, Options{}); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, filepath.Join(dir, "CLAUDE.md"))
	// CLAUDE.md caps at 1 pair; only first do example should appear
	if !strings.Contains(content, "errors.As(err, &target)") {
		t.Error("first do example missing")
	}
	// second do example should NOT appear in CLAUDE.md (capped at 1 pair)
	if strings.Contains(content, "errors.As(err, &myErr)") {
		t.Error("second do example should not appear in CLAUDE.md (max 1 pair)")
	}
}

func TestWriteSkillFileExamplesMovedToCompanionFile(t *testing.T) {
	dir := t.TempDir()
	r := approvedRule("Use errors.As", "Use errors.As", "api", "established", 1)
	r.DoExamples = []state.Example{
		{Code: "example one code", Language: "go"},
		{Code: "example two code", Language: "go"},
		{Code: "example three code", Language: "go"},
		{Code: "example four code", Language: "go"},
	}
	if err := Write(stateWith(r), dir, Options{}); err != nil {
		t.Fatal(err)
	}

	// SKILL.md carries the rule and a pointer, never the example code.
	skill := readFile(t, filepath.Join(dir, ".claude", "skills", "api", "SKILL.md"))
	if strings.Contains(skill, "example one code") || strings.Contains(skill, "```") {
		t.Error("example code must not be inlined in SKILL.md")
	}
	if !strings.Contains(skill, "_Examples: `examples/use-errors-as.md`_") {
		t.Errorf("SKILL.md should point at the examples file; got:\n%s", skill)
	}

	examples := readFile(t, filepath.Join(dir, ".claude", "skills", "api", "examples", "use-errors-as.md"))
	for _, ex := range []string{"example one code", "example two code", "example three code", "example four code"} {
		if !strings.Contains(examples, ex) {
			t.Errorf("expected %q in examples file", ex)
		}
	}
}

func TestWriteSkillFileExamplesFileHasRuleContext(t *testing.T) {
	dir := t.TempDir()
	r := approvedRule("Use errors.As", "Always use errors.As for type checking", "api", "established", 1)
	r.DoExamples = []state.Example{{Code: "errors.As(err, &target)", Language: "go"}}
	if err := Write(stateWith(r), dir, Options{}); err != nil {
		t.Fatal(err)
	}

	examples := readFile(t, filepath.Join(dir, ".claude", "skills", "api", "examples", "use-errors-as.md"))
	rulePos := strings.Index(examples, "Always use errors.As for type checking")
	examplePos := strings.Index(examples, "errors.As(err, &target)")
	if rulePos == -1 || examplePos == -1 {
		t.Fatal("examples file should contain both the rule text and the example")
	}
	if rulePos > examplePos {
		t.Error("examples file should restate the rule before the example code")
	}
}

func TestWriteSkillFileNoExamplesNoPointer(t *testing.T) {
	dir := t.TempDir()
	r := approvedRule("Bare rule", "just do it", "api", "established", 1)
	if err := Write(stateWith(r), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	skill := readFile(t, filepath.Join(dir, ".claude", "skills", "api", "SKILL.md"))
	if strings.Contains(skill, "_Examples:") {
		t.Error("rule without examples must not link an examples file")
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude", "skills", "api", "examples", "bare-rule.md")); !os.IsNotExist(err) {
		t.Error("no examples file should be written for a rule without examples")
	}
}

func TestWriteSkillFileFileRef(t *testing.T) {
	dir := t.TempDir()
	r := approvedRule("Use errors.As", "Use errors.As", "api", "established", 1)
	r.DoExamples = []state.Example{
		{Code: "errors.As(err, &target)", Language: "go", FileRef: "internal/api/handler.go:L42"},
	}
	if err := Write(stateWith(r), dir, Options{}); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, filepath.Join(dir, ".claude", "skills", "api", "examples", "use-errors-as.md"))
	if !strings.Contains(content, "internal/api/handler.go:L42") {
		t.Error("FileRef should appear in the examples file")
	}
	if !strings.Contains(content, "Real instance: see") {
		t.Error("FileRef label should appear")
	}
}

func TestWriteManualRuleSourceLabel(t *testing.T) {
	dir := t.TempDir()
	// Manual rule: no PR sources, origin "manual".
	r := state.Rule{
		ID: "rule_manual", Title: "Wrap errors with %w", Rule: "Always wrap propagated errors with %w",
		Status: "approved", Confidence: "stated", Origin: "manual",
		Target:  state.Target{Location: "api"},
		Sources: []state.Signal{{Reviewer: "mryave", Snippet: "always wrap with %w", Strength: "explicit"}},
	}
	if err := Write(stateWith(r), dir, Options{}); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, filepath.Join(dir, ".claude", "skills", "api", "SKILL.md"))
	if !strings.Contains(content, "_Source: manually added_") {
		t.Errorf("manual rule should render 'manually added' source label; got:\n%s", content)
	}
	if strings.Contains(content, "PRs #0") || strings.Contains(content, "PRs _") {
		t.Error("manual rule must not render a bogus PR list")
	}
}

func TestPluralExamplesFallsBackToSingular(t *testing.T) {
	dir := t.TempDir()
	// Rule with only singular examples (backward compat)
	r := approvedRule("Old rule", "old rule text", "api", "established", 1)
	r.DoExample = &state.Example{Code: "singular do code", Language: "go"}
	r.DontExample = &state.Example{Code: "singular dont code", Language: "go"}
	if err := Write(stateWith(r), dir, Options{}); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, filepath.Join(dir, ".claude", "skills", "api", "examples", "old-rule.md"))
	if !strings.Contains(content, "singular do code") {
		t.Error("singular do example should appear via fallback")
	}
	if !strings.Contains(content, "singular dont code") {
		t.Error("singular dont example should appear via fallback")
	}
}

// Skill file tests

func TestWriteSkillFileCreated(t *testing.T) {
	dir := t.TempDir()
	s := stateWith(approvedRule("Wrap errors", "use writeError", "api", "established", 1))

	if err := Write(s, dir, Options{}); err != nil {
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
	if err := Write(stateWith(r), dir, Options{}); err != nil {
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

	if err := Write(s, dir, Options{}); err != nil {
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

	if err := Write(s, dir, Options{}); err != nil {
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

	if err := Write(s, dir, Options{}); err != nil {
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

	if err := Write(s, dir, Options{}); err != nil {
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
	desc := buildDescription("api", globs, "")
	if len(desc) > 200 {
		t.Errorf("description should be capped at 200 chars, got %d", len(desc))
	}
	if !strings.HasSuffix(desc, "...") {
		t.Error("truncated description should end with ...")
	}
}

func TestBuildDescriptionOverride(t *testing.T) {
	override := "Conventions for HTTP API endpoints: error responses, validation, auth middleware. Use when editing src/api/"
	desc := buildDescription("api", []string{"src/api/**"}, override)
	if desc != override {
		t.Errorf("override should be used verbatim when present, got %q", desc)
	}
}

func TestBuildDescriptionOverrideTruncated(t *testing.T) {
	override := strings.Repeat("a", 250)
	desc := buildDescription("api", nil, override)
	if len(desc) != 200 {
		t.Errorf("override should be truncated to 200 chars, got %d", len(desc))
	}
	if !strings.HasSuffix(desc, "...") {
		t.Error("truncated override should end with ...")
	}
}

func TestWriteSkillFileUsesDomainDescription(t *testing.T) {
	dir := t.TempDir()
	s := stateWith(approvedRule("API rule", "use writeError", "api", "established", 1))
	s.DomainDescriptions = map[string]string{
		"api": "HTTP API endpoint conventions. Use when editing src/api/.",
	}

	if err := Write(s, dir, Options{}); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, filepath.Join(dir, ".claude", "skills", "api", "SKILL.md"))
	if !strings.Contains(content, "HTTP API endpoint conventions. Use when editing src/api/.") {
		t.Errorf("skill file should use the domain description from state, got:\n%s", content)
	}
	if strings.Contains(content, "Coding conventions for api") {
		t.Error("generic fallback description should not appear when override is provided")
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
	if err := Write(stateWith(r), dir, Options{}); err != nil {
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

func TestRAGHintsAppearsInSkillFile(t *testing.T) {
	dir := t.TempDir()
	r := approvedRule("Use errors.As", "Use errors.As", "api", "established", 1)
	if err := Write(stateWith(r), dir, Options{RAGHints: true}); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, filepath.Join(dir, ".claude", "skills", "api", "SKILL.md"))
	if !strings.Contains(content, "cursor-agent") {
		t.Error("RAG hint should contain cursor-agent command")
	}
	if !strings.Contains(content, "Use errors.As") {
		t.Error("RAG hint should include the rule title")
	}
	if !strings.Contains(content, "Live examples:") {
		t.Error("RAG hint label missing")
	}
}

func TestRAGHintsAbsentWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	r := approvedRule("Use errors.As", "Use errors.As", "api", "established", 1)
	if err := Write(stateWith(r), dir, Options{RAGHints: false}); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, filepath.Join(dir, ".claude", "skills", "api", "SKILL.md"))
	if strings.Contains(content, "cursor-agent") {
		t.Error("cursor-agent hint should not appear when RAGHints is false")
	}
}

func TestRAGHintsNotInCLAUDEMD(t *testing.T) {
	dir := t.TempDir()
	r := approvedRule("Use errors.As", "Use errors.As", "CLAUDE.md", "established", 1)
	if err := Write(stateWith(r), dir, Options{RAGHints: true}); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, filepath.Join(dir, "CLAUDE.md"))
	if strings.Contains(content, "cursor-agent") {
		t.Error("cursor-agent hint should not appear in CLAUDE.md")
	}
}

func TestExemplaryFilesSection(t *testing.T) {
	dir := t.TempDir()
	r := approvedRule("Use errors.As", "Use errors.As", "api", "established", 1)
	r.DoExamples = []state.Example{
		{Code: "errors.As(err, &t)", Language: "go", FileRef: "internal/api/handler.go:L10"},
		{Code: "errors.As(err, &e)", Language: "go", FileRef: "internal/api/handler.go:L55"},
		{Code: "errors.As(err, &m)", Language: "go", FileRef: "internal/api/middleware.go:L20"},
	}
	r2 := approvedRule("Wrap errors", "Wrap errors with context", "api", "established", 2)
	r2.DoExamples = []state.Example{
		{Code: `fmt.Errorf("op: %w", err)`, Language: "go", FileRef: "internal/api/handler.go:L80"},
	}
	if err := Write(stateWith(r, r2), dir, Options{}); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, filepath.Join(dir, ".claude", "skills", "api", "SKILL.md"))
	if !strings.Contains(content, "## Exemplary Files") {
		t.Error("Exemplary Files section missing")
	}
	// handler.go appears 3 times, middleware.go once — handler should be first
	if !strings.Contains(content, "internal/api/handler.go") {
		t.Error("top exemplary file missing")
	}
	handlerPos := strings.Index(content, "internal/api/handler.go")
	middlewarePos := strings.Index(content, "internal/api/middleware.go")
	if handlerPos > middlewarePos {
		t.Error("more-frequent file should appear first in exemplary files")
	}
}

func TestExemplaryFilesSectionAbsentWhenNoFileRefs(t *testing.T) {
	dir := t.TempDir()
	r := approvedRule("Use errors.As", "Use errors.As", "api", "established", 1)
	// No FileRef set on any example
	r.DoExample = &state.Example{Code: "errors.As(err, &t)", Language: "go"}
	if err := Write(stateWith(r), dir, Options{}); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, filepath.Join(dir, ".claude", "skills", "api", "SKILL.md"))
	if strings.Contains(content, "## Exemplary Files") {
		t.Error("Exemplary Files section should not appear when no FileRefs exist")
	}
}

func TestStalenessNoteOnOldRule(t *testing.T) {
	dir := t.TempDir()
	s := stateWith(
		func() state.Rule {
			r := approvedRule("Old convention", "do the old thing", "api", "established", 1)
			r.LastSeenPR = 5
			return r
		}(),
	)
	s.LastExtractedPRNumber = 250 // watermark 245 ahead of last_seen_pr=5

	if err := Write(s, dir, Options{}); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, filepath.Join(dir, ".claude", "skills", "api", "SKILL.md"))
	if !strings.Contains(content, "verify this convention is still current") {
		t.Error("staleness note should appear for rules 200+ PRs behind watermark")
	}
	if !strings.Contains(content, "last seen: PR #5") {
		t.Error("staleness note should include last_seen_pr number")
	}
}

func TestStalenessNoteAbsentForRecentRule(t *testing.T) {
	dir := t.TempDir()
	s := stateWith(
		func() state.Rule {
			r := approvedRule("Recent convention", "do the new thing", "api", "established", 1)
			r.LastSeenPR = 195
			return r
		}(),
	)
	s.LastExtractedPRNumber = 200

	if err := Write(s, dir, Options{}); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, filepath.Join(dir, ".claude", "skills", "api", "SKILL.md"))
	if strings.Contains(content, "verify this convention is still current") {
		t.Error("staleness note should not appear for rules within 200 PRs of watermark")
	}
}

func TestAgentsMDWritten(t *testing.T) {
	dir := t.TempDir()
	universal := approvedRule("No abbreviations", "Never abbreviate identifiers", "CLAUDE.md", "stated", 1)
	domain := approvedRule("API rule", "do it in api", "api", "stated", 2)
	domain.Target.FileGlob = []string{"src/api/**/*.go"}
	if err := Write(stateWith(universal, domain), dir, Options{}); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, filepath.Join(dir, "AGENTS.md"))
	if !strings.Contains(content, "https://agents.md") {
		t.Error("AGENTS.md should name the convention it follows")
	}
	if !strings.Contains(content, "Never abbreviate identifiers") {
		t.Error("AGENTS.md should carry the universal rules")
	}
	if !strings.Contains(content, "`src/api/**/*.go`") {
		t.Error("domain index should list the domain's globs")
	}
	if !strings.Contains(content, filepath.Join(".claude", "skills", "api", "SKILL.md")) {
		t.Errorf("domain index should link the skill file relatively; got:\n%s", content)
	}
}

func TestAgentsMDDomainOnlyStillWritten(t *testing.T) {
	// Even with no universal rules, other agents need the domain index since
	// they never auto-load .claude/skills.
	dir := t.TempDir()
	domain := approvedRule("API rule", "do it in api", "api", "stated", 1)
	domain.Target.FileGlob = []string{"src/api/**/*.go"}
	if err := Write(stateWith(domain), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	content := readFile(t, filepath.Join(dir, "AGENTS.md"))
	if !strings.Contains(content, "## Domain conventions") {
		t.Error("AGENTS.md should be written for domain-only states")
	}
	if strings.Contains(content, "## Universal rules") {
		t.Error("empty universal section should be omitted")
	}
}

func TestAgentsMDNeverClobbersHandWrittenFile(t *testing.T) {
	dir := t.TempDir()
	handWritten := "# My own agent instructions\n\nDo not touch.\n"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(handWritten), 0644); err != nil {
		t.Fatal(err)
	}
	r := approvedRule("Universal rule", "always do it", "CLAUDE.md", "stated", 1)
	if err := Write(stateWith(r), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, "AGENTS.md")); got != handWritten {
		t.Errorf("hand-written AGENTS.md must be left untouched; got:\n%s", got)
	}
}

func TestAgentsMDRegeneratesItsOwnFile(t *testing.T) {
	dir := t.TempDir()
	r := approvedRule("Universal rule", "always do it", "CLAUDE.md", "stated", 1)
	if err := Write(stateWith(r), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	r2 := approvedRule("Second rule", "also do this", "CLAUDE.md", "stated", 2)
	if err := Write(stateWith(r, r2), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	content := readFile(t, filepath.Join(dir, "AGENTS.md"))
	if !strings.Contains(content, "also do this") {
		t.Error("generator-owned AGENTS.md should be regenerated on later runs")
	}
}

func TestAgentsMDSuppressedWithNone(t *testing.T) {
	r := approvedRule("Universal rule", "always do it", "CLAUDE.md", "stated", 1)
	for _, target := range []string{"none", os.DevNull} {
		out := t.TempDir()
		if err := Write(stateWith(r), out, Options{AgentsMDPath: target}); err != nil {
			t.Fatalf("AgentsMDPath=%q: %v", target, err)
		}
		if _, err := os.Stat(filepath.Join(out, "AGENTS.md")); !os.IsNotExist(err) {
			t.Errorf("AgentsMDPath=%q: AGENTS.md should not be written", target)
		}
	}
}

func TestSkillFrontmatterPathsGate(t *testing.T) {
	dir := t.TempDir()
	withGlobs := approvedRule("API rule", "do it", "api", "stated", 1)
	withGlobs.Target.FileGlob = []string{"src/api/**/*.go", "src/api/**/*.sql"}
	noGlobs := approvedRule("Docs rule", "do it", "docs", "stated", 2)
	noGlobs.Target.FileGlob = nil
	if err := Write(stateWith(withGlobs, noGlobs), dir, Options{}); err != nil {
		t.Fatal(err)
	}

	api := readFile(t, filepath.Join(dir, ".claude", "skills", "api", "SKILL.md"))
	if !strings.Contains(api, "paths: src/api/**/*.go, src/api/**/*.sql\n") {
		t.Errorf("skill with globs should emit a paths gate; got header:\n%s", api[:200])
	}
	docs := readFile(t, filepath.Join(dir, ".claude", "skills", "docs", "SKILL.md"))
	if strings.Contains(docs, "paths:") {
		t.Error("skill without globs must omit paths so it can still auto-load")
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Use errors.As for type checks": "use-errors-as-for-type-checks",
		"  Weird -- punctuation!! ":     "weird-punctuation",
		"":                              "rule",
		"ALL CAPS":                      "all-caps",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
	long := slugify(strings.Repeat("very long title ", 10))
	if len(long) > 60 {
		t.Errorf("slug should be capped at 60 chars, got %d", len(long))
	}
}

func TestRuleSlugsDeduped(t *testing.T) {
	rules := []state.Rule{{Title: "Same title"}, {Title: "Same title"}, {Title: "Same title"}}
	slugs := ruleSlugs(rules)
	want := []string{"same-title", "same-title-2", "same-title-3"}
	for i := range want {
		if slugs[i] != want[i] {
			t.Errorf("slug[%d] = %q, want %q", i, slugs[i], want[i])
		}
	}
}

// bigRules builds n approved rules with enough text to force the chunked layout.
func bigRules(n int) []state.Rule {
	var rules []state.Rule
	for i := 0; i < n; i++ {
		r := approvedRule(
			fmt.Sprintf("Convention number %03d with a reasonably long title", i),
			strings.Repeat(fmt.Sprintf("Rule %03d body sentence stating the convention imperatively. ", i), 3),
			"api", "established", i+1)
		r.DoExamples = []state.Example{{Code: fmt.Sprintf("do_example_%03d()", i), Language: "go"}}
		rules = append(rules, r)
	}
	return rules
}

func TestWriteSkillFileChunkedWhenLarge(t *testing.T) {
	dir := t.TempDir()
	if err := Write(stateWith(bigRules(120)...), dir, Options{}); err != nil {
		t.Fatal(err)
	}

	skillDir := filepath.Join(dir, ".claude", "skills", "api")
	skill := readFile(t, filepath.Join(skillDir, "SKILL.md"))
	if got := strings.Count(skill, "\n") + 1; got > maxSkillLines {
		t.Errorf("chunked SKILL.md should stay within %d lines, got %d", maxSkillLines, got)
	}
	if !strings.Contains(skill, "## Rule Index") {
		t.Error("chunked SKILL.md should carry a rule index")
	}
	if !strings.Contains(skill, "grep -ril") {
		t.Error("chunked SKILL.md should mention the grep lookup over rules/")
	}
	if strings.Contains(skill, "do_example_000()") {
		t.Error("chunked SKILL.md must not inline rule bodies or examples")
	}
	if !strings.Contains(skill, "(rules/convention-number-000-with-a-reasonably-long-title.md)") {
		t.Errorf("index should link rule chunk files; got head:\n%s", skill[:600])
	}

	chunk := readFile(t, filepath.Join(skillDir, "rules", "convention-number-000-with-a-reasonably-long-title.md"))
	for _, want := range []string{"do_example_000()", "Rule 000 body sentence", "**Confidence:** established"} {
		if !strings.Contains(chunk, want) {
			t.Errorf("rule chunk missing %q", want)
		}
	}
	// Chunked layout keeps examples inside the chunk — no examples/ dir.
	if _, err := os.Stat(filepath.Join(skillDir, "examples")); !os.IsNotExist(err) {
		t.Error("chunked layout should not also write an examples/ dir")
	}
}

func TestWriteSkillFileSmallStaysSingleFile(t *testing.T) {
	dir := t.TempDir()
	if err := Write(stateWith(bigRules(3)...), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(dir, ".claude", "skills", "api")
	skill := readFile(t, filepath.Join(skillDir, "SKILL.md"))
	if strings.Contains(skill, "## Rule Index") {
		t.Error("small skill should keep the inline rules layout")
	}
	if _, err := os.Stat(filepath.Join(skillDir, "rules")); !os.IsNotExist(err) {
		t.Error("small skill should not write rule chunks")
	}
}

func TestStaleGeneratedFilesRemovedOnRewrite(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, ".claude", "skills", "api")
	// Simulate leftovers from a prior run whose rules were renamed/removed.
	for _, stale := range []string{"examples/old-rule.md", "rules/old-rule.md"} {
		p := filepath.Join(skillDir, stale)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("stale"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	r := approvedRule("Fresh rule", "do the fresh thing", "api", "established", 1)
	r.DoExamples = []state.Example{{Code: "fresh()", Language: "go"}}
	if err := Write(stateWith(r), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	for _, stale := range []string{"examples/old-rule.md", "rules/old-rule.md"} {
		if _, err := os.Stat(filepath.Join(skillDir, stale)); !os.IsNotExist(err) {
			t.Errorf("stale generated file %s should be removed on rewrite", stale)
		}
	}
	if _, err := os.Stat(filepath.Join(skillDir, "examples", "fresh-rule.md")); err != nil {
		t.Errorf("fresh examples file should exist: %v", err)
	}
}

func TestClaudeMDSuppressedWithNone(t *testing.T) {
	// --claude-md none must skip the CLAUDE.md output entirely while still
	// writing skill files (the /dev/null form used to abort write-outputs).
	s := stateWith(
		approvedRule("Universal rule", "always do it", "CLAUDE.md", "stated", 1),
		approvedRule("API rule", "do it in api", "api", "stated", 2),
	)
	for _, target := range []string{"none", os.DevNull} {
		out := t.TempDir()
		if err := Write(s, out, Options{ClaudeMDPath: target, SkillsDir: filepath.Join(out, "skills")}); err != nil {
			t.Fatalf("ClaudeMDPath=%q: %v", target, err)
		}
		if _, err := os.Stat(filepath.Join(out, "CLAUDE.md")); !os.IsNotExist(err) {
			t.Errorf("ClaudeMDPath=%q: CLAUDE.md should not be written", target)
		}
		if _, err := os.Stat(filepath.Join(out, "skills", "api", "SKILL.md")); err != nil {
			t.Errorf("ClaudeMDPath=%q: skill file should still be written: %v", target, err)
		}
	}
}

func TestWriteOutputsIndependentPaths(t *testing.T) {
	// ClaudeMDPath and SkillsDir can be set independently so they don't have
	// to share a common outputDir root — the core of the "clobber" bug.
	repoRoot := t.TempDir()
	customClaude := filepath.Join(t.TempDir(), "docs", "CLAUDE.md")
	customSkills := filepath.Join(t.TempDir(), "my-skills")

	s := stateWith(
		approvedRule("CLAUDE rule", "global rule", "CLAUDE.md", "established", 1),
		approvedRule("API rule", "api rule", "api", "established", 2),
	)
	opts := Options{
		ClaudeMDPath: customClaude,
		SkillsDir:    customSkills,
	}
	if err := Write(s, repoRoot, opts); err != nil {
		t.Fatal(err)
	}

	// CLAUDE.md must land at the custom path, not under repoRoot.
	if _, err := os.Stat(customClaude); err != nil {
		t.Errorf("CLAUDE.md not written to custom path %s: %v", customClaude, err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "CLAUDE.md")); err == nil {
		t.Error("CLAUDE.md must not be written under repoRoot when ClaudeMDPath is set")
	}

	// Skill files must land under customSkills, not under repoRoot/.claude/skills.
	if _, err := os.Stat(filepath.Join(customSkills, "api", "SKILL.md")); err != nil {
		t.Errorf("skill file not written under custom skills dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, ".claude", "skills", "api", "SKILL.md")); err == nil {
		t.Error("skill file must not be written under repoRoot when SkillsDir is set")
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
