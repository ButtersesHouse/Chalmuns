package pipeline

import (
	"encoding/json"
	"testing"
)

// makeCandidate builds a raw candidate JSON with the given sources.
// strength is "explicit" or "implicit" (or "" for implicit).
func makeCandidate(t *testing.T, sources []struct {
	prNum    int
	strength string
}) json.RawMessage {
	t.Helper()
	type src struct {
		PRNumber int    `json:"pr_number"`
		Strength string `json:"strength,omitempty"`
	}
	var ss []src
	for _, s := range sources {
		ss = append(ss, src{PRNumber: s.prNum, Strength: s.strength})
	}
	m := map[string]interface{}{
		"title":   "test",
		"rule":    "do the thing",
		"sources": ss,
	}
	b, _ := json.Marshal(m)
	return b
}

func classifyOne(t *testing.T, raw json.RawMessage, maxPR, sincePR int) map[string]interface{} {
	t.Helper()
	result, err := Classify([]json.RawMessage{raw}, maxPR, sincePR)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Kept) == 0 {
		return nil
	}
	var m map[string]interface{}
	json.Unmarshal(result.Kept[0], &m)
	return m
}

func TestClassify_explicitSingle_stated(t *testing.T) {
	raw := makeCandidate(t, []struct {
		prNum    int
		strength string
	}{{100, "explicit"}})
	out := classifyOne(t, raw, 200, 0)
	if out == nil {
		t.Fatal("expected 1 kept candidate")
	}
	if out["confidence"] != "stated" {
		t.Errorf("1 explicit source → stated; got %v", out["confidence"])
	}
	if out["signal_count"].(float64) != 1 {
		t.Errorf("signal_count should be 1, got %v", out["signal_count"])
	}
}

func TestClassify_explicitTwo_stated(t *testing.T) {
	raw := makeCandidate(t, []struct {
		prNum    int
		strength string
	}{{100, "explicit"}, {110, "explicit"}})
	out := classifyOne(t, raw, 200, 0)
	if out["confidence"] != "stated" {
		t.Errorf("2 explicit sources → stated; got %v", out["confidence"])
	}
}

func TestClassify_explicitThree_established(t *testing.T) {
	raw := makeCandidate(t, []struct {
		prNum    int
		strength string
	}{{100, "explicit"}, {110, "explicit"}, {120, "explicit"}})
	out := classifyOne(t, raw, 200, 0)
	if out["confidence"] != "established" {
		t.Errorf("3 explicit sources → established; got %v", out["confidence"])
	}
}

func TestClassify_implicitFour_emerging(t *testing.T) {
	raw := makeCandidate(t, []struct {
		prNum    int
		strength string
	}{{100, ""}, {110, ""}, {120, ""}, {130, ""}})
	out := classifyOne(t, raw, 200, 0)
	if out["confidence"] != "emerging" {
		t.Errorf("4 implicit sources → emerging; got %v", out["confidence"])
	}
}

func TestClassify_implicitFive_established(t *testing.T) {
	// Use maxPRSeen=200 so cutoff=100. Sources at 100-140 have max=140 ≥ 100 → no downgrade.
	sources := make([]struct {
		prNum    int
		strength string
	}, 5)
	for i := range sources {
		sources[i] = struct {
			prNum    int
			strength string
		}{100 + i*10, ""}
	}
	raw := makeCandidate(t, sources)
	out := classifyOne(t, raw, 200, 0)
	if out["confidence"] != "established" {
		t.Errorf("5 implicit sources → established; got %v", out["confidence"])
	}
}

func TestClassify_implicitOld_downgradeEstablishedToEmerging(t *testing.T) {
	// 5 implicit sources all from PR 100; maxPRSeen=500, sincePR=0 → cutoff=250.
	// PR 100 < 250 → downgrade established → emerging.
	sources := make([]struct {
		prNum    int
		strength string
	}, 5)
	for i := range sources {
		sources[i] = struct {
			prNum    int
			strength string
		}{100, ""}
	}
	raw := makeCandidate(t, sources)
	out := classifyOne(t, raw, 500, 0)
	if out == nil {
		t.Fatal("candidate should be kept (downgraded to emerging, not dropped)")
	}
	if out["confidence"] != "emerging" {
		t.Errorf("old implicit established → emerging; got %v", out["confidence"])
	}
}

