package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode"
)

// groundingSignal extracts only the fields needed for grounding verification;
// all other fields are passed through unchanged via the outer json.RawMessage.
type groundingSignal struct {
	RawSignal struct {
		PRNumber int    `json:"pr_number"`
		Snippet  string `json:"snippet"`
	} `json:"raw_signal"`
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

// RunVerifyGrounding reads signals from stdin, checks each against its
// source PR cache file, and writes a GroundingResult to stdout.
//
// Usage: verify-grounding --cache-dir <dir>  (signals JSON array on stdin)
func RunVerifyGrounding(args []string) error {
	cacheDir := flagVal(args, "--cache-dir", "")
	if cacheDir == "" {
		return fmt.Errorf("--cache-dir required")
	}

	var rawSignals []json.RawMessage
	if err := json.NewDecoder(os.Stdin).Decode(&rawSignals); err != nil {
		return fmt.Errorf("decode stdin: %w", err)
	}

	result, err := VerifyGrounding(rawSignals, cacheDir)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

// VerifyGrounding applies grounding checks to a slice of raw signal JSON
// objects. Each signal must carry a .raw_signal.snippet (≥20 chars) that
// appears as a substring of the corresponding pr-N.json cache file (after
// normalisation). Signals that fail either check are dropped and counted.
func VerifyGrounding(rawSignals []json.RawMessage, cacheDir string) (GroundingResult, error) {
	var result GroundingResult
	// Cache normalised file contents keyed by PR number to avoid re-reading.
	fileCache := map[int]string{}

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

		prNum := sig.RawSignal.PRNumber
		if prNum == 0 {
			result.Stats.NotFound++
			continue
		}

		// Rule 2: normalised snippet must be a substring of the normalised
		// cache file content. The normalisation tolerates whitespace
		// differences (wrapped lines, escaped newlines).
		normSnippet := NormalizeForGrounding(snippet)

		fileContent, ok := fileCache[prNum]
		if !ok {
			data, err := os.ReadFile(fmt.Sprintf("%s/pr-%d.json", cacheDir, prNum))
			if err != nil {
				result.Stats.NotFound++
				continue
			}
			fileContent = NormalizeForGrounding(string(data))
			fileCache[prNum] = fileContent
		}

		if !strings.Contains(fileContent, normSnippet) {
			result.Stats.NotFound++
			continue
		}

		result.Kept = append(result.Kept, raw)
		result.Stats.Kept++
	}

	return result, nil
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
