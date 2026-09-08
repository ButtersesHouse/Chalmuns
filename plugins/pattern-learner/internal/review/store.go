package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ButtersesHouse/Chalmuns/internal/fsatomic"
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
	// One atomic write, shared with state.Write — see internal/fsatomic.
	if err := fsatomic.WriteFile(path, data); err != nil {
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

// ListArtifactMeta returns only what a status tally needs, without decoding
// each artifact's RawText — which is the bulk of the file and can be megabytes
// apiece. `watch --list` runs on the routine status path and has no use for
// the review bodies.
func ListArtifactMeta(cacheDir string) ([]Artifact, error) {
	matches, err := filepath.Glob(filepath.Join(cacheDir, artifactGlob))
	if err != nil {
		return nil, err
	}
	var out []Artifact
	for _, path := range matches {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			// Warned, not swallowed: ListArtifacts warns on exactly this, and
			// which lister a caller happened to use must not decide whether a
			// corrupt artifact is visible or vanishes from every tally and
			// every selection.
			fmt.Fprintf(os.Stderr, "warn: skip %s: %v\n", path, readErr)
			continue
		}
		var meta struct {
			ReviewID   string `json:"review_id"`
			Source     string `json:"source"`
			CapturedAt string `json:"captured_at"`
		}
		if err := json.Unmarshal(data, &meta); err != nil {
			fmt.Fprintf(os.Stderr, "warn: skip %s: %v\n", path, err)
			continue
		}
		captured := meta.CapturedAt
		if capturedTime(captured).IsZero() {
			// A stamp that does not parse is not a position on the watermark
			// line, and every way of handling that downstream is bad: skipping
			// the artifact loses the review the moment a watermark exists,
			// keeping it re-mines it on every run forever. The file's
			// modification time is a position — it is when the artifact
			// appeared — so the stamp is repaired here and the rest of the
			// path never has to know.
			if fi, statErr := os.Stat(path); statErr == nil {
				captured = fi.ModTime().UTC().Format(time.RFC3339Nano)
				fmt.Fprintf(os.Stderr,
					"warn: %s has an unreadable captured_at (%q); using the file's timestamp (%s)\n",
					meta.ReviewID, meta.CapturedAt, captured)
			} else {
				fmt.Fprintf(os.Stderr,
					"warn: %s has an unreadable captured_at (%q) and its file could not be dated: %v\n",
					meta.ReviewID, meta.CapturedAt, statErr)
			}
		}
		out = append(out, Artifact{ReviewID: meta.ReviewID, Source: meta.Source, CapturedAt: captured})
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
		t := capturedTime(a.CapturedAt)
		if t.IsZero() {
			// Unreachable through ListArtifactMeta, which repairs a stamp it
			// cannot parse rather than handing one on. Kept because Select is
			// exported and takes a caller's slice: without it, one such
			// artifact sorts first, is never after any watermark, and vanishes
			// with no warning at all.
			fmt.Fprintf(os.Stderr,
				"warn: %s has an unreadable captured_at (%q) and is skipped\n",
				a.ReviewID, a.CapturedAt)
			continue
		}
		if t.After(mark) {
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

// ExtractLean reads cacheDir and returns the lean views a run should mine,
// plus the watermark a watermark-driven run should advance to.
//
// Selection happens on metadata alone and only the chosen artifacts' bodies
// are read. A watermark run typically wants one or two of hundreds, and each
// artifact carries its whole review text — decoding the entire cache to throw
// almost all of it away was the bulk of the work on a path that runs on every
// review-mode invocation.
func ExtractLean(cacheDir string, ids []string, since string) (lean []LeanReview, watermark string, unreadable []string, err error) {
	meta, err := ListArtifactMeta(cacheDir)
	if err != nil {
		return nil, "", nil, err
	}
	lean, watermark, unreadable = ExtractLeanFrom(cacheDir, meta, ids, since)
	return lean, watermark, unreadable, nil
}

// ExtractLeanFrom is ExtractLean over a metadata listing the caller already
// has, so a run that validated ids against the cache does not read it twice.
//
// meta must be ordered oldest-first, as ListArtifactMeta returns it: the
// watermark is a position in that order, not a maximum over the set.
//
// unreadable names the artifacts this run selected but could not read. They
// are reported rather than swallowed because neither silent option is honest.
// Advancing the watermark over a broken artifact loses that review on nothing
// louder than a stderr warning; holding the watermark behind one pins it
// forever, so a single truncated file makes every later run re-mine the whole
// tail of the cache and re-present conventions the user already decided on. So
// the watermark advances over everything this run considered — read or not —
// and the run says once what it could not read, so the file can be repaired or
// deleted.
//
// An artifact whose captured_at does not parse is normally not reported here,
// because it is not lost: ListArtifactMeta repairs the stamp from the file's
// timestamp and warns, so the artifact is mined once like any other and the
// watermark moves past it. One that reaches here still unparseable is a
// repair that could not run, and that one is a loss like any other.
func ExtractLeanFrom(cacheDir string, meta []Artifact, ids []string, since string) (lean []LeanReview, watermark string, unreadable []string) {
	chosen := Select(meta, ids, since)
	full := make([]Artifact, 0, len(chosen))
	for _, m := range chosen {
		a, readErr := ReadArtifact(cacheDir, m.ReviewID)
		if readErr != nil {
			fmt.Fprintf(os.Stderr, "warn: skip %s: %v\n", m.ReviewID, readErr)
			unreadable = append(unreadable, m.ReviewID)
			continue
		}
		// The metadata stamp is authoritative: ListArtifactMeta may have
		// repaired one the file could not supply, and the lean view is what
		// the subagent orders contradictions by and what the approval line
		// prints. Leaving the file's own value here meant the one artifact the
		// repair exists for was still unorderable everywhere it was used.
		a.CapturedAt = m.CapturedAt
		full = append(full, a)
	}

	// An explicit id list never advances the watermark: stepping past reviews
	// the run skipped on purpose would make them unmineable for good.
	if len(ids) == 0 {
		// A stamp that still does not parse means the repair could not run —
		// the file went away between the read and the stat. Select drops such
		// an artifact from a watermark run, so it is a real loss and is named
		// rather than left to a stderr line the run never relays.
		//
		// Only the ones this run actually dropped: reporting every bad stamp
		// in the cache announced a loss for an artifact the same run had just
		// delivered (a run with no watermark selects everything, bad stamps
		// included), and named it again on every run after that.
		if since != "" {
			for _, m := range meta {
				if capturedTime(m.CapturedAt).IsZero() {
					unreadable = append(unreadable, m.ReviewID)
				}
			}
		}
		// The newest artifact considered, not the newest one read. Taking it
		// from what was read left an unreadable *newest* artifact on the wrong
		// side of the watermark, so it was re-selected and re-reported on every
		// run from then on — the pinning this design exists to avoid. The stamp
		// itself must parse: chosen is ordered with unparseable stamps first,
		// so the last parseable one is the newest, and handing back an
		// unparseable stamp would make the next run reject its own watermark.
		for i := len(chosen) - 1; i >= 0; i-- {
			if !capturedTime(chosen[i].CapturedAt).IsZero() {
				watermark = chosen[i].CapturedAt
				break
			}
		}
	}
	return Lean(full), watermark, unreadable
}

// MissingIDs returns the requested ids that name no artifact in the cache.
// An unknown id selects nothing, and "nothing" is the same answer as "already
// mined" — so a typo would be reported to the user as a review already
// consumed, when in fact none was looked at.
func MissingIDs(all []Artifact, ids []string) []string {
	have := map[string]bool{}
	for _, a := range all {
		have[a.ReviewID] = true
	}
	var missing []string
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !have[id] {
			missing = append(missing, id)
		}
	}
	return missing
}
