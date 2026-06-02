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
// Recency downgrade (implicit-only):
//
//	cutoff = maxPRSeen − (maxPRSeen − sincePR) × 0.5
//	If max(source.pr_number) < cutoff: established → emerging; emerging → dropped.
//	Explicit signals are exempt — a stated preference does not expire.
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
			result.Dropped++
			continue
		}

		isExplicit := false
		maxSourcePR := 0
		for _, src := range c.Sources {
			if src.Strength == "explicit" {
				isExplicit = true
			}
			if src.PRNumber > maxSourcePR {
				maxSourcePR = src.PRNumber
			}
		}
		n := len(c.Sources)

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
		// is older than the midpoint of the scanned range are suspect.
		if !isExplicit && recencyCutoff > 0 && float64(maxSourcePR) < recencyCutoff {
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
