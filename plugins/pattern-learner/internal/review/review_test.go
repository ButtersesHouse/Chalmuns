package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixed is a stable capture time so an artifact's fields are comparable
// across runs; ReviewID does not depend on it (it is content-addressed).
var fixed = time.Date(2026, 1, 15, 10, 4, 0, 0, time.UTC)

// capture is the common "normalize this blob" call the tests make.
func capture(t *testing.T, source, format, body string) Artifact {
	t.Helper()
	a, err := Capture(Input{Data: []byte(body), Source: source, Format: format, Now: fixed})
	if err != nil {
		t.Fatalf("Capture(%s/%s): %v", source, format, err)
	}
	return a
}

// --- format detection ---

func TestDetect_byShape(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"findings wrapper", `{"findings":[{"file":"a.go","summary":"s"}],"level":"high"}`, FormatFindings},
		{"findings bare array", `[{"file":"a.go","summary":"s"}]`, FormatFindings},
		{"findings by failure_scenario", `[{"file":"a.go","failure_scenario":"boom"}]`, FormatFindings},
		{"sarif", `{"version":"2.1.0","runs":[{"results":[]}]}`, FormatSARIF},
		{"eslint", `[{"filePath":"/a.js","messages":[]}]`, FormatESLint},
		{"semgrep by check_id", `{"results":[{"check_id":"x","path":"a.py"}]}`, FormatSemgrep},
		{"semgrep by extra", `{"results":[{"extra":{"message":"m"}}]}`, FormatSemgrep},
		{"semgrep empty results", `{"results":[]}`, FormatSemgrep},
		{"markdown", "# Review\n\n## A finding\n\ntext", FormatMarkdown},
		{"invalid JSON is prose", `{ not json at all`, FormatMarkdown},
		{"unknown JSON object is prose", `{"totally":"unrelated"}`, FormatMarkdown},
		{"BOM then JSON", "\xef\xbb\xbf" + `{"findings":[]}`, FormatFindings},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Detect([]byte(tc.body)); got != tc.want {
				t.Errorf("Detect: want %q, got %q", tc.want, got)
			}
		})
	}
}

// A SARIF document also has a "results" key one level down. Detect must not
// read it as semgrep, or every SARIF capture parses to zero findings.
func TestDetect_sarifBeatsSemgrep(t *testing.T) {
	body := `{"runs":[{"results":[{"ruleId":"r","message":{"text":"m"}}]}]}`
	if got := Detect([]byte(body)); got != FormatSARIF {
		t.Errorf("SARIF with nested results detected as %q", got)
	}
}

// --- parsers ---

func TestCapture_findings(t *testing.T) {
	body := `{"findings":[{"file":"internal/api/h.go","line":42,"category":"correctness",
	  "short_summary":"Wrap errors with %w","summary":"This codebase wraps errors with %w.",
	  "failure_scenario":"errors.Is returns false.","verdict":"CONFIRMED"}]}`
	a := capture(t, "code-review", FormatAuto, body)

	if a.Format != FormatFindings {
		t.Fatalf("format: want findings, got %q", a.Format)
	}
	if len(a.Findings) != 1 {
		t.Fatalf("want 1 finding, got %d", len(a.Findings))
	}
	f := a.Findings[0]
	if f.File != "internal/api/h.go" || f.Line != 42 || f.Category != "correctness" {
		t.Errorf("location/category wrong: %+v", f)
	}
	if f.Title != "Wrap errors with %w" || f.Verdict != "CONFIRMED" {
		t.Errorf("title/verdict wrong: %+v", f)
	}
	// summary and failure_scenario are both the reviewer's own words, and a
	// rule may quote either, so both must survive into the body.
	if !strings.Contains(f.Body, "wraps errors with %w") || !strings.Contains(f.Body, "errors.Is returns false") {
		t.Errorf("body must carry summary and failure_scenario; got %q", f.Body)
	}
}

