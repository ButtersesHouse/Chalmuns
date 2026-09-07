package pipeline

import (
	"encoding/json"
	"fmt"
	"testing"
)

// makeRule builds a minimal proposed rule JSON for triage tests.
func makeRule(t *testing.T, id, confidence string, signalCount int, supersedes []string, conflicted bool, sources []struct {
	prNum    int
	strength string
}, snap *triageSnapshot) json.RawMessage {
	t.Helper()
	type src struct {
		PRNumber int    `json:"pr_number"`
		Strength string `json:"strength,omitempty"`
	}
	var ss []src
	for _, s := range sources {
		ss = append(ss, src{PRNumber: s.prNum, Strength: s.strength})
	}
	type rule struct {
		ID               string          `json:"id"`
		Status           string          `json:"status"`
		Confidence       string          `json:"confidence"`
		SignalCount      int             `json:"signal_count"`
		Sources          []src           `json:"sources"`
		Supersedes       []string        `json:"supersedes,omitempty"`
		Conflicted       bool            `json:"conflicted,omitempty"`
		ReviewedSnapshot *triageSnapshot `json:"reviewed_snapshot,omitempty"`
	}
	r := rule{
		ID:               id,
		Status:           "proposed",
		Confidence:       confidence,
		SignalCount:      signalCount,
		Sources:          ss,
		Supersedes:       supersedes,
		Conflicted:       conflicted,
		ReviewedSnapshot: snap,
	}
	b, _ := json.Marshal(r)
	return b
}

func getStatus(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var m map[string]interface{}
	json.Unmarshal(raw, &m)
	s, _ := m["status"].(string)
	return s
}

// --- TriageAuto tests ---