func TestClassify_implicitVeryOld_dropped(t *testing.T) {
	// 2 implicit sources from PR 50; maxPRSeen=500, sincePR=0 → cutoff=250.
	// PR 50 < 250 AND confidence starts as emerging → downgrade drops it.
	raw := makeCandidate(t, []struct {
		prNum    int
		strength string
	}{{50, ""}, {60, ""}})
	result, _ := Classify([]json.RawMessage{raw}, 500, 0)
	if len(result.Kept) != 0 {
		t.Error("old implicit emerging candidate should be dropped by recency downgrade")
	}
	if result.Dropped != 1 {
		t.Errorf("dropped: want 1, got %d", result.Dropped)
	}
}

func TestClassify_explicitOld_exempt(t *testing.T) {
	// Explicit signal from PR 50; maxPRSeen=500 → old, but explicit signals are exempt.
	raw := makeCandidate(t, []struct {
		prNum    int
		strength string
	}{{50, "explicit"}})
	out := classifyOne(t, raw, 500, 0)
	if out == nil {
		t.Fatal("explicit candidate should not be dropped by recency downgrade")
	}
	if out["confidence"] != "stated" {
		t.Errorf("old explicit single → stated; got %v", out["confidence"])
	}
}

func TestClassify_mixedStrength_treatedAsExplicit(t *testing.T) {
	// One explicit source + one implicit source → treated as explicit.
	raw := makeCandidate(t, []struct {
		prNum    int
		strength string
	}{{100, "explicit"}, {110, ""}})
	out := classifyOne(t, raw, 200, 0)
	// 2 sources, explicit → "stated" (not "emerging")
	if out["confidence"] != "stated" {
		t.Errorf("mixed (1 explicit + 1 implicit) with 2 sources → stated; got %v", out["confidence"])
	}
}

func TestClassify_signalCountSetFromSources(t *testing.T) {
	// Even if signal_count was wrong in input, output should set it to len(sources).
	raw := []byte(`{"title":"t","rule":"r","signal_count":99,"sources":[{"pr_number":1,"strength":"explicit"},{"pr_number":2,"strength":"explicit"}]}`)
	out := classifyOne(t, raw, 100, 0)
	if out["signal_count"].(float64) != 2 {
		t.Errorf("signal_count should be 2 (len sources), got %v", out["signal_count"])
	}
}

func TestClassify_passthroughFields(t *testing.T) {
	// Other fields (title, rule, suggested_target) must be preserved.
	raw := []byte(`{"title":"Use errors.As","rule":"prefer errors.As","suggested_target":{"location":"api"},"sources":[{"pr_number":1,"strength":"explicit"}]}`)
	out := classifyOne(t, raw, 100, 0)
	if out["title"] != "Use errors.As" {
		t.Errorf("title not preserved: %v", out["title"])
	}
	target := out["suggested_target"].(map[string]interface{})
	if target["location"] != "api" {
		t.Errorf("suggested_target not preserved: %v", out["suggested_target"])
	}
}

func TestClassify_recencyCutoffBoundary(t *testing.T) {
	// PR exactly at the cutoff (= 250 when maxPRSeen=500, sincePR=0) should NOT be downgraded.
	raw := makeCandidate(t, []struct {
		prNum    int
		strength string
	}{{250, ""}, {260, ""}})
	out := classifyOne(t, raw, 500, 0)
	if out == nil {
		t.Fatal("candidate at cutoff should be kept")
	}
	// 2 implicit sources at cutoff → emerging (not downgraded since maxPR=260 >= 250).
	if out["confidence"] != "emerging" {
		t.Errorf("2 implicit at cutoff → emerging; got %v", out["confidence"])
	}
}

