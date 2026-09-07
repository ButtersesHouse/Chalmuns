package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode"

	"github.com/ButtersesHouse/Chalmuns/internal/review"
)

// groundingSignal extracts only the fields needed for grounding verification;
// all other fields are passed through unchanged via the outer json.RawMessage.
type groundingSignal struct {
	RawSignal struct {
		PRNumber int    `json:"pr_number"`
		Snippet  string `json:"snippet"`
		// ReviewID names a captured code-review artifact instead of a PR. A
		// signal mined from a watched reviewer has no PR to be grounded
		// against, so it names the review it was read out of and is checked
		// against that file in the review cache. The two are exclusive:
		// whichever is set decides which corpus the snippet must appear in.
		ReviewID string `json:"review_id"`
	} `json:"raw_signal"`
}

// GroundingDirs names the corpora a signal may be grounded against. A run
// passes whichever it has: a PR run only Cache, a --learn-reviews run only
// ReviewCache, a combined run both.
type GroundingDirs struct {
	Cache       string // PR raw cache — pr-N.json
	ReviewCache string // captured review artifacts — review-<id>.json
}

// GroundingResult is the output of RunVerifyGrounding.
type GroundingResult struct {
	Kept  []json.RawMessage `json:"kept"`
	Stats GroundingStats    `json:"stats"`
}

// GroundingStats counts outcomes for each signal.
type GroundingStats struct {
	TooShort int `json:"too_short"` // snippet < 20 runes after trimming
	NotFound int `json:"not_found"` // not a substring of the cached PR file
	Kept     int `json:"kept"`
}