// A finding with no short_summary still needs a handle to be recognised by.
func TestCapture_findingsTitleFallsBackToSummary(t *testing.T) {
	a := capture(t, "code-review", FormatAuto,
		`[{"file":"a.go","summary":"Use the shared logger. It adds request ids."}]`)
	if got := a.Findings[0].Title; got != "Use the shared logger" {
		t.Errorf("title fallback: got %q", got)
	}
}

func TestCapture_sarif(t *testing.T) {
	body := `{"runs":[{"tool":{"driver":{"name":"semgrep","rules":[
	  {"id":"go.no-panic","fullDescription":{"text":"Return errors as values; never panic for an expected case."}}]}},
	  "results":[{"ruleId":"go.no-panic","level":"error","message":{"text":"panic() used"},
	  "locations":[{"physicalLocation":{"artifactLocation":{"uri":"file://internal/api/h.go"},
	  "region":{"startLine":17,"snippet":{"text":"panic(err)"}}}}],
	  "fixes":[{"artifactChanges":[{"replacements":[{"insertedContent":{"text":"return err"}}]}]}]}]}]}`
	a := capture(t, "semgrep", FormatAuto, body)

	f := a.Findings[0]
	if f.Title != "go.no-panic" || f.File != "internal/api/h.go" || f.Line != 17 {
		t.Errorf("sarif finding wrong: %+v", f)
	}
	// The rule description is where the convention is actually stated; a
	// result's terse message alone would rarely support a rule.
	if !strings.Contains(f.Body, "Return errors as values") {
		t.Errorf("rule description must be folded into the body; got %q", f.Body)
	}
	if f.CodeBefore != "panic(err)" || f.CodeAfter != "return err" {
		t.Errorf("snippet/fix wrong: before=%q after=%q", f.CodeBefore, f.CodeAfter)
	}
}

func TestCapture_eslint(t *testing.T) {
	a := capture(t, "eslint", FormatAuto,
		`[{"filePath":"/repo/src/a.js","messages":[{"ruleId":"no-console","severity":2,"message":"Use the shared logger.","line":9}]},
		  {"filePath":"/repo/src/b.js","messages":[]}]`)
	if len(a.Findings) != 1 {
		t.Fatalf("a file with no messages must contribute no findings; got %d", len(a.Findings))
	}
	f := a.Findings[0]
	if f.Title != "no-console" || f.Severity != "error" || f.Line != 9 {
		t.Errorf("eslint finding wrong: %+v", f)
	}
}

func TestCapture_semgrep(t *testing.T) {
	a := capture(t, "semgrep", FormatAuto,
		`{"results":[{"check_id":"py.no-requests","path":"svc/c.py","start":{"line":4},
		  "extra":{"message":"Use the shared http client.","severity":"WARNING",
		  "lines":"import requests","fix":"from svc.http import client"}}]}`)
	f := a.Findings[0]
	if f.Title != "py.no-requests" || f.File != "svc/c.py" || f.Line != 4 {
		t.Errorf("semgrep finding wrong: %+v", f)
	}
	if f.CodeBefore != "import requests" || f.CodeAfter != "from svc.http import client" {
		t.Errorf("lines/fix wrong: %+v", f)
	}
}

func TestCapture_markdownSectionsAndFences(t *testing.T) {
	body := "# Review of the auth refactor\n\n" +
		"## Hash tokens before storage — internal/auth/session.go:88\n\n" +
		"Everywhere else the token is hashed first.\n\n" +
		"Currently:\n\n```go\ndb.Save(session.Token)\n```\n\n" +
		"Prefer:\n\n```go\ndb.Save(hashToken(session.Token))\n```\n"
	a := capture(t, "code-review", FormatAuto, body)

	// The document title introduces deeper headings and states no finding of
	// its own, so it must not become one.
	if len(a.Findings) != 1 {
		t.Fatalf("want 1 finding (title heading excluded), got %d: %+v", len(a.Findings), a.Findings)
	}
	f := a.Findings[0]
	if f.File != "internal/auth/session.go" || f.Line != 88 {
		t.Errorf("file:line not recovered from the heading: %+v", f)
	}
	if f.CodeBefore != "db.Save(session.Token)" {
		t.Errorf(`fence after "Currently:" is the before example; got %q`, f.CodeBefore)
	}
	if f.CodeAfter != "db.Save(hashToken(session.Token))" {
		t.Errorf(`fence after "Prefer:" is the after example; got %q`, f.CodeAfter)
	}
}

