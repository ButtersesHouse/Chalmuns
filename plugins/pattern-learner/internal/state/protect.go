package state

import (
	"fmt"
	"slices"
	"strings"
)

// A rule the developer stated by hand is the one thing in state that no run
// produced and no run can reproduce. Everything else here is derived: drop a
// PR-mined rule and the next --refresh re-mines it from the same comment;
// drop a manual rule and the sentence is gone, because its only source was a
// person saying it once.
//
// That asymmetry is why this file exists. state-write replaces the rules
// array wholesale from a payload the model types out by hand, so every
// ingestion run rewrites every manual rule from memory — and a rule silently
// dropped there is then pruned from .claude/skills by the next write-outputs,
// with nothing in the summary to say it ever existed. Preserving it was asked
// for in prose, in a step that also asks the model to re-type the entire
// document; this makes it a refusal instead.
//
// The policy is deliberately narrow. Ingestion may still *strengthen* a
// manual rule — appending the sources and examples that corroborate it is the
// whole point of mining, and a rule the reviewers keep echoing should show
// that. What it may not do is rewrite, retarget, reject, downgrade or drop
// one. Strengthening is additive and reversible; overriding is neither.

// IsProtected reports whether a rule may not be overridden without explicit
// per-rule permission. A rule is protected when the developer authored it
// directly (--add writes origin "manual"), whatever its status: a manual rule
// the user rejected is still their decision to revisit, not the pipeline's.
func IsProtected(r Rule) bool { return r.Origin == "manual" }

// Violation is one field of one protected rule that an incoming payload would
// change without permission. Was and Now are rendered for a human to read in
// the refusal, not parsed by anything.
type Violation struct {
	RuleID string
	Title  string
	Field  string
	Was    string
	Now    string
}

// presenceField is the Field value used when the payload drops a protected
// rule entirely. It is spelled as a phrase rather than a field name because
// that is what the refusal has to say: nothing changed, the rule is gone.
const presenceField = "presence"

// EnforceProtected repairs what a payload can only have got wrong, then
// reports every remaining change it would make to a protected rule that it
// does not have permission to make. It mutates next, so the caller writes what
// it checked rather than checking one document and writing another.
//
// Two fields are restored rather than refused: origin and created_at. Both
// have exactly one correct value, no run has a reason to change either, and an
// empty one in a hand-retyped payload means the model did not carry the field
// through — SKILL.md does not even mention origin. Refusing there would cost a
// rerun to arrive at the value already on disk. A *different* non-empty value
// is another matter and is still refused: that is a rule being relabelled as
// the pipeline's, which is how a protected rule stops being protected.
//
// allow holds the rule IDs the developer approved this write for; violations
// against those rules are suppressed. An ID that names no protected rule in
// the prior state is an error rather than a no-op — an override that protects
// nothing is a typo, and letting it pass silently would turn the one gate
// this package exists to hold into a formality.
//
// A prior state with no protected rules (a fresh repo, or one that has never
// used --add) yields no violations and no error, whatever the payload does.
func EnforceProtected(prior State, next *State, allow []string) ([]Violation, error) {
	protected := map[string]Rule{}
	for _, r := range prior.Rules {
		if IsProtected(r) && r.ID != "" {
			protected[r.ID] = r
		}
	}

	// Restore before comparing, and before the allow list is consulted: the
	// developer approving a change to a rule's text is not them asking for its
	// provenance or its creation date to be rewritten.
	for i := range next.Rules {
		was, ok := protected[next.Rules[i].ID]
		if !ok {
			continue
		}
		if next.Rules[i].Origin == "" {
			next.Rules[i].Origin = was.Origin
		}
		if next.Rules[i].CreatedAt == "" {
			next.Rules[i].CreatedAt = was.CreatedAt
		}
	}

	allowed := map[string]bool{}
	for _, id := range allow {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := protected[id]; !ok {
			return nil, fmt.Errorf("--allow-protected names %q, which is not a protected rule in the "+
				"current state: an override that protects nothing is a typo, not a permission. "+
				"Protected rules are the ones added by hand (origin \"manual\"); check the ID "+
				"against the refusal message that sent you here", id)
		}
		allowed[id] = true
	}

	incoming := map[string]Rule{}
	for _, r := range next.Rules {
		if r.ID != "" {
			incoming[r.ID] = r
		}
	}

	var out []Violation
	// Iterate prior.Rules, not the map, so the refusal lists rules in the
	// order they sit in state — stable between runs, and the order the user
	// saw them added in.
	for _, was := range prior.Rules {
		if !IsProtected(was) || was.ID == "" || allowed[was.ID] {
			continue
		}
		now, ok := incoming[was.ID]
		if !ok {
			out = append(out, Violation{
				RuleID: was.ID, Title: was.Title, Field: presenceField,
				Was: "in state", Now: "dropped from the payload",
			})
			continue
		}
		out = append(out, diffProtected(was, now)...)
	}
	return out, nil
}