func TestTriageAuto_approvesClean(t *testing.T) {
	raw := makeRule(t, "rule_1", "established", 3, nil, false,
		[]struct {
			prNum    int
			strength string
		}{{1, "explicit"}, {2, "explicit"}, {3, "explicit"}},
		nil)
	out, err := TriageAuto([]json.RawMessage{raw}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || getStatus(t, out[0]) != "approved" {
		t.Errorf("clean rule should be approved; got status %q", getStatus(t, out[0]))
	}
}

func TestTriageAuto_defersSupersedes(t *testing.T) {
	raw := makeRule(t, "rule_2", "established", 3, []string{"rule_old"}, false,
		[]struct {
			prNum    int
			strength string
		}{{1, "explicit"}, {2, "explicit"}, {3, "explicit"}},
		nil)
	out, _ := TriageAuto([]json.RawMessage{raw}, false)
	if getStatus(t, out[0]) != "proposed" {
		t.Error("superseding rule should be deferred (status=proposed)")
	}
}

func TestTriageAuto_defersConflicted(t *testing.T) {
	raw := makeRule(t, "rule_3", "emerging", 2, nil, true,
		[]struct {
			prNum    int
			strength string
		}{{1, "explicit"}, {2, "explicit"}},
		nil)
	out, _ := TriageAuto([]json.RawMessage{raw}, false)
	if getStatus(t, out[0]) != "proposed" {
		t.Error("conflicted rule should be deferred")
	}
}

func TestTriageAuto_defersSingleton(t *testing.T) {
	raw := makeRule(t, "rule_4", "emerging", 1, nil, false,
		[]struct {
			prNum    int
			strength string
		}{{1, ""}},
		nil)
	out, _ := TriageAuto([]json.RawMessage{raw}, false)
	if getStatus(t, out[0]) != "proposed" {
		t.Error("singleton implicit rule should be deferred without --auto-threshold")
	}
}

func TestTriageAuto_autoThresholdApproveSingleton(t *testing.T) {
	raw := makeRule(t, "rule_5", "emerging", 1, nil, false,
		[]struct {
			prNum    int
			strength string
		}{{1, ""}},
		nil)
	out, _ := TriageAuto([]json.RawMessage{raw}, true) // autoThreshold=true
	if getStatus(t, out[0]) != "approved" {
		t.Error("singleton implicit should be approved when --auto-threshold is set")
	}
}

func TestTriageAuto_explicitSingletonApproved(t *testing.T) {
	// 1 explicit source is NOT a singleton for deferral purposes (strength matters).
	raw := makeRule(t, "rule_6", "stated", 1, nil, false,
		[]struct {
			prNum    int
			strength string
		}{{1, "explicit"}},
		nil)
	out, _ := TriageAuto([]json.RawMessage{raw}, false)
	if getStatus(t, out[0]) != "approved" {
		t.Error("single explicit source should be approved (not singleton-deferred)")
	}
}

// --- TriageReviewFilter tests ---

func TestTriageReviewFilter_suppressUnchanged(t *testing.T) {
	snap := &triageSnapshot{SignalCount: 2, SourcePRNumbers: []int{42, 67}}
	raw := makeRule(t, "rule_7", "emerging", 2, nil, false,
		[]struct {
			prNum    int
			strength string
		}{{42, ""}, {67, ""}},
		snap)
	result, err := TriageReviewFilter([]json.RawMessage{raw}, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Suppressed != 1 {
		t.Errorf("unchanged emerging with matching snapshot should be suppressed; suppressed=%d", result.Suppressed)
	}
	if len(result.Show) != 0 {
		t.Error("unchanged rule should not appear in show")
	}
	if len(result.SuppressedIDs) != 1 || result.SuppressedIDs[0] != "rule_7" {
		t.Errorf("suppressed_ids should contain rule_7; got %v", result.SuppressedIDs)
	}
}

func TestTriageReviewFilter_showUpdated(t *testing.T) {
	// Snapshot recorded signal_count=2 but now there are 3 sources.
	snap := &triageSnapshot{SignalCount: 2, SourcePRNumbers: []int{42, 67}}
	raw := makeRule(t, "rule_8", "emerging", 3, nil, false,
		[]struct {
			prNum    int
			strength string
		}{{42, ""}, {67, ""}, {99, ""}},
		snap)
	result, _ := TriageReviewFilter([]json.RawMessage{raw}, false)
	if result.Suppressed != 0 {
		t.Error("updated emerging rule (new signal) should NOT be suppressed")
	}
	if len(result.Show) != 1 {
		t.Error("updated rule should appear in show")
	}
}

func TestTriageReviewFilter_showNewPR(t *testing.T) {
	// Same count but a different PR number appeared.
	snap := &triageSnapshot{SignalCount: 2, SourcePRNumbers: []int{42, 67}}
	raw := makeRule(t, "rule_9", "emerging", 2, nil, false,
		[]struct {
			prNum    int
			strength string
		}{{42, ""}, {100, ""}}, // PR 100 is new
		snap)
	result, _ := TriageReviewFilter([]json.RawMessage{raw}, false)
	if result.Suppressed != 0 {
		t.Error("rule with new source PR should not be suppressed")
	}
}

func TestTriageReviewFilter_noSnapshot(t *testing.T) {
	// No snapshot → never suppressed (rule not yet seen by user).
	raw := makeRule(t, "rule_10", "emerging", 2, nil, false,
		[]struct {
			prNum    int
			strength string
		}{{42, ""}, {67, ""}},
		nil)
	result, _ := TriageReviewFilter([]json.RawMessage{raw}, false)
	if result.Suppressed != 0 {
		t.Error("rule without snapshot should not be suppressed")
	}
}

func TestTriageReviewFilter_nonEmergingNotSuppressed(t *testing.T) {
	// Snapshot exists but confidence is established — filter only targets emerging.
	snap := &triageSnapshot{SignalCount: 2, SourcePRNumbers: []int{42, 67}}
	raw := makeRule(t, "rule_11", "established", 2, nil, false,
		[]struct {
			prNum    int
			strength string
		}{{42, ""}, {67, ""}},
		snap)
	result, _ := TriageReviewFilter([]json.RawMessage{raw}, false)
	if result.Suppressed != 0 {
		t.Error("non-emerging rule should not be suppressed even with snapshot")
	}
}

func TestTriageReviewFilter_showAll(t *testing.T) {
	// --all bypasses suppression entirely.
	snap := &triageSnapshot{SignalCount: 2, SourcePRNumbers: []int{42, 67}}
	raw := makeRule(t, "rule_12", "emerging", 2, nil, false,
		[]struct {
			prNum    int
			strength string
		}{{42, ""}, {67, ""}},
		snap)
	result, _ := TriageReviewFilter([]json.RawMessage{raw}, true) // showAll=true
	if result.Suppressed != 0 {
		t.Error("--all should bypass suppression")
	}
	if len(result.Show) != 1 {
		t.Error("rule should appear in show when --all is set")
	}
}

func TestTriageReviewFilter_sortOrderIndependent(t *testing.T) {
	// Snapshot has PRs in reverse order; current sources in different order.
	// The comparison must sort both sides before comparing.
	snap := &triageSnapshot{SignalCount: 2, SourcePRNumbers: []int{67, 42}} // reversed
	raw := makeRule(t, "rule_13", "emerging", 2, nil, false,
		[]struct {
			prNum    int
			strength string
		}{{42, ""}, {67, ""}},
		snap)
	result, _ := TriageReviewFilter([]json.RawMessage{raw}, false)
	if result.Suppressed != 1 {
		t.Error("PR number comparison should be order-independent (sort both sides)")
	}
}

// makeReviewRule builds a proposed rule whose sources are all code-review
// signals: no PR number, and a review_id naming the artifact.
func makeReviewRule(t *testing.T, n int, strength string) json.RawMessage {
	t.Helper()
	type src struct {
		PRNumber int    `json:"pr_number"`
		Strength string `json:"strength,omitempty"`
		ReviewID string `json:"review_id,omitempty"`
	}
	var ss []src
	for i := 0; i < n; i++ {
		ss = append(ss, src{Strength: strength, ReviewID: fmt.Sprintf("rev-%012d", i)})
	}
	b, _ := json.Marshal(map[string]interface{}{
		"title": "a review rule", "confidence": "stated",
		"signal_count": n, "status": "proposed", "sources": ss,
	})
	return b
}

func triageStatus(t *testing.T, raw json.RawMessage, autoThreshold bool) string {
	t.Helper()
	out, err := TriageAuto([]json.RawMessage{raw}, autoThreshold)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out[0], &m); err != nil {
		t.Fatal(err)
	}
	return m.Status
}

// A convention seen in exactly one code review is deferred whatever its
// strength. Strength cannot carry this judgement on the review path: a finding
// counts as explicit when it states a rule, and a linter states a rule id on
// every finding, so one semgrep run would otherwise auto-approve one standing
// rule per check it tripped with nobody having read any of them.
func TestTriageAuto_singleReviewIsDeferredEvenWhenExplicit(t *testing.T) {
	if got := triageStatus(t, makeReviewRule(t, 1, "explicit"), false); got != "proposed" {
		t.Errorf("one explicit review signal should defer; got %q", got)
	}
	if got := triageStatus(t, makeReviewRule(t, 1, "implicit"), false); got != "proposed" {
		t.Errorf("one implicit review signal should defer; got %q", got)
	}
	// Recurrence is what makes a finding a convention, so two reviews agreeing
	// clears the bar.
	if got := triageStatus(t, makeReviewRule(t, 2, "explicit"), false); got != "approved" {
		t.Errorf("two reviews agreeing should approve; got %q", got)
	}
	// --auto-threshold is the documented opt-out of exactly this hold-back.
	if got := triageStatus(t, makeReviewRule(t, 1, "explicit"), true); got != "approved" {
		t.Errorf("--auto-threshold should approve a single review signal; got %q", got)
	}
}

// The hold-back is for review signals only: one explicitly-stated PR
// preference is still auto-approved, as it always was.
func TestTriageAuto_singleExplicitPRSignalStillApproves(t *testing.T) {
	raw, _ := json.Marshal(map[string]interface{}{
		"title": "a PR rule", "signal_count": 1, "status": "proposed",
		"sources": []map[string]interface{}{{"pr_number": 42, "strength": "explicit"}},
	})
	if got := triageStatus(t, raw, false); got != "approved" {
		t.Errorf("one explicit PR signal should still approve; got %q", got)
	}
}

// The count is of distinct reviews, not signals. One linter run tripping the
// same check in five files yields five sources and one review; counting
// signals would wave that through as established on the strength of one run.
func TestTriageAuto_manySignalsFromOneReviewStillDefers(t *testing.T) {
	type src struct {
		PRNumber int    `json:"pr_number"`
		Strength string `json:"strength,omitempty"`
		ReviewID string `json:"review_id,omitempty"`
	}
	build := func(ids ...string) json.RawMessage {
		var ss []src
		for _, id := range ids {
			ss = append(ss, src{Strength: "explicit", ReviewID: id})
		}
		b, _ := json.Marshal(map[string]interface{}{
			"title": "r", "confidence": "established",
			"signal_count": len(ss), "status": "proposed", "sources": ss,
		})
		return b
	}

	one := build("rev-000000000001", "rev-000000000001", "rev-000000000001", "rev-000000000001", "rev-000000000001")
	if got := triageStatus(t, one, false); got != "proposed" {
		t.Errorf("five findings from one review is still one review; got %q", got)
	}
	two := build("rev-000000000001", "rev-000000000002")
	if got := triageStatus(t, two, false); got != "approved" {
		t.Errorf("two reviews agreeing should approve; got %q", got)
	}
}

// Review signals all report pr_number 0, so a rule rebuilt from entirely
// different reviews has an identical PR list and would be suppressed as
// "nothing new" — hiding fresh corroboration from the watched reviewer.
func TestTriageReviewFilter_newReviewsAreNotSuppressed(t *testing.T) {
	rule := func(ids ...string) json.RawMessage {
		type src struct {
			PRNumber int    `json:"pr_number"`
			ReviewID string `json:"review_id,omitempty"`
		}
		var ss []src
		for _, id := range ids {
			ss = append(ss, src{ReviewID: id})
		}
		b, _ := json.Marshal(map[string]interface{}{
			"id": "rule_1", "confidence": "emerging", "signal_count": len(ss), "sources": ss,
			"reviewed_snapshot": map[string]interface{}{
				"signal_count": 2, "source_pr_numbers": []int{0, 0},
				"source_review_ids": []string{"rev-00000000000a", "rev-00000000000b"},
			},
		})
		return b
	}

	unchanged, err := TriageReviewFilter([]json.RawMessage{rule("rev-00000000000a", "rev-00000000000b")}, false)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Suppressed != 1 {
		t.Errorf("the same two reviews is unchanged; suppressed=%d", unchanged.Suppressed)
	}

	fresh, err := TriageReviewFilter([]json.RawMessage{rule("rev-00000000000c", "rev-00000000000d")}, false)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Suppressed != 0 {
		t.Error("two different reviews is new corroboration and must be shown")
	}
}
