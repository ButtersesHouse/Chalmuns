package output

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ButtersesHouse/Chalmuns/internal/state"
)

// Promotion writes pattern-learner's conventions into a top-level agent
// instruction file (AGENTS.md, CLAUDE.md, ...).
//
// This is deliberately NOT part of Write. A normal pipeline run only touches
// the generator-owned skills directory; escalating content to the repo root
// is an explicit, user-initiated act, because that file is usually
// hand-maintained and lives in the user's git history.
//
// The write is scoped to a managed block delimited by BeginMarker/EndMarker.
// Everything outside the block is preserved byte-for-byte, so a hand-written
// file keeps its content and a re-run only refreshes our own section. A file
// with no markers is appended to, never overwritten; a file that does not
// exist is only created when the caller opts in.
//
// This replaces an earlier whole-file ownership check that decided the file
// was ours if it merely contained the substring "by pattern-learner". That
// test inverted in the dangerous case: a generated file a human then edited
// still carried the phrase and was silently overwritten, losing the edits,
// while a hand-written file that happened to credit the tool was clobbered
// too. Marker-scoped writes remove the need to guess ownership at all.

const (
	// BeginMarker and EndMarker delimit the generated block. They are HTML
	// comments so they render as nothing in every markdown viewer.
	BeginMarker = "<!-- pattern-learner:begin -->"
	EndMarker   = "<!-- pattern-learner:end -->"
)

// PromoteOutcome describes what Promote did to the target file.
type PromoteOutcome string

const (
	PromoteCreated   PromoteOutcome = "created"   // file did not exist; created with the block
	PromoteUpdated   PromoteOutcome = "updated"   // existing block replaced
	PromoteAppended  PromoteOutcome = "appended"  // no block found; block appended, rest preserved
	PromoteUnchanged PromoteOutcome = "unchanged" // block already matched; nothing written
	PromoteSkipped   PromoteOutcome = "skipped"   // nothing to promote, or file absent without Create
)

// PromoteResult reports the outcome for one promoted file.
type PromoteResult struct {
	Path    string         `json:"path"`
	Outcome PromoteOutcome `json:"outcome"`
	Reason  string         `json:"reason,omitempty"`
}

// PromoteOptions controls a promotion.
type PromoteOptions struct {
	// SkillsDir is the skills root, used to compute the paths the index
	// points at. Defaults to <dir-of-target>/.claude/skills when empty.
	SkillsDir string
	// Create allows creating the target file when it does not exist. Without
	// it, a missing file is skipped rather than created, so `promote` can
	// never introduce a repo-root file the user did not ask for.
	Create bool
}

// Promote writes the managed block into path. It never modifies content
// outside the block.
func Promote(s state.State, path string, opts PromoteOptions) (PromoteResult, error) {
	res := PromoteResult{Path: path}

	skillsDir := opts.SkillsDir
	if skillsDir == "" {
		skillsDir = filepath.Join(filepath.Dir(path), ".claude", "skills")
	}

	block := renderPromotedBlock(s, path, skillsDir)
	if block == "" {
		res.Outcome, res.Reason = PromoteSkipped, "no approved rules to promote"
		return res, nil
	}

	existing, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		if !opts.Create {
			res.Outcome = PromoteSkipped
			res.Reason = fmt.Sprintf("%s does not exist; pass --create to create it", filepath.Base(path))
			return res, nil
		}
		res.Outcome = PromoteCreated
		return res, atomicWrite(path, block)
	case err != nil:
		return res, err
	}

	merged, outcome, err := spliceBlock(string(existing), block)
	if err != nil {
		return res, fmt.Errorf("%s: %w", path, err)
	}
	res.Outcome = outcome
	if outcome == PromoteUnchanged {
		return res, nil
	}
	return res, atomicWrite(path, merged)
}

// spliceBlock merges block into doc, replacing an existing marked block or
// appending one when absent. Content outside the markers is preserved.
func spliceBlock(doc, block string) (string, PromoteOutcome, error) {
	begin := strings.Index(doc, BeginMarker)
	end := strings.Index(doc, EndMarker)

	switch {
	case begin < 0 && end < 0:
		// No managed block yet: append, keeping every existing byte.
		sep := "\n\n"
		if doc == "" {
			sep = ""
		} else if strings.HasSuffix(doc, "\n\n") {
			sep = ""
		} else if strings.HasSuffix(doc, "\n") {
			sep = "\n"
		}
		return doc + sep + block, PromoteAppended, nil

	case begin < 0 || end < 0:
		return "", "", fmt.Errorf(
			"found only one of the pattern-learner markers — refusing to guess the block bounds; "+
				"restore both %s and %s, or delete the stray one", BeginMarker, EndMarker)

	case end < begin:
		return "", "", fmt.Errorf(
			"pattern-learner end marker precedes the begin marker — refusing to rewrite a malformed block")
	}

	endStop := end + len(EndMarker)
	// Absorb the newline directly after the end marker so replacing a block
	// does not accumulate blank lines across runs.
	if strings.HasPrefix(doc[endStop:], "\n") {
		endStop++
	}
	current := doc[begin:endStop]
	if strings.TrimRight(current, "\n") == strings.TrimRight(block, "\n") {
		return doc, PromoteUnchanged, nil
	}
	return doc[:begin] + block + doc[endStop:], PromoteUpdated, nil
}

