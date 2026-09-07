package format_test

// Integration with the generator. These lock in the division of labor
// documented in the package comment: output.Write manages the body-line
// budget itself (inline vs. chunked), and audit-format's residual job is the
// frontmatter validity that output.Write never checks. If either half
// changes, one of these should fail rather than the subcommand quietly
// becoming redundant or wrong.
//
// This is an external test package because internal/output imports
// internal/format (for ParseFrontmatter); an internal test importing output
// would form a cycle.

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ButtersesHouse/Chalmuns/internal/format"
	"github.com/ButtersesHouse/Chalmuns/internal/output"
	"github.com/ButtersesHouse/Chalmuns/internal/state"
)

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
		res := format.AuditFile(genSkill(t, "api", n))
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
	res := format.AuditFile(genSkill(t, "Legacy_API", 3))
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
