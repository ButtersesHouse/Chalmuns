package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	result, err := VerifyGrounding([]json.RawMessage{sig}, dir)
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
	result, err := VerifyGrounding([]json.RawMessage{sig}, dir)
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
	result, err := VerifyGrounding([]json.RawMessage{sig}, dir)
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
	result, err := VerifyGrounding([]json.RawMessage{sig}, dir)
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
	result, err := VerifyGrounding([]json.RawMessage{sig}, dir)
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
	result, _ := VerifyGrounding([]json.RawMessage{sig}, dir)
	if result.Stats.Kept != 1 {
		t.Errorf("case-insensitive match should be kept; kept=%d", result.Stats.Kept)
	}
}

func TestVerifyGrounding_missingCacheFile(t *testing.T) {
	dir := t.TempDir()
	snippet := "could we use context.WithTimeout here?"
	// No cache file written — should count as not_found.
	sig := makeSignal(t, snippet, 999)
	result, err := VerifyGrounding([]json.RawMessage{sig}, dir)
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
	result, _ := VerifyGrounding([]json.RawMessage{raw}, dir)
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
