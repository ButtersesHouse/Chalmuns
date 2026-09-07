package output

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ButtersesHouse/Chalmuns/internal/format"
	"github.com/ButtersesHouse/Chalmuns/internal/state"
	"gopkg.in/yaml.v3"
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

// promoted renders the managed block for s into a fresh AGENTS.md and returns
// the file content. Universal-rule rendering moved out of a generated
// CLAUDE.md and into the promoted block when top-level writes became opt-in,
// so the rendering assertions that used to run against CLAUDE.md run here.
func promoted(t *testing.T, s state.State) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	if _, err := Promote(s, path, PromoteOptions{Create: true}); err != nil {
		t.Fatal(err)
	}
	return readFile(t, path)
}

// CLAUDE.md tests

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
	if !strings.Contains(content, "name: \"api\"") {
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

func TestWriteNoRulesWritesNoSkills(t *testing.T) {
	dir := t.TempDir()
	if err := Write(state.Empty(), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude", "skills")); !os.IsNotExist(err) {
		t.Error(".claude/skills should not be created when there are no approved rules")
	}
}

// Universal rules get a real skill like everything else, rather than being
// written to a repo-root file.
func TestUniversalRulesBecomeAskill(t *testing.T) {
	dir := t.TempDir()
	s := stateWith(approvedRule("No abbreviations", "Never abbreviate identifiers", UniversalLocation, "stated", 1))
	if err := Write(s, dir, Options{}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, ".claude", "skills", UniversalSkillName, "SKILL.md")
	content := readFile(t, path)
	if !strings.Contains(content, "Never abbreviate identifiers") {
		t.Error("universal rule should appear in the conventions skill")
	}
	if !strings.Contains(content, "name: \""+UniversalSkillName+"\"") {
		t.Error("skill should be named after the universal bucket")
	}
	// A paths gate would hide repo-wide rules on every non-matching file.
	if strings.Contains(content, "\npaths:") {
		t.Errorf("conventions skill must not be paths-gated; got:\n%s", content)
	}
}

// A scoped domain that happens to be named "conventions" merges with the
// universal rules, and the merged skill stays ungated.
func TestUniversalRulesMergeWithCollidingDomain(t *testing.T) {
	dir := t.TempDir()
	scoped := approvedRule("Scoped rule", "scoped behaviour", UniversalSkillName, "stated", 2)
	scoped.Target.FileGlob = []string{"src/**/*.go"}
	s := stateWith(
		approvedRule("Universal rule", "applies everywhere", UniversalLocation, "stated", 1),
		scoped,
	)
	if err := Write(s, dir, Options{}); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, filepath.Join(dir, ".claude", "skills", UniversalSkillName, "SKILL.md"))
	for _, want := range []string{"applies everywhere", "scoped behaviour"} {
		if !strings.Contains(content, want) {
			t.Errorf("merged skill missing %q", want)
		}
	}
	if strings.Contains(content, "\npaths:") {
		t.Errorf("merged skill must stay ungated so universal rules always load; got:\n%s", content)
	}
}

// description truncation

func TestBuildDescriptionTruncation(t *testing.T) {
	globs := []string{strings.Repeat("a", 200)}
	desc := buildDescription("api", globs, false, "")
	if len(desc) > 200 {
		t.Errorf("description should be capped at 200 chars, got %d", len(desc))
	}
	if !strings.HasSuffix(desc, "...") {
		t.Error("truncated description should end with ...")
	}
}

func TestBuildDescriptionOverride(t *testing.T) {
	override := "Conventions for HTTP API endpoints: error responses, validation, auth middleware. Use when editing src/api/"
	desc := buildDescription("api", []string{"src/api/**"}, false, override)
	if desc != override {
		t.Errorf("override should be used verbatim when present, got %q", desc)
	}
}

func TestBuildDescriptionOverrideTruncated(t *testing.T) {
	override := strings.Repeat("a", 250)
	desc := buildDescription("api", nil, false, override)
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
	r := approvedRule("Rule", "text", "CLAUDE.md", "established")
	// same PR number appears twice in sources
	r.Sources = []state.Signal{
		{PRNumber: 5, Reviewer: "alice", Snippet: "a"},
		{PRNumber: 5, Reviewer: "bob", Snippet: "b"},
		{PRNumber: 3, Reviewer: "carol", Snippet: "c"},
	}
	content := promoted(t, stateWith(r))
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
	if !strings.Contains(api, "paths: \"src/api/**/*.go,src/api/**/*.sql\"\n") {
		t.Errorf("skill with globs should emit a paths gate; got header:\n%s", api[:200])
	}
	docs := readFile(t, filepath.Join(dir, ".claude", "skills", "docs", "SKILL.md"))
	if strings.Contains(docs, "paths:") {
		t.Error("skill without globs must omit paths so it can still auto-load")
	}
}

// frontmatter reads back from the generated SKILL.md via a real YAML parser,
// the way Claude Code loads it. Returns the parsed block and the raw file.
func frontmatter(t *testing.T, path string) (map[string]any, string) {
	t.Helper()
	content := readFile(t, path)
	rest := strings.TrimPrefix(content, "---\n")
	if rest == content {
		t.Fatalf("no frontmatter in %s:\n%s", path, content)
	}
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		t.Fatalf("unterminated frontmatter in %s:\n%s", path, content)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(rest[:end]), &doc); err != nil {
		t.Fatalf("frontmatter in %s is not valid YAML (%v):\n%s", path, err, content)
	}
	return doc, content
}

// The two shapes the pipeline produces routinely, and which an unquoted
// frontmatter cannot carry: a description with ": " in it (the form Step 11
// of SKILL.md asks the model to write) and a glob that starts with "*" (a
// YAML alias when unquoted). Both must round-trip through a strict parser.
func TestSkillFrontmatterIsValidYAML(t *testing.T) {
	dir := t.TempDir()
	r := approvedRule("Hooks in hooks dir", "do it", "components", "stated", 1)
	r.Target.FileGlob = []string{"*.tsx", "**/*.jsx", "src/components/**/*.ts"}
	s := stateWith(r)
	s.DomainDescriptions = map[string]string{
		"components": `React conventions: hooks, props, "render" helpers. Use when editing *.tsx files`,
	}
	if err := Write(s, dir, Options{}); err != nil {
		t.Fatal(err)
	}

	doc, _ := frontmatter(t, filepath.Join(dir, ".claude", "skills", "components", "SKILL.md"))
	if got := doc["name"]; got != "components" {
		t.Errorf("name = %v", got)
	}
	if got := doc["description"]; got != s.DomainDescriptions["components"] {
		t.Errorf("description round-trip lost content: %v", got)
	}
	if got := doc["paths"]; got != "*.tsx,**/*.jsx,src/components/**/*.ts" {
		t.Errorf("paths = %v", got)
	}
}

// A byte-indexed cut of a 200+ char description can land inside a multi-byte
// character and leave invalid UTF-8 in the frontmatter.
func TestBuildDescriptionTruncatesOnRunes(t *testing.T) {
	long := strings.Repeat("é", 250) // 2 bytes each
	desc := buildDescription("api", nil, false, long)
	if !utf8.ValidString(desc) {
		t.Fatal("truncated description is not valid UTF-8")
	}
	if n := utf8.RuneCountInString(desc); n > maxDescriptionRunes {
		t.Errorf("description is %d runes, want <= %d", n, maxDescriptionRunes)
	}
	if !strings.HasSuffix(desc, "...") {
		t.Error("truncated description should end in an ellipsis")
	}
	if short := buildDescription("api", nil, false, "short"); short != "short" {
		t.Errorf("short description should be untouched, got %q", short)
	}
}

// Domain names are model-supplied; one that is not a single path segment
// must be refused rather than written outside the skills directory.
func TestWriteRejectsPathUnsafeDomain(t *testing.T) {
	for _, bad := range []string{"../escape", "api/v2", `api\v2`, "..", "."} {
		dir := t.TempDir()
		err := Write(stateWith(approvedRule("r", "do it", bad, "stated", 1)), dir, Options{})
		if err == nil {
			t.Errorf("domain %q should be rejected", bad)
		}
	}
}