// diffProtected compares one protected rule against its incoming replacement.
func diffProtected(was, now Rule) []Violation {
	var out []Violation
	add := func(field, oldV, newV string) {
		out = append(out, Violation{RuleID: was.ID, Title: was.Title, Field: field, Was: oldV, Now: newV})
	}

	// Immutable: the rule's identity, its text, where it lands, and what the
	// pipeline is allowed to consider it. Changing any of these is not
	// strengthening the developer's rule, it is replacing it with another one
	// under the same ID.
	if was.Title != now.Title {
		add("title", was.Title, now.Title)
	}
	if was.Rule != now.Rule {
		add("rule", was.Rule, now.Rule)
	}
	if was.Origin != now.Origin {
		// Only a non-empty mismatch reaches here — an empty origin was
		// restored above. This is the quiet one: "" reads as pr-review
		// everywhere downstream, so a relabelled rule keeps working and merely
		// stops being the developer's, including on the next run.
		add("origin", quoteOrEmpty(was.Origin), quoteOrEmpty(now.Origin))
	}
	if was.Status != now.Status {
		add("status", was.Status, now.Status)
	}
	if was.CreatedAt != now.CreatedAt {
		add("created_at", was.CreatedAt, now.CreatedAt)
	}
	if was.Target.Location != now.Target.Location {
		// Retargeting moves the rule to another skill, and write-outputs then
		// prunes the directory it left if nothing else lives there.
		add("target.location", was.Target.Location, now.Target.Location)
	}
	if !slices.Equal(was.Target.FileGlob, now.Target.FileGlob) {
		add("target.file_glob", strings.Join(was.Target.FileGlob, ", "), strings.Join(now.Target.FileGlob, ", "))
	}
	if !slices.Equal(was.Supersedes, now.Supersedes) {
		add("supersedes", strings.Join(was.Supersedes, ", "), strings.Join(now.Supersedes, ", "))
	}
	if deref(was.SupersededBy) != deref(now.SupersededBy) {
		add("superseded_by", quoteOrEmpty(deref(was.SupersededBy)), quoteOrEmpty(deref(now.SupersededBy)))
	}

	// Confidence may rise — a manual rule the reviewers keep echoing becomes
	// "established" — but it may not fall to the implicit tier. "emerging"
	// means nobody has said this out loud yet, which is false of a rule the
	// developer typed, and classify drops an emerging candidate outright.
	if now.Confidence != was.Confidence && (now.Confidence == "" || now.Confidence == "emerging") {
		add("confidence", was.Confidence, quoteOrEmpty(now.Confidence))
	}

	// Append-only: the corroboration behind the rule and the examples shown
	// with it. New entries are the pipeline doing its job; the existing ones
	// are the record of why the rule is there. The examples matter as much as
	// the sources — Step 8C caps the merged arrays at four with no rule about
	// which survive, so without this a mined example evicts one the developer
	// wrote.
	if len(now.Sources) < len(was.Sources) {
		add("sources", fmt.Sprintf("%d", len(was.Sources)), fmt.Sprintf("%d (entries removed)", len(now.Sources)))
	} else if i, ok := firstSignalDiff(was.Sources, now.Sources); ok {
		add(fmt.Sprintf("sources[%d]", i), describeSignal(was.Sources[i]), describeSignal(now.Sources[i]))
	}
	out = append(out, diffExamples(was, "do_examples", was.DoExamples, now.DoExamples)...)
	out = append(out, diffExamples(was, "dont_examples", was.DontExamples, now.DontExamples)...)
	return out
}

