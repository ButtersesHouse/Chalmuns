package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The review cache mirrors the PR raw-cache: one JSON file per captured
// artifact, named for its ID, in a directory the run is pointed at. Keeping
// the two caches parallel means an artifact is grounded, re-read, and
// inspected the same way a cached PR is.

// artifactGlob matches every artifact file in a cache directory. IDs carry
// their own "rev-" prefix (see ReviewID), so the file is named for the ID
// alone rather than prefixed again.
const artifactGlob = "rev-*.json"

// ArtifactPath is where an artifact with this ID lives under cacheDir.
func ArtifactPath(cacheDir, reviewID string) string {
	return filepath.Join(cacheDir, reviewID+".json")
}

// WriteArtifact stores a normalized artifact, creating cacheDir if needed. It
// reports whether the artifact was already present: capture is
// content-addressed, so a repeat capture of one review is a no-op rather than
// a second piece of evidence for the same convention.
func WriteArtifact(cacheDir string, a Artifact) (written bool, err error) {
	if a.ReviewID == "" {
		return false, fmt.Errorf("artifact has no review_id")
	}
	path := ArtifactPath(cacheDir, a.ReviewID)
	if _, err := os.Stat(path); err == nil {
		return false, nil
	}
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return false, err
	}
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return false, err
	}
	// The temp file must be unique per writer, not per artifact. A fixed
	// "<path>.tmp" is shared by every process writing that ReviewID, and two
	// captures of one review are entirely ordinary — the hook firing while the
	// user runs `capture-review --file` on the same report, or two parallel
	// calls of a watched tool whose output is identical. Two writers of
	// different lengths interleaving on one temp file left a permanently
	// corrupt artifact: the early-return above means no later capture ever
	// repairs it, ListArtifacts skips it with a warning, and the review is
	// never mined.
	tmp, err := os.CreateTemp(cacheDir, a.ReviewID+".*.tmp")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return false, err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return false, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return false, err
	}
	return true, nil
}

// ReadArtifact loads one artifact by ID.
//
// The ID is checked before it names a file. Today every caller already holds a
// minted ID or has validated one, so this guards nothing — which is exactly
// why it belongs here: the function is exported, takes an ID as a string, and
// joins it into a path, so the first caller to pass one through from a model
// or a payload turns it into a directory traversal. Confining it at the read
// itself means that caller cannot get it wrong.
func ReadArtifact(cacheDir, reviewID string) (Artifact, error) {
	if !ValidReviewID(reviewID) {
		return Artifact{}, fmt.Errorf("%q is not an artifact id", reviewID)
	}
	data, err := os.ReadFile(ArtifactPath(cacheDir, reviewID))
	if err != nil {
		return Artifact{}, err
	}
	var a Artifact
	if err := json.Unmarshal(data, &a); err != nil {
		return Artifact{}, fmt.Errorf("parse %s: %w", ArtifactPath(cacheDir, reviewID), err)
	}
	return a, nil
}

// ListArtifacts returns every artifact in cacheDir, oldest capture first. A
// file that does not parse is skipped with a warning rather than failing the
// batch, matching ExtractLean's handling of a bad PR cache file.
func ListArtifacts(cacheDir string) ([]Artifact, error) {
	matches, err := filepath.Glob(filepath.Join(cacheDir, artifactGlob))
	if err != nil {
		return nil, err
	}
	var out []Artifact
	for _, path := range matches {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			fmt.Fprintf(os.Stderr, "warn: skip %s: %v\n", path, readErr)
			continue
		}
		var a Artifact
		if err := json.Unmarshal(data, &a); err != nil {
			fmt.Fprintf(os.Stderr, "warn: skip %s: %v\n", path, err)
			continue
		}
		out = append(out, a)
	}
	sortArtifacts(out)
	return out, nil
}

