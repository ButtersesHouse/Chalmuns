package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCodeBefore_basic(t *testing.T) {
	// Context lines have their leading space stripped (we want the actual code).
	// Removed lines (-) have their leading - stripped.
	// Added lines (+) are discarded.
	hunk := "@@ -10,4 +10,4 @@\n func foo() {\n-\tctx := context.Background()\n+\tctx, cancel := context.WithTimeout(ctx, 5*time.Second)\n+\tdefer cancel()\n }"
	got := CodeBefore(hunk)
	want := "func foo() {\n\tctx := context.Background()\n}"
	if got != want {
		t.Errorf("CodeBefore got:\n%q\nwant:\n%q", got, want)
	}
}

func TestCodeBefore_addOnly(t *testing.T) {
	// A hunk that only adds lines — code_before is context lines only.
	hunk := "@@ -10,2 +10,3 @@\n func foo() {\n+\tnewLine()\n }"
	got := CodeBefore(hunk)
	want := "func foo() {\n}"
	if got != want {
		t.Errorf("CodeBefore (add-only) got:\n%q\nwant:\n%q", got, want)
	}
}

func TestCodeBefore_CRLF(t *testing.T) {
	hunk := "@@ -1,2 +1,2 @@\r\n-oldLine\r\n+newLine\r\n context"
	got := CodeBefore(hunk)
	// CRLF normalised; @@ and +lines stripped; leading space stripped from context.
	if got != "oldLine\ncontext" {
		t.Errorf("CodeBefore (CRLF) got %q", got)
	}
}

func TestCodeBefore_emptyHunk(t *testing.T) {
	got := CodeBefore("")
	if got != "" {
		t.Errorf("CodeBefore empty hunk: want empty, got %q", got)
	}
}

func TestCodeBefore_headerOnly(t *testing.T) {
	got := CodeBefore("@@ -1,0 +1,1 @@")
	if got != "" {
		t.Errorf("CodeBefore header-only: want empty, got %q", got)
	}
}

// writeCacheFile writes a minimal pr-N.json fixture to dir.
func writeCacheFile(t *testing.T, dir string, prNum int, prJSON string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash("pr-"+itoa(prNum)+".json"))
	if err := os.WriteFile(path, []byte(prJSON), 0644); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

const fixtureCache = `{
  "pr_number": 42,
  "comment_sources": {"review_comments": 1, "issue_comments": 1, "review_bodies": 0},
  "raw": {
    "number": 42,
    "user": {"login": "alice"},
    "review_comments": [
      {
        "id": 12345,
        "user": {"login": "bob"},
        "body": "Could we use context.WithTimeout here?",
        "created_at": "2024-01-15T10:00:00Z",
        "path": "internal/api/handler.go",
        "diff_hunk": "@@ -10,2 +10,2 @@\n func handle() {\n-\tctx := context.Background()\n+\tctx, cancel := context.WithTimeout(ctx, 5*time.Second)"
      }
    ],
    "reviews": [],
    "issue_comments": [
      {
        "id": 67890,
        "user": {"login": "charlie"},
        "body": "Looks good to me!",
        "created_at": "2024-01-15T11:00:00Z"
      }
    ],
    "files": [
      {"filename": "internal/api/handler.go"}
    ]
  }
}`

func TestExtractLean_basic(t *testing.T) {
	dir := t.TempDir()
	writeCacheFile(t, dir, 42, fixtureCache)

	lean, err := ExtractLean(dir, []int{42})
	if err != nil {
		t.Fatal(err)
	}
	if len(lean) != 1 {
		t.Fatalf("expected 1 lean PR, got %d", len(lean))
	}
	lp := lean[0]
	if lp.PRNumber != 42 {
		t.Errorf("pr_number: want 42, got %d", lp.PRNumber)
	}
	if len(lp.FilesTouched) == 0 {
		t.Error("expected files_touched to be non-empty")
	}
	if lp.FilesTouched[0] != "internal/api/handler.go" {
		t.Errorf("files_touched[0]: want internal/api/handler.go, got %s", lp.FilesTouched[0])
	}
	// Expect 2 comments: one review_comment + one issue_comment.
	if len(lp.Comments) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(lp.Comments))
	}

	rc := lp.Comments[0]
	if rc.Type != "review_comment" {
		t.Errorf("first comment type: want review_comment, got %s", rc.Type)
	}
	if rc.User != "bob" {
		t.Errorf("reviewer: want bob, got %s", rc.User)
	}
	if rc.IsPRAuthor {
		t.Error("bob is not the PR author (alice is)")
	}
	if rc.CodeBefore == "" {
		t.Error("code_before should be populated from diff_hunk")
	}

	ic := lp.Comments[1]
	if ic.Type != "issue_comment" {
		t.Errorf("second comment type: want issue_comment, got %s", ic.Type)
	}
	if ic.CodeBefore != "" {
		t.Error("issue_comment should not have code_before")
	}
}

func TestExtractLean_authorDetection(t *testing.T) {
	// PR author is alice; review by alice should be is_pr_author=true.
	fixture := `{
  "pr_number": 1,
  "raw": {
    "number": 1,
    "user": {"login": "alice"},
    "review_comments": [
      {"id": 1, "user": {"login": "alice"}, "body": "self-note", "path": "foo.go", "diff_hunk": "@@ -1 +1 @@\n-old\n+new"}
    ],
    "reviews": [],
    "issue_comments": [],
    "files": []
  }
}`
	dir := t.TempDir()
	writeCacheFile(t, dir, 1, fixture)
	lean, _ := ExtractLean(dir, []int{1})
	if len(lean) == 0 || len(lean[0].Comments) == 0 {
		t.Fatal("expected at least one comment")
	}
	if !lean[0].Comments[0].IsPRAuthor {
		t.Error("alice commenting on her own PR should have is_pr_author=true")
	}
}

func TestExtractLean_missingFile(t *testing.T) {
	dir := t.TempDir()
	// Request a PR that doesn't exist — should be skipped with a warning.
	lean, err := ExtractLean(dir, []int{999})
	if err != nil {
		t.Fatal("expected no error for missing file, got", err)
	}
	if len(lean) != 0 {
		t.Errorf("expected empty result for missing cache file, got %d entries", len(lean))
	}
}

func TestExtractLean_allFiles(t *testing.T) {
	dir := t.TempDir()
	writeCacheFile(t, dir, 42, fixtureCache)
	// No --prs filter — should pick up all pr-*.json files.
	lean, err := ExtractLean(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(lean) != 1 {
		t.Fatalf("expected 1 lean PR from glob, got %d", len(lean))
	}
}

func TestExtractLean_outputIsValidJSON(t *testing.T) {
	dir := t.TempDir()
	writeCacheFile(t, dir, 42, fixtureCache)
	lean, _ := ExtractLean(dir, []int{42})
	data, err := json.Marshal(lean)
	if err != nil {
		t.Fatal("marshal error:", err)
	}
	var check []json.RawMessage
	if err := json.Unmarshal(data, &check); err != nil {
		t.Fatal("output is not valid JSON array:", err)
	}
}
