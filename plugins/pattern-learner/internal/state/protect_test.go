package state

import (
	"strings"
	"testing"
)

// manualRule is the shape --add writes: a rule the developer stated, with one
// source that is their own sentence rather than a PR comment.
func manualRule() Rule {
	return Rule{
		ID:         "rule_ab12cd34",
		Title:      "Wrap errors with %w",
		Rule:       "Wrap returned errors with fmt.Errorf and %w when propagating.",
		Target:     Target{Location: "api", FileGlob: []string{"internal/api/**/*.go"}},
		Confidence: "stated",
		Status:     "approved",
		Origin:     "manual",
		CreatedAt:  "2026-09-01T10:00:00Z",
		Sources: []Signal{{
			Reviewer: "mryave", Date: "2026-09-01",
			Snippet: "wrap returned errors with %w", Strength: "explicit",
		}},
		DoExamples:  []Example{{Code: "return fmt.Errorf(\"read: %w\", err)", Language: "go"}},
		SignalCount: 1,
	}
}

func priorWith(rules ...Rule) State {
	s := Empty()
	s.Rules = rules
	return s
}

// check runs EnforceProtected and fails the test on an unexpected error.
func check(t *testing.T, prior, next State, allow ...string) []Violation {
	t.Helper()
	vs, err := EnforceProtected(prior, &next, allow)
	if err != nil {
		t.Fatalf("EnforceProtected: %v", err)
	}
	return vs
}

func TestProtectedRuleUnchangedIsClean(t *testing.T) {
	prior := priorWith(manualRule())
	if vs := check(t, prior, priorWith(manualRule())); len(vs) != 0 {
		t.Errorf("an unchanged manual rule must not be flagged; got %+v", vs)
	}
}

// The failure this whole file exists for: Step 11 asks the model to retype the
// entire state document, and a rule it forgets is erased with no error.
func TestProtectedRuleDroppedFromPayload(t *testing.T) {
	prior := priorWith(manualRule())
	vs := check(t, prior, Empty())
	if len(vs) != 1 || vs[0].Field != presenceField {
		t.Fatalf("dropping a manual rule must be refused; got %+v", vs)
	}
	if vs[0].RuleID != "rule_ab12cd34" || vs[0].Title != "Wrap errors with %w" {
		t.Errorf("the refusal must name the rule so the developer can approve it; got %+v", vs[0])
	}
}

func TestProtectedImmutableFields(t *testing.T) {
	str := func(s string) *string { return &s }
	cases := []struct {
		name   string
		field  string
		mutate func(*Rule)
	}{
		{"retitled", "title", func(r *Rule) { r.Title = "Something else" }},
		{"rewritten", "rule", func(r *Rule) { r.Rule = "Always wrap errors." }},
		{"origin reassigned", "origin", func(r *Rule) { r.Origin = "pr-review" }},
		{"rejected", "status", func(r *Rule) { r.Status = "rejected" }},
		{"superseded", "status", func(r *Rule) { r.Status = "superseded" }},
		{"created_at rewritten", "created_at", func(r *Rule) { r.CreatedAt = "2026-09-08T00:00:00Z" }},
		{"retargeted", "target.location", func(r *Rule) { r.Target.Location = "auth" }},
		{"globs changed", "target.file_glob", func(r *Rule) { r.Target.FileGlob = []string{"**/*.go"} }},
		{"supersedes added", "supersedes", func(r *Rule) { r.Supersedes = []string{"rule_ffff"} }},
		{"superseded_by set", "superseded_by", func(r *Rule) { r.SupersededBy = str("rule_ffff") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			next := manualRule()
			tc.mutate(&next)
			vs := check(t, priorWith(manualRule()), priorWith(next))
			if len(vs) != 1 {
				t.Fatalf("want exactly one violation, got %+v", vs)
			}
			if vs[0].Field != tc.field {
				t.Errorf("violation must name the field changed: want %q, got %q", tc.field, vs[0].Field)
			}
		})
	}
}

// Mining a manual rule is welcome — that is corroboration, and the reason to
// run the pipeline at all. Only overriding is refused.
func TestProtectedRuleMayBeStrengthened(t *testing.T) {
	next := manualRule()
	next.Sources = append(next.Sources, Signal{
		PRNumber: 118, Reviewer: "someone-else", Date: "2026-09-05",
		Snippet: "please wrap this with %w", Strength: "implicit",
	})
	next.SignalCount = 2
	next.LastSeenPR = 118
	next.DoExamples = append(next.DoExamples, Example{Code: "return fmt.Errorf(\"open %s: %w\", name, err)", Language: "go"})
	next.Confidence = "established"
	next.UpdatedAt = ""

	if vs := check(t, priorWith(manualRule()), priorWith(next)); len(vs) != 0 {
		t.Errorf("appending sources, examples and a confidence rise must be allowed; got %+v", vs)
	}
}