// sortArtifacts orders by capture time, then ID so the order is total and
// stable — two artifacts captured in the same instant must not swap between
// runs, or a watermark could step over one of them.
func sortArtifacts(as []Artifact) {
	sort.Slice(as, func(i, j int) bool {
		ti, tj := capturedTime(as[i].CapturedAt), capturedTime(as[j].CapturedAt)
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return as[i].ReviewID < as[j].ReviewID
	})
}

// capturedTime parses a stamp for comparison. Stamps are compared as times
// rather than as strings because the two are not equivalent once sub-second
// precision is involved: "…05.5Z" sorts *before* "…05Z" lexically, so a string
// compare would silently skip artifacts written by a different build. An
// unparseable stamp sorts first, where it is visible rather than skipped.
func capturedTime(s string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	return time.Time{}
}

// LeanReview is the preprocessed view of one captured review handed to the
// extraction subagent — the review path's counterpart to pipeline.LeanPR.
type LeanReview struct {
	ReviewID     string    `json:"review_id"`
	Source       string    `json:"source"`
	Format       string    `json:"format"`
	CapturedAt   string    `json:"captured_at"`
	Label        string    `json:"label,omitempty"`
	FilesTouched []string  `json:"files_touched"`
	Findings     []Finding `json:"findings,omitempty"`
	// Text carries the review verbatim, and is set only when the format
	// produced no structured findings. Sending both would double every prose
	// review in the prompt for no gain.
	Text string `json:"text,omitempty"`
}

// Select filters artifacts to the ones a run should mine: an explicit ID list
// when given, otherwise everything captured strictly after the `since`
// watermark (an empty watermark selects all).
func Select(all []Artifact, ids []string, since string) []Artifact {
	if len(ids) > 0 {
		want := map[string]bool{}
		for _, id := range ids {
			want[strings.TrimSpace(id)] = true
		}
		var out []Artifact
		for _, a := range all {
			if want[a.ReviewID] {
				out = append(out, a)
			}
		}
		return out
	}
	if since == "" {
		return all
	}
	// Compared as times, not strings — see capturedTime. Stamps now carry
	// sub-second precision so two captures in one second stay distinguishable;
	// a second-resolution watermark from an older run still compares correctly
	// because both sides are parsed.
	mark := capturedTime(since)
	var out []Artifact
	for _, a := range all {
		// An unparseable stamp is included rather than dropped. It sorts
		// first, so it is visible at the head of the batch — whereas skipping
		// it makes the review captured, counted by `watch --list`, and
		// permanently unmineable with nothing to explain the silence.
		if t := capturedTime(a.CapturedAt); t.IsZero() || t.After(mark) {
			out = append(out, a)
		}
	}
	return out
}

// Lean converts artifacts to the subagent-facing view.
func Lean(as []Artifact) []LeanReview {
	out := make([]LeanReview, 0, len(as))
	for _, a := range as {
		lr := LeanReview{
			ReviewID:   a.ReviewID,
			Source:     a.Source,
			Format:     a.Format,
			CapturedAt: a.CapturedAt,
			Label:      a.Label,
			Findings:   a.Findings,
		}
		seen := map[string]bool{}
		for _, f := range a.Findings {
			if f.File != "" && !seen[f.File] {
				seen[f.File] = true
				lr.FilesTouched = append(lr.FilesTouched, f.File)
			}
		}
		if len(a.Findings) == 0 {
			lr.Text = a.RawText
		}
		out = append(out, lr)
	}
	return out
}

// ExtractLean reads cacheDir and returns the lean views a run should mine.
func ExtractLean(cacheDir string, ids []string, since string) ([]LeanReview, error) {
	all, err := ListArtifacts(cacheDir)
	if err != nil {
		return nil, err
	}
	return Lean(Select(all, ids, since)), nil
}

// Newest returns the capture timestamp of the last artifact in an ordered
// list, for advancing the watermark. Empty when the list is empty.
func Newest(as []Artifact) string {
	if len(as) == 0 {
		return ""
	}
	return as[len(as)-1].CapturedAt
}
