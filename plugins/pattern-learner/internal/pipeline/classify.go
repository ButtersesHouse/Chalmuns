package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

// classifySource is the part of a signal source needed for confidence assignment.
type classifySource struct {
	PRNumber int    `json:"pr_number"`
	Strength string `json:"strength,omitempty"` // "explicit" or "implicit" (empty = implicit)
	// ReviewID marks a signal mined from a captured code review. Triage reads
	// it to hold back a convention seen in only one review.
	ReviewID string `json:"review_id,omitempty"`
}

// classifyInput is the minimal set of fields read from a candidate for classification.
type classifyInput struct {
	Sources []classifySource `json:"sources"`
}

// ClassifyResult is the output of RunClassify.
type ClassifyResult struct {
	Kept    []json.RawMessage `json:"kept"`
	Dropped int               `json:"dropped"`
}

// RunClassify reads candidates from stdin, assigns confidence based on
// sources[].strength, applies the threshold and recency downgrade, then
// writes a ClassifyResult to stdout. Guaranteed to set signal_count on every
// kept candidate (prevents drift from hand-maintenance), to the same number
// the threshold was applied to: len(sources), except that sources sharing a
// review_id count once between them. See countEvidence.
//
// Usage: classify --max-pr-seen <N> [--since-pr <N>]  (candidates JSON array on stdin)
func RunClassify(args []string) error {
	if err := checkFlags(args, []string{"--max-pr-seen", "--since-pr"}, nil); err != nil {
		return err
	}

	maxPRStr := flagVal(args, "--max-pr-seen", "")
	if maxPRStr == "" {
		return fmt.Errorf("--max-pr-seen required")
	}
	maxPR, err := strconv.Atoi(maxPRStr)
	if err != nil {
		return fmt.Errorf("--max-pr-seen must be an integer: %w", err)
	}
	sinceStr := flagVal(args, "--since-pr", "0")
	sincePR, err := strconv.Atoi(sinceStr)
	if err != nil {
		return fmt.Errorf("--since-pr must be an integer: %w", err)
	}

	var rawCandidates []json.RawMessage
	if err := json.NewDecoder(os.Stdin).Decode(&rawCandidates); err != nil {
		return fmt.Errorf("decode stdin: %w", err)
	}

	result, err := Classify(rawCandidates, maxPR, sincePR)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

// countEvidence collapses a candidate's review sources to one signal per
// review and leaves every other source counted as it always was.
//
// One review reporting the same convention in five files is one piece of
// evidence, not five: counting the findings graded a single linter run
// "established" — the top tier, which the approval UI sanctions bulk-approving
// — off one run nobody had read.
//
// Only review sources collapse. Folding PR sources together as well made the
// count non-monotonic, which is worse than the inflation it was meant to fix:
// a rule established across six comments in three PRs *dropped* to emerging
// the moment a watched reviewer confirmed it, because seven signals became
// four occasions. Evidence that corroborates a rule must never lower its tier.
func countEvidence(sources []classifySource) int {
	reviews := map[string]bool{}
	others := 0
	for _, s := range sources {
		if s.ReviewID != "" {
			reviews[s.ReviewID] = true
			continue
		}
		others++
	}
	return len(reviews) + others
}

// Classify assigns confidence and applies the Step 9 threshold and recency
// downgrade to a slice of raw candidate JSON objects. Each candidate must
// carry a "sources" array; each source may carry a "strength" field.
//
// Confidence rules:
//   - Explicit (any source has strength=="explicit"):
//     3+ signals → "established"; 1–2 → "stated"
//   - Implicit (all sources implicit or empty):
//     5+ signals → "established"; 1–4 → "emerging"
//
// A candidate's review sources collapse to one signal per review — one review
// is one piece of evidence however many findings it held — while every other
// source counts as one signal, exactly as before. See countEvidence.
//
// Recency downgrade (implicit-only, and only for candidates with at least one
// PR source and no review source):
//
//	cutoff = maxPRSeen − (maxPRSeen − sincePR) × 0.5
//	If max(source.pr_number) < cutoff: established → emerging; emerging → dropped.
//	Explicit signals are exempt — a stated preference does not expire.
//
// A candidate with no PR source at all is exempt too, and this is not a
// detail. Recency is a judgement on the PR number line, and such a candidate
// has no point on that line: its sources all report pr_number 0, which is
// below every cutoff, so scoring them anyway drops the implicit ones outright
// — a convention a watched reviewer flagged four separate times would
// disappear before ever reaching the approval prompt.
//
// Review Mode is what makes this reachable. The other no-PR paths do not hit
// it: --add rules bypass classify entirely, and --discover stamps its
// synthetic source "explicit", which the exemption above already covers.
func Classify(rawCandidates []json.RawMessage, maxPRSeen, sincePR int) (ClassifyResult, error) {
	var result ClassifyResult

	// Recency cutoff is the midpoint of the scanned PR range.
	// Guard: if no new PRs were scanned, skip recency downgrade entirely.
	var recencyCutoff float64
	if maxPRSeen > sincePR {
		recencyCutoff = float64(maxPRSeen) - float64(maxPRSeen-sincePR)*0.5
	}

	for _, raw := range rawCandidates {
		var c classifyInput
		if err := json.Unmarshal(raw, &c); err != nil {
			// Parse failures are a malformed-input bug, not a recency drop —
			// make them visible instead of silently folding into Dropped.
			fmt.Fprintf(os.Stderr, "warn: classify: dropping unparseable candidate: %v\n", err)
			result.Dropped++
			continue
		}

		isExplicit := false
		maxSourcePR := 0
		hasPRSource := false
		hasReviewSource := false
		for _, src := range c.Sources {
			if src.ReviewID != "" {
				hasReviewSource = true
			}
			if src.Strength == "explicit" {
				isExplicit = true
			}
			if src.PRNumber > 0 {
				hasPRSource = true
			}
			if src.PRNumber > maxSourcePR {
				maxSourcePR = src.PRNumber
			}
		}
		// One review is one signal however many findings it held; everything
		// else counts as it always did, so a candidate mined only from PRs is
		// graded exactly as it was before Review Mode existed. See
		// countEvidence.
		n := countEvidence(c.Sources)

		// Assign initial confidence.
		var confidence string
		if isExplicit {
			if n >= 3 {
				confidence = "established"
			} else {
				confidence = "stated"
			}
		} else {
			if n >= 5 {
				confidence = "established"
			} else {
				confidence = "emerging"
			}
		}

		// Recency downgrade: implicit-only candidates whose most-recent source
		// is older than the midpoint of the scanned range are suspect. A
		// candidate with no PR source is not old, it is elsewhere — see the
		// function doc.
		// A candidate carrying review evidence is not stale whatever its PR
		// numbers say: Step 8C merges a review signal into an existing PR rule,
		// and judging that merged rule on its one old PR number dropped it
		// outright — despite several reviews having flagged it since, which is
		// the freshest evidence there is.
		if !isExplicit && hasPRSource && !hasReviewSource && recencyCutoff > 0 && float64(maxSourcePR) < recencyCutoff {
			switch confidence {
			case "established":
				confidence = "emerging"
			case "emerging":
				result.Dropped++
				continue
			}
		}

		// Patch confidence and signal_count into the raw JSON, preserving all
		// other fields the semantic dedup step may have added.
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			result.Dropped++
			continue
		}
		if m == nil {
			m = map[string]json.RawMessage{}
		}
		confJSON, _ := json.Marshal(confidence)
		m["confidence"] = confJSON
		scJSON, _ := json.Marshal(n)
		m["signal_count"] = scJSON

		patched, err := json.Marshal(m)
		if err != nil {
			result.Dropped++
			continue
		}
		result.Kept = append(result.Kept, patched)
	}

	return result, nil
}
