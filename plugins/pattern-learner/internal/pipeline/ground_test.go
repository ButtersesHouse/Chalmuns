package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeForGrounding_basic(t *testing.T) {
	got := NormalizeForGrounding("  Hello\n\tWorld  ")
	want := "hello world"
	if got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}

func TestNormalizeForGrounding_multipleSpaces(t *testing.T) {
	got := NormalizeForGrounding("could  we   use  X?")
	want := "could we use x?"
	if got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}

func TestNormalizeForGrounding_newlines(t *testing.T) {
	got := NormalizeForGrounding("line1\nline2\r\nline3")
	want := "line1 line2 line3"
	if got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}

// makeSignal builds a raw signal JSON object with the given snippet and PR number.
func makeSignal(t *testing.T, snippet string, prNum int) json.RawMessage {
	t.Helper()
	m := map[string]interface{}{
		"title": "test rule",
		"rule":  "do the thing",
		"raw_signal": map[string]interface{}{
			"pr_number": prNum,
			"snippet":   snippet,
		},
	}
	b, _ := json.Marshal(m)
	return b
}

// writePRCache writes a minimal pr-N.json fixture containing the given body text.
func writePRCache(t *testing.T, dir, body string, prNum int) {
	t.Helper()
	content := fmt.Sprintf(`{"pr_number":%d,"raw":{"body":%s,"review_comments":[{"body":%s}]}}`,
		prNum,
		jsonString(body),
		jsonString(body),
	)
	path := filepath.Join(dir, fmt.Sprintf("pr-%d.json", prNum))
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestVerifyGrounding_tooShort(t *testing.T) {
	dir := t.TempDir()
	// 19 runes — below threshold.
	sig := makeSignal(t, "could we use X here", 1)
	result, err := VerifyGrounding([]json.RawMessage{sig}, GroundingDirs{Cache: dir})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.TooShort != 1 {
		t.Errorf("too_short: want 1, got %d", result.Stats.TooShort)
	}
	if len(result.Kept) != 0 {
		t.Error("expected signal to be dropped")
	}
}

func TestVerifyGrounding_exactlyTwenty(t *testing.T) {
	dir := t.TempDir()
	snippet := "could we use X here?" // 20 runes exactly
	writePRCache(t, dir, snippet, 1)
	sig := makeSignal(t, snippet, 1)
	result, err := VerifyGrounding([]json.RawMessage{sig}, GroundingDirs{Cache: dir})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.TooShort != 0 {
		t.Error("20-char snippet should not be dropped as too_short")
	}
	if result.Stats.Kept != 1 {
		t.Errorf("expected 1 kept, got %d (not_found=%d)", result.Stats.Kept, result.Stats.NotFound)
	}
}

func TestVerifyGrounding_notFound(t *testing.T) {
	dir := t.TempDir()
	snippet := "this exact phrase is not in the cache file at all"
	writePRCache(t, dir, "completely different content here", 1)
	sig := makeSignal(t, snippet, 1)
	result, err := VerifyGrounding([]json.RawMessage{sig}, GroundingDirs{Cache: dir})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.NotFound != 1 {
		t.Errorf("not_found: want 1, got %d", result.Stats.NotFound)
	}
	if len(result.Kept) != 0 {
		t.Error("expected signal to be dropped")
	}
}

func TestVerifyGrounding_found(t *testing.T) {
	dir := t.TempDir()
	snippet := "could we use context.WithTimeout here?"
	writePRCache(t, dir, snippet, 42)
	sig := makeSignal(t, snippet, 42)
	result, err := VerifyGrounding([]json.RawMessage{sig}, GroundingDirs{Cache: dir})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.Kept != 1 {
		t.Errorf("kept: want 1, got %d (not_found=%d, too_short=%d)",
			result.Stats.Kept, result.Stats.NotFound, result.Stats.TooShort)
	}
}

func TestVerifyGrounding_whitespaceCollapse(t *testing.T) {
	dir := t.TempDir()
	// Snippet has extra interior spaces; cache body has single spaces.
	// Both should normalize to the same string via whitespace collapse.
	snippet := "could  we   use  context.WithTimeout  here?"
	cacheBody := "could we use context.WithTimeout here?"
	writePRCache(t, dir, cacheBody, 5)
	sig := makeSignal(t, snippet, 5)
	result, err := VerifyGrounding([]json.RawMessage{sig}, GroundingDirs{Cache: dir})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.Kept != 1 {
		t.Errorf("whitespace-collapsed match should be kept; kept=%d not_found=%d",
			result.Stats.Kept, result.Stats.NotFound)
	}
}

func TestVerifyGrounding_caseInsensitive(t *testing.T) {
	dir := t.TempDir()
	snippet := "COULD WE USE Context.WithTimeout HERE?"
	cacheBody := "could we use context.withTimeout here?"
	writePRCache(t, dir, cacheBody, 7)
	sig := makeSignal(t, snippet, 7)
	result, _ := VerifyGrounding([]json.RawMessage{sig}, GroundingDirs{Cache: dir})
	if result.Stats.Kept != 1 {
		t.Errorf("case-insensitive match should be kept; kept=%d", result.Stats.Kept)
	}
}

func TestVerifyGrounding_snippetWithQuotes(t *testing.T) {
	// A snippet containing double quotes is stored JSON-escaped (\") in the
	// cache file; grounding must match against the decoded value.
	dir := t.TempDir()
	snippet := `we prefer errors.New("not found") over fmt.Errorf here`
	writePRCache(t, dir, snippet, 11)
	sig := makeSignal(t, snippet, 11)
	result, err := VerifyGrounding([]json.RawMessage{sig}, GroundingDirs{Cache: dir})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.Kept != 1 {
		t.Errorf("quote-containing snippet should be kept; kept=%d not_found=%d",
			result.Stats.Kept, result.Stats.NotFound)
	}
}

func TestVerifyGrounding_multilineSnippet(t *testing.T) {
	// Priority-0 suggestion-block snippets are multi-line code. In the cache
	// file newlines are stored as the escape sequence \n, which is not
	// whitespace in the raw bytes — grounding must decode before matching.
	dir := t.TempDir()
	snippet := "ctx, cancel := context.WithTimeout(ctx, 5*time.Second)\ndefer cancel()"
	writePRCache(t, dir, snippet, 12)
	sig := makeSignal(t, snippet, 12)
	result, err := VerifyGrounding([]json.RawMessage{sig}, GroundingDirs{Cache: dir})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.Kept != 1 {
		t.Errorf("multi-line snippet should be kept; kept=%d not_found=%d",
			result.Stats.Kept, result.Stats.NotFound)
	}
}

func TestGroundableValues_invalidJSONFallsBack(t *testing.T) {
	raw := "not json at all { but contains the snippet text"
	got := groundableValues([]byte(raw))
	if len(got) != 1 || got[0] != NormalizeForGrounding(raw) {
		t.Errorf("invalid JSON should fall back to the whole raw text; got %q", got)
	}
}

func TestVerifyGrounding_missingCacheFile(t *testing.T) {
	dir := t.TempDir()
	snippet := "could we use context.WithTimeout here?"
	// No cache file written — should count as not_found.
	sig := makeSignal(t, snippet, 999)
	result, err := VerifyGrounding([]json.RawMessage{sig}, GroundingDirs{Cache: dir})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.NotFound != 1 {
		t.Errorf("missing cache file should be not_found; got %+v", result.Stats)
	}
}

func TestVerifyGrounding_passthrough(t *testing.T) {
	// Raw signal JSON should be returned unchanged (all fields preserved).
	dir := t.TempDir()
	snippet := "could we use context.WithTimeout here?"
	writePRCache(t, dir, snippet, 1)
	raw := makeSignal(t, snippet, 1)
	result, _ := VerifyGrounding([]json.RawMessage{raw}, GroundingDirs{Cache: dir})
	if result.Stats.Kept != 1 {
		t.Fatal("signal should be kept")
	}
	var orig, kept map[string]interface{}
	json.Unmarshal(raw, &orig)
	json.Unmarshal(result.Kept[0], &kept)
	if orig["title"] != kept["title"] {
		t.Error("title field should be preserved in kept signal")
	}
}

// makeReviewSignal builds a raw signal that cites a captured review rather
// than a PR — the shape Review Mode's extraction subagent emits.
func makeReviewSignal(t *testing.T, snippet, reviewID string) json.RawMessage {
	t.Helper()
	m := map[string]interface{}{
		"title": "test rule",
		"rule":  "do the thing",
		"raw_signal": map[string]interface{}{
			"review_id": reviewID,
			"reviewer":  "code-review",
			"snippet":   snippet,
		},
	}
	b, _ := json.Marshal(m)
	return b
}

// writeReviewArtifact writes a review-cache artifact whose finding body is
// the given text, matching internal/review's on-disk shape.
func writeReviewArtifact(t *testing.T, dir, reviewID, body string) {
	t.Helper()
	content := fmt.Sprintf(
		`{"review_id":%s,"source":"code-review","format":"findings","captured_at":"2026-01-15T10:04:00Z",`+
			`"findings":[{"index":0,"file":"a.go","body":%s}],"raw_text":%s}`,
		jsonString(reviewID), jsonString(body), jsonString(body))
	if err := os.WriteFile(filepath.Join(dir, reviewID+".json"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyGrounding_reviewSignalFound(t *testing.T) {
	dir := t.TempDir()
	snippet := "This codebase wraps errors with %w so callers can errors.Is them."
	writeReviewArtifact(t, dir, "rev-abc123def456", snippet)

	result, err := VerifyGrounding(
		[]json.RawMessage{makeReviewSignal(t, snippet, "rev-abc123def456")},
		GroundingDirs{ReviewCache: dir})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.Kept != 1 {
		t.Errorf("kept: want 1, got %d (not_found=%d too_short=%d)",
			result.Stats.Kept, result.Stats.NotFound, result.Stats.TooShort)
	}
}

// The whole point of grounding: a rule may not cite words the reviewer never
// wrote, whichever corpus it claims to come from.
func TestVerifyGrounding_reviewSignalFabricatedQuote(t *testing.T) {
	dir := t.TempDir()
	writeReviewArtifact(t, dir, "rev-abc123def456", "the reviewer said something else entirely")

	result, err := VerifyGrounding(
		[]json.RawMessage{makeReviewSignal(t, "a sentence the reviewer never wrote at all", "rev-abc123def456")},
		GroundingDirs{ReviewCache: dir})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.NotFound != 1 || result.Stats.Kept != 0 {
		t.Errorf("a fabricated quote must be dropped; stats %+v", result.Stats)
	}
}

func TestVerifyGrounding_reviewSignalMissingArtifact(t *testing.T) {
	result, err := VerifyGrounding(
		[]json.RawMessage{makeReviewSignal(t, "a snippet long enough to pass the length check", "rev-000000000000")},
		GroundingDirs{ReviewCache: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.NotFound != 1 {
		t.Errorf("a signal citing an absent review is not grounded; stats %+v", result.Stats)
	}
}

// A review batch with no review cache is a misconfigured run, not a batch of
// ungrounded signals. Reporting it as not_found would tell the user the
// reviewer never said any of it.
func TestVerifyGrounding_reviewSignalWithoutReviewCacheIsAnError(t *testing.T) {
	_, err := VerifyGrounding(
		[]json.RawMessage{makeReviewSignal(t, "a snippet long enough to pass the length check", "rev-abc123def456")},
		GroundingDirs{Cache: t.TempDir()})
	if err == nil {
		t.Fatal("want an error naming --review-cache-dir")
	}
	if !strings.Contains(err.Error(), "--review-cache-dir") {
		t.Errorf("the error must name the missing flag; got %v", err)
	}
}

// One batch may legitimately hold both kinds; each is checked against its own
// corpus.
func TestVerifyGrounding_mixedCorpora(t *testing.T) {
	prDir, reviewDir := t.TempDir(), t.TempDir()
	prSnippet := "could we use context.WithTimeout here?"
	reviewSnippet := "This codebase wraps errors with %w so callers can errors.Is them."
	writePRCache(t, prDir, prSnippet, 42)
	writeReviewArtifact(t, reviewDir, "rev-abc123def456", reviewSnippet)

	result, err := VerifyGrounding([]json.RawMessage{
		makeSignal(t, prSnippet, 42),
		makeReviewSignal(t, reviewSnippet, "rev-abc123def456"),
	}, GroundingDirs{Cache: prDir, ReviewCache: reviewDir})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.Kept != 2 {
		t.Errorf("both signals should ground; stats %+v", result.Stats)
	}
}

// The length rule is the same rule, applied before either corpus is opened.
func TestVerifyGrounding_reviewSignalTooShort(t *testing.T) {
	result, err := VerifyGrounding(
		[]json.RawMessage{makeReviewSignal(t, "too short", "rev-abc123def456")},
		GroundingDirs{ReviewCache: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.TooShort != 1 {
		t.Errorf("stats %+v", result.Stats)
	}
}

// A review_id is model-authored and is joined into a path, so it must be
// proven to be an artifact ID before it can name a file. Unchecked, it walks
// out of the review cache and grounds the batch against some other JSON on
// disk — at worst the very signals file being verified, which would pass every
// fabricated quote in it.
func TestVerifyGrounding_reviewIDCannotEscapeTheCache(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside")
	cache := filepath.Join(dir, "review-cache")
	for _, d := range []string{outside, cache} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	snippet := "a sentence that lives only in the file outside the review cache"
	if err := os.WriteFile(filepath.Join(outside, "state.json"),
		[]byte(`{"rules":[{"snippet":`+jsonString(snippet)+`}]}`), 0644); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"../outside/state", "../../etc/passwd", "rev-../outside/state", "rev-XYZ", ""} {
		sig := makeReviewSignal(t, snippet, id)
		result, err := VerifyGrounding([]json.RawMessage{sig}, GroundingDirs{ReviewCache: cache})
		if err != nil {
			t.Fatalf("review_id %q: %v", id, err)
		}
		if result.Stats.Kept != 0 {
			t.Errorf("review_id %q grounded against a file outside the cache", id)
		}
	}
}

// Grounding proves a snippet is one reviewer's verbatim words, so the whole
// snippet must sit inside a single value. Joining every value into one corpus
// let a quote run from the end of one field into the start of another and
// still verify — a sentence nobody wrote.
func TestVerifyGrounding_snippetCannotSpanTwoFields(t *testing.T) {
	dir := t.TempDir()
	content := `{"review_id":"rev-abc123def456","findings":[{"body":"Wrap errors with %w so callers can errors.Is them.",` +
		`"evidence":"A caller doing errors.Is gets false and returns 500."}],"raw_text":"unrelated"}`
	if err := os.WriteFile(filepath.Join(dir, "rev-abc123def456.json"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// Each field on its own grounds.
	for _, snippet := range []string{
		"Wrap errors with %w so callers can errors.Is them.",
		"A caller doing errors.Is gets false and returns 500.",
	} {
		result, err := VerifyGrounding(
			[]json.RawMessage{makeReviewSignal(t, snippet, "rev-abc123def456")},
			GroundingDirs{ReviewCache: dir})
		if err != nil {
			t.Fatal(err)
		}
		if result.Stats.Kept != 1 {
			t.Errorf("a whole field should ground; %q got %+v", snippet, result.Stats)
		}
	}

	// A quote welded across the two does not.
	spanning := "errors.Is them. A caller doing errors.Is gets false"
	result, err := VerifyGrounding(
		[]json.RawMessage{makeReviewSignal(t, spanning, "rev-abc123def456")},
		GroundingDirs{ReviewCache: dir})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.Kept != 0 {
		t.Error("a snippet spanning two separate fields is not anyone's verbatim words")
	}
}