func TestProtectedSourcesAreAppendOnly(t *testing.T) {
	t.Run("removed", func(t *testing.T) {
		next := manualRule()
		next.Sources = nil
		vs := check(t, priorWith(manualRule()), priorWith(next))
		if len(vs) != 1 || vs[0].Field != "sources" {
			t.Fatalf("dropping the developer's own source must be refused; got %+v", vs)
		}
	})
	t.Run("rewritten in place", func(t *testing.T) {
		next := manualRule()
		next.Sources = []Signal{{PRNumber: 118, Reviewer: "bot", Snippet: "different", Strength: "implicit"}}
		vs := check(t, priorWith(manualRule()), priorWith(next))
		if len(vs) != 1 || vs[0].Field != "sources[0]" {
			t.Fatalf("replacing an existing source must be refused; got %+v", vs)
		}
	})
}

// Step 8C caps merged example arrays at four with no rule about which survive,
// so without this a mined example evicts one the developer wrote.
func TestProtectedExamplesAreAppendOnly(t *testing.T) {
	t.Run("evicted", func(t *testing.T) {
		next := manualRule()
		next.DoExamples = []Example{{Code: "err = fmt.Errorf(\"%w\", err)", Language: "go"}}
		vs := check(t, priorWith(manualRule()), priorWith(next))
		if len(vs) != 1 || vs[0].Field != "do_examples[0]" {
			t.Fatalf("evicting a hand-written example must be refused; got %+v", vs)
		}
	})
	t.Run("removed entirely", func(t *testing.T) {
		next := manualRule()
		next.DoExamples = nil
		vs := check(t, priorWith(manualRule()), priorWith(next))
		if len(vs) != 1 || vs[0].Field != "do_examples" {
			t.Fatalf("removing examples must be refused; got %+v", vs)
		}
	})
	t.Run("re-annotated", func(t *testing.T) {
		next := manualRule()
		next.DoExamples = []Example{{
			Code: "return fmt.Errorf(\"read: %w\", err)", Language: "go",
			FileRef: "internal/api/read.go:L45", Context: "func read()",
		}}
		if vs := check(t, priorWith(manualRule()), priorWith(next)); len(vs) != 0 {
			t.Errorf("enriching an example with a file_ref is not eviction; got %+v", vs)
		}
	})
}

// Confidence may rise but not fall to the implicit tier: "emerging" means
// nobody has said this out loud, which is false of a rule the developer typed,
// and classify drops an emerging candidate outright.
func TestProtectedConfidenceMayNotBeDowngraded(t *testing.T) {
	for _, c := range []string{"emerging", ""} {
		next := manualRule()
		next.Confidence = c
		vs := check(t, priorWith(manualRule()), priorWith(next))
		if len(vs) != 1 || vs[0].Field != "confidence" {
			t.Errorf("confidence %q must be refused; got %+v", c, vs)
		}
	}
}

// Everything the pipeline mined, it can re-mine. Only the hand-written rules
// are irreplaceable, so only they are locked.
func TestMinedRulesAreNotProtected(t *testing.T) {
	mined := manualRule()
	mined.Origin = "pr-review"
	minedToo := mined
	minedToo.Origin = ""

	for _, r := range []Rule{mined, minedToo} {
		if vs := check(t, priorWith(r), Empty()); len(vs) != 0 {
			t.Errorf("a mined rule (origin %q) is the pipeline's to rewrite; got %+v", r.Origin, vs)
		}
	}
}

// A manual rule the developer rejected is still their decision to revisit.
func TestProtectionCoversEveryStatus(t *testing.T) {
	prior := manualRule()
	prior.Status = "rejected"
	if vs := check(t, priorWith(prior), Empty()); len(vs) != 1 {
		t.Errorf("a rejected manual rule is still the developer's; got %+v", vs)
	}
}

func TestAllowProtectedSuppressesViolations(t *testing.T) {
	next := manualRule()
	next.Rule = "Always wrap errors."
	next.Status = "rejected"
	if vs := check(t, priorWith(manualRule()), priorWith(next), "rule_ab12cd34"); len(vs) != 0 {
		t.Errorf("an approved override must let the change through; got %+v", vs)
	}
	// Whitespace around a listed ID is the shape a comma-separated flag
	// arrives in; it must not silently fail to match.
	if vs := check(t, priorWith(manualRule()), priorWith(next), " rule_ab12cd34 ", ""); len(vs) != 0 {
		t.Errorf("a padded ID must still match; got %+v", vs)
	}
}

