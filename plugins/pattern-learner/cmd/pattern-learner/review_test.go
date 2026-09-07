package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ButtersesHouse/Chalmuns/internal/review"
	"github.com/ButtersesHouse/Chalmuns/internal/state"
)

// captureStdout runs fn with stdout redirected and returns what it printed.
// The subcommands write JSON to stdout, which is the contract SKILL.md reads.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	runErr := fn()
	w.Close()
	os.Stdout = saved

	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	r.Close()
	return sb.String(), runErr
}

// projectFixture makes a project directory with the conventional state and
// review-cache paths, and returns both.
func projectFixture(t *testing.T) (dir, statePath, cacheDir string) {
	t.Helper()
	dir = t.TempDir()
	statePath = filepath.Join(dir, ".claude", "pattern-learner", "state.json")
	cacheDir = filepath.Join(dir, reviewCacheRel)
	return dir, statePath, cacheDir
}

func decodeWatchers(t *testing.T, out string) []review.WatcherStatus {
	t.Helper()
	var got []review.WatcherStatus
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("watch output is not the documented JSON array: %v\n%s", err, out)
	}
	return got
}

// Designating a reviewer must work in a repo that has never run the pipeline —
// that is the first thing a user does.
func TestRunWatch_addOnAFreshRepo(t *testing.T) {
	_, statePath, _ := projectFixture(t)

	out, err := captureStdout(t, func() error {
		return runWatch([]string{"--state", statePath, "--add", "/code-review", "--kind", "skill"})
	})
	if err != nil {
		t.Fatal(err)
	}
	got := decodeWatchers(t, out)
	if len(got) != 1 || got[0].ID != "code-review" || got[0].Kind != "skill" {
		t.Fatalf("watcher not recorded: %+v", got)
	}
	if got[0].Captures != 0 {
		t.Errorf("a new designation has captured nothing yet; got %d", got[0].Captures)
	}

	s, err := state.Read(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Watchers) != 1 {
		t.Errorf("designation not persisted: %+v", s.Watchers)
	}
}

func TestRunWatch_listCountsCapturesFromTheCache(t *testing.T) {
	_, statePath, cacheDir := projectFixture(t)
	if _, err := captureStdout(t, func() error {
		return runWatch([]string{"--state", statePath, "--add", "code-review"})
	}); err != nil {
		t.Fatal(err)
	}
	if err := runCaptureReview([]string{
		"--cache-dir", cacheDir, "--source", "code-review", "--file", writeTemp(t, `[{"file":"a.go","summary":"s"}]`),
	}); err != nil {
		t.Fatal(err)
	}

	out, err := captureStdout(t, func() error {
		return runWatch([]string{"--state", statePath, "--list"})
	})
	if err != nil {
		t.Fatal(err)
	}
	got := decodeWatchers(t, out)
	if len(got) != 1 || got[0].Captures != 1 {
		t.Errorf("list must count the cache live; got %+v", got)
	}
}

