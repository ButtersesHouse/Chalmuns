package review

import (
	"encoding/json"
	"fmt"
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
	// summary and failure_scenario are both the reviewer's own words and a
	// rule may quote either, so both survive — in separate fields. Joining
	// them would create a sentence spanning the seam that no one wrote, and
	// grounding would accept a quote of it as verbatim.
	if f.Body != "This codebase wraps errors with %w." {
		t.Errorf("body should be the summary alone; got %q", f.Body)
	}
	if f.Evidence != "errors.Is returns false." {
		t.Errorf("failure_scenario belongs in evidence; got %q", f.Evidence)
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
	// result's terse message alone would rarely support a rule. It is carried
	// in its own field for the same reason failure_scenario is.
	if f.Body != "panic() used" {
		t.Errorf("body should be the result message alone; got %q", f.Body)
	}
	if !strings.Contains(f.Evidence, "Return errors as values") {
		t.Errorf("rule description belongs in evidence; got %q", f.Evidence)
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

	// ReadArtifact is exported and joins its argument into a path, so it
	// confines the id itself rather than trusting every future caller to.
	for _, bad := range []string{"../../etc/passwd", "rev-../x", "", "not-an-id"} {
		if _, err := ReadArtifact(dir, bad); err == nil {
			t.Errorf("ReadArtifact(%q) should refuse a non-id", bad)
		}
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

// The single worst outcome this package can produce is recording the flagged
// code as the example to imitate: the generated skill then instructs the agent
// to write exactly what review rejected. Each case below did that.
func TestCapture_markdownFenceLabellingIsNotInverted(t *testing.T) {
	cases := []struct {
		name, body, before, after string
	}{
		{
			// A cue word anywhere in the paragraph used to outrank the label
			// immediately above the fence, because after-cues were tested first.
			name: "a stray cue in the prose does not outrank the label at the fence",
			body: "## Wrap errors with %w\n\nAvoid returning bare errors; the fix is to wrap them.\n\n" +
				"Before:\n\n```go\nreturn err\n```\n\nAfter:\n\n```go\nreturn fmt.Errorf(\"ctx: %w\", err)\n```\n",
			before: "return err",
			after:  `return fmt.Errorf("ctx: %w", err)`,
		},
		{
			// "instead" belonged to the after-cues, so "Instead of:" — the most
			// canonical before-label there is — read as an after-label.
			name: "Instead of / Do this",
			body: "## Use the shared logger\n\nInstead of:\n\n```go\nfmt.Println(msg)\n```\n\n" +
				"Do this:\n\n```go\nlog.Info(msg)\n```\n",
			before: "fmt.Println(msg)",
			after:  "log.Info(msg)",
		},
		{
			// Each sub-heading becomes its own finding, so a section's only
			// label is its own title.
			name:   "sub-headings label their own fences",
			body:   "## Before\n\n```go\nreturn err\n```\n",
			before: "return err",
			after:  "",
		},
		{
			name:   "Bad / Good",
			body:   "## A rule\n\nBad:\n\n```go\nx()\n```\n\nGood:\n\n```go\ny()\n```\n",
			before: "x()",
			after:  "y()",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := capture(t, "code-review", FormatMarkdown, tc.body)
			var before, after string
			for _, f := range a.Findings {
				if f.CodeBefore != "" {
					before = f.CodeBefore
				}
				if f.CodeAfter != "" {
					after = f.CodeAfter
				}
			}
			if before != tc.before || after != tc.after {
				t.Errorf("before=%q after=%q; want before=%q after=%q", before, after, tc.before, tc.after)
			}
		})
	}
}

// An unclosed fence used to pair its own opener with the next fence's opener,
// storing the reviewer's prose as code.
func TestCapture_markdownUnclosedFenceStoresNoCode(t *testing.T) {
	a := capture(t, "code-review", FormatMarkdown,
		"## A finding\n\nCurrently:\n\n```go\nfoo()\n\nPrefer:\n\n```go\nbar()\n```\n")
	for _, f := range a.Findings {
		if strings.Contains(f.CodeBefore, "Prefer:") || strings.Contains(f.CodeAfter, "Prefer:") {
			t.Errorf("prose stored as code: before=%q after=%q", f.CodeBefore, f.CodeAfter)
		}
	}
}

// A non-ASCII byte in a path truncated the match, attributing the finding to a
// file that does not exist.
func TestCapture_markdownFileRefWithNonASCII(t *testing.T) {
	a := capture(t, "code-review", FormatMarkdown,
		"## Ne paniquez pas — café/naïve.go:12\n\nUse errors, not panics.\n")
	if got := a.Findings[0].File; got != "café/naïve.go" {
		t.Errorf("file ref: want %q, got %q", "café/naïve.go", got)
	}
	if got := a.Findings[0].Line; got != 12 {
		t.Errorf("line: want 12, got %d", got)
	}
}

// Title is what the recurrence check recognises a repeated finding by, so
// cutting it at a qualified identifier's dot loses that identity.
func TestCapture_titleKeepsQualifiedIdentifiers(t *testing.T) {
	cases := map[string]string{
		`Don't call os.Exit in library code; return an error instead.`: "Don't call os.Exit in library code; return an error instead",
		`Use v2.Client here, not the v1 shim.`:                         "Use v2.Client here, not the v1 shim",
		`Prefer errors.Is over == for sentinel errors. It is safer.`:   "Prefer errors.Is over == for sentinel errors",
	}
	for summary, want := range cases {
		a := capture(t, "code-review", FormatAuto,
			`[{"file":"a.go","summary":`+jsonQuote(summary)+`}]`)
		if got := a.Findings[0].Title; got != want {
			t.Errorf("summary %q\n  got  %q\n  want %q", summary, got, want)
		}
	}
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// A foreign report that merely uses a "findings" key must not parse to
// content-free findings: capture would report success while the reviewer's
// words reached nobody, because the lean view sends raw text only when there
// are no findings at all.
func TestCapture_foreignFindingsVocabularyFallsBackToProse(t *testing.T) {
	body := `{"findings":[{"id":"SNYK-JS-1","title":"Avoid eval()","description":"eval is unsafe here",
	  "location":{"path":"src/a.js","line":9}}]}`
	a := capture(t, "scanner", FormatAuto, body)

	if a.Format == FormatFindings {
		t.Error("a foreign vocabulary must not be claimed as the ReportFindings shape")
	}
	lean := Lean([]Artifact{a})
	if !strings.Contains(lean[0].Text, "eval is unsafe here") {
		t.Errorf("the reviewer's words must still reach the extraction step; text=%q", lean[0].Text)
	}
}

// Asking for a shape the data does not have is an error, not a silent
// zero-finding capture.
func TestCapture_explicitFormatWithNoContentErrors(t *testing.T) {
	body := `{"findings":[{"id":"X","description":"d"}]}`
	if _, err := Capture(Input{Data: []byte(body), Source: "x", Format: FormatFindings, Now: fixed}); err == nil {
		t.Fatal("an explicit format whose data carries no text should error")
	}
}

// The confidence model counts artifacts as independent reviews, so re-running
// a tool over a finding nobody has fixed must not manufacture corroboration.
func TestCapture_reRunOverAnUnfixedFindingIsTheSameReview(t *testing.T) {
	first := capture(t, "eslint", FormatAuto,
		`[{"filePath":"/repo/a.js","messages":[{"ruleId":"no-console","severity":2,"message":"Use the shared logger.","line":3}]}]`)
	// Same unfixed finding after an unrelated edit shifted its line.
	shifted := capture(t, "eslint", FormatAuto,
		`[{"filePath":"/repo/a.js","messages":[{"ruleId":"no-console","severity":2,"message":"Use the shared logger.","line":4}]}]`)
	if first.ReviewID != shifted.ReviewID {
		t.Errorf("a line shift must not make a new review: %q vs %q", first.ReviewID, shifted.ReviewID)
	}

	// A genuinely different finding is still a different review.
	other := capture(t, "eslint", FormatAuto,
		`[{"filePath":"/repo/a.js","messages":[{"ruleId":"no-var","severity":2,"message":"Use let or const.","line":3}]}]`)
	if other.ReviewID == first.ReviewID {
		t.Error("a different finding must be a different review")
	}
}

// The watermark is compared as a time, so sub-second stamps and the
// second-resolution stamps older builds wrote both order correctly.
func TestSelect_subSecondWatermark(t *testing.T) {
	all := []Artifact{
		{ReviewID: "rev-a", CapturedAt: "2026-01-01T10:00:05Z"},
		{ReviewID: "rev-b", CapturedAt: "2026-01-01T10:00:05.4Z"},
		{ReviewID: "rev-c", CapturedAt: "2026-01-01T10:00:05.9Z"},
	}
	sortArtifacts(all)
	if all[0].ReviewID != "rev-a" || all[2].ReviewID != "rev-c" {
		t.Fatalf("sub-second stamps sort after the whole second: %+v", all)
	}
	// A watermark written by an older build still selects the newer captures
	// from the same second, which a string compare would have skipped.
	got := Select(all, nil, "2026-01-01T10:00:05Z")
	if len(got) != 2 {
		t.Errorf("want the two sub-second captures, got %+v", got)
	}
}

// A CRLF document would otherwise carry a stray carriage return into every
// extracted example, and from there into the generated skill's code block.
func TestCapture_markdownCRLF(t *testing.T) {
	a := capture(t, "code-review", FormatMarkdown,
		"## R\r\n\r\nBefore:\r\n\r\n```go\r\nx()\r\n```\r\n\r\nAfter:\r\n\r\n```go\r\ny()\r\n```\r\n")
	f := a.Findings[0]
	if f.CodeBefore != "x()" || f.CodeAfter != "y()" {
		t.Errorf("carriage returns leaked into the code: before=%q after=%q", f.CodeBefore, f.CodeAfter)
	}
}

// Both fence syntaxes are legal markdown and reviewers use both.
func TestCapture_markdownTildeFences(t *testing.T) {
	a := capture(t, "code-review", FormatMarkdown,
		"## R\n\nBefore:\n\n~~~go\nx()\n~~~\n\nAfter:\n\n~~~go\ny()\n~~~\n")
	f := a.Findings[0]
	if f.CodeBefore != "x()" || f.CodeAfter != "y()" {
		t.Errorf("tilde fences not extracted: before=%q after=%q", f.CodeBefore, f.CodeAfter)
	}
	// A fence must be closed by its own character: a ``` inside a ~~~ block is
	// content, not a terminator.
	b := capture(t, "code-review", FormatMarkdown, "## R\n\nBefore:\n\n~~~\na ``` b\nx()\n~~~\n")
	if !strings.Contains(b.Findings[0].CodeBefore, "a ``` b") {
		t.Errorf("a foreign fence marker should be content; got %q", b.Findings[0].CodeBefore)
	}
}

// The confidence model counts artifacts as independent reviews, so a tool that
// reports the same findings in a different order between runs must still be
// recognised as the same review rather than a second one agreeing.
func TestCapture_findingOrderDoesNotChangeTheReviewID(t *testing.T) {
	one := capture(t, "eslint", FormatAuto,
		`[{"filePath":"/a.js","messages":[{"ruleId":"no-console","message":"m1","line":1},{"ruleId":"no-var","message":"m2","line":2}]}]`)
	swapped := capture(t, "eslint", FormatAuto,
		`[{"filePath":"/a.js","messages":[{"ruleId":"no-var","message":"m2","line":2},{"ruleId":"no-console","message":"m1","line":1}]}]`)
	if one.ReviewID != swapped.ReviewID {
		t.Errorf("reordered findings must be the same review: %q vs %q", one.ReviewID, swapped.ReviewID)
	}
	// Genuinely different findings are still a different review.
	other := capture(t, "eslint", FormatAuto,
		`[{"filePath":"/a.js","messages":[{"ruleId":"no-eval","message":"m3","line":1}]}]`)
	if other.ReviewID == one.ReviewID {
		t.Error("different findings must be a different review")
	}
}

// A sniffed format is a guess, so a parse failure means the guess was wrong,
// not that the review is unusable. A BOM or one mistyped field would otherwise
// lose the whole review — silently under the hook.
func TestCapture_sniffedParseFailureFallsBackToProse(t *testing.T) {
	body := `{"findings":[{"file":"a.go","line":"42","summary":"Use the shared logger."}]}`
	a, err := Capture(Input{Data: []byte(body), Source: "code-review", Format: FormatAuto, Now: fixed})
	if err != nil {
		t.Fatalf("a sniffed guess that fails to parse should degrade, not error: %v", err)
	}
	if !strings.Contains(Lean([]Artifact{a})[0].Text, "shared logger") {
		t.Error("the reviewer's words must still reach the extraction step")
	}
	// An explicit format is the caller asserting a shape, so it still errors.
	bad := `{"findings":[{"file":"a.go","line":"42","summary":"x"}]}`
	if _, err := Capture(Input{Data: []byte(bad), Source: "x", Format: FormatFindings, Now: fixed}); err == nil {
		t.Error("an explicitly-requested format should report the parse failure")
	}
}

// A report written on Windows carries a byte-order mark. Detect strips it
// before sniffing, so the parsers must see the same bytes — otherwise the
// shape is recognised and then fails to parse, and every finding it held
// (file, line, the before/after code) is lost to the prose fallback.
func TestCapture_byteOrderMarkIsStrippedBeforeParsing(t *testing.T) {
	const bom = "\xef\xbb\xbf"
	cases := map[string]string{
		"findings array":   bom + `[{"file":"a.go","line":9,"summary":"Use the shared logger everywhere."}]`,
		"findings wrapper": bom + `{"findings":[{"file":"a.go","line":9,"summary":"Use the shared logger everywhere."}]}`,
		"semgrep":          bom + `{"results":[{"check_id":"c","path":"a.py","start":{"line":4},"extra":{"message":"Use the shared client."}}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			a := capture(t, "code-review", FormatAuto, body)
			if len(a.Findings) != 1 {
				t.Fatalf("want the findings parsed, got %d (format %q)", len(a.Findings), a.Format)
			}
			if a.Findings[0].File == "" || a.Findings[0].Line == 0 {
				t.Errorf("location lost: %+v", a.Findings[0])
			}
		})
	}
}

// A null or absent findings list is a clean review, not a malformed one.
func TestCapture_nullFindingsIsACleanReview(t *testing.T) {
	a, err := Capture(Input{Data: []byte(`{"findings":null}`), Source: "code-review", Format: FormatFindings, Now: fixed})
	if err != nil {
		t.Fatalf("a null findings list should parse as empty: %v", err)
	}
	if len(a.Findings) != 0 {
		t.Errorf("want no findings, got %d", len(a.Findings))
	}
}

// Reviewers normally write a path in a code span; capturing the delimiter into
// the path names a file that does not exist.
func TestCapture_markdownFileRefInCodeSpan(t *testing.T) {
	a := capture(t, "code-review", FormatMarkdown,
		"## A finding\n\nFlagged at `internal/api/handler.go:42` in the handler chain.\n")
	f := a.Findings[0]
	if f.File != "internal/api/handler.go" || f.Line != 42 {
		t.Errorf("file ref: got %q:%d", f.File, f.Line)
	}
}

// A block dropped as nested still has to advance the cursor: its code would
// otherwise sit in the next fence's lead, and a cue word inside it would label
// that fence — teaching the reverse of the convention.
func TestCapture_droppedFenceDoesNotLabelTheNextOne(t *testing.T) {
	body := "## A finding\n\nCurrently:\n\n```go\nfoo()\n\n```go\nbar()\n```\n\n```go\nbaz()\n```\n"
	a := capture(t, "code-review", FormatMarkdown, body)
	for _, f := range a.Findings {
		if strings.Contains(f.CodeBefore, "Currently") || strings.Contains(f.CodeAfter, "Currently") {
			t.Errorf("prose stored as code: before=%q after=%q", f.CodeBefore, f.CodeAfter)
		}
	}
}

// A fence must be closed by its own character, so a line of the *other* fence
// character inside a block is content — not a sign the writer forgot a closer.
// Treating it as nested dropped the block and lost the reviewer's example.
func TestCapture_foreignFenceCharacterInsideABlockIsContent(t *testing.T) {
	a := capture(t, "code-review", FormatMarkdown,
		"## A finding\n\nPrefer:\n\n```go\nfmt.Println(\"~~~\")\n~~~\nx()\n```\n")
	f := a.Findings[0]
	if !strings.Contains(f.CodeAfter, "x()") {
		t.Errorf("the block should survive a foreign fence marker; after=%q", f.CodeAfter)
	}
}

// A closing fence with trailing whitespace is still a closing fence. Treating
// it as content left every following heading trapped inside the block, so a
// review whose first fence happened to end in a space produced one finding
// instead of ten.
func TestCapture_closingFenceWithTrailingSpace(t *testing.T) {
	// Three fences: if the trailing-whitespace closer is not recognised, the
	// *next* block's opener is paired as it instead, and everything between —
	// the second heading — is swallowed as code.
	a := capture(t, "code-review", FormatMarkdown,
		"## First\n\nUse the helper.\n\n```go\nx := 1\n```   \n\n"+
			"## Second\n\nAnd wrap errors.\n\n```go\ny := 2\n```\n\n"+
			"## Third\n\nAnd name the receiver.\n")
	if len(a.Findings) != 3 {
		t.Fatalf("want 3 findings, got %d: %+v", len(a.Findings), a.Findings)
	}
	if a.Findings[1].Title != "Second" || a.Findings[2].Title != "Third" {
		t.Errorf("headings after the fence were swallowed: %q, %q", a.Findings[1].Title, a.Findings[2].Title)
	}
	if !strings.Contains(a.Findings[0].Body, "Use the helper.") || strings.Contains(a.Findings[0].Body, "wrap errors") {
		t.Errorf("the first finding absorbed the sections after it: %q", a.Findings[0].Body)
	}
}

// A '#' at the start of a line inside a code fence is a comment, a shell
// prompt or a Markdown example — not a section of the review. Splitting on it
// invented findings whose bodies were someone else's source code.
func TestCapture_hashInsideAFenceIsNotAHeading(t *testing.T) {
	a := capture(t, "code-review", FormatMarkdown,
		"## Only finding\n\nRun it like this:\n\n```sh\n# not a heading\nmake test\n```\n")
	if len(a.Findings) != 1 {
		t.Fatalf("want 1 finding, got %d: %+v", len(a.Findings), a.Findings)
	}
	if a.Findings[0].Title != "Only finding" {
		t.Errorf("title: %q", a.Findings[0].Title)
	}
}

// A review that reports no findings still says something, and its RawText is
// the grounding corpus. Keying a clean capture on the format alone collapsed
// two unrelated clean reviews onto one id, so the second was dropped with
// written:false — indistinguishable from an idempotent re-capture — and any
// rule quoting its prose later failed grounding with nothing to explain why.
func TestCapture_cleanReviewsAreDistinguishedByWhatTheySay(t *testing.T) {
	auth := capture(t, "code-review", FormatAuto,
		`{"findings":[],"note":"reviewed the auth package, nothing to flag"}`)
	billing := capture(t, "code-review", FormatAuto,
		`{"findings":[],"note":"reviewed the billing package, nothing to flag"}`)
	if len(auth.Findings) != 0 || len(billing.Findings) != 0 {
		t.Fatalf("both should be clean: %d / %d", len(auth.Findings), len(billing.Findings))
	}
	if auth.ReviewID == billing.ReviewID {
		t.Errorf("two different clean reviews share id %s", auth.ReviewID)
	}
	// Re-capturing one of them is still the no-op it claims to be.
	if again := capture(t, "code-review", FormatAuto,
		`{"findings":[],"note":"reviewed the auth package, nothing to flag"}`); again.ReviewID != auth.ReviewID {
		t.Errorf("re-capture should be the same review: %s vs %s", again.ReviewID, auth.ReviewID)
	}
}

// The resolved format says how the capture was requested, not what the
// reviewer said. Putting it in the digest split one clean run into two
// artifacts the moment a watcher's --format was named explicitly instead of
// sniffed, and the confidence model reads two artifacts as two reviews.
func TestCapture_theSameBytesAreOneReviewUnderAutoOrAnExplicitFormat(t *testing.T) {
	sniffed := capture(t, "eslint", FormatAuto, `[]`)
	explicit := capture(t, "eslint", FormatESLint, `[]`)
	if sniffed.Format == explicit.Format {
		t.Fatalf("fixture no longer exercises two resolved formats (both %q)", sniffed.Format)
	}
	if sniffed.ReviewID != explicit.ReviewID {
		t.Errorf("one clean run captured two ways: %s (%s) vs %s (%s)",
			sniffed.ReviewID, sniffed.Format, explicit.ReviewID, explicit.Format)
	}
}

// An artifact that fails to read is reported, not swallowed. Neither silent
// option is honest: advancing the watermark over one loses that review on
// nothing louder than a stderr warning, and holding the watermark behind one
// pins it forever, so a single truncated file makes every later run re-mine
// the whole tail of the cache. The watermark advances and the id is named.
func TestExtractLean_anUnreadableArtifactIsReported(t *testing.T) {
	// The broken artifact is the *newest* one on purpose. When it sat in the
	// middle, a watermark taken from the last artifact read still advanced
	// past it and the test could not see the pinning case at all.
	dir := t.TempDir()
	stamps := []string{"2026-01-15T10:00:00Z", "2026-01-15T11:00:00Z", "2026-01-15T12:00:00Z"}
	for i, stamp := range stamps {
		if _, err := WriteArtifact(dir, Artifact{
			ReviewID:   fmt.Sprintf("rev-00000000000%d", i+1),
			Source:     "code-review",
			Format:     FormatMarkdown,
			CapturedAt: stamp,
			RawText:    "## A\n\nsome review text\n",
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Broken in a way the metadata listing survives: it decodes only
	// review_id, source and captured_at, so the file still lists and is still
	// selected — it is ReadArtifact that fails on it.
	if err := os.WriteFile(ArtifactPath(dir, "rev-000000000003"),
		[]byte(`{"review_id":"rev-000000000003","source":"code-review",`+
			`"captured_at":"2026-01-15T12:00:00Z","findings":"not an array"}`), 0644); err != nil {
		t.Fatal(err)
	}

	lean, watermark, unreadable, err := ExtractLean(dir, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(lean) != 2 {
		t.Fatalf("want the two readable reviews, got %d", len(lean))
	}
	// The watermark advances past the broken artifact. Holding it behind one
	// would re-select and re-report that artifact on every run, forever.
	if watermark != "2026-01-15T12:00:00Z" {
		t.Errorf("watermark: got %q", watermark)
	}
	if len(unreadable) != 1 || unreadable[0] != "rev-000000000003" {
		t.Errorf("the unreadable artifact should be reported by id; got %v", unreadable)
	}

	// And the next run genuinely moves on: nothing left, nothing re-reported.
	lean, watermark, unreadable, err = ExtractLean(dir, nil, "2026-01-15T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(lean) != 0 || watermark != "" || len(unreadable) != 0 {
		t.Errorf("the broken artifact is still being offered: lean=%d watermark=%q unreadable=%v",
			len(lean), watermark, unreadable)
	}
}

// A capture stamp that does not parse is not a position on the watermark line,
// and every way of handling that downstream is bad: skipping the artifact
// loses the review the moment a watermark exists, and keeping it in every
// selection re-mines it forever and makes "everything already mined"
// unreachable. The stamp is repaired from the file's timestamp instead, so the
// artifact is mined once like any other and the watermark moves past it.
func TestExtractLean_anUnparseableStampIsRepaired(t *testing.T) {
	dir := t.TempDir()
	if _, err := WriteArtifact(dir, Artifact{
		ReviewID: "rev-000000000001", Source: "code-review", Format: FormatMarkdown,
		CapturedAt: "2026-01-15T10:00:00Z", RawText: "## A\n\nsome review text\n",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteArtifact(dir, Artifact{
		ReviewID: "rev-000000000002", Source: "code-review", Format: FormatMarkdown,
		CapturedAt: "not-a-stamp", RawText: "## B\n\nmore review text\n",
	}); err != nil {
		t.Fatal(err)
	}

	lean, watermark, unreadable, err := ExtractLean(dir, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(lean) != 2 || len(unreadable) != 0 {
		t.Errorf("a first run mines both and reports nothing lost: lean=%d unreadable=%v", len(lean), unreadable)
	}
	// The watermark must be a stamp the next run can parse — it validates its
	// own --since — and must be past the repaired artifact, not behind it.
	if _, parseErr := time.Parse(time.RFC3339Nano, watermark); parseErr != nil {
		t.Fatalf("watermark %q does not parse: %v", watermark, parseErr)
	}

	// Which means the next run is quiet: mined once, not offered again.
	lean, _, unreadable, err = ExtractLean(dir, nil, watermark)
	if err != nil {
		t.Fatal(err)
	}
	if len(lean) != 0 || len(unreadable) != 0 {
		t.Errorf("a routine run should report nothing: lean=%d unreadable=%v", len(lean), unreadable)
	}
}