// A rule stated entirely in its heading is a real finding; a heading that
// merely introduces deeper ones is not.
func TestCapture_markdownHeadingOnlyFinding(t *testing.T) {
	a := capture(t, "code-review", FormatAuto,
		"# Auth review\n\n## Never panic in request handlers\n\n## Always hash tokens\n\nWe do this everywhere.\n")
	var titles []string
	for _, f := range a.Findings {
		titles = append(titles, f.Title)
	}
	want := []string{"Never panic in request handlers", "Always hash tokens"}
	if strings.Join(titles, "|") != strings.Join(want, "|") {
		t.Errorf("want %v, got %v", want, titles)
	}
}

// An unlabelled fence is left out rather than guessed at: assigning it by
// position would record the offending code as the example to imitate.
func TestCapture_markdownUnlabelledFenceIsNotAnExample(t *testing.T) {
	a := capture(t, "code-review", FormatAuto,
		"## A finding\n\nSome prose with no cue words.\n\n```go\nfoo()\n```\n")
	f := a.Findings[0]
	if f.CodeBefore != "" || f.CodeAfter != "" {
		t.Errorf("unlabelled fence must not be assigned; before=%q after=%q", f.CodeBefore, f.CodeAfter)
	}
}

func TestCapture_markdownNoHeadingsYieldsNoFindings(t *testing.T) {
	a := capture(t, "code-review", FormatAuto, "Just some prose about the code, no headings at all.\n")
	if len(a.Findings) != 0 {
		t.Errorf("want no findings, got %d", len(a.Findings))
	}
	if a.RawText == "" {
		t.Error("the review must still be kept whole for grounding")
	}
}

// A shape that claims to be a known format but is not must fail loudly. A
// silent empty parse reads as "the reviewer said nothing".
func TestCapture_wrongExplicitFormatErrors(t *testing.T) {
	_, err := Capture(Input{Data: []byte("# not json"), Source: "x", Format: FormatSARIF, Now: fixed})
	if err == nil {
		t.Fatal("parsing prose as SARIF should error, not return zero findings")
	}
}

// --- capture invariants ---

func TestCapture_rawTextIsVerbatim(t *testing.T) {
	// Grounding substring-matches against this, so any rewriting here would
	// drop signals that quote the review correctly.
	body := "## A finding\n\n  odd   spacing\tand \"quotes\" kept as-is\n"
	a := capture(t, "code-review", FormatMarkdown, body)
	if a.RawText != body {
		t.Errorf("RawText must be byte-identical:\n got %q\nwant %q", a.RawText, body)
	}
}

func TestCapture_idIsContentAddressed(t *testing.T) {
	body := `{"findings":[{"file":"a.go","summary":"s"}]}`
	first := capture(t, "code-review", FormatAuto, body)
	again := capture(t, "code-review", FormatAuto, body)
	if first.ReviewID != again.ReviewID {
		t.Errorf("same review must yield the same id: %q vs %q", first.ReviewID, again.ReviewID)
	}
	// Two tools agreeing is corroboration and must stay two artifacts.
	other := capture(t, "semgrep", FormatAuto, body)
	if other.ReviewID == first.ReviewID {
		t.Error("different sources must yield different ids")
	}
	different := capture(t, "code-review", FormatAuto, `{"findings":[{"file":"b.go","summary":"s"}]}`)
	if different.ReviewID == first.ReviewID {
		t.Error("different content must yield different ids")
	}
}

func TestCapture_rejectsEmptyAndSourceless(t *testing.T) {
	if _, err := Capture(Input{Data: []byte("   \n"), Source: "x", Now: fixed}); err == nil {
		t.Error("an empty review should be refused")
	}
	if _, err := Capture(Input{Data: []byte("something"), Source: "", Now: fixed}); err == nil {
		t.Error("a sourceless capture should be refused: an artifact is attributed to its tool")
	}
}