func TestHeadingTitle(t *testing.T) {
	cases := map[string]string{
		"api":        "API",
		"rest-api":   "REST API",
		"components": "Components",
		"db_models":  "DB Models",
		"auth":       "Auth",
		"état-api":   "État API", // first rune is multi-byte
		"---":        "---",      // no words: fall back to the raw domain
		"i18n":       "i18n",     // numeronyms keep their customary form
		"k8s-config": "k8s Config",
		"grpc":       "gRPC",
	}
	for in, want := range cases {
		if got := headingTitle(in); got != want {
			t.Errorf("headingTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// The RAG hint is a shell command; a title with a quote must not break it.
func TestRAGHintEscapesShellMetacharacters(t *testing.T) {
	dir := t.TempDir()
	r := approvedRule("Use \"errors.As\" for `$checks`", "do it", "api", "stated", 1)
	if err := Write(stateWith(r), dir, Options{RAGHints: true}); err != nil {
		t.Fatal(err)
	}
	content := readFile(t, filepath.Join(dir, ".claude", "skills", "api", "SKILL.md"))
	if !strings.Contains(content, `'Use \"errors.As\" for \$checks'`) {
		t.Errorf("hint should escape quotes and dollars and drop backticks; got:\n%s", content)
	}
	// The hint must remain one intact code span.
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "_Live examples:") && strings.Count(line, "`") != 2 {
			t.Errorf("hint line should contain exactly one code span: %s", line)
		}
	}
}

// Every rune the generator might be handed must survive a strict YAML
// round-trip, including the C1 controls and non-characters the spec forbids
// raw even inside quotes.
func TestYAMLQuoteRoundTripsAllRunes(t *testing.T) {
	var runes []rune
	for r := rune(0); r <= 0x2ff; r++ {
		runes = append(runes, r)
	}
	runes = append(runes, 0x2028, 0x2029, 0xfeff, 0xfffd, 0xfffe, 0xffff, 0x1f600)
	for _, r := range runes {
		// Spaces on both sides: a rune YAML treats as a line break would
		// otherwise be folded together with them.
		in := "a " + string(r) + " b"
		var back string
		if err := yaml.Unmarshal([]byte(yamlQuote(in)), &back); err != nil {
			t.Errorf("U+%04X: %s does not parse: %v", r, yamlQuote(in), err)
		} else if back != in {
			t.Errorf("U+%04X: round-trip changed the value: %q -> %q", r, in, back)
		}
	}
}

// A skill written by the generator before the marker line existed must still
// be recognised so it can be pruned on the first run of the new binary.
func TestPrunesLegacyGeneratedSkills(t *testing.T) {
	dir := t.TempDir()
	skills := filepath.Join(dir, ".claude", "skills")
	legacy := filepath.Join(skills, "components")
	if err := os.MkdirAll(legacy, 0755); err != nil {
		t.Fatal(err)
	}
	old := "---\nname: components\ndescription: Component conventions.\n---\n\n# Components Conventions\n\n## Rules\n\n" +
		generatedBodyLines[0] + "\n\n### Old rule\n\nDo it.\n\n_Source: PRs #1_\n"
	if err := os.WriteFile(filepath.Join(legacy, "SKILL.md"), []byte(old), 0644); err != nil {
		t.Fatal(err)
	}
	// The same generated content copied to a directory of another name is a
	// user's adopted skill, not ours: it must survive.
	adopted := filepath.Join(skills, "components-style")
	if err := os.MkdirAll(adopted, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(adopted, "SKILL.md"), []byte(old), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Write(stateWith(approvedRule("New rule", "do it", "ui", "stated", 1)), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Error("legacy generated skill should have been pruned")
	}
	if _, err := os.Stat(filepath.Join(adopted, "SKILL.md")); err != nil {
		t.Errorf("a copied-and-renamed skill must be preserved: %v", err)
	}
}

// A marker-era skill copied under a different directory name is likewise
// left alone: the frontmatter name no longer matches the directory.
func TestCopiedMarkerSkillIsNotPruned(t *testing.T) {
	dir := t.TempDir()
	skills := filepath.Join(dir, ".claude", "skills")
	if err := Write(stateWith(approvedRule("Rule", "do it", "api", "stated", 1)), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	src := readFile(t, filepath.Join(skills, "api", "SKILL.md"))
	copyDir := filepath.Join(skills, "api-mine")
	if err := os.MkdirAll(copyDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(copyDir, "SKILL.md"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Write(state.Empty(), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(skills, "api")); !os.IsNotExist(err) {
		t.Error("original generated skill should have been pruned")
	}
	if _, err := os.Stat(filepath.Join(copyDir, "SKILL.md")); err != nil {
		t.Errorf("copied skill must be preserved: %v", err)
	}
}

// Two domains that differ only by case would share one directory on a
// case-insensitive filesystem; refuse the run instead of losing one.
func TestCaseCollidingDomainsRejected(t *testing.T) {
	err := Write(stateWith(
		approvedRule("A", "do it", "Api", "stated", 1),
		approvedRule("B", "do it", "api", "stated", 2),
	), t.TempDir(), Options{})
	if err == nil || !strings.Contains(err.Error(), "differ only by case") {
		t.Errorf("expected a case-collision error, got %v", err)
	}
}

// A hand-written skill whose directory name collides with a model-chosen
// domain must never be overwritten; the run is refused with nothing written.
func TestRefusesToOverwriteHandWrittenSkill(t *testing.T) {
	dir := t.TempDir()
	skills := filepath.Join(dir, ".claude", "skills")
	mine := filepath.Join(skills, "api")
	if err := os.MkdirAll(filepath.Join(mine, "rules"), 0755); err != nil {
		t.Fatal(err)
	}
	handWritten := "---\nname: api\ndescription: My own API skill.\n---\n\nMy steps.\n"
	if err := os.WriteFile(filepath.Join(mine, "SKILL.md"), []byte(handWritten), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mine, "rules", "mine.md"), []byte("keep me"), 0644); err != nil {
		t.Fatal(err)
	}
	err := Write(stateWith(
		approvedRule("Auth rule", "do it", "auth", "stated", 1),
		approvedRule("API rule", "do it", "api", "stated", 2),
	), dir, Options{})
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("expected a refusal, got %v", err)
	}
	if got := readFile(t, filepath.Join(mine, "SKILL.md")); got != handWritten {
		t.Error("hand-written SKILL.md was modified")
	}
	if _, err := os.Stat(filepath.Join(mine, "rules", "mine.md")); err != nil {
		t.Error("hand-written companion file was removed")
	}
	if _, err := os.Stat(filepath.Join(skills, "auth")); !os.IsNotExist(err) {
		t.Error("nothing should be written when the run is refused")
	}
}

// Two repositories sharing one skills directory must not prune each other's
// generated skills; each run only owns the skills stamped with its repo.
func TestSharedSkillsDirPrunesOnlyOwnSkills(t *testing.T) {
	// A shared directory lives outside every repository that writes to it.
	skills := filepath.Join(t.TempDir(), "shared")
	repoA, repoB, repoC := t.TempDir(), t.TempDir(), t.TempDir()
	opts := Options{SkillsDir: skills}

	a := stateWith(approvedRule("A rule", "do it", "api", "stated", 1))
	a.Repo = state.RepoInfo{Owner: "acme", Repo: "alpha"}
	if err := Write(a, repoA, opts); err != nil {
		t.Fatal(err)
	}
	content := readFile(t, filepath.Join(skills, "api", "SKILL.md"))
	if !strings.Contains(content, "<!-- pattern-learner:repo=acme/alpha -->") {
		t.Errorf("generated skill should carry the owner stamp; got:\n%s", content)
	}

	b := stateWith(approvedRule("B rule", "do it", "ui", "stated", 1))
	b.Repo = state.RepoInfo{Owner: "acme", Repo: "beta"}
	if err := Write(b, repoB, opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(skills, "api", "SKILL.md")); err != nil {
		t.Error("repo beta must not prune repo alpha's generated skill")
	}

	// Alpha drops its domain: its own skill goes, beta's stays.
	a2 := state.Empty()
	a2.Repo = a.Repo
	if err := Write(a2, repoA, opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(skills, "api")); !os.IsNotExist(err) {
		t.Error("alpha should prune its own stale skill")
	}
	if _, err := os.Stat(filepath.Join(skills, "ui", "SKILL.md")); err != nil {
		t.Error("alpha must not prune beta's skill")
	}

	// A run with no repo identity of its own neither prunes nor overwrites
	// a stamped skill in the shared directory.
	if err := Write(state.Empty(), repoC, opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(skills, "ui", "SKILL.md")); err != nil {
		t.Error("an unowned run must not prune a stamped skill")
	}
	unowned := stateWith(approvedRule("C rule", "do it", "ui", "stated", 1))
	if err := Write(unowned, repoC, opts); err == nil || !strings.Contains(err.Error(), "unidentified repository") {
		t.Errorf("an unowned run must not overwrite another repo's stamped skill, got %v", err)
	}
	if got := readFile(t, filepath.Join(skills, "ui", "SKILL.md")); !strings.Contains(got, "B rule") {
		t.Error("beta's skill should be untouched")
	}
}

// In a shared directory an unstamped generated skill (written before
// stamping) cannot be attributed to a repository, so no run prunes it; a
// run whose live domain collides with it regenerates it, which is the only
// way it ever becomes stamped.
func TestSharedDirLeavesUnstampedSkillsToTheirOwner(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "shared")
	legacy := filepath.Join(shared, "ui")
	if err := os.MkdirAll(legacy, 0755); err != nil {
		t.Fatal(err)
	}
	old := "---\nname: ui\ndescription: UI conventions.\n---\n\n# Ui Conventions\n\n## Rules\n\n" +
		generatedBodyLines[0] + "\n\n### Old rule\n\nDo it.\n"
	if err := os.WriteFile(filepath.Join(legacy, "SKILL.md"), []byte(old), 0644); err != nil {
		t.Fatal(err)
	}
	a := stateWith(approvedRule("A rule", "do it", "api", "stated", 1))
	a.Repo = state.RepoInfo{Owner: "acme", Repo: "alpha"}
	if err := Write(a, t.TempDir(), Options{SkillsDir: shared}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(legacy, "SKILL.md")); err != nil {
		t.Error("an unstamped skill in a shared directory must not be pruned by another repo")
	}
	b := stateWith(approvedRule("B rule", "do it", "ui", "stated", 1))
	b.Repo = state.RepoInfo{Owner: "acme", Repo: "beta"}
	if err := Write(b, t.TempDir(), Options{SkillsDir: shared}); err != nil {
		t.Fatalf("a live domain regenerates an unstamped generated skill: %v", err)
	}
	if got := readFile(t, filepath.Join(legacy, "SKILL.md")); !strings.Contains(got, ownerLine("acme/beta")) {
		t.Error("regenerated skill should now be stamped")
	}
}

// Inside the repo's own tree, ownership ignores the stamp for pruning as
// well as for overwriting: after a fork or rename, a stale skill stamped
// with the previous identity is still pruned.
func TestForkPrunesStaleSkillWithOldStamp(t *testing.T) {
	dir := t.TempDir()
	skills := filepath.Join(dir, ".claude", "skills")
	a := stateWith(approvedRule("Rule", "do it", "components", "stated", 1))
	a.Repo = state.RepoInfo{Owner: "acme", Repo: "alpha"}
	if err := Write(a, dir, Options{}); err != nil {
		t.Fatal(err)
	}
	fork := stateWith(approvedRule("Rule", "do it", "ui", "stated", 1))
	fork.Repo = state.RepoInfo{Owner: "bob", Repo: "alpha"}
	if err := Write(fork, dir, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(skills, "components")); !os.IsNotExist(err) {
		t.Error("stale skill stamped with the old identity should be pruned inside the repo tree")
	}
}

// The universal skill has a fixed name, so the shared-directory refusal
// must point at the only workable remedy.
func TestSharedDirUniversalConflictMessage(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "shared")
	a := stateWith(approvedRule("A", "do it", UniversalLocation, "stated", 1))
	a.Repo = state.RepoInfo{Owner: "acme", Repo: "alpha"}
	if err := Write(a, t.TempDir(), Options{SkillsDir: shared}); err != nil {
		t.Fatal(err)
	}
	b := stateWith(approvedRule("B", "do it", UniversalLocation, "stated", 1))
	b.Repo = state.RepoInfo{Owner: "acme", Repo: "beta"}
	err := Write(b, t.TempDir(), Options{SkillsDir: shared})
	if err == nil || !strings.Contains(err.Error(), "own skills directory") {
		t.Errorf("expected the universal-skill remedy, got %v", err)
	}
}

// Universal rules never emit a paths gate, so their globs are not validated
// and a comma in one cannot refuse the run.
func TestUniversalGlobsNotValidated(t *testing.T) {
	r := approvedRule("U", "do it", UniversalLocation, "stated", 1)
	r.Target.FileGlob = []string{"src/[a,b]/**/*.go"}
	if err := Write(stateWith(r), t.TempDir(), Options{}); err != nil {
		t.Errorf("universal rule globs must not be validated: %v", err)
	}
}

// Globs are joined with commas in `paths`, so a brace group is expanded and
// empty entries are dropped before the join.
func TestCollectGlobsExpandsBracesAndDropsEmpties(t *testing.T) {
	r := approvedRule("R", "do it", "api", "stated", 1)
	r.Target.FileGlob = []string{"", "src/{a,b}/**/*.go", "lib/{x,{y,z}}/*.ts", "src/a/**/*.go"}
	globs, err := collectGlobs([]state.Rule{r})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(globs, "|")
	want := "src/a/**/*.go|src/b/**/*.go|lib/x/*.ts|lib/y/*.ts|lib/z/*.ts"
	if got != want {
		t.Errorf("collectGlobs = %q, want %q", got, want)
	}
	for _, bad := range []string{"src/{a/**/*.go", "src/a}/**", "src/{a,{b}/x"} {
		if _, err := ExpandBraces(bad); err == nil {
			t.Errorf("ExpandBraces(%q) should report an unbalanced brace", bad)
		}
	}
	if x, err := ExpandBraces("plain/*.go"); err != nil || len(x) != 1 || x[0] != "plain/*.go" {
		t.Errorf("brace-free glob should pass through, got %v, %v", x, err)
	}

	// A comma that survives expansion cannot be represented: the run must
	// refuse rather than silently widen the paths gate.
	bad := approvedRule("R", "do it", "api", "stated", 1)
	bad.Target.FileGlob = []string{"src/[a,b]/**/*.go"}
	if err := Write(stateWith(bad), t.TempDir(), Options{}); err == nil || !strings.Contains(err.Error(), "contains a comma") {
		t.Errorf("expected a comma-glob refusal, got %v", err)
	}
	// An empty brace alternative cannot be expressed in a paths gate; refuse.
	for _, g := range []string{"src/{a,}/**/*.go", "{,src}/**/*.go", "src/{}/x.go", "**/*.{ts,}", "lib/{x,{y,}}/*.ts"} {
		empty := approvedRule("R", "do it", "api", "stated", 1)
		empty.Target.FileGlob = []string{g}
		if err := Write(stateWith(empty), t.TempDir(), Options{}); err == nil || !strings.Contains(err.Error(), "empty brace alternative") {
			t.Errorf("%s: expected an empty-alternative refusal, got %v", g, err)
		}
	}
	// A brace-free glob that ends in a slash is the author's choice, not an
	// empty alternative, and is accepted as before.
	plain := approvedRule("R", "do it", "api", "stated", 1)
	plain.Target.FileGlob = []string{"docs/"}
	if err := Write(stateWith(plain), t.TempDir(), Options{}); err != nil {
		t.Errorf("trailing-slash glob should be accepted: %v", err)
	}
	// An unbalanced brace is named as such.
	unbalanced := approvedRule("R", "do it", "api", "stated", 1)
	unbalanced.Target.FileGlob = []string{"src/{a,b/**/*.go"}
	if err := Write(stateWith(unbalanced), t.TempDir(), Options{}); err == nil || !strings.Contains(err.Error(), "unbalanced brace") {
		t.Errorf("expected an unbalanced-brace refusal, got %v", err)
	}
}

func TestIsOutsideRel(t *testing.T) {
	sep := string(filepath.Separator)
	for rel, want := range map[string]bool{
		"..": true, ".." + sep + "x": true, "x": false, ".": false, "..skills": false, "x" + sep + "..": false,
	} {
		if got := IsOutsideRel(rel); got != want {
			t.Errorf("IsOutsideRel(%q) = %v, want %v", rel, got, want)
		}
	}
}

// RepoRoot, not outputDir, decides whether the skills directory is shared.
func TestRepoRootDecidesSharing(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "work", "repo")
	shared := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	// Another repo's stamped skill in the user-level directory.
	other := filepath.Join(shared, "api")
	if err := os.MkdirAll(other, 0755); err != nil {
		t.Fatal(err)
	}
	stamped := "---\nname: \"api\"\ndescription: \"x\"\n---\n\n" + GeneratedMarker + "\n" + ownerLine("acme/other") + "\n\nbody\n"
	if err := os.WriteFile(filepath.Join(other, "SKILL.md"), []byte(stamped), 0644); err != nil {
		t.Fatal(err)
	}
	a := stateWith(approvedRule("Rule", "do it", "ui", "stated", 1))
	a.Repo = state.RepoInfo{Owner: "acme", Repo: "alpha"}
	// outputDir is $HOME (the command was run from there), which contains
	// the shared directory; RepoRoot says the repo is elsewhere.
	if err := Write(a, home, Options{SkillsDir: shared, RepoRoot: repo}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(other, "SKILL.md")); err != nil {
		t.Error("another repo's skill in the user-level directory was pruned")
	}
}

// A fork or renamed repo changes the stamp, not the fact that the skill was
// generated: a live domain's generated skill is regenerated and re-stamped.
func TestForkRegeneratesStampedSkill(t *testing.T) {
	dir := t.TempDir()
	a := stateWith(approvedRule("Rule", "do it", "api", "stated", 1))
	a.Repo = state.RepoInfo{Owner: "acme", Repo: "alpha"}
	if err := Write(a, dir, Options{}); err != nil {
		t.Fatal(err)
	}
	fork := stateWith(approvedRule("Rule", "do it again", "api", "stated", 1))
	fork.Repo = state.RepoInfo{Owner: "bob", Repo: "alpha"}
	if err := Write(fork, dir, Options{}); err != nil {
		t.Fatalf("fork must be able to regenerate a generated skill: %v", err)
	}
	content := readFile(t, filepath.Join(dir, ".claude", "skills", "api", "SKILL.md"))
	if !strings.Contains(content, "do it again") || !strings.Contains(content, ownerLine("bob/alpha")) {
		t.Errorf("skill should be regenerated and re-stamped; got:\n%s", content)
	}
	// An unowned run regenerates too.
	if err := Write(stateWith(approvedRule("Rule", "third", "api", "stated", 1)), dir, Options{}); err != nil {
		t.Fatalf("unowned run must be able to regenerate a generated skill: %v", err)
	}
}

// On a case-sensitive filesystem a re-cased domain leaves the old casing as
// a separate directory; it is stale and must be pruned, while on a
// case-insensitive one the single directory is the live skill and must stay.
func TestRecasedDomainPrunesOldCasing(t *testing.T) {
	dir := t.TempDir()
	skills := filepath.Join(dir, ".claude", "skills")
	if err := Write(stateWith(approvedRule("Rule", "do it", "Api", "stated", 1)), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	if err := Write(stateWith(approvedRule("Rule", "do it", "api", "stated", 1)), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(skills)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	caseSensitive := len(names) == 2 || (len(names) == 1 && names[0] == "api")
	if caseSensitive && (len(names) != 1 || names[0] != "api") {
		t.Errorf("old casing should be pruned on a case-sensitive filesystem, got %v", names)
	}
	if len(names) != 1 {
		t.Errorf("exactly one skill directory should remain, got %v", names)
	}
}

// Stamp writing and parsing share one format.
func TestOwnerLineRoundTrip(t *testing.T) {
	got, ok := parseOwnerLine(ownerLine("acme/alpha"))
	if !ok || got != "acme/alpha" {
		t.Errorf("parseOwnerLine(ownerLine(x)) = %q, %v", got, ok)
	}
	if _, ok := parseOwnerLine("<!-- something else -->"); ok {
		t.Error("non-stamp line should not parse as a stamp")
	}
}

// The name is read through the YAML parser, so quoting styles and comments
// are handled the way the consuming agent handles them.
func TestFrontmatterNameParsing(t *testing.T) {
	cases := map[string]string{
		"---\nname: \"caf\\u00e9\"\ndescription: \"x\"\n---\nbody":     "café",
		"---\nname: 'it''s'\ndescription: x\n---\nbody":                "it's",
		"---\nname: api # trailing comment\ndescription: x\n---\nbody": "api",
		"---\nname:\ndescription: \"x\"\n---\nbody":                    "",
		"no frontmatter": "",
		// Not valid YAML (legacy unquoted output): the lenient fallback
		// still recovers the name.
		"---\nname: api\ndescription: a: b\npaths: *.go\n---\nbody": "api",
	}
	for in, want := range cases {
		fields, _, _ := format.ParseFrontmatter(in)
		if got := fields["name"]; got != want {
			t.Errorf("ParseFrontmatter(%q) name = %q, want %q", in, got, want)
		}
	}
}

func TestValidDomainRejectsOSInvalidNames(t *testing.T) {
	for _, bad := range []string{strings.Repeat("a", 256), "api\x00", "api\tx", "a:b", "a?b", "a*b", "a<b", "a|b", `a"b`,
		"aux", "CON", "com1", "nul.md", "lpt9", "api.", "api "} {
		if validDomain(bad) {
			t.Errorf("validDomain(%q) should be false", bad)
		}
	}
	for _, ok := range []string{"api", "rest-api", "état", strings.Repeat("a", maxDomainBytes-transientExtraBytes)} {
		if !validDomain(ok) {
			t.Errorf("validDomain(%q) should be true", ok)
		}
	}
}

// The name line is read back through the YAML parser so an escaped name
// still identifies its directory.
func TestIsGeneratedSkillUnquotesName(t *testing.T) {
	dir := t.TempDir()
	content := "---\nname: \"caf\\u00e9\"\ndescription: \"x\"\n---\n\n" + GeneratedMarker + "\n\nbody\n"
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	if !inspectSkill(path, "café").generated {
		t.Error("escaped name should match its directory after unquoting")
	}
}

// A .claude/skills that is a symlink into a shared directory is judged by
// its target, so the shared-directory rules apply to it.
func TestSymlinkedSkillsDirIsShared(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "shared")
	if err := os.MkdirAll(shared, 0755); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(repo, ".claude", "skills")
	if err := os.Symlink(shared, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if !isSharedSkillsDir(link, repo) {
		t.Error("a symlink into a directory outside the repo is shared")
	}
	// A not-yet-existing subdirectory of the link resolves through it.
	if !isSharedSkillsDir(filepath.Join(link, "sub"), repo) {
		t.Error("a missing path under the symlink resolves through it")
	}
	// Another repo's stamped skill in the shared target must survive a run
	// through the symlink.
	other := filepath.Join(shared, "api")
	if err := os.MkdirAll(other, 0755); err != nil {
		t.Fatal(err)
	}
	stamped := "---\nname: \"api\"\ndescription: \"x\"\n---\n\n" + GeneratedMarker + "\n" + ownerLine("acme/other") + "\n\nbody\n"
	if err := os.WriteFile(filepath.Join(other, "SKILL.md"), []byte(stamped), 0644); err != nil {
		t.Fatal(err)
	}
	a := stateWith(approvedRule("Rule", "do it", "ui", "stated", 1))
	a.Repo = state.RepoInfo{Owner: "acme", Repo: "alpha"}
	if err := Write(a, repo, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(other, "SKILL.md")); err != nil {
		t.Error("another repo's skill in the symlinked shared directory was pruned")
	}
}

// Skills written by the 0.2.0 generator have unquoted frontmatter that is
// not valid YAML (a ": " in the description, a "*"-leading glob). They must
// still be recognised as generated, so the upgrade run regenerates them and
// prunes the stale ones instead of refusing.
func TestLegacyUnparseableFrontmatterIsRecognised(t *testing.T) {
	dir := t.TempDir()
	skills := filepath.Join(dir, ".claude", "skills")
	legacy := "---\nname: api\ndescription: Conventions for HTTP API endpoints: error response format. Use when editing src/api/.\npaths: *.go, src/**/*.go\n---\n\n# Api Conventions\n\n## Rules\n\n" +
		generatedBodyLines[0] + "\n\n### Old rule\n\nDo it.\n\n_Source: PRs #1_\n"
	for _, d := range []string{"api", "old"} {
		if err := os.MkdirAll(filepath.Join(skills, d), 0755); err != nil {
			t.Fatal(err)
		}
		content := strings.Replace(legacy, "name: api", "name: "+d, 1)
		if err := os.WriteFile(filepath.Join(skills, d, "SKILL.md"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := Write(stateWith(approvedRule("New rule", "do it", "api", "stated", 1)), dir, Options{}); err != nil {
		t.Fatalf("upgrade run must regenerate a legacy skill: %v", err)
	}
	if got := readFile(t, filepath.Join(skills, "api", "SKILL.md")); !strings.Contains(got, GeneratedMarker) {
		t.Error("legacy skill should have been regenerated with the marker")
	}
	if _, err := os.Stat(filepath.Join(skills, "old")); !os.IsNotExist(err) {
		t.Error("stale legacy skill with unparseable frontmatter should have been pruned")
	}
}

// In a shared skills directory, a domain another repository generated must
// not be clobbered; inside the repo's own tree a foreign stamp is a fork or
// rename and is regenerated (TestForkRegeneratesStampedSkill).
func TestSharedDirRefusesOtherRepoDomain(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "shared")
	repoA, repoB := t.TempDir(), t.TempDir()
	a := stateWith(approvedRule("A rule", "do it", "api", "stated", 1))
	a.Repo = state.RepoInfo{Owner: "acme", Repo: "alpha"}
	if err := Write(a, repoA, Options{SkillsDir: shared}); err != nil {
		t.Fatal(err)
	}
	b := stateWith(approvedRule("B rule", "do it", "api", "stated", 1))
	b.Repo = state.RepoInfo{Owner: "acme", Repo: "beta"}
	err := Write(b, repoB, Options{SkillsDir: shared})
	if err == nil || !strings.Contains(err.Error(), "was generated for repository") {
		t.Fatalf("expected a shared-directory refusal, got %v", err)
	}
	if got := readFile(t, filepath.Join(shared, "api", "SKILL.md")); !strings.Contains(got, "A rule") {
		t.Error("alpha's skill should be untouched")
	}
	// The same repo regenerating its own skill in the shared dir is fine.
	if err := Write(a, repoA, Options{SkillsDir: shared}); err != nil {
		t.Errorf("same owner should regenerate: %v", err)
	}
}

func TestIsSharedSkillsDir(t *testing.T) {
	root := t.TempDir()
	if isSharedSkillsDir(filepath.Join(root, ".claude", "skills"), root) {
		t.Error("a skills dir inside the repo is not shared")
	}
	if isSharedSkillsDir(root, root) {
		t.Error("the repo root itself is not shared")
	}
	if !isSharedSkillsDir(filepath.Join(t.TempDir(), "skills"), root) {
		t.Error("a skills dir outside the repo is shared")
	}
}

// An empty directory at a domain's path, or one holding only the temp file
// of an older build's interrupted write, holds nothing to lose and is used.
func TestEmptyDirectoryIsRegenerated(t *testing.T) {
	for _, leftover := range []string{"", "SKILL.md.tmp"} {
		dir := t.TempDir()
		skillDir := filepath.Join(dir, ".claude", "skills", "api")
		if err := os.MkdirAll(skillDir, 0755); err != nil {
			t.Fatal(err)
		}
		if leftover != "" {
			if err := os.WriteFile(filepath.Join(skillDir, leftover), []byte("partial"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		if err := Write(stateWith(approvedRule("Rule", "do it", "api", "stated", 1)), dir, Options{}); err != nil {
			t.Errorf("leftover %q: expected regeneration, got %v", leftover, err)
		}
	}
}

// A run interrupted between swapDir's two renames leaves only the retired
// copy; the next run restores it before rendering, so a render failure
// cannot destroy the last copy, and a domain that vanished is pruned from
// the restored directory like any other.
func TestInterruptedSwapIsRecovered(t *testing.T) {
	dir := t.TempDir()
	skills := filepath.Join(dir, ".claude", "skills")
	if err := Write(stateWith(approvedRule("Rule", "old text", "api", "stated", 1)), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(skills, "api"), transientPath(filepath.Join(skills, "api"), retiredSuffix)); err != nil {
		t.Fatal(err)
	}
	if err := Write(stateWith(approvedRule("Rule", "new text", "api", "stated", 1)), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(skills, "api", "SKILL.md")); !strings.Contains(got, "new text") {
		t.Error("live skill should be the regenerated one")
	}
	if left := transientEntries(t, skills); len(left) != 0 {
		t.Errorf("retired copy should be gone after a successful swap, found %v", left)
	}
	// A stale domain mid-swap is restored and then pruned like any other.
	if err := os.Rename(filepath.Join(skills, "api"), transientPath(filepath.Join(skills, "api"), retiredSuffix)); err != nil {
		t.Fatal(err)
	}
	if err := Write(stateWith(approvedRule("Rule", "x", "ui", "stated", 1)), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(skills, "api")); !os.IsNotExist(err) {
		t.Error("restored stale domain should have been pruned")
	}
	if left := transientEntries(t, skills); len(left) != 0 {
		t.Errorf("no transient should remain, found %v", left)
	}
}

// Validate reports what Write would refuse, without touching the tree, and
// the Prepared it returns writes the run it validated.
func TestValidateReportsWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	bad := stateWith(
		approvedRule("Good", "do it", "api", "stated", 1),
		approvedRule("Bad", "do it", "api/v2", "stated", 2),
	)
	if _, err := Validate(&bad, dir, Options{}); err == nil {
		t.Fatal("expected a validation error")
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude")); !os.IsNotExist(err) {
		t.Error("Validate must not create anything")
	}
	good := stateWith(approvedRule("Good", "do it", "api", "stated", 1))
	prepared, err := Validate(&good, dir, Options{})
	if err != nil {
		t.Fatalf("valid state should pass: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude")); !os.IsNotExist(err) {
		t.Error("Validate must not create anything")
	}
	// Enrichment after validation (what anchoring does) is seen by Write
	// through the pointer ...
	good.Rules[0].DoExamples = []state.Example{{Code: "x()", Language: "go", FileRef: "a.go:L1"}}
	if err := prepared.Write(); err != nil {
		t.Fatalf("prepared write: %v", err)
	}
	if got := readFile(t, filepath.Join(dir, ".claude", "skills", "api", "examples", "good.md")); !strings.Contains(got, "a.go:L1") {
		t.Error("enrichment made after Validate should be written")
	}
	// ... and so is a change that would have failed validation: Write
	// re-checks rather than rendering a stale, never-validated gate.
	good.Rules[0].Target.FileGlob = []string{"src/{a,b"}
	if err := prepared.Write(); err == nil || !strings.Contains(err.Error(), "unbalanced brace") {
		t.Errorf("a glob changed after validation must be re-checked, got %v", err)
	}
}

// A retired copy whose live slot holds only a dangling symlink is restored
// (the link is cleared), not folded into the link.
func TestRetiredCopyRestoredOverDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	skills := filepath.Join(dir, ".claude", "skills")
	if err := Write(stateWith(approvedRule("Rule", "v1", "api", "stated", 1)), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	retired := transientPath(filepath.Join(skills, "api"), retiredSuffix)
	if err := os.Rename(filepath.Join(skills, "api"), retired); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(retired, "notes.md"), []byte("mine"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "gone"), filepath.Join(skills, "api")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := Write(stateWith(approvedRule("Rule", "v2", "api", "stated", 1)), dir, Options{}); err != nil {
		t.Fatalf("run should recover and regenerate: %v", err)
	}
	if got := readFile(t, filepath.Join(skills, "api", "SKILL.md")); !strings.Contains(got, "v2") {
		t.Error("skill should be regenerated")
	}
	if _, err := os.Stat(filepath.Join(skills, "api", "notes.md")); err != nil {
		t.Error("user file from the restored copy should be carried into the regenerated skill")
	}
	if left := transientEntries(t, skills); len(left) != 0 {
		t.Errorf("no transient should remain, found %v", left)
	}
}

// An absolute target with the CLI's relative default skills dir still gets
// a correct relative link.
func TestPromoteLinksAbsoluteTargetWithRelativeSkillsDir(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)
	s := stateWith(approvedRule("API rule", "do it", "api", "stated", 1))
	target := filepath.Join(dir, "docs", "CLAUDE.md")
	if _, err := Promote(s, target, PromoteOptions{SkillsDir: filepath.Join(".claude", "skills"), Create: true}); err != nil {
		t.Fatal(err)
	}
	content := readFile(t, target)
	want := "`" + filepath.Join("..", ".claude", "skills", "api", "SKILL.md") + "`"
	if !strings.Contains(content, want) {
		t.Errorf("expected link %s; got:\n%s", want, content)
	}
}

// A retired copy whose live slot is an empty directory (regenerable, as
// validateDisk treats it) is folded and cleared.
func TestRetiredCopyFoldedIntoEmptyLiveDir(t *testing.T) {
	dir := t.TempDir()
	skills := filepath.Join(dir, ".claude", "skills")
	if err := Write(stateWith(approvedRule("Rule", "v1", "api", "stated", 1)), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	retired := transientPath(filepath.Join(skills, "api"), retiredSuffix)
	if err := os.Rename(filepath.Join(skills, "api"), retired); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(retired, "notes.md"), []byte("mine"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(skills, "api"), 0755); err != nil {
		t.Fatal(err)
	}
	next := stateWith(approvedRule("Rule", "v2", "api", "stated", 1))
	prepared, err := Validate(&next, dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Write(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(skills, "api", "notes.md")); got != "mine" {
		t.Error("user file should be folded into the regenerated skill")
	}
	if left := transientEntries(t, skills); len(left) != 0 || len(prepared.Warnings()) != 0 {
		t.Errorf("no leftover or warning expected, got %v, %v", left, prepared.Warnings())
	}
}

// A promote target below the repo root gets links relative to its own
// directory, climbing out with "..", when the skills dir is given
// relative to the repo root as the CLI does.
func TestPromoteLinksRelativeToTargetDirectory(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)
	s := stateWith(approvedRule("API rule", "do it", "api", "stated", 1))
	target := filepath.Join("docs", "AGENTS.md")
	if _, err := Promote(s, target, PromoteOptions{SkillsDir: filepath.Join(".claude", "skills"), Create: true}); err != nil {
		t.Fatal(err)
	}
	content := readFile(t, target)
	want := "`" + filepath.Join("..", ".claude", "skills", "api", "SKILL.md") + "`"
	if !strings.Contains(content, want) {
		t.Errorf("expected link %s relative to docs/; got:\n%s", want, content)
	}
}

// A domain name carrying a transient suffix would be cleared by prune.
func TestTransientSuffixDomainRejected(t *testing.T) {
	for _, bad := range []string{filepath.Base(transientPath("api", stagingSuffix)), filepath.Base(transientPath("api", retiredSuffix))} {
		if validDomain(bad) {
			t.Errorf("validDomain(%q) should be false", bad)
		}
	}
}

// Fingerprints count only in the header region, so a hand-written skill
// that quotes a generator sentence or the marker in its body is left alone.
func TestQuotedFingerprintInBodyIsNotGenerated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	content := "---\nname: api\ndescription: My skill.\n---\n\n# API\n\n## Rules\n\n### About pattern-learner\n\n" +
		"Its generated files start with " + GeneratedMarker + "\n\n" + exemplaryFilesIntro + "\n\n" + ownerLine("acme/other") + "\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	info := inspectSkill(path, "api")
	if info.generated || info.stamped {
		t.Errorf("quoted fingerprints in the body must not classify the skill as generated: %+v", info)
	}
	// The real header layout still classifies.
	real := "---\nname: \"api\"\ndescription: \"x\"\n---\n\n" + GeneratedMarker + "\n" + ownerLine("acme/alpha") + "\n\n# API Conventions\n\n## Rules\n\n" + inlineRulesIntro + "\n\n### Rule\n"
	if err := os.WriteFile(path, []byte(real), 0644); err != nil {
		t.Fatal(err)
	}
	if info := inspectSkill(path, "api"); !info.generated || info.stamp != "acme/alpha" {
		t.Errorf("generated header should classify: %+v", info)
	}
}

// A dangling symlink at a domain's path is cleared, not retired, so the
// run and every later run succeed.
func TestDanglingSymlinkAtDomainPathIsReplaced(t *testing.T) {
	dir := t.TempDir()
	skills := filepath.Join(dir, ".claude", "skills")
	if err := os.MkdirAll(skills, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "no-such-target"), filepath.Join(skills, "api")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	s := stateWith(approvedRule("Rule", "do it", "api", "stated", 1))
	for i := 0; i < 2; i++ {
		if err := Write(s, dir, Options{}); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	if !inspectSkill(filepath.Join(skills, "api", "SKILL.md"), "api").generated {
		t.Error("skill should have been written in place of the dangling link")
	}
	if left := transientEntries(t, skills); len(left) != 0 {
		t.Errorf("no transient sibling should remain, found %v", left)
	}
}

// transientEntries lists the staging/retired siblings present under dir.
func transientEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if isTransientDir(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

// A live symlinked domain directory is refused rather than replaced.
func TestSymlinkedLiveDomainIsRefused(t *testing.T) {
	dir := t.TempDir()
	skills := filepath.Join(dir, ".claude", "skills")
	if err := Write(stateWith(approvedRule("Rule", "do it", "api", "stated", 1)), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "api-target")
	if err := os.Rename(filepath.Join(skills, "api"), target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(skills, "api")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "notes.md"), []byte("mine"), 0644); err != nil {
		t.Fatal(err)
	}
	err := Write(stateWith(approvedRule("Rule", "changed", "api", "stated", 1)), dir, Options{})
	if err == nil || !strings.Contains(err.Error(), "is a symlink") {
		t.Fatalf("expected a symlink refusal, got %v", err)
	}
	if fi, err := os.Lstat(filepath.Join(skills, "api")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink must be left in place")
	}
	if _, err := os.Stat(filepath.Join(target, "notes.md")); err != nil {
		t.Error("the link target's files must be untouched")
	}
}

// Another repository's leftover transient in a shared directory (named with
// its own random tail) cannot block this repository's swap.
func TestForeignTransientDoesNotBlockSwap(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "shared")
	a := stateWith(approvedRule("Rule", "v1", "api", "stated", 1))
	a.Repo = state.RepoInfo{Owner: "acme", Repo: "alpha"}
	if err := Write(a, t.TempDir(), Options{SkillsDir: shared}); err != nil {
		t.Fatal(err)
	}
	foreign := transientPath(filepath.Join(shared, "api"), retiredSuffix)
	if err := os.MkdirAll(foreign, 0755); err != nil {
		t.Fatal(err)
	}
	theirs := "---\nname: \"api\"\ndescription: \"x\"\n---\n\n" + GeneratedMarker + "\n" + ownerLine("acme/beta") + "\n\nbody\n"
	if err := os.WriteFile(filepath.Join(foreign, "SKILL.md"), []byte(theirs), 0644); err != nil {
		t.Fatal(err)
	}
	a2 := stateWith(approvedRule("Rule", "v2", "api", "stated", 1))
	a2.Repo = a.Repo
	if err := Write(a2, t.TempDir(), Options{SkillsDir: shared}); err != nil {
		t.Fatalf("regeneration must not be blocked by another repo's leftover: %v", err)
	}
	if got := readFile(t, filepath.Join(shared, "api", "SKILL.md")); !strings.Contains(got, "v2") {
		t.Error("skill should be regenerated")
	}
	if _, err := os.Stat(filepath.Join(foreign, "SKILL.md")); err != nil {
		t.Error("the other repo's leftover must be left for it")
	}
	left := transientEntries(t, shared)
	if len(left) != 1 || left[0] != filepath.Base(foreign) {
		t.Errorf("only the foreign leftover should remain, found %v", left)
	}
}

// In a shared directory, a retired copy whose live slot another repository
// has since taken is left alone rather than folded into their skill.
func TestSharedDirRetiredCopyNotFoldedIntoForeignSkill(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "shared")
	retired := transientPath(filepath.Join(shared, "api"), retiredSuffix)
	if err := os.MkdirAll(retired, 0755); err != nil {
		t.Fatal(err)
	}
	ours := "---\nname: \"api\"\ndescription: \"x\"\n---\n\n" + GeneratedMarker + "\n" + ownerLine("a/one") + "\n\nbody\n"
	if err := os.WriteFile(filepath.Join(retired, "SKILL.md"), []byte(ours), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(retired, "NOTES.md"), []byte("mine"), 0644); err != nil {
		t.Fatal(err)
	}
	// Repo B took the slot meanwhile.
	b := stateWith(approvedRule("B rule", "do it", "api", "stated", 1))
	b.Repo = state.RepoInfo{Owner: "b", Repo: "two"}
	if err := Write(b, t.TempDir(), Options{SkillsDir: shared}); err != nil {
		t.Fatal(err)
	}
	// Repo A runs without the domain: its retired copy stays untouched.
	a := state.Empty()
	a.Repo = state.RepoInfo{Owner: "a", Repo: "one"}
	if err := Write(a, t.TempDir(), Options{SkillsDir: shared}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(retired, "NOTES.md")); err != nil {
		t.Error("our retired copy must not be removed while another repo holds the live slot")
	}
	if _, err := os.Stat(filepath.Join(shared, "api", "NOTES.md")); !os.IsNotExist(err) {
		t.Error("our files must not be moved into another repo's skill")
	}
}

// The staging sibling is the longest name written, so the length check
// must leave room for its suffix and tail — and a domain at the limit
// must actually write, end to end.
func TestValidDomainLeavesRoomForSuffix(t *testing.T) {
	limit := maxDomainBytes - transientExtraBytes
	longest := strings.Repeat("a", limit)
	if !validDomain(longest) {
		t.Error("a domain that fits with its sibling names should be valid")
	}
	if validDomain(strings.Repeat("a", limit+1)) {
		t.Error("a domain whose staging name would exceed the limit should be invalid")
	}
	dir := t.TempDir()
	if err := Write(stateWith(approvedRule("Rule", "do it", longest, "stated", 1)), dir, Options{}); err != nil {
		t.Fatalf("a domain at the limit must write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude", "skills", longest, "SKILL.md")); err != nil {
		t.Errorf("skill at the limit should exist: %v", err)
	}
}

// Only names of exactly the shape transientPath produces (optionally
// without the random tail) are transients; a user's file or directory that
// merely contains the marker is not.
func TestIsTransientDirShape(t *testing.T) {
	for _, yes := range []string{transientPath("api", stagingSuffix), transientPath("api", retiredSuffix)} {
		if !isTransientDir(yes) {
			t.Errorf("isTransientDir(%q) should be true", yes)
		}
	}
	for _, no := range []string{"README.pattern-learner-old.md", "api.pattern-learner-old-backup", "api", ".pattern-learner-old", "api.pattern-learner-old-XYZ", "api" + retiredSuffix, "api" + stagingSuffix} {
		if isTransientDir(no) {
			t.Errorf("isTransientDir(%q) should be false", no)
		}
	}
	if live, ok := liveOfRetired(transientPath("api", retiredSuffix)); !ok || live != "api" {
		t.Errorf("liveOfRetired = %q, %v", live, ok)
	}
	if _, ok := liveOfRetired(transientPath("api", stagingSuffix)); ok {
		t.Error("a staging sibling is not a retired one")
	}
}

// A user's file whose name merely contains the marker survives a run.
func TestUserFileWithMarkerLikeNameSurvives(t *testing.T) {
	dir := t.TempDir()
	skills := filepath.Join(dir, ".claude", "skills")
	for _, keepDir := range []string{"api.pattern-learner-old-backup", "api" + retiredSuffix} {
		if err := os.MkdirAll(filepath.Join(skills, keepDir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(skills, "README.pattern-learner-old.md"), []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Write(stateWith(approvedRule("Rule", "do it", "api", "stated", 1)), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{"api.pattern-learner-old-backup", "api" + retiredSuffix, "README.pattern-learner-old.md"} {
		if _, err := os.Lstat(filepath.Join(skills, keep)); err != nil {
			t.Errorf("%s should survive: %v", keep, err)
		}
	}
}

// The paths gate follows rendering order, not state order, so reordering
// rules in state does not churn the frontmatter.
func TestPathsGateFollowsRenderOrder(t *testing.T) {
	dir := t.TempDir()
	zed := approvedRule("Zed", "z", "api", "emerging", 1)
	zed.Target.FileGlob = []string{"b/**"}
	alpha := approvedRule("Alpha", "a", "api", "stated", 2)
	alpha.Target.FileGlob = []string{"a/**"}
	if err := Write(stateWith(zed, alpha), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	doc, _ := frontmatter(t, filepath.Join(dir, ".claude", "skills", "api", "SKILL.md"))
	if got := doc["paths"]; got != "a/**,b/**" {
		t.Errorf("paths = %v, want stated rule's glob first", got)
	}
}

// The temp file of an older build's interrupted write is debris, not a
// user file, and is not carried into the regenerated directory.
func TestLeftoverTmpNotCarriedOver(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, ".claude", "skills", "api")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md.tmp"), []byte("partial"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Write(stateWith(approvedRule("Rule", "do it", "api", "stated", 1)), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md.tmp")); !os.IsNotExist(err) {
		t.Error("stray temp file should not survive regeneration")
	}
}

// Pruning a stale generated skill removes only what the generator wrote;
// a user's file kept beside it stays, and the directory goes only when
// empty.
func TestPruneKeepsUserFiles(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, ".claude", "skills", "api")
	if err := Write(stateWith(approvedRule("Rule", "do it", "api", "stated", 1)), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "notes.md"), []byte("mine"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Write(stateWith(approvedRule("Rule", "do it", "http-api", "stated", 1)), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); !os.IsNotExist(err) {
		t.Error("stale SKILL.md should be removed")
	}
	if got := readFile(t, filepath.Join(skillDir, "notes.md")); got != "mine" {
		t.Error("user file should survive pruning")
	}
	// Without user files the directory itself goes.
	other := t.TempDir()
	if err := Write(stateWith(approvedRule("Rule", "do it", "api", "stated", 1)), other, Options{}); err != nil {
		t.Fatal(err)
	}
	if err := Write(stateWith(approvedRule("Rule", "do it", "ui", "stated", 1)), other, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(other, ".claude", "skills", "api")); !os.IsNotExist(err) {
		t.Error("an emptied stale directory should be removed")
	}
}

// A hand-written skill that appears between Validate and Write (the slow
// anchoring window) is still refused.
func TestPreparedWriteRechecksDisk(t *testing.T) {
	dir := t.TempDir()
	s := stateWith(approvedRule("Rule", "do it", "api", "stated", 1))
	prepared, err := Validate(&s, dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(dir, ".claude", "skills", "api")
	if err := os.MkdirAll(mine, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mine, "SKILL.md"), []byte("---\nname: api\ndescription: mine\n---\n\nMine.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Write(); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Errorf("expected the disk re-check to refuse, got %v", err)
	}
}

// Braces inside a character class are class members, not a group.
func TestExpandBracesSkipsCharacterClasses(t *testing.T) {
	for _, g := range []string{"src/[{}]*.js", "src/[{]*.ts", "src/[]{]x", "src/[!{]*.go"} {
		out, err := ExpandBraces(g)
		if err != nil || len(out) != 1 || out[0] != g {
			t.Errorf("ExpandBraces(%q) = %v, %v; want the glob unchanged", g, out, err)
		}
	}
	out, err := ExpandBraces("src/{a,[,]}/x")
	if err != nil || len(out) != 2 || out[0] != "src/a/x" || out[1] != "src/[,]/x" {
		t.Errorf("a comma inside a class must not split the group: %v, %v", out, err)
	}
}

func TestNormalizeOwnerKey(t *testing.T) {
	cases := map[string]string{
		"Acme/Alpha": "acme/alpha", "/acme/alpha/": "acme/alpha",
		"acme": "", "a/b/c": "", "acme/al pha": "", "acme/al\npha": "", "": "",
	}
	for in, want := range cases {
		if got := NormalizeOwnerKey(in); got != want {
			t.Errorf("NormalizeOwnerKey(%q) = %q, want %q", in, got, want)
		}
	}
	if got := OwnerKey("acme", "al\npha"); got != "" {
		t.Errorf("OwnerKey with a newline should be empty, got %q", got)
	}
}

// A retired copy left in a shared directory because another repo holds
// the slot is reported as a warning rather than passed over silently.
func TestSharedDirLeftoverIsWarned(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "shared")
	retired := transientPath(filepath.Join(shared, "api"), retiredSuffix)
	if err := os.MkdirAll(retired, 0755); err != nil {
		t.Fatal(err)
	}
	ours := "---\nname: \"api\"\ndescription: \"x\"\n---\n\n" + GeneratedMarker + "\n" + ownerLine("a/one") + "\n\nbody\n"
	if err := os.WriteFile(filepath.Join(retired, "SKILL.md"), []byte(ours), 0644); err != nil {
		t.Fatal(err)
	}
	b := stateWith(approvedRule("B rule", "do it", "api", "stated", 1))
	b.Repo = state.RepoInfo{Owner: "b", Repo: "two"}
	if err := Write(b, t.TempDir(), Options{SkillsDir: shared}); err != nil {
		t.Fatal(err)
	}
	a := state.Empty()
	a.Repo = state.RepoInfo{Owner: "a", Repo: "one"}
	prepared, err := Validate(&a, t.TempDir(), Options{SkillsDir: shared})
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Write(); err != nil {
		t.Fatal(err)
	}
	if w := prepared.Warnings(); len(w) != 1 || !strings.Contains(w[0], filepath.Base(retired)) {
		t.Errorf("expected one warning naming the leftover, got %v", w)
	}
}

// A scoped domain that merely shares the universal skill's name keeps its
// paths gate and heading when no universal rules exist.
func TestConventionsDomainWithoutUniversalRulesStaysGated(t *testing.T) {
	dir := t.TempDir()
	r := approvedRule("Legacy rule", "do it", UniversalSkillName, "stated", 1)
	r.Target.FileGlob = []string{"src/legacy/**/*.go"}
	if err := Write(stateWith(r), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	doc, content := frontmatter(t, filepath.Join(dir, ".claude", "skills", UniversalSkillName, "SKILL.md"))
	if got := doc["paths"]; got != "src/legacy/**/*.go" {
		t.Errorf("scoped 'conventions' domain should keep its paths gate, got %v", got)
	}
	if strings.Contains(content, "Repo-Wide Conventions") {
		t.Error("scoped 'conventions' domain must not be presented as repo-wide")
	}
	// And its globs are validated like any other domain's.
	bad := approvedRule("Legacy rule", "do it", UniversalSkillName, "stated", 1)
	bad.Target.FileGlob = []string{"src/{a,b"}
	if err := Write(stateWith(bad), t.TempDir(), Options{}); err == nil {
		t.Error("a scoped 'conventions' domain's globs must be validated")
	}
}

// A retired copy whose live slot is a resolving symlink is left alone and
// reported; nothing is moved through the link.
func TestRetiredCopyNotFoldedThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	skills := filepath.Join(dir, ".claude", "skills")
	if err := Write(stateWith(approvedRule("Rule", "v1", "api", "stated", 1)), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	retired := transientPath(filepath.Join(skills, "api"), retiredSuffix)
	if err := os.Rename(filepath.Join(skills, "api"), retired); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(retired, "notes.md"), []byte("mine"), 0644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "api-target")
	if err := Write(stateWith(approvedRule("Rule", "t", "api", "stated", 1)), filepath.Dir(filepath.Dir(target)), Options{SkillsDir: filepath.Dir(target)}); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(filepath.Dir(target), "api"), filepath.Join(skills, "api")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	next := stateWith(approvedRule("Rule", "x", "ui", "stated", 1))
	prepared, err := Validate(&next, dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Write(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(target), "api", "notes.md")); !os.IsNotExist(err) {
		t.Error("nothing may be moved through the symlink")
	}
	if _, err := os.Stat(filepath.Join(retired, "notes.md")); err != nil {
		t.Error("the leftover must be kept")
	}
	if w := prepared.Warnings(); len(w) != 1 || !strings.Contains(w[0], "is now a symlink") {
		t.Errorf("expected a symlink warning, got %v", w)
	}
}

func TestNormalizeOwnerKeyStripsGitSuffix(t *testing.T) {
	if got := NormalizeOwnerKey("Acme/Widgets.git"); got != "acme/widgets" {
		t.Errorf("got %q", got)
	}
}

func TestBuildDescriptionWhitespaceOverride(t *testing.T) {
	if got := buildDescription("api", []string{"src/**"}, false, "  \n \t"); got == "" || !strings.Contains(got, "api") {
		t.Errorf("whitespace-only override should fall back to the generated description, got %q", got)
	}
}

// Leftovers of an interrupted swap are settled by the next run: staging
// trees are cleared, a retired copy with no live directory is restored
// (and then pruned if its domain is gone), and a live directory is only
// ever complete.
func TestTransientDirsAreCleared(t *testing.T) {
	dir := t.TempDir()
	skills := filepath.Join(dir, ".claude", "skills")
	if err := Write(stateWith(approvedRule("Rule", "do it", "old", "stated", 1)), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(skills, "old"), transientPath(filepath.Join(skills, "old"), retiredSuffix)); err != nil {
		t.Fatal(err)
	}
	for _, leftover := range []string{filepath.Base(transientPath("api", stagingSuffix)), filepath.Base(transientPath("gone", stagingSuffix))} {
		if err := os.MkdirAll(filepath.Join(skills, leftover), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skills, leftover, "SKILL.md"), []byte("partial"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := Write(stateWith(approvedRule("Rule", "do it", "api", "stated", 1)), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(skills)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "api" {
			t.Errorf("unexpected entry left behind: %s", e.Name())
		}
	}
	if !inspectSkill(filepath.Join(skills, "api", "SKILL.md"), "api").generated {
		t.Error("live skill should be the complete generated one")
	}
}

// A symlinked skill directory is judged by what it points at, for pruning
// as well as for overwriting.
func TestSymlinkedStaleSkillIsPruned(t *testing.T) {
	dir := t.TempDir()
	skills := filepath.Join(dir, ".claude", "skills")
	if err := Write(stateWith(approvedRule("Rule", "do it", "api", "stated", 1)), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	// Move the generated directory aside and link it back under its name.
	moved := filepath.Join(t.TempDir(), "api-real")
	if err := os.Rename(filepath.Join(skills, "api"), moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, filepath.Join(skills, "api")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := Write(stateWith(approvedRule("Rule", "do it", "ui", "stated", 1)), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(skills, "api")); !os.IsNotExist(err) {
		t.Error("stale symlinked generated skill should have been pruned")
	}
	// Only the link goes; the target keeps its contents.
	if _, err := os.Stat(filepath.Join(moved, "SKILL.md")); err != nil {
		t.Error("the link target's SKILL.md must survive pruning the link")
	}
}

// A retired copy is folded only into a generated live skill; a hand-written
// one at the slot is left alone and the leftover is reported.
func TestRetiredCopyNotFoldedIntoHandWrittenSkill(t *testing.T) {
	dir := t.TempDir()
	skills := filepath.Join(dir, ".claude", "skills")
	if err := Write(stateWith(approvedRule("Rule", "v1", "api", "stated", 1)), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	retired := transientPath(filepath.Join(skills, "api"), retiredSuffix)
	if err := os.Rename(filepath.Join(skills, "api"), retired); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(retired, "notes.md"), []byte("mine"), 0644); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(skills, "api")
	if err := os.MkdirAll(mine, 0755); err != nil {
		t.Fatal(err)
	}
	handWritten := "---\nname: api\ndescription: mine\n---\n\nMine.\n"
	if err := os.WriteFile(filepath.Join(mine, "SKILL.md"), []byte(handWritten), 0644); err != nil {
		t.Fatal(err)
	}
	next := stateWith(approvedRule("Rule", "x", "ui", "stated", 1))
	prepared, err := Validate(&next, dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Write(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(mine, "notes.md")); !os.IsNotExist(err) {
		t.Error("nothing may be moved into a hand-written skill")
	}
	if got := readFile(t, filepath.Join(mine, "SKILL.md")); got != handWritten {
		t.Error("hand-written skill must be untouched")
	}
	if _, err := os.Stat(filepath.Join(retired, "notes.md")); err != nil {
		t.Error("the leftover must be kept for the user")
	}
	if w := prepared.Warnings(); len(w) != 1 || !strings.Contains(w[0], "was not written by pattern-learner") {
		t.Errorf("expected a warning about the leftover, got %v", w)
	}
}

// A user's regular file at a domain's path is never deleted to make room
// for a retired copy.
func TestRetiredCopyLeavesUserFileAtSlot(t *testing.T) {
	dir := t.TempDir()
	skills := filepath.Join(dir, ".claude", "skills")
	if err := Write(stateWith(approvedRule("Rule", "v1", "api", "stated", 1)), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	retired := transientPath(filepath.Join(skills, "api"), retiredSuffix)
	if err := os.Rename(filepath.Join(skills, "api"), retired); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skills, "api"), []byte("a note"), 0644); err != nil {
		t.Fatal(err)
	}
	next := stateWith(approvedRule("Rule", "x", "ui", "stated", 1))
	prepared, err := Validate(&next, dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Write(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(skills, "api")); got != "a note" {
		t.Error("user's file at the slot must be untouched")
	}
	if _, err := os.Stat(filepath.Join(retired, "SKILL.md")); err != nil {
		t.Error("the leftover must be kept")
	}
	if w := prepared.Warnings(); len(w) != 1 {
		t.Errorf("expected one warning, got %v", w)
	}
}

// Backslash-escaped braces are literal characters, as path.Match reads them.
func TestExpandBracesHonoursEscapes(t *testing.T) {
	for _, g := range []string{`src/\{legacy\}/**/*.go`, `src/\{a/**`, `a\,b`} {
		out, err := ExpandBraces(g)
		if err != nil || len(out) != 1 || out[0] != g {
			t.Errorf("ExpandBraces(%q) = %v, %v; want the glob unchanged", g, out, err)
		}
	}
	out, err := ExpandBraces(`src/{a\,b,c}/x`)
	if err != nil || len(out) != 2 || out[0] != `src/a\,b/x` || out[1] != "src/c/x" {
		t.Errorf("an escaped comma must not split the group: %v, %v", out, err)
	}
}

// A domain directory that exists without a SKILL.md is not ours either:
// wiping its examples/ and rules/ would destroy whatever a user keeps there.
func TestRefusesDirectoryWithoutSkillFile(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, ".claude", "skills", "api", "rules", "checklist.md")
	if err := os.MkdirAll(filepath.Dir(keep), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("mine"), 0644); err != nil {
		t.Fatal(err)
	}
	err := Write(stateWith(approvedRule("Rule", "do it", "api", "stated", 1)), dir, Options{})
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("expected a refusal, got %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Error("user content under the directory was removed")
	}
}

func TestOwnerKey(t *testing.T) {
	if got := OwnerKey("Acme", "Alpha"); got != "acme/alpha" {
		t.Errorf("OwnerKey = %q", got)
	}
	if OwnerKey("", "x") != "" || OwnerKey("x", "") != "" {
		t.Error("OwnerKey should be empty when a part is missing")
	}
	for _, ok := range []string{"acme/alpha", "a-b/c.d"} {
		if !ValidOwnerKey(ok) {
			t.Errorf("ValidOwnerKey(%q) should be true", ok)
		}
	}
	for _, bad := range []string{"", "acme", "acme/", "/alpha", "a/b/c", "any owner", "a b/c"} {
		if ValidOwnerKey(bad) {
			t.Errorf("ValidOwnerKey(%q) should be false", bad)
		}
	}
}

// A bad domain must fail the run before anything is written or pruned.
func TestBadDomainWritesNothing(t *testing.T) {
	dir := t.TempDir()
	skills := filepath.Join(dir, ".claude", "skills")
	if err := Write(stateWith(approvedRule("Old", "do it", "old", "stated", 1)), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	err := Write(stateWith(
		approvedRule("Good", "do it", "api", "stated", 1),
		approvedRule("Bad", "do it", "api/v2", "stated", 2),
	), dir, Options{})
	if err == nil {
		t.Fatal("expected an error for the bad domain")
	}
	if _, err := os.Stat(filepath.Join(skills, "api")); !os.IsNotExist(err) {
		t.Error("no skill should be written when validation fails")
	}
	if _, err := os.Stat(filepath.Join(skills, "old")); err != nil {
		t.Error("nothing should be pruned when validation fails")
	}
}

func TestPromoteNotesOmittedUniversalRules(t *testing.T) {
	var rules []state.Rule
	for i := 0; i < maxCLAUDERules+5; i++ {
		rules = append(rules, approvedRule(fmt.Sprintf("Universal rule %02d", i), "do it", UniversalLocation, "stated", i+1))
	}
	content := promoted(t, stateWith(rules...))
	if !strings.Contains(content, "5 more universal rule(s) are not inlined here") {
		t.Errorf("promoted block should say how many universal rules were omitted; got:\n%s", content)
	}
	if !strings.Contains(content, "`.claude/skills/conventions/SKILL.md`") {
		t.Errorf("universal skill path should be relative to the target file; got:\n%s", content)
	}
}

func TestYAMLQuote(t *testing.T) {
	cases := map[string]string{
		`plain`:            `"plain"`,
		`has: colon`:       `"has: colon"`,
		`*.tsx`:            `"*.tsx"`,
		`say "hi"`:         `"say \"hi\""`,
		`back\slash`:       `"back\\slash"`,
		"multi\nline\ttab": `"multi\nline\ttab"`,
		"ctrl\x01char":     `"ctrl\x01char"`,
		"em — dash":        `"em — dash"`,
		"nel\u0085sep":     `"nel\u0085sep"`,
		"line\u2028sep":    `"line\u2028sep"`,
	}
	for in, want := range cases {
		got := yamlQuote(in)
		if got != want {
			t.Errorf("yamlQuote(%q) = %s, want %s", in, got, want)
			continue
		}
		var back string
		if err := yaml.Unmarshal([]byte(got), &back); err != nil || back != in {
			t.Errorf("yamlQuote(%q) does not round-trip: %q, %v", in, back, err)
		}
	}
}

// A domain that vanishes from state (renamed, merged, all rules rejected)
// must not leave its old skill behind to keep auto-loading; a skill that
// pattern-learner did not write must survive a regeneration untouched.
func TestWritePrunesStaleGeneratedSkills(t *testing.T) {
	dir := t.TempDir()
	skills := filepath.Join(dir, ".claude", "skills")

	old := approvedRule("Old rule", "do it", "components", "stated", 1)
	if err := Write(stateWith(old), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(skills, "components", "SKILL.md")); err != nil {
		t.Fatalf("first run should have written components: %v", err)
	}
	if !inspectSkill(filepath.Join(skills, "components", "SKILL.md"), "components").generated {
		t.Fatal("generated SKILL.md should carry the generated marker")
	}

	// A hand-written skill sharing the root, one that even mentions the tool.
	handWritten := filepath.Join(skills, "deploy")
	if err := os.MkdirAll(handWritten, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(handWritten, "SKILL.md"),
		[]byte("---\nname: deploy\ndescription: Deploy steps (see pattern-learner for conventions).\n---\n\nSteps.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Domain renamed: components -> ui.
	renamed := approvedRule("Old rule", "do it", "ui", "stated", 1)
	if err := Write(stateWith(renamed), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(skills, "components")); !os.IsNotExist(err) {
		t.Error("stale generated skill 'components' should have been pruned")
	}
	if _, err := os.Stat(filepath.Join(skills, "ui", "SKILL.md")); err != nil {
		t.Errorf("renamed domain should be written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(handWritten, "SKILL.md")); err != nil {
		t.Errorf("hand-written skill must be preserved: %v", err)
	}

	// Every rule gone: the generated skill goes too, the hand-written one stays.
	if err := Write(state.Empty(), dir, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(skills, "ui")); !os.IsNotExist(err) {
		t.Error("generated skill with no remaining rules should have been pruned")
	}
	if _, err := os.Stat(filepath.Join(handWritten, "SKILL.md")); err != nil {
		t.Errorf("hand-written skill must be preserved: %v", err)
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
	// A prior run wrote the skill (a directory with no generated SKILL.md
	// would be refused as foreign); then simulate leftovers from that run
	// whose rules were since renamed/removed.
	if err := Write(stateWith(approvedRule("Old rule", "old", "api", "established", 1)), dir, Options{}); err != nil {
		t.Fatal(err)
	}
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

func TestWriteOutputsSkillsDirIsIndependent(t *testing.T) {
	// SkillsDir can point outside outputDir so the skills tree doesn't have to
	// live under the repo root being scanned — the core of the "clobber" bug.
	repoRoot := t.TempDir()
	customSkills := filepath.Join(t.TempDir(), "my-skills")

	s := stateWith(
		approvedRule("Universal rule", "global rule", "CLAUDE.md", "established", 1),
		approvedRule("API rule", "api rule", "api", "established", 2),
	)
	if err := Write(s, repoRoot, Options{SkillsDir: customSkills}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(customSkills, "api", "SKILL.md")); err != nil {
		t.Errorf("skill file not written under custom skills dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, ".claude", "skills", "api", "SKILL.md")); err == nil {
		t.Error("skill file must not be written under repoRoot when SkillsDir is set")
	}
}

func TestPromoteTargetIsIndependentOfSkillsDir(t *testing.T) {
	// The promoted index must point at wherever the skills actually live.
	target := filepath.Join(t.TempDir(), "AGENTS.md")
	customSkills := filepath.Join(t.TempDir(), "my-skills")

	domain := approvedRule("API rule", "api rule", "api", "established", 1)
	domain.Target.FileGlob = []string{"src/api/**/*.go"}
	if _, err := Promote(stateWith(domain), target, PromoteOptions{SkillsDir: customSkills, Create: true}); err != nil {
		t.Fatal(err)
	}
	content := readFile(t, target)
	if !strings.Contains(content, filepath.Join(customSkills, "api", "SKILL.md")) {
		t.Errorf("index should point at the real skills dir; got:\n%s", content)
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

// --- promotion ---
//
// Write never touches anything above the skills directory; publishing to a
// repo-root instruction file is the explicit `promote` step. These cover both
// halves of that split, plus the marker-scoped merge that lets a promoted
// block coexist with hand-written content.

func TestWriteNeverWritesTopLevelFiles(t *testing.T) {
	dir := t.TempDir()
	s := stateWith(
		approvedRule("Universal rule", "always do it", "CLAUDE.md", "stated", 1),
		approvedRule("API rule", "do it in api", "api", "stated", 2),
	)
	if err := Write(s, dir, Options{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("write-outputs must not create %s; promotion is opt-in", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude", "skills", "api", "SKILL.md")); err != nil {
		t.Errorf("skill files should still be written: %v", err)
	}
}

func TestPromoteBasicContent(t *testing.T) {
	content := promoted(t, stateWith(
		approvedRule("Use errors.As", "Always use errors.As", "CLAUDE.md", "established", 1, 2),
	))
	for _, want := range []string{"# Coding Conventions", "Use errors.As", "Always use errors.As", "#1", "#2", BeginMarker, EndMarker} {
		if !strings.Contains(content, want) {
			t.Errorf("promoted block missing %q", want)
		}
	}
}

func TestPromoteOnlyApprovedRules(t *testing.T) {
	proposed := approvedRule("Proposed rule", "maybe this", "CLAUDE.md", "emerging", 2)
	proposed.Status = "proposed"
	rejected := approvedRule("Rejected rule", "not this", "CLAUDE.md", "established", 3)
	rejected.Status = "rejected"
	content := promoted(t, stateWith(
		approvedRule("Approved rule", "do this", "CLAUDE.md", "established", 1),
		proposed, rejected,
	))
	if !strings.Contains(content, "Approved rule") {
		t.Error("approved rule should be present")
	}
	for _, bad := range []string{"Proposed rule", "Rejected rule"} {
		if strings.Contains(content, bad) {
			t.Errorf("%s should not be promoted", bad)
		}
	}
}

func TestPromoteConfidenceOrdering(t *testing.T) {
	content := promoted(t, stateWith(
		approvedRule("Emerging rule", "emerging text", "CLAUDE.md", "emerging", 1),
		approvedRule("Established rule", "established text", "CLAUDE.md", "established", 2),
		approvedRule("Stated rule", "stated text", "CLAUDE.md", "stated", 3),
	))
	stated, est, emg := strings.Index(content, "Stated rule"), strings.Index(content, "Established rule"), strings.Index(content, "Emerging rule")
	if stated < 0 || est < 0 || emg < 0 {
		t.Fatal("all three rules should be present")
	}
	if !(stated < est && est < emg) {
		t.Errorf("want stated < established < emerging, got %d/%d/%d", stated, est, emg)
	}
}

func TestPromoteCapsUniversalRules(t *testing.T) {
	var rules []state.Rule
	for i := 0; i < 35; i++ {
		rules = append(rules, approvedRule("Rule", "text", "CLAUDE.md", "established", i+1))
	}
	if got := strings.Count(promoted(t, stateWith(rules...)), "### Rule"); got != maxCLAUDERules {
		t.Errorf("expected %d promoted rules, got %d", maxCLAUDERules, got)
	}
}

func TestPromoteExamplesRenderBeforeProse(t *testing.T) {
	r := approvedRule("Use errors.As", "Always use errors.As for type checking", "CLAUDE.md", "established", 1)
	r.DoExamples = []state.Example{
		{Code: "errors.As(err, &target)", Language: "go"},
		{Code: "errors.As(err, &myErr)", Language: "go"},
	}
	r.DontExamples = []state.Example{{Code: "err.(*MyErr)", Language: "go"}}
	content := promoted(t, stateWith(r))

	for _, want := range []string{"errors.As(err, &target)", "err.(*MyErr)", "**Do:**", "**Don't:**"} {
		if !strings.Contains(content, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(content, "errors.As(err, &myErr)") {
		t.Error("promoted rules cap at one example pair")
	}
	if ex, prose := strings.Index(content, "errors.As(err, &target)"), strings.Index(content, "Always use errors.As for type checking"); ex > prose {
		t.Error("examples should appear before rule prose")
	}
}

func TestPromoteDomainIndex(t *testing.T) {
	domain := approvedRule("API rule", "do it in api", "api", "stated", 1)
	domain.Target.FileGlob = []string{"src/api/**/*.go"}
	content := promoted(t, stateWith(domain))
	if !strings.Contains(content, "## Domain conventions") {
		t.Error("domain index should be present for domain-only states")
	}
	if !strings.Contains(content, "`src/api/**/*.go`") {
		t.Error("index should list the domain's globs")
	}
	if strings.Contains(content, "## Universal rules") {
		t.Error("empty universal section should be omitted")
	}
}

func TestPromoteSkipsMissingFileWithoutCreate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	res, err := Promote(stateWith(approvedRule("R", "t", "CLAUDE.md", "stated", 1)), path, PromoteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != PromoteSkipped {
		t.Errorf("want skipped without --create, got %q", res.Outcome)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("promote must not create the file without Create")
	}
}

func TestPromotePreservesHandWrittenContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	handWritten := "# My own agent instructions\n\nDo not touch this line.\n"
	if err := os.WriteFile(path, []byte(handWritten), 0644); err != nil {
		t.Fatal(err)
	}
	s := stateWith(approvedRule("Universal rule", "always do it", "CLAUDE.md", "stated", 1))

	res, err := Promote(s, path, PromoteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != PromoteAppended {
		t.Errorf("want appended, got %q", res.Outcome)
	}
	got := readFile(t, path)
	if !strings.Contains(got, "Do not touch this line.") {
		t.Error("hand-written content must survive promotion")
	}
	if !strings.Contains(got, "always do it") {
		t.Error("promoted block should have been appended")
	}

	// A second promotion with new rules replaces only the block.
	s2 := stateWith(
		approvedRule("Universal rule", "always do it", "CLAUDE.md", "stated", 1),
		approvedRule("Second rule", "also do this", "CLAUDE.md", "stated", 2),
	)
	res2, err := Promote(s2, path, PromoteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Outcome != PromoteUpdated {
		t.Errorf("want updated, got %q", res2.Outcome)
	}
	got = readFile(t, path)
	if !strings.Contains(got, "Do not touch this line.") {
		t.Error("hand-written content must survive a refresh")
	}
	if !strings.Contains(got, "also do this") {
		t.Error("refreshed block should carry the new rule")
	}
	if strings.Count(got, BeginMarker) != 1 {
		t.Errorf("refresh must not duplicate the block, got %d markers", strings.Count(got, BeginMarker))
	}
}

func TestPromoteIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	s := stateWith(approvedRule("Universal rule", "always do it", "CLAUDE.md", "stated", 1))
	if _, err := Promote(s, path, PromoteOptions{Create: true}); err != nil {
		t.Fatal(err)
	}
	first := readFile(t, path)
	res, err := Promote(s, path, PromoteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != PromoteUnchanged {
		t.Errorf("re-promoting identical state should report unchanged, got %q", res.Outcome)
	}
	if readFile(t, path) != first {
		t.Error("re-promoting identical state must not change the file")
	}
}

func TestPromoteRefusesMalformedMarkers(t *testing.T) {
	s := stateWith(approvedRule("R", "t", "CLAUDE.md", "stated", 1))
	cases := map[string]string{
		"begin only":       "# Doc\n\n" + BeginMarker + "\nstuff\n",
		"end only":         "# Doc\n\n" + EndMarker + "\n",
		"end before begin": "# Doc\n\n" + EndMarker + "\nstuff\n" + BeginMarker + "\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "AGENTS.md")
			if err := os.WriteFile(path, []byte(doc), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := Promote(s, path, PromoteOptions{}); err == nil {
				t.Error("expected an error rather than guessing the block bounds")
			}
			if readFile(t, path) != doc {
				t.Error("file must be left untouched when markers are malformed")
			}
		})
	}
}

func TestPromoteSkipsEmptyState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	res, err := Promote(state.Empty(), path, PromoteOptions{Create: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != PromoteSkipped {
		t.Errorf("nothing to promote should skip, got %q", res.Outcome)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("no file should be created when there is nothing to promote")
	}
}