// RunVerifyGrounding reads signals from stdin, checks each against the
// artifact it names — a cached PR, or a captured review — and writes a
// GroundingResult to stdout.
//
// Usage: verify-grounding [--cache-dir <dir>] [--review-cache-dir <dir>]
// (signals JSON array on stdin). At least one directory is required; which
// one a given signal needs is decided by the signal itself.
func RunVerifyGrounding(args []string) error {
	if err := checkFlags(args, []string{"--cache-dir", "--review-cache-dir"}, nil); err != nil {
		return err
	}

	dirs := GroundingDirs{
		Cache:       flagVal(args, "--cache-dir", ""),
		ReviewCache: flagVal(args, "--review-cache-dir", ""),
	}
	if dirs.Cache == "" && dirs.ReviewCache == "" {
		return fmt.Errorf("--cache-dir or --review-cache-dir required")
	}

	var rawSignals []json.RawMessage
	if err := json.NewDecoder(os.Stdin).Decode(&rawSignals); err != nil {
		return fmt.Errorf("decode stdin: %w", err)
	}

	result, err := VerifyGrounding(rawSignals, dirs)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

// VerifyGrounding applies grounding checks to a slice of raw signal JSON
// objects. Each signal must carry a .raw_signal.snippet (≥20 chars) that
// appears as a substring of the artifact it names (after normalisation): the
// pr-N.json cache file for a PR signal, or the review-<id>.json artifact for
// a signal mined from a watched code reviewer. Signals that fail either check
// are dropped and counted.
func VerifyGrounding(rawSignals []json.RawMessage, dirs GroundingDirs) (GroundingResult, error) {
	var result GroundingResult
	// Cache normalised artifact values keyed by path to avoid re-reading.
	fileCache := map[string][]string{}

	for _, raw := range rawSignals {
		var sig groundingSignal
		if err := json.Unmarshal(raw, &sig); err != nil {
			result.Stats.NotFound++
			continue
		}

		snippet := strings.TrimSpace(sig.RawSignal.Snippet)

		// Rule 1: snippet must be ≥ 20 characters (runes, not bytes).
		if len([]rune(snippet)) < 20 {
			result.Stats.TooShort++
			continue
		}

		reviewID := strings.TrimSpace(sig.RawSignal.ReviewID)
		var path string
		switch {
		case reviewID != "":
			// A review signal with nowhere to be grounded is a misconfigured
			// run, not an ungrounded signal. Counting it as not_found would
			// report "the reviewer never said that" about every rule in the
			// batch, so refuse the run and name the missing flag instead.
			if dirs.ReviewCache == "" {
				return GroundingResult{}, fmt.Errorf(
					"signal cites review %q but no --review-cache-dir was given; "+
						"review signals are grounded against the captured artifact, not a PR", reviewID)
			}
			// The ID comes from the extraction subagent and is joined into a
			// path, so it is checked against the shape capture mints before it
			// can name a file. Unchecked, "../state" or "../../../signals"
			// walks out of the review cache and grounds the batch against some
			// other JSON on disk — including, at its worst, the very file of
			// signals being verified, which would pass every fabricated quote
			// in it. The PR arm cannot do this because a PR number is an int.
			if !review.ValidReviewID(reviewID) {
				result.Stats.NotFound++
				continue
			}
			path = review.ArtifactPath(dirs.ReviewCache, reviewID)
		case sig.RawSignal.PRNumber != 0:
			if dirs.Cache == "" {
				return GroundingResult{}, fmt.Errorf(
					"signal cites PR #%d but no --cache-dir was given", sig.RawSignal.PRNumber)
			}
			path = fmt.Sprintf("%s/pr-%d.json", dirs.Cache, sig.RawSignal.PRNumber)
		default:
			// Neither a PR nor a review: nothing to check the quote against.
			result.Stats.NotFound++
			continue
		}

		// Rule 2: normalised snippet must be a substring of the normalised
		// artifact content. The normalisation tolerates whitespace
		// differences (wrapped lines, escaped newlines).
		normSnippet := NormalizeForGrounding(snippet)

		values, ok := fileCache[path]
		if !ok {
			data, err := os.ReadFile(path)
			if err != nil {
				result.Stats.NotFound++
				continue
			}
			values = groundableValues(data)
			fileCache[path] = values
		}

		if !containsAny(values, normSnippet) {
			result.Stats.NotFound++
			continue
		}

		result.Kept = append(result.Kept, raw)
		result.Stats.Kept++
	}

	return result, nil
}

// groundableValues returns the normalised strings a snippet may be found in.
// The cache is JSON, so string values are stored escaped (a quote as `\"`, a
// newline as the two characters `\n`); matching against the raw bytes would
// falsely drop any multi-line or quote-containing snippet. Decode the JSON and
// take its decoded string values instead. Falls back to the whole raw text
// when the file does not parse as JSON.
//
// Each value is kept separate rather than concatenated, and that separation is
// the check. A reviewer's remark lives inside one value — one comment body,
// one finding's message. Joining every value into one corpus put unrelated
// fields next to each other, so a snippet running from the end of one into the
// start of another verified as something the reviewer said verbatim when no
// such sentence was ever written. Requiring the whole snippet to sit inside a
// single value makes a quote provably one person's words.
func groundableValues(data []byte) []string {
	var doc interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		return []string{NormalizeForGrounding(string(data))}
	}
	var out []string
	collectStrings(doc, &out)
	return out
}

// collectStrings walks a decoded JSON document appending every string value,
// normalised for comparison.
func collectStrings(v interface{}, out *[]string) {
	switch t := v.(type) {
	case string:
		*out = append(*out, NormalizeForGrounding(t))
	case []interface{}:
		for _, e := range t {
			collectStrings(e, out)
		}
	case map[string]interface{}:
		for _, e := range t {
			collectStrings(e, out)
		}
	}
}

// containsAny reports whether the snippet appears inside any single value.
func containsAny(values []string, snippet string) bool {
	for _, v := range values {
		if strings.Contains(v, snippet) {
			return true
		}
	}
	return false
}

// NormalizeForGrounding lowercases s and collapses all whitespace runs
// (spaces, tabs, newlines) to single spaces. This tolerates minor formatting
// differences between the extracted snippet and the stored raw JSON.
func NormalizeForGrounding(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(s))
	inSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !inSpace {
				b.WriteByte(' ')
				inSpace = true
			}
		} else {
			b.WriteRune(r)
			inSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}
