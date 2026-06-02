package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// triageRule is the minimal set of fields read from a rule for triage decisions.
type triageRule struct {
	ID          string           `json:"id"`
	Confidence  string           `json:"confidence"`
	SignalCount  int              `json:"signal_count"`
	Sources     []classifySource `json:"sources"`
	Supersedes  []string         `json:"supersedes,omitempty"`
	Conflicted  bool             `json:"conflicted,omitempty"`
	ReviewedSnapshot *triageSnapshot `json:"reviewed_snapshot,omitempty"`
}

// triageSnapshot mirrors state.ReviewedSnapshot without importing the state package.
type triageSnapshot struct {
	SignalCount     int   `json:"signal_count"`
	SourcePRNumbers []int `json:"source_pr_numbers"`
}

// TriageFilterResult is the output of review-filter mode.
type TriageFilterResult struct {
	Show          []json.RawMessage `json:"show"`
	Suppressed    int               `json:"suppressed"`
	SuppressedIDs []string          `json:"suppressed_ids"`
}

// RunTriage reads proposed rules from stdin and applies the Step 10 triage
// predicate, returning the result to stdout.
//
// Modes:
//   auto           [--auto-threshold]  — apply approve/defer predicate; output rules
//                                        with status patched
//   review-filter  [--all]             — suppress unchanged emerging rules; output
//                                        {show, suppressed, suppressed_ids}
func RunTriage(args []string) error {
	mode := flagVal(args, "--mode", "")
	if mode == "" {
		return fmt.Errorf("--mode required (auto or review-filter)")
	}
	autoThreshold := hasFlag(args, "--auto-threshold")
	showAll := hasFlag(args, "--all")

	var rawRules []json.RawMessage
	if err := json.NewDecoder(os.Stdin).Decode(&rawRules); err != nil {
		return fmt.Errorf("decode stdin: %w", err)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")

	switch mode {
	case "auto":
		out, err := TriageAuto(rawRules, autoThreshold)
		if err != nil {
			return err
		}
		return enc.Encode(out)
	case "review-filter":
		out, err := TriageReviewFilter(rawRules, showAll)
		if err != nil {
			return err
		}
		return enc.Encode(out)
	default:
		return fmt.Errorf("unknown --mode %q: use auto or review-filter", mode)
	}
}

// TriageAuto applies the Step 10 auto-approve predicate to a slice of
// proposed rules. Returns the same rules with "status" patched to "approved"
// for rules that pass, or left as "proposed" (deferred) for those that don't.
//
// Defer conditions (evaluated in order; first match wins):
//  1. supersedes is non-empty
//  2. conflicted == true
//  3. signal_count == 1 AND all sources implicit (unless autoThreshold)
func TriageAuto(rawRules []json.RawMessage, autoThreshold bool) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(rawRules))
	for _, raw := range rawRules {
		var rule triageRule
		if err := json.Unmarshal(raw, &rule); err != nil {
			out = append(out, raw) // pass through unparseable rules unchanged
			continue
		}

		decision, _ := autoDecision(rule, autoThreshold)
		if decision == "approve" {
			var m map[string]json.RawMessage
			if err := json.Unmarshal(raw, &m); err != nil {
				out = append(out, raw)
				continue
			}
			approvedJSON, _ := json.Marshal("approved")
			m["status"] = approvedJSON
			patched, err := json.Marshal(m)
			if err != nil {
				out = append(out, raw)
				continue
			}
			out = append(out, patched)
		} else {
			out = append(out, raw)
		}
	}
	return out, nil
}

// TriageReviewFilter suppresses "unchanged emerging" rules from the review
// loop. A rule is unchanged when its confidence is "emerging", it has a
// reviewed_snapshot, and both signal_count and sorted source PR numbers
// exactly match the snapshot. Pass showAll=true to bypass suppression.
func TriageReviewFilter(rawRules []json.RawMessage, showAll bool) (TriageFilterResult, error) {
	var result TriageFilterResult
	for _, raw := range rawRules {
		if showAll {
			result.Show = append(result.Show, raw)
			continue
		}
		var rule triageRule
		if err := json.Unmarshal(raw, &rule); err != nil {
			result.Show = append(result.Show, raw)
			continue
		}
		if unchangedEmerging(rule) {
			result.Suppressed++
			result.SuppressedIDs = append(result.SuppressedIDs, rule.ID)
		} else {
			result.Show = append(result.Show, raw)
		}
	}
	return result, nil
}

// autoDecision returns ("approve","") or ("defer", reason) for one rule.
func autoDecision(rule triageRule, autoThreshold bool) (string, string) {
	if len(rule.Supersedes) > 0 {
		return "defer", "supersedes"
	}
	if rule.Conflicted {
		return "defer", "conflict"
	}
	if !autoThreshold {
		allImplicit := true
		for _, src := range rule.Sources {
			if src.Strength == "explicit" {
				allImplicit = false
				break
			}
		}
		if rule.SignalCount == 1 && allImplicit {
			return "defer", "singleton"
		}
	}
	return "approve", ""
}

// unchangedEmerging returns true when an emerging rule is unchanged since
// the user last skipped it — both signal_count and sorted source PR numbers
// match the stored reviewed_snapshot.
func unchangedEmerging(rule triageRule) bool {
	if rule.Confidence != "emerging" || rule.ReviewedSnapshot == nil {
		return false
	}
	snap := rule.ReviewedSnapshot
	if snap.SignalCount != rule.SignalCount {
		return false
	}

	current := make([]int, len(rule.Sources))
	for i, s := range rule.Sources {
		current[i] = s.PRNumber
	}
	sort.Ints(current)

	snapPRs := make([]int, len(snap.SourcePRNumbers))
	copy(snapPRs, snap.SourcePRNumbers)
	sort.Ints(snapPRs)

	if len(current) != len(snapPRs) {
		return false
	}
	for i := range current {
		if current[i] != snapPRs[i] {
			return false
		}
	}
	return true
}