// An override that protects nothing is a typo, and letting it pass silently
// turns the gate into a formality.
func TestAllowProtectedUnknownIDIsAnError(t *testing.T) {
	empty := Empty()
	_, err := EnforceProtected(priorWith(manualRule()), &empty, []string{"rule_typo"})
	if err == nil {
		t.Fatal("an --allow-protected ID naming no protected rule must be an error")
	}
	if !strings.Contains(err.Error(), "rule_typo") {
		t.Errorf("the error must name the ID that did not match; got %v", err)
	}
}

func TestFirstRunHasNothingToProtect(t *testing.T) {
	next := priorWith(manualRule())
	if vs := check(t, Empty(), next); len(vs) != 0 {
		t.Errorf("a fresh state protects nothing; got %+v", vs)
	}
}

// The refusal is read by an agent deciding what to do next, so it has to name
// the rule, name the change, and say what the flag is actually for.
func TestFormatViolationsIsActionable(t *testing.T) {
	next := manualRule()
	next.Rule = "Always wrap errors."
	vs := check(t, priorWith(manualRule()), priorWith(next))
	msg := FormatViolations(vs)

	for _, want := range []string{
		"rule_ab12cd34",
		"Wrap errors with %w",
		"Always wrap errors.",
		"--allow-protected",
		"nothing was written",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal must mention %q:\n%s", want, msg)
		}
	}
	if !strings.Contains(msg, "Do not\npass the flag to get past this error") {
		t.Errorf("refusal must tell the agent the flag is not an escape hatch:\n%s", msg)
	}
}

func TestFormatViolationsGroupsByRule(t *testing.T) {
	second := manualRule()
	second.ID = "rule_ef567890"
	second.Title = "No abbreviations in identifiers"
	second.Target.Location = "CLAUDE.md"

	nextFirst := manualRule()
	nextFirst.Title = "Changed"
	nextFirst.Status = "rejected"

	vs := check(t, priorWith(manualRule(), second), priorWith(nextFirst))
	msg := FormatViolations(vs)
	if !strings.Contains(msg, "2 rules the developer added by hand") {
		t.Errorf("refusal must count the affected rules:\n%s", msg)
	}
	if strings.Count(msg, "rule_ab12cd34") != 1 {
		t.Errorf("each rule must be listed once, with its fields under it:\n%s", msg)
	}
	if !strings.Contains(msg, "rule_ef567890") {
		t.Errorf("every affected rule must be listed:\n%s", msg)
	}
}

// A payload that omits origin or created_at has not decided anything — the
// model retyped the document and did not carry the field through, which for
// origin SKILL.md never even asks it to. There is one correct value, so
// restore it rather than spending a rerun to arrive back at it. Restoring
// origin is also what stops a protected rule being laundered into a mined one
// by dropping the field.
func TestOmittedOriginAndCreatedAtAreRestored(t *testing.T) {
	next := manualRule()
	next.Origin = ""
	next.CreatedAt = ""
	next.SignalCount = 2

	got := priorWith(next)
	vs, err := EnforceProtected(priorWith(manualRule()), &got, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Fatalf("an omitted origin/created_at is a gap to fill, not a change to refuse; got %+v", vs)
	}
	if got.Rules[0].Origin != "manual" {
		t.Errorf("origin must be restored so the rule stays protected next run; got %q", got.Rules[0].Origin)
	}
	if got.Rules[0].CreatedAt != "2026-09-01T10:00:00Z" {
		t.Errorf("created_at must be restored rather than re-stamped; got %q", got.Rules[0].CreatedAt)
	}
}

// Restoring only fills a gap. Relabelling a manual rule as the pipeline's is a
// decision, and it is refused like any other override.
func TestOriginReassignedIsStillRefused(t *testing.T) {
	next := manualRule()
	next.Origin = "pr-review"
	vs := check(t, priorWith(manualRule()), priorWith(next))
	if len(vs) != 1 || vs[0].Field != "origin" {
		t.Fatalf("relabelling a manual rule must be refused; got %+v", vs)
	}
}

// The restore runs before the allow list is consulted: approving a change to a
// rule's text is not asking for its provenance to be rewritten too.
func TestRestoreAppliesEvenToAllowedRules(t *testing.T) {
	next := manualRule()
	next.Rule = "Always wrap errors."
	next.Origin = ""

	got := priorWith(next)
	if _, err := EnforceProtected(priorWith(manualRule()), &got, []string{"rule_ab12cd34"}); err != nil {
		t.Fatal(err)
	}
	if got.Rules[0].Origin != "manual" {
		t.Errorf("an approved text change must not silently drop origin; got %q", got.Rules[0].Origin)
	}
}
