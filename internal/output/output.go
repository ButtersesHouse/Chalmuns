package output

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ButtersesHouse/Chalmuns/internal/state"
)

const maxCLAUDERules = 30

// Write generates CLAUDE.md and per-domain skill files in outputDir.
func Write(s state.State, outputDir string) error {
	if err := writeCLAUDEMD(s, outputDir); err != nil {
		return err
	}
	return writeSkillFiles(s, outputDir)
}

func writeCLAUDEMD(s state.State, dir string) error {
	rules := approvedRules(s, "CLAUDE.md")
	if len(rules) == 0 {
		return nil
	}
	if len(rules) > maxCLAUDERules {
		rules = rules[:maxCLAUDERules]
	}

	var b strings.Builder
	b.WriteString("# Coding Conventions\n\n")
	b.WriteString("These conventions were extracted from PR review history.")
	b.WriteString(" See `.claude/pattern-learner/state.json` for provenance.\n\n")

	for _, r := range rules {
		b.WriteString(fmt.Sprintf("## %s\n\n", r.Title))
		b.WriteString(r.Rule + "\n\n")
		if r.DoExample != nil {
			b.WriteString(fmt.Sprintf("**Do:**\n```%s\n%s\n```\n\n", r.DoExample.Language, r.DoExample.Code))
		}
		if r.DontExample != nil {
			b.WriteString(fmt.Sprintf("**Don't:**\n```%s\n%s\n```\n\n", r.DontExample.Language, r.DontExample.Code))
		}
		b.WriteString(fmt.Sprintf("_Source: PRs %s_\n\n", prList(r.Sources)))
	}

	return atomicWrite(filepath.Join(dir, "CLAUDE.md"), b.String())
}

func writeSkillFiles(s state.State, dir string) error {
	byDomain := map[string][]state.Rule{}
	for _, r := range s.Rules {
		if r.Status != "approved" || r.Target.Location == "CLAUDE.md" || r.Target.Location == "" {
			continue
		}
		d := r.Target.Location
		byDomain[d] = append(byDomain[d], r)
	}

	for domain, rules := range byDomain {
		if err := writeSkillFile(domain, rules, dir); err != nil {
			return err
		}
	}
	return nil
}

func writeSkillFile(domain string, rules []state.Rule, dir string) error {
	sort.Slice(rules, func(i, j int) bool {
		ri, rj := confidenceRank(rules[i].Confidence), confidenceRank(rules[j].Confidence)
		if ri != rj {
			return ri < rj
		}
		return rules[i].Title < rules[j].Title
	})

	globs := collectGlobs(rules)
	desc := buildDescription(domain, globs)

	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString(fmt.Sprintf("name: %s\n", domain))
	b.WriteString(fmt.Sprintf("description: %s\n", desc))
	b.WriteString("---\n\n")
	b.WriteString(fmt.Sprintf("# %s Conventions\n\n", capitalize(domain)))
	b.WriteString("## Rules\n\n")

	for _, r := range rules {
		b.WriteString(fmt.Sprintf("### %s\n\n", r.Title))
		b.WriteString(r.Rule + "\n\n")
		if r.DoExample != nil {
			b.WriteString(fmt.Sprintf("**Do:**\n```%s\n%s\n```\n\n", r.DoExample.Language, r.DoExample.Code))
		}
		if r.DontExample != nil {
			b.WriteString(fmt.Sprintf("**Don't:**\n```%s\n%s\n```\n\n", r.DontExample.Language, r.DontExample.Code))
		}
		b.WriteString(fmt.Sprintf("_Source: PRs %s_\n\n", prList(r.Sources)))
	}

	skillDir := filepath.Join(dir, ".claude", "skills", domain)
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(skillDir, "SKILL.md"), b.String())
}

// approvedRules returns approved rules for a given target location,
// sorted stated → established → emerging, then alphabetically by title.
func approvedRules(s state.State, location string) []state.Rule {
	var out []state.Rule
	for _, r := range s.Rules {
		if r.Status == "approved" && r.Target.Location == location {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := confidenceRank(out[i].Confidence), confidenceRank(out[j].Confidence)
		if ri != rj {
			return ri < rj
		}
		return out[i].Title < out[j].Title
	})
	return out
}

// confidenceRank returns the sort priority for a confidence level.
// Lower is higher priority: stated(0) > established(1) > emerging(2).
func confidenceRank(c string) int {
	switch c {
	case "stated":
		return 0
	case "established":
		return 1
	case "emerging":
		return 2
	default:
		return 3
	}
}

func prList(sources []state.Signal) string {
	seen := map[int]bool{}
	var nums []int
	for _, s := range sources {
		if !seen[s.PRNumber] {
			seen[s.PRNumber] = true
			nums = append(nums, s.PRNumber)
		}
	}
	sort.Ints(nums)
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = fmt.Sprintf("#%d", n)
	}
	return strings.Join(parts, ", ")
}

func collectGlobs(rules []state.Rule) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rules {
		for _, g := range r.Target.FileGlob {
			if !seen[g] {
				seen[g] = true
				out = append(out, g)
			}
		}
	}
	return out
}

func buildDescription(domain string, globs []string) string {
	base := fmt.Sprintf("Coding conventions for %s", domain)
	if len(globs) > 0 {
		base += fmt.Sprintf(". Use when editing files matching: %s", strings.Join(globs, ", "))
	}
	if len(base) > 200 {
		base = base[:197] + "..."
	}
	return base
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func atomicWrite(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