// renderPromotedBlock builds the managed block: a short provenance note, the
// universal rules, and an index routing an agent to each domain's skill file
// by glob. Returns "" when there is nothing to promote.
//
// The index is the point of this block for Claude Code (which auto-loads
// .claude/skills itself) and the whole point for agents that do not.
func renderPromotedBlock(s state.State, targetPath, skillsDir string) string {
	universal := approvedRules(s, UniversalLocation)
	omitted := 0
	if len(universal) > maxCLAUDERules {
		omitted = len(universal) - maxCLAUDERules
		universal = universal[:maxCLAUDERules]
	}

	// Paths in the block are written relative to the target file's directory
	// where possible, so the file reads the same from any checkout location.
	// Links are relative to the target file's directory whenever that is
	// meaningful: a skills directory given relatively (the CLI's
	// ".claude/skills") is linked relatively even when the target sits
	// below the repo root and the link must climb out ("../.claude/..."),
	// and so is the default skills directory under the target's own tree.
	// An absolute skills directory elsewhere is a fixed location the user
	// chose, and a relative path to it would only hold from one checkout,
	// so it is linked as given.
	relRef := func(parts ...string) string {
		ref := filepath.Join(append([]string{skillsDir}, parts...)...)
		// Compare absolute forms so a relative skills dir and an absolute
		// target (or the reverse) still yield a correct relative link.
		absRef, err1 := filepath.Abs(ref)
		absTargetDir, err2 := filepath.Abs(filepath.Dir(targetPath))
		if err1 != nil || err2 != nil {
			return ref
		}
		rel, err := filepath.Rel(absTargetDir, absRef)
		if err != nil {
			return ref
		}
		if !filepath.IsAbs(ref) || !IsOutsideRel(rel) {
			return rel
		}
		return ref
	}

	byDomain := map[string][]string{}
	for _, r := range s.Rules {
		if r.Status != "approved" || r.Target.Location == UniversalLocation || r.Target.Location == "" {
			continue
		}
		byDomain[r.Target.Location] = append(byDomain[r.Target.Location], r.Target.FileGlob...)
	}
	domains := make([]string, 0, len(byDomain))
	for name := range byDomain {
		domains = append(domains, name)
	}
	sort.Strings(domains)

	if len(universal) == 0 && len(domains) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(BeginMarker + "\n")
	b.WriteString("<!-- Managed by pattern-learner. Edits inside this block are overwritten;\n")
	b.WriteString("     anything outside it is preserved. Refresh with: pattern-learner promote -->\n\n")
	b.WriteString("# Coding Conventions\n\n")
	b.WriteString("Extracted from this repo's PR review history by pattern-learner.")
	b.WriteString(" See `.claude/pattern-learner/state.json` for provenance.\n\n")

	if len(universal) > 0 {
		b.WriteString("## Universal rules\n\n")
		b.WriteString("These apply to every file in the repository.")
		b.WriteString(fmt.Sprintf(" They are also generated as `%s`, which Claude Code auto-loads;"+
			" they are inlined here for agents that do not read that directory.\n\n",
			relRef(UniversalSkillName, "SKILL.md")))
		renderUniversalRules(&b, universal, "###")
		if omitted > 0 {
			b.WriteString(fmt.Sprintf("_%d more universal rule(s) are not inlined here (this block shows the first %d by confidence); read `%s` for the full set._\n\n",
				omitted, maxCLAUDERules, relRef(UniversalSkillName, "SKILL.md")))
		}
	}

	if len(domains) > 0 {
		b.WriteString("## Domain conventions\n\n")
		b.WriteString("Rules scoped to parts of the codebase live in per-domain files (plain markdown).")
		b.WriteString(" Before editing files matching a domain's globs, read that domain's file and follow its links to the `examples/` or `rules/` companion files for code samples.\n\n")
		for _, name := range domains {
			ref := relRef(name, "SKILL.md")
			scope := name
			if globs := dedupeStrings(byDomain[name]); len(globs) > 0 {
				scope = "`" + strings.Join(globs, "`, `") + "`"
			}
			b.WriteString(fmt.Sprintf("- %s → `%s`\n", scope, ref))
		}
		b.WriteString("\n")
	}

	b.WriteString(EndMarker + "\n")
	return b.String()
}
