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
	// /code-review reports through ReportFindings, so that call — not its own
	// Skill call — is the one carrying the review.
	skillCall := `{"tool_name":"ReportFindings","tool_input":` + findings + `}`

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
	var res extractReviewResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("extract-review output is not the documented JSON object: %v\n%s", err, out)
	}
	lean := res.Reviews
	if res.NextWatermark == "" {
		t.Error("a watermark run should hand back the watermark to store")
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
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Reviews) != 0 {
		t.Errorf("empty cache should yield no reviews; got %d", len(res.Reviews))
	}
	// Asserted on the printed JSON, not the decoded value: json.Unmarshal maps
	// both `[]` and `null` to a nil slice, so a decoded check cannot tell them
	// apart and the contract SKILL.md relies on would go unpinned.
	if !strings.Contains(out, `"reviews": []`) {
		t.Errorf("empty cache must print `\"reviews\": []`, never null:\n%s", out)
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
		{ReviewID: "rev-000000000001", Source: "code-review", Format: review.FormatFindings, CapturedAt: "2026-01-01T00:00:00Z", RawText: "x"},
		{ReviewID: "rev-000000000005", Source: "code-review", Format: review.FormatFindings, CapturedAt: "2026-01-05T00:00:00Z", RawText: "y"},
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
	var res extractReviewResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Reviews) != 1 || res.Reviews[0].ReviewID != "rev-000000000005" {
		t.Errorf("watermark should exclude the already-mined review; got %+v", res.Reviews)
	}
	if res.NextWatermark != "2026-01-05T00:00:00Z" {
		t.Errorf("watermark should advance to the newest mined review; got %q", res.NextWatermark)
	}

	// An explicit id list overrides the watermark.
	res = extractReviewResult{}
	out, err = captureStdout(t, func() error {
		return runExtractReview([]string{"--cache-dir", cacheDir, "--reviews", "rev-000000000001"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Reviews) != 1 || res.Reviews[0].ReviewID != "rev-000000000001" {
		t.Errorf("--reviews should select exactly what it names; got %+v", res.Reviews)
	}
	// An explicit id run mines out of order, so it hands back no watermark:
	// advancing past the reviews it skipped would strand them for good.
	if res.NextWatermark != "" {
		t.Errorf("an explicit --reviews run must not advance the watermark; got %q", res.NextWatermark)
	}

	// A typo selects nothing, which is the same answer as "already mined" —
	// so it is named rather than reported as consumed.
	if err := runExtractReview([]string{"--cache-dir", cacheDir, "--reviews", "rev-typo123456"}); err == nil {
		t.Error("an unknown review id should be refused, not silently empty")
	}
}

// state-write replaces the file wholesale from a payload the model builds by
// hand. Every other invariant it protects is enforced here rather than asked
// for in prose, and the designations belong in that set: dropping one silently
// stops all future capture from that reviewer, with no error and nothing in
// the cache to explain why.
func TestRunStateWrite_carriesWatchersForward(t *testing.T) {
	_, statePath, _ := projectFixture(t)
	if _, err := captureStdout(t, func() error {
		return runWatch([]string{"--state", statePath, "--add", "code-review", "--kind", "skill"})
	}); err != nil {
		t.Fatal(err)
	}
	s, err := state.Read(statePath)
	if err != nil {
		t.Fatal(err)
	}
	s.LastIngestedReviewAt = "2026-01-15T10:04:00Z"
	if err := state.Write(statePath, s); err != nil {
		t.Fatal(err)
	}

	// A Step 11 payload that forgets both fields — the common case, since the
	// model retypes the whole document.
	payload := `{"schema_version":"1","repo":{"owner":"o","repo":"r"},"last_extracted_pr_number":42,
	  "rules":[{"title":"t","rule":"r","status":"approved","confidence":"stated","sources":[]}]}`
	withStdin(t, payload, func() {
		if err := runStateWrite([]string{"--state", statePath}); err != nil {
			t.Fatal(err)
		}
	})

	got, err := state.Read(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Watchers) != 1 || got.Watchers[0].ID != "code-review" {
		t.Errorf("designations must survive a state-write that omits them; got %+v", got.Watchers)
	}
	if got.LastIngestedReviewAt != "2026-01-15T10:04:00Z" {
		t.Errorf("review watermark must survive; got %q", got.LastIngestedReviewAt)
	}
	if got.LastExtractedPRNumber != 42 || len(got.Rules) != 1 {
		t.Errorf("the payload's own fields must still be written: %+v", got)
	}
}

// withStdin runs fn with stdin fed from body.
func withStdin(t *testing.T, body string, fn func()) {
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
	defer func() { os.Stdin = saved; r.Close() }()
	fn()
}

// The watermark is written by hand into state, and one that does not parse
// would compare as the zero time and select every artifact — silently
// re-mining the whole cache and re-presenting settled conventions on every run.
func TestRunExtractReview_rejectsAMalformedWatermark(t *testing.T) {
	_, _, cacheDir := projectFixture(t)
	err := runExtractReview([]string{"--cache-dir", cacheDir, "--since", "2026-01-15 10:04"})
	if err == nil || !strings.Contains(err.Error(), "RFC3339") {
		t.Errorf("want an error naming the expected format; got %v", err)
	}
	if err := runExtractReview([]string{"--cache-dir", cacheDir, "--since", "2026-01-15T10:04:00Z"}); err != nil {
		t.Errorf("a well-formed watermark should be accepted: %v", err)
	}
}

// A typo in --reviews used to select nothing, which is the same answer the
// watermark path gives for "already mined" — so the user was told a review had
// been consumed when none was looked at. Naming the id is the whole point;
// selecting the real ones alongside it must still work.
func TestRunExtractReview_namesAnUnknownReviewID(t *testing.T) {
	_, _, cacheDir := projectFixture(t)
	if err := runCaptureReview([]string{
		"--cache-dir", cacheDir, "--source", "code-review",
		"--file", writeTemp(t, `{"findings":[{"file":"a.go","summary":"Use the shared logger."}]}`),
	}); err != nil {
		t.Fatal(err)
	}
	metas, err := review.ListArtifactMeta(cacheDir)
	if err != nil || len(metas) != 1 {
		t.Fatalf("fixture: %v (%d artifacts)", err, len(metas))
	}
	real := metas[0].ReviewID

	err = runExtractReview([]string{"--cache-dir", cacheDir, "--reviews", real + ",rev-0000deadbeef"})
	if err == nil {
		t.Fatal("an unknown review id should be an error, not an empty selection")
	}
	if !strings.Contains(err.Error(), "rev-0000deadbeef") {
		t.Errorf("the error should name the id that is missing; got %v", err)
	}
	if strings.Contains(err.Error(), real) {
		t.Errorf("the error should not name the id that exists; got %v", err)
	}

	// An explicit selection hands back no watermark: advancing past reviews
	// this run skipped on purpose would make them unmineable for good.
	out, err := captureStdout(t, func() error {
		return runExtractReview([]string{"--cache-dir", cacheDir, "--reviews", real})
	})
	if err != nil {
		t.Fatal(err)
	}
	var res extractReviewResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Reviews) != 1 || res.NextWatermark != "" {
		t.Errorf("explicit selection: want 1 review and no watermark; got %d / %q", len(res.Reviews), res.NextWatermark)
	}
}

// seedManualRule writes a state holding one rule the developer added by hand,
// the shape --add produces, and returns its assigned ID.
func seedManualRule(t *testing.T, statePath string) string {
	t.Helper()
	s := state.Empty()
	s.Rules = []state.Rule{{
		Title:      "Wrap errors with %w",
		Rule:       "Wrap returned errors with fmt.Errorf and %w when propagating.",
		Target:     state.Target{Location: "api", FileGlob: []string{"internal/api/**/*.go"}},
		Confidence: "stated",
		Status:     "approved",
		Origin:     "manual",
		Sources: []state.Signal{{
			Reviewer: "mryave", Date: "2026-09-01",
			Snippet: "wrap returned errors with %w", Strength: "explicit",
		}},
		SignalCount: 1,
	}}
	if err := os.MkdirAll(filepath.Dir(statePath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := state.Write(statePath, s); err != nil {
		t.Fatal(err)
	}
	got, err := state.Read(statePath)
	if err != nil {
		t.Fatal(err)
	}
	return got.Rules[0].ID
}

// The failure the protected-rule check exists for: Step 11 hands state-write a
// document the model retyped from scratch, and a manual rule missing from it
// is erased — after which write-outputs prunes the skill it lived in, with
// nothing in the summary to say it ever existed.
func TestRunStateWrite_refusesToDropAManualRule(t *testing.T) {
	_, statePath, _ := projectFixture(t)
	seedManualRule(t, statePath)
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}

	payload := `{"schema_version":"1","repo":{"owner":"o","repo":"r"},"last_extracted_pr_number":42,
	  "rules":[{"title":"mined","rule":"something else","status":"approved","confidence":"emerging","sources":[]}]}`
	var writeErr error
	withStdin(t, payload, func() {
		writeErr = runStateWrite([]string{"--state", statePath})
	})
	if writeErr == nil {
		t.Fatal("dropping a rule the developer added by hand must be refused")
	}
	for _, want := range []string{"Wrap errors with %w", "--allow-protected", "nothing was written"} {
		if !strings.Contains(writeErr.Error(), want) {
			t.Errorf("the refusal must mention %q so the developer can decide; got:\n%s", want, writeErr)
		}
	}

	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Error("a refused write must leave the state file untouched, not partly applied")
	}
}

// The permission the refusal asks for: the developer said yes, and the run
// names the rule they approved.
func TestRunStateWrite_allowProtectedLetsTheChangeThrough(t *testing.T) {
	_, statePath, _ := projectFixture(t)
	id := seedManualRule(t, statePath)

	payload := `{"schema_version":"1","repo":{"owner":"o","repo":"r"},
	  "rules":[{"title":"mined","rule":"something else","status":"approved","confidence":"emerging","sources":[]}]}`
	withStdin(t, payload, func() {
		if err := runStateWrite([]string{"--state", statePath, "--allow-protected", id}); err != nil {
			t.Fatalf("an approved override must be honoured: %v", err)
		}
	})

	got, err := state.Read(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rules) != 1 || got.Rules[0].Title != "mined" {
		t.Errorf("the approved payload must be written as given; got %+v", got.Rules)
	}
}

// Mining a manual rule is the pipeline doing its job. Only overriding is
// refused, so a payload that appends corroboration needs no flag.
func TestRunStateWrite_manualRuleMayBeStrengthenedWithoutPermission(t *testing.T) {
	_, statePath, _ := projectFixture(t)
	id := seedManualRule(t, statePath)

	payload := `{"schema_version":"1","repo":{"owner":"o","repo":"r"},"rules":[{
	  "id":"` + id + `","title":"Wrap errors with %w",
	  "rule":"Wrap returned errors with fmt.Errorf and %w when propagating.",
	  "target":{"location":"api","file_glob":["internal/api/**/*.go"]},
	  "confidence":"established","status":"approved","origin":"manual","signal_count":2,"last_seen_pr":118,
	  "sources":[
	    {"reviewer":"mryave","date":"2026-09-01","snippet":"wrap returned errors with %w","strength":"explicit"},
	    {"pr_number":118,"reviewer":"someone-else","date":"2026-09-05","snippet":"please wrap this","strength":"implicit"}
	  ]}]}`
	withStdin(t, payload, func() {
		if err := runStateWrite([]string{"--state", statePath}); err != nil {
			t.Fatalf("appending a mined source to a manual rule must not need permission: %v", err)
		}
	})

	got, err := state.Read(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rules) != 1 || len(got.Rules[0].Sources) != 2 || got.Rules[0].Origin != "manual" {
		t.Errorf("the corroborated rule must be written and stay the developer's; got %+v", got.Rules)
	}
}

// An --allow-protected that matches nothing is a typo, and a typo that reads
// as permission is worse than no gate at all.
func TestRunStateWrite_allowProtectedUnknownIDIsRefused(t *testing.T) {
	_, statePath, _ := projectFixture(t)
	seedManualRule(t, statePath)

	payload := `{"schema_version":"1","rules":[]}`
	var writeErr error
	withStdin(t, payload, func() {
		writeErr = runStateWrite([]string{"--state", statePath, "--allow-protected", "rule_typo"})
	})
	if writeErr == nil || !strings.Contains(writeErr.Error(), "rule_typo") {
		t.Fatalf("an override naming no protected rule must be refused by name; got %v", writeErr)
	}
}

// A state file that will not parse cannot be checked, and writing over it
// discards exactly the rules the check protects.
func TestRunStateWrite_refusesAnUnparseablePriorState(t *testing.T) {
	_, statePath, _ := projectFixture(t)
	if err := os.MkdirAll(filepath.Dir(statePath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("{ this is not json"), 0644); err != nil {
		t.Fatal(err)
	}

	var writeErr error
	withStdin(t, `{"schema_version":"1","rules":[]}`, func() {
		writeErr = runStateWrite([]string{"--state", statePath})
	})
	if writeErr == nil {
		t.Fatal("a corrupt state file must not become the way past the protected-rule check")
	}
}

// The flag belongs to state-write alone; cliflags refuses it elsewhere rather
// than ignoring it, which is how --skils-dir once pruned the wrong directory.
func TestAllowProtectedIsStateWriteOnly(t *testing.T) {
	_, statePath, _ := projectFixture(t)
	err := runStateRead([]string{"--state", statePath, "--allow-protected", "rule_x"})
	if err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("state-read must reject --allow-protected; got %v", err)
	}
}