func TestCapture_findingsAreIndexed(t *testing.T) {
	a := capture(t, "code-review", FormatAuto,
		`[{"file":"a.go","summary":"one"},{"file":"b.go","summary":"two"}]`)
	for i, f := range a.Findings {
		if f.Index != i {
			t.Errorf("finding %d has index %d", i, f.Index)
		}
	}
}

// --- store ---

func TestWriteArtifact_idempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "review-cache") // must be created for us
	a := capture(t, "code-review", FormatAuto, `{"findings":[{"file":"a.go","summary":"s"}]}`)

	written, err := WriteArtifact(dir, a)
	if err != nil || !written {
		t.Fatalf("first write: written=%v err=%v", written, err)
	}
	written, err = WriteArtifact(dir, a)
	if err != nil {
		t.Fatal(err)
	}
	if written {
		t.Error("re-capturing one review must not look like a second, corroborating review")
	}

	got, err := ReadArtifact(dir, a.ReviewID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ReviewID != a.ReviewID || got.RawText != a.RawText {
		t.Error("round-trip lost content")
	}
}

func TestListArtifacts_orderedAndSkipsJunk(t *testing.T) {
	dir := t.TempDir()
	write := func(id, at string) {
		a := Artifact{ReviewID: id, Source: "code-review", Format: FormatFindings, CapturedAt: at, RawText: "x"}
		data, _ := json.Marshal(a)
		if err := os.WriteFile(filepath.Join(dir, id+".json"), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("rev-ccc", "2026-01-03T00:00:00Z")
	write("rev-aaa", "2026-01-01T00:00:00Z")
	write("rev-bbb", "2026-01-02T00:00:00Z")
	// One unparseable file must not abort the batch, and an unrelated file in
	// the directory must be ignored entirely.
	if err := os.WriteFile(filepath.Join(dir, "rev-bad.json"), []byte("{oops"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := ListArtifacts(dir)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, a := range got {
		ids = append(ids, a.ReviewID)
	}
	if strings.Join(ids, ",") != "rev-aaa,rev-bbb,rev-ccc" {
		t.Errorf("want capture order, got %v", ids)
	}
}

func TestSelect_byWatermarkAndIDs(t *testing.T) {
	all := []Artifact{
		{ReviewID: "rev-a", CapturedAt: "2026-01-01T00:00:00Z"},
		{ReviewID: "rev-b", CapturedAt: "2026-01-02T00:00:00Z"},
		{ReviewID: "rev-c", CapturedAt: "2026-01-03T00:00:00Z"},
	}
	// The watermark is exclusive: a review already mined must not be mined again.
	got := Select(all, nil, "2026-01-02T00:00:00Z")
	if len(got) != 1 || got[0].ReviewID != "rev-c" {
		t.Errorf("watermark select: got %+v", got)
	}
	if n := len(Select(all, nil, "")); n != 3 {
		t.Errorf("empty watermark selects all; got %d", n)
	}
	// An explicit id list overrides the watermark entirely.
	got = Select(all, []string{"rev-a", " rev-c "}, "2026-01-02T00:00:00Z")
	if len(got) != 2 {
		t.Errorf("explicit ids should override the watermark; got %+v", got)
	}
	if Newest(all) != "2026-01-03T00:00:00Z" {
		t.Errorf("Newest: got %q", Newest(all))
	}
}

func TestLean_textOnlyWhenUnstructured(t *testing.T) {
	structured := capture(t, "code-review", FormatAuto, `[{"file":"a.go","summary":"s"}]`)
	prose := capture(t, "code-review", FormatMarkdown, "just prose, no headings\n")

	lean := Lean([]Artifact{structured, prose})
	if lean[0].Text != "" {
		t.Error("a structured review must not also send its raw text — it would double the prompt")
	}
	if len(lean[0].FilesTouched) != 1 || lean[0].FilesTouched[0] != "a.go" {
		t.Errorf("files_touched: %+v", lean[0].FilesTouched)
	}
	if lean[1].Text == "" {
		t.Error("an unstructured review must send its text; there is nothing else to read")
	}
}
