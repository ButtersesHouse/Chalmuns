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
// writes a ClassifyResult to stdout. Guaranteed to set signal_count =
// len(sources) on every kept candidate (prevents drift from hand-maintenance).
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

// countEvidence counts the distinct occasions a candidate's sources came from:
// one per review, one per PR, and one per source that names neither (a manual
// or discovered rule, which is its own occasion).
//
// It is applied only to candidates that carry at least one review source. A
// candidate mined purely from PRs keeps the historical signal count, so adding
// Review Mode does not silently re-grade every rule learned before it existed.
func countEvidence(sources []classifySource) int {
	seen := map[string]bool{}
	loose := 0
	for _, s := range sources {
		switch {
		case s.ReviewID != "":
			seen["rev:"+s.ReviewID] = true
		case s.PRNumber > 0:
			seen[fmt.Sprintf("pr:%d", s.PRNumber)] = true
		default:
			loose++
		}
	}
	return len(seen) + loose
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
// A candidate carrying review evidence is counted in distinct occasions rather
// than signals — one review is one occasion however many findings it held —
// see countEvidence.
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
		// Once review evidence is in play, evidence is counted in distinct
		// occasions rather than signals: one review is one occasion however
		// many findings it held. Counting signals graded a single linter run
		// that tripped one check in five files as "established" — top tier,
		// which the approval UI sanctions bulk-approving.
		n := len(c.Sources)
		if hasReviewSource {
			n = countEvidence(c.Sources)
		}

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