// diffExamples enforces the append-only rule on one example array.
func diffExamples(was Rule, field string, oldEx, newEx []Example) []Violation {
	if len(newEx) < len(oldEx) {
		return []Violation{{
			RuleID: was.ID, Title: was.Title, Field: field,
			Was: fmt.Sprintf("%d", len(oldEx)),
			Now: fmt.Sprintf("%d (entries removed)", len(newEx)),
		}}
	}
	for i := range oldEx {
		// Compare on code alone, trimmed — the same identity Step 8C uses to
		// deduplicate examples. Re-annotating an example with a file_ref or a
		// richer context is enrichment, not eviction.
		if strings.TrimSpace(oldEx[i].Code) != strings.TrimSpace(newEx[i].Code) {
			return []Violation{{
				RuleID: was.ID, Title: was.Title, Field: fmt.Sprintf("%s[%d]", field, i),
				Was: firstLine(oldEx[i].Code), Now: firstLine(newEx[i].Code),
			}}
		}
	}
	return nil
}

// firstSignalDiff returns the index of the first existing source the payload
// changed. Sources are append-only, so only the prefix is compared: anything
// past len(old) is new corroboration and is allowed.
func firstSignalDiff(oldS, newS []Signal) (int, bool) {
	for i := range oldS {
		if oldS[i] != newS[i] {
			return i, true
		}
	}
	return 0, false
}

func describeSignal(s Signal) string {
	switch {
	case s.ReviewID != "":
		return fmt.Sprintf("review %s by %s", s.ReviewID, orUnknown(s.Reviewer))
	case s.PRNumber > 0:
		return fmt.Sprintf("PR #%d by %s", s.PRNumber, orUnknown(s.Reviewer))
	default:
		return fmt.Sprintf("stated by %s", orUnknown(s.Reviewer))
	}
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}

func quoteOrEmpty(s string) string {
	if s == "" {
		return "(empty)"
	}
	return s
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// FormatViolations renders the refusal. It names every rule and field so the
// developer can approve or decline the specific change, and it tells the
// caller what the flag is for — the failure this whole file guards against is
// an agent that reads "pass --allow-protected" as the way to make an error go
// away.
func FormatViolations(vs []Violation) string {
	var b strings.Builder
	byRule := map[string][]Violation{}
	var order []string
	for _, v := range vs {
		if _, seen := byRule[v.RuleID]; !seen {
			order = append(order, v.RuleID)
		}
		byRule[v.RuleID] = append(byRule[v.RuleID], v)
	}

	fmt.Fprintf(&b, "refusing to write: this payload would change %s without permission, and nothing was written.\n",
		plural(len(order), "rule the developer added by hand", "rules the developer added by hand"))
	for _, id := range order {
		group := byRule[id]
		fmt.Fprintf(&b, "\n  %s  %q\n", id, group[0].Title)
		for _, v := range group {
			if v.Field == presenceField {
				fmt.Fprintf(&b, "    the rule itself: was %s, now %s\n", v.Was, v.Now)
				continue
			}
			fmt.Fprintf(&b, "    %s:\n      was: %s\n      now: %s\n", v.Field, v.Was, v.Now)
		}
	}
	b.WriteString("\nA rule with origin \"manual\" is the developer's, not the pipeline's. Ingestion may\n" +
		"append sources and examples to one — that is corroboration, and it is welcome —\n" +
		"but it may not rewrite, retarget, reject, downgrade or drop it.\n" +
		"\n" +
		"Show the change above to the developer and ask. Only if they approve, re-run with\n" +
		"--allow-protected <id>[,<id>...] naming exactly the rules they approved. Do not\n" +
		"pass the flag to get past this error, and do not re-type the rule to match the\n" +
		"prior state to avoid asking: either drops a change they may have wanted.\n")
	return b.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