func TestRunWatch_removeAndErrors(t *testing.T) {
	_, statePath, _ := projectFixture(t)
	if _, err := captureStdout(t, func() error {
		return runWatch([]string{"--state", statePath, "--add", "semgrep", "--kind", "tool"})
	}); err != nil {
		t.Fatal(err)
	}

	out, err := captureStdout(t, func() error {
		return runWatch([]string{"--state", statePath, "--remove", "semgrep"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeWatchers(t, out); len(got) != 0 {
		t.Errorf("watcher should be gone; got %+v", got)
	}

	if err := runWatch([]string{"--state", statePath, "--remove", "never-designated"}); err == nil {
		t.Error("removing an absent watcher should error rather than report success")
	}
	if err := runWatch([]string{"--state", statePath}); err == nil {
		t.Error("watch with no action should error")
	}
	if err := runWatch([]string{"--state", statePath, "--list", "--add", "x"}); err == nil {
		t.Error("watch takes exactly one action")
	}
	if err := runWatch([]string{"--add", "x"}); err == nil {
		t.Error("--state is required")
	}
}

// writeTemp puts content in a temp file and returns its path.
func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "review.json")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunCaptureReview_writesAndIsIdempotent(t *testing.T) {
	_, _, cacheDir := projectFixture(t)
	file := writeTemp(t, `{"findings":[{"file":"a.go","line":1,"summary":"Use the shared logger everywhere."}]}`)

	out, err := captureStdout(t, func() error {
		return runCaptureReview([]string{"--cache-dir", cacheDir, "--source", "code-review", "--file", file})
	})
	if err != nil {
		t.Fatal(err)
	}
	var first captureResult
	if err := json.Unmarshal([]byte(out), &first); err != nil {
		t.Fatalf("capture output is not the documented JSON object: %v\n%s", err, out)
	}
	if !first.Written || first.Findings != 1 || first.Format != review.FormatFindings {
		t.Fatalf("first capture: %+v", first)
	}
	if _, err := os.Stat(first.Path); err != nil {
		t.Errorf("reported path does not exist: %v", err)
	}

	out, err = captureStdout(t, func() error {
		return runCaptureReview([]string{"--cache-dir", cacheDir, "--source", "code-review", "--file", file})
	})
	if err != nil {
		t.Fatal(err)
	}
	var second captureResult
	if err := json.Unmarshal([]byte(out), &second); err != nil {
		t.Fatal(err)
	}
	if second.ReviewID != first.ReviewID || second.Written {
		t.Errorf("re-capturing one review must be a no-op; got %+v", second)
	}
}

func TestRunCaptureReview_requiresCacheDirSourceAndKnownFormat(t *testing.T) {
	_, _, cacheDir := projectFixture(t)
	file := writeTemp(t, `[{"file":"a.go","summary":"s"}]`)

	if err := runCaptureReview([]string{"--source", "x", "--file", file}); err == nil {
		t.Error("--cache-dir is required")
	}
	if err := runCaptureReview([]string{"--cache-dir", cacheDir, "--file", file}); err == nil {
		t.Error("--source is required: an artifact is attributed to its tool")
	}
	err := runCaptureReview([]string{"--cache-dir", cacheDir, "--source", "x", "--format", "yaml", "--file", file})
	if err == nil || !strings.Contains(err.Error(), "yaml") {
		t.Errorf("an unknown format should be named in the error; got %v", err)
	}
	if err := runCaptureReview([]string{"--cache-dir", cacheDir, "--source", "x", "--skils-dir", "typo"}); err == nil {
		t.Error("an unknown flag must be refused, not silently ignored")
	}
}

// The hook runs inside someone's tool call: it must never fail, and must
// record nothing unless the tool was designated.
func TestRunCaptureReview_hookIsFailOpenAndDesignationGated(t *testing.T) {
	dir, statePath, cacheDir := projectFixture(t)
	t.Setenv("CLAUDE_PROJECT_DIR", dir)

	payload := func(t *testing.T, body string) func() {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		saved := os.Stdin
		os.Stdin = r
		go func() {
			w.WriteString(body)
			w.Close()
		}()
		return func() { os.Stdin = saved }
	}
	countArtifacts := func() int {
		matches, _ := filepath.Glob(filepath.Join(cacheDir, "rev-*.json"))
		return len(matches)
	}
	run := func(t *testing.T, body string) {
		t.Helper()
		restore := payload(t, body)
		defer restore()
		if err := runCaptureReview([]string{"--hook"}); err != nil {
			t.Errorf("the hook must never return an error; got %v", err)
		}
	}

	const findings = `{"findings":[{"file":"a.go","line":1,"summary":"Use the shared logger everywhere."}]}`
	skillCall := `{"tool_name":"Skill","tool_input":{"skill":"code-review"},"tool_response":` + findings + `}`

	// Nothing designated yet: a matching tool call is still not recorded.
	run(t, skillCall)
	if n := countArtifacts(); n != 0 {
		t.Fatalf("with nothing designated the hook must record nothing; got %d artifacts", n)
	}

	if _, err := captureStdout(t, func() error {
		return runWatch([]string{"--state", statePath, "--add", "code-review", "--kind", "skill"})
	}); err != nil {
		t.Fatal(err)
	}

	run(t, skillCall)
	if n := countArtifacts(); n != 1 {
		t.Fatalf("a designated reviewer's output should be captured; got %d artifacts", n)
	}

	// Undesignated tools, and every kind of malformed payload, are no-ops.
	for _, body := range []string{
		`{"tool_name":"Bash","tool_input":{"command":"go test ./..."},"tool_response":{"stdout":"ok"}}`,
		`{"tool_name":"Bash","tool_input":{"command":"grep code-review notes.txt"},"tool_response":{"stdout":"x"}}`,
		"not json at all",
		"",
		"null",
	} {
		run(t, body)
	}
	if n := countArtifacts(); n != 1 {
		t.Errorf("no further artifacts should have been written; got %d", n)
	}
}

func TestRunExtractReview_selectsAndShapesForTheSubagent(t *testing.T) {
	_, _, cacheDir := projectFixture(t)
	for _, body := range []string{
		`{"findings":[{"file":"a.go","line":1,"summary":"Use the shared logger everywhere."}]}`,
		`{"findings":[{"file":"b.go","line":2,"summary":"Wrap errors with %w for callers."}]}`,
	} {
		if err := runCaptureReview([]string{
			"--cache-dir", cacheDir, "--source", "code-review", "--file", writeTemp(t, body),
		}); err != nil {
			t.Fatal(err)
		}
	}

	out, err := captureStdout(t, func() error {
		return runExtractReview([]string{"--cache-dir", cacheDir})
	})
	if err != nil {
		t.Fatal(err)
	}
	var lean []review.LeanReview
	if err := json.Unmarshal([]byte(out), &lean); err != nil {
		t.Fatalf("extract-review output is not the documented JSON array: %v\n%s", err, out)
	}
	if len(lean) != 2 {
		t.Fatalf("want 2 lean reviews, got %d", len(lean))
	}
	for _, lr := range lean {
		if lr.ReviewID == "" || lr.Source != "code-review" || len(lr.Findings) != 1 {
			t.Errorf("lean review incomplete: %+v", lr)
		}
	}

	// An empty cache is an empty array, not null — SKILL.md tells the agent to
	// check for `[]`, and a bare `null` would read as a broken subcommand.
	out, err = captureStdout(t, func() error {
		return runExtractReview([]string{"--cache-dir", filepath.Join(t.TempDir(), "empty")})
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("empty cache should print []; got %q", strings.TrimSpace(out))
	}

	if err := runExtractReview([]string{}); err == nil {
		t.Error("--cache-dir is required")
	}
}

// A watermark run must not re-mine reviews already turned into rules.
func TestRunExtractReview_sinceWatermark(t *testing.T) {
	_, _, cacheDir := projectFixture(t)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, a := range []review.Artifact{
		{ReviewID: "rev-old", Source: "code-review", Format: review.FormatFindings, CapturedAt: "2026-01-01T00:00:00Z", RawText: "x"},
		{ReviewID: "rev-new", Source: "code-review", Format: review.FormatFindings, CapturedAt: "2026-01-05T00:00:00Z", RawText: "y"},
	} {
		if _, err := review.WriteArtifact(cacheDir, a); err != nil {
			t.Fatal(err)
		}
	}

	out, err := captureStdout(t, func() error {
		return runExtractReview([]string{"--cache-dir", cacheDir, "--since", "2026-01-01T00:00:00Z"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var lean []review.LeanReview
	if err := json.Unmarshal([]byte(out), &lean); err != nil {
		t.Fatal(err)
	}
	if len(lean) != 1 || lean[0].ReviewID != "rev-new" {
		t.Errorf("watermark should exclude the already-mined review; got %+v", lean)
	}

	// An explicit id list overrides the watermark.
	out, err = captureStdout(t, func() error {
		return runExtractReview([]string{"--cache-dir", cacheDir, "--reviews", "rev-old"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &lean); err != nil {
		t.Fatal(err)
	}
	if len(lean) != 1 || lean[0].ReviewID != "rev-old" {
		t.Errorf("--reviews should select exactly what it names; got %+v", lean)
	}
}