func TestClassify_syntheticPRZeroExplicit_keptAsStated(t *testing.T) {
	// Discover/manual rules carry one synthetic explicit Signal with
	// pr_number: 0 (no PR provenance). They must classify on the explicit
	// path (stated) and never be dropped by the recency downgrade, however
	// old pr_number 0 looks relative to the scanned range.
	raw := makeCandidate(t, []struct {
		prNum    int
		strength string
	}{{0, "explicit"}})
	out := classifyOne(t, raw, 500, 0)
	if out == nil {
		t.Fatal("synthetic explicit source should be kept")
	}
	if out["confidence"] != "stated" {
		t.Errorf("single explicit source → stated; got %v", out["confidence"])
	}
}

func TestClassify_refreshMode_noCutoff(t *testing.T) {
	// Refresh where sincePR == maxPRSeen → no recency downgrade at all.
	raw := makeCandidate(t, []struct {
		prNum    int
		strength string
	}{{100, ""}, {110, ""}})
	out := classifyOne(t, raw, 500, 500)
	if out == nil {
		t.Fatal("should be kept when no range to compute cutoff")
	}
}

// A candidate whose sources carry no PR number has no point on the PR number
// line, so the recency downgrade must not touch it. It used to: pr_number 0
// is below every cutoff, so an implicit candidate mined from a watched code
// reviewer — or added by --add or --discover — was dropped before it ever
// reached the approval prompt.
func TestClassify_noPRSourceIsExemptFromRecency(t *testing.T) {
	raw := makeCandidate(t, []struct {
		prNum    int
		strength string
	}{{0, "implicit"}, {0, "implicit"}})

	got := classifyOne(t, raw, 100, 0)
	if got == nil {
		t.Fatal("a candidate with no PR source must not be dropped by the recency downgrade")
	}
	if got["confidence"] != "emerging" {
		t.Errorf("confidence: want emerging, got %v", got["confidence"])
	}
	if got["signal_count"] != float64(2) {
		t.Errorf("signal_count: want 2, got %v", got["signal_count"])
	}
}

// The exemption is for candidates with no PR at all; a genuinely stale
// PR-derived candidate must still be dropped.
func TestClassify_stalePRCandidateStillDropped(t *testing.T) {
	raw := makeCandidate(t, []struct {
		prNum    int
		strength string
	}{{5, "implicit"}})

	if got := classifyOne(t, raw, 100, 0); got != nil {
		t.Errorf("an old implicit PR candidate should still be dropped; got %v", got)
	}
}

// A candidate mixing review and PR sources is judged on the PR sources it has:
// the review source contributes no recency information either way.
func TestClassify_mixedSourcesJudgedOnPRSources(t *testing.T) {
	stale := makeCandidate(t, []struct {
		prNum    int
		strength string
	}{{0, "implicit"}, {5, "implicit"}})
	if got := classifyOne(t, stale, 100, 0); got != nil {
		t.Errorf("a candidate whose only PR source is stale should be dropped; got %v", got)
	}

	fresh := makeCandidate(t, []struct {
		prNum    int
		strength string
	}{{0, "implicit"}, {90, "implicit"}})
	if got := classifyOne(t, fresh, 100, 0); got == nil {
		t.Error("a candidate with a recent PR source should be kept")
	}
}

// Step 8C merges a review signal into an existing PR rule. Judging that merged
// rule on its one old PR number dropped it outright, despite several reviews
// having flagged it since — the freshest evidence there is.
func TestClassify_reviewEvidenceKeepsAnOldPRRuleAlive(t *testing.T) {
	type src struct {
		PRNumber int    `json:"pr_number"`
		Strength string `json:"strength,omitempty"`
		ReviewID string `json:"review_id,omitempty"`
	}
	raw, _ := json.Marshal(map[string]interface{}{
		"title": "merged rule",
		"sources": []src{
			{PRNumber: 10, Strength: "implicit"},
			{ReviewID: "rev-00000000000a", Strength: "implicit"},
			{ReviewID: "rev-00000000000b", Strength: "implicit"},
		},
	})
	if got := classifyOne(t, raw, 100, 0); got == nil {
		t.Error("a rule two reviews have flagged since is not stale")
	}
}
