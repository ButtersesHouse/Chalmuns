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
	raw := makeCandidate(t, []struct{ prNum int; strength string }{{100, "explicit"}})
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
	raw := makeCandidate(t, []struct{ prNum int; strength string }{{100, "explicit"}, {110, "explicit"}})
	out := classifyOne(t, raw, 200, 0)
	if out["confidence"] != "stated" {
		t.Errorf("2 explicit sources → stated; got %v", out["confidence"])
	}
}

func TestClassify_explicitThree_established(t *testing.T) {
	raw := makeCandidate(t, []struct{ prNum int; strength string }{{100, "explicit"}, {110, "explicit"}, {120, "explicit"}})
	out := classifyOne(t, raw, 200, 0)
	if out["confidence"] != "established" {
		t.Errorf("3 explicit sources → established; got %v", out["confidence"])
	}
}

func TestClassify_implicitFour_emerging(t *testing.T) {
	raw := makeCandidate(t, []struct{ prNum int; strength string }{{100, ""}, {110, ""}, {120, ""}, {130, ""}})
	out := classifyOne(t, raw, 200, 0)
	if out["confidence"] != "emerging" {
		t.Errorf("4 implicit sources → emerging; got %v", out["confidence"])
	}
}

func TestClassify_implicitFive_established(t *testing.T) {
	// Use maxPRSeen=200 so cutoff=100. Sources at 100-140 have max=140 ≥ 100 → no downgrade.
	sources := make([]struct{ prNum int; strength string }, 5)
	for i := range sources {
		sources[i] = struct{ prNum int; strength string }{100 + i*10, ""}
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
	sources := make([]struct{ prNum int; strength string }, 5)
	for i := range sources {
		sources[i] = struct{ prNum int; strength string }{100, ""}
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
	raw := makeCandidate(t, []struct{ prNum int; strength string }{{50, ""}, {60, ""}})
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
	raw := makeCandidate(t, []struct{ prNum int; strength string }{{50, "explicit"}})
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
	raw := makeCandidate(t, []struct{ prNum int; strength string }{{100, "explicit"}, {110, ""}})
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
	raw := makeCandidate(t, []struct{ prNum int; strength string }{{250, ""}, {260, ""}})
	out := classifyOne(t, raw, 500, 0)
	if out == nil {
		t.Fatal("candidate at cutoff should be kept")
	}
	// 2 implicit sources at cutoff → emerging (not downgraded since maxPR=260 >= 250).
	if out["confidence"] != "emerging" {
		t.Errorf("2 implicit at cutoff → emerging; got %v", out["confidence"])
	}
}

func TestClassify_refreshMode_noCutoff(t *testing.T) {
	// Refresh where sincePR == maxPRSeen → no recency downgrade at all.
	raw := makeCandidate(t, []struct{ prNum int; strength string }{{100, ""}, {110, ""}})
	out := classifyOne(t, raw, 500, 500)
	if out == nil {
		t.Fatal("should be kept when no range to compute cutoff")
	}
}
