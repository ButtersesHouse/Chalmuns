package review

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ButtersesHouse/Chalmuns/internal/state"
)

// watchers builds a designation list from "name:kind" pairs.
func watchers(t *testing.T, specs ...string) []state.Watcher {
	t.Helper()
	var out []state.Watcher
	for _, spec := range specs {
		name, kind, _ := strings.Cut(spec, ":")
		w, err := NewWatcher(name, kind, "", fixed)
		if err != nil {
			t.Fatalf("NewWatcher(%q): %v", spec, err)
		}
		out = append(out, w)
	}
	return out
}

func TestNewWatcher_normalizesAndDefaults(t *testing.T) {
	w, err := NewWatcher("  /Code-Review  ", "", "", fixed)
	if err != nil {
		t.Fatal(err)
	}
	// The leading slash is how a user names a skill; it is not part of the name.
	if w.Name != "Code-Review" || w.ID != "code-review" {
		t.Errorf("name/id: got %q/%q", w.Name, w.ID)
	}
	if w.Kind != KindAny || w.Format != FormatAuto {
		t.Errorf("defaults: got kind=%q format=%q", w.Kind, w.Format)
	}
	if w.AddedAt != fixed.Format(time.RFC3339) {
		t.Errorf("added_at: got %q", w.AddedAt)
	}
}

// A designation that can never match records nothing, and silence is
// indistinguishable from "the tool found no conventions" — so refuse it.
func TestNewWatcher_rejectsUnusable(t *testing.T) {
	cases := []struct{ name, kind, format string }{
		{"", "", ""},
		{"   ", "", ""},
		{"///", "", ""},
		{"code-review", "spaceship", ""},
		{"code-review", "", "yaml"},
	}
	for _, tc := range cases {
		if _, err := NewWatcher(tc.name, tc.kind, tc.format, fixed); err == nil {
			t.Errorf("NewWatcher(%q,%q,%q) should have failed", tc.name, tc.kind, tc.format)
		}
	}
}

func TestAddWatcher_refusesDuplicate(t *testing.T) {
	ws := watchers(t, "code-review:skill")
	dup, err := NewWatcher("Code Review", "", "", fixed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AddWatcher(ws, dup); err == nil {
		t.Error("a second designation with the same id must be refused, not silently shadow the first")
	}
}

func TestRemoveWatcher(t *testing.T) {
	ws := watchers(t, "code-review:skill", "semgrep:tool")
	got, found := RemoveWatcher(ws, "semgrep")
	if !found || len(got) != 1 || got[0].ID != "code-review" {
		t.Errorf("remove: found=%v left=%+v", found, got)
	}
	if _, found := RemoveWatcher(ws, "nope"); found {
		t.Error("removing an absent watcher should report not-found")
	}
}

func TestMatch_skillInvocation(t *testing.T) {
	ws := watchers(t, "code-review:skill")
	cases := []struct {
		name        string
		tool, skill string
		wantMatch   bool
	}{
		{"bare skill name", "Skill", "code-review", true},
		{"leading slash", "Skill", "/code-review", true},
		{"plugin-qualified", "Skill", "myplugin:code-review", true},
		{"case-insensitive", "Skill", "Code-Review", true},
		{"tool name is the skill", "code-review", "", true},
		{"a different skill", "Skill", "security-review", false},
		{"no skill at all", "Bash", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Match(ws, tc.tool, tc.skill, "") != nil
			if got != tc.wantMatch {
				t.Errorf("Match(tool=%q skill=%q): want %v, got %v", tc.tool, tc.skill, tc.wantMatch, got)
			}
		})
	}
}

// A tool merely named in an argument is not a run of it. Capturing grep's
// output as semgrep's review would attribute one tool's words to another.
func TestMatch_commandPosition(t *testing.T) {
	ws := watchers(t, "semgrep:tool", "eslint:tool")
	cases := []struct {
		command string
		want    bool
	}{
		{"semgrep --json .", true},
		{"/usr/local/bin/semgrep scan", true},
		{"SEMGREP_RULES=x semgrep scan", true},
		{"npx eslint --format json .", true},
		{"cat f | semgrep --json -", true},
		{"make lint && eslint .", true},
		{"grep semgrep notes.txt", false},
		{"echo semgrep", false},
		{"cat semgrep.log", false},
		{"eslint-config-check .", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			if got := Match(ws, "Bash", "", tc.command) != nil; got != tc.want {
				t.Errorf("Match(command=%q): want %v, got %v", tc.command, tc.want, got)
			}
		})
	}
}

// Kind narrows what a designation can match, so a tool and a skill sharing a
// name are not confused for each other.
func TestMatch_kindNarrows(t *testing.T) {
	skillOnly := watchers(t, "review:skill")
	if Match(skillOnly, "Bash", "", "review --json .") != nil {
		t.Error("a skill watcher must not match a command")
	}
	toolOnly := watchers(t, "review:tool")
	if Match(toolOnly, "Skill", "review", "") != nil {
		t.Error("a tool watcher must not match a skill invocation")
	}
	any := watchers(t, "review:any")
	if Match(any, "Skill", "review", "") == nil || Match(any, "Bash", "", "review .") == nil {
		t.Error("an 'any' watcher should match both")
	}
}

func TestStatus_countsFromTheCache(t *testing.T) {
	ws := watchers(t, "code-review:skill", "semgrep:tool")
	artifacts := []Artifact{
		{ReviewID: "rev-a", Source: "code-review", CapturedAt: "2026-01-01T00:00:00Z"},
		{ReviewID: "rev-b", Source: "Code-Review", CapturedAt: "2026-01-03T00:00:00Z"},
		{ReviewID: "rev-c", Source: "other-tool", CapturedAt: "2026-01-09T00:00:00Z"},
	}
	got := Status(ws, artifacts)
	if got[0].Captures != 2 || got[0].LastCapturedAt != "2026-01-03T00:00:00Z" {
		t.Errorf("code-review status: %+v", got[0])
	}
	if got[1].Captures != 0 || got[1].LastCapturedAt != "" {
		t.Errorf("a watcher with no captures reports zero: %+v", got[1])
	}
}

// --- hook capture ---

const hookFindings = `{"findings":[{"file":"a.go","line":1,"summary":"Wrap errors with %w so callers can errors.Is them."}]}`

func TestFromHook_capturesDesignatedSources(t *testing.T) {
	ws := watchers(t, "code-review:skill", "semgrep:tool")

	t.Run("skill invocation", func(t *testing.T) {
		payload := `{"tool_name":"Skill","tool_input":{"skill":"code-review"},"tool_response":` + hookFindings + `}`
		a, w, ok := FromHook([]byte(payload), ws, fixed)
		if !ok {
			t.Fatal("designated skill output should be captured")
		}
		if w.ID != "code-review" || a.Source != "code-review" {
			t.Errorf("attribution wrong: watcher=%q source=%q", w.ID, a.Source)
		}
		// A structured response must reach the findings parser whole rather
		// than being flattened into prose.
		if a.Format != FormatFindings || len(a.Findings) != 1 {
			t.Errorf("format=%q findings=%d", a.Format, len(a.Findings))
		}
		if !strings.Contains(a.Label, "/code-review") {
			t.Errorf("label should name what produced it; got %q", a.Label)
		}
	})

	t.Run("command output as a string", func(t *testing.T) {
		payload := `{"tool_name":"Bash","tool_input":{"command":"semgrep --json ."},"tool_response":{"stdout":` +
			`"{\"results\":[{\"check_id\":\"c\",\"path\":\"a.py\",\"extra\":{\"message\":\"Use the shared client.\"}}]}"}}`
		a, _, ok := FromHook([]byte(payload), ws, fixed)
		if !ok {
			t.Fatal("designated tool output should be captured")
		}
		if a.Format != FormatSemgrep {
			t.Errorf("format: got %q", a.Format)
		}
	})

	t.Run("content blocks are joined", func(t *testing.T) {
		payload := `{"tool_name":"Skill","tool_input":{"skill":"code-review"},` +
			`"tool_response":[{"type":"text","text":"## A finding\n\nUse the shared logger everywhere."}]}`
		a, _, ok := FromHook([]byte(payload), ws, fixed)
		if !ok {
			t.Fatal("a content-block response should be captured")
		}
		if !strings.Contains(a.RawText, "Use the shared logger") {
			t.Errorf("raw text: got %q", a.RawText)
		}
	})
}

// Nothing is captured from a tool nobody designated — that is what makes the
// hook a designation rather than surveillance.
func TestFromHook_ignoresUndesignated(t *testing.T) {
	ws := watchers(t, "code-review:skill")
	payload := `{"tool_name":"Bash","tool_input":{"command":"go test ./..."},"tool_response":{"stdout":"ok"}}`
	if _, _, ok := FromHook([]byte(payload), ws, fixed); ok {
		t.Error("an undesignated tool must not be captured")
	}
}

func TestFromHook_noWatchersCapturesNothing(t *testing.T) {
	payload := `{"tool_name":"Skill","tool_input":{"skill":"code-review"},"tool_response":` + hookFindings + `}`
	if _, _, ok := FromHook([]byte(payload), nil, fixed); ok {
		t.Error("with nothing designated the hook must do nothing")
	}
}

// The hook runs inside someone's tool call. Every malformed input has to be a
// quiet no-op, never a panic and never an error the session would surface.
func TestFromHook_failsOpen(t *testing.T) {
	ws := watchers(t, "code-review:skill")
	payloads := []string{
		"",
		"not json at all",
		"null",
		"[]",
		`{"tool_name":"Skill"}`,
		`{"tool_name":"Skill","tool_input":{"skill":"code-review"}}`,                       // no response
		`{"tool_name":"Skill","tool_input":{"skill":"code-review"},"tool_response":""}`,    // empty response
		`{"tool_name":"Skill","tool_input":{"skill":"code-review"},"tool_response":{}}`,    // empty object
		`{"tool_name":"Skill","tool_input":{"skill":"code-review"},"tool_response":null}`,  // null
		`{"tool_name":"Skill","tool_input":"a string, not an object","tool_response":"x"}`, // wrong type
		`{"tool_name":123,"tool_input":{"skill":456},"tool_response":"some text"}`,         // wrong types
	}
	for _, payload := range payloads {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panicked on %q: %v", payload, r)
				}
			}()
			if _, _, ok := FromHook([]byte(payload), ws, fixed); ok {
				t.Errorf("payload %q should not have produced an artifact", payload)
			}
		}()
	}
}

// The result field's name is the one thing about the payload this repository
// cannot verify, so capture must read whichever field actually carries text.
func TestFromHook_findsOutputUnderAnyKnownKey(t *testing.T) {
	ws := watchers(t, "code-review:skill")
	for _, key := range responseKeys {
		payload := `{"tool_name":"Skill","tool_input":{"skill":"code-review"},"` + key + `":` + hookFindings + `}`
		if _, _, ok := FromHook([]byte(payload), ws, fixed); !ok {
			t.Errorf("output under %q was not found", key)
		}
	}
}

// /code-review reports its findings by calling ReportFindings, whose payload
// carries them as that call's *input*. Reading only tool responses missed the
// one format this package names as its house shape and captured the skill's
// own prose instead.
func TestFromHook_reportFindingsIsTheReview(t *testing.T) {
	ws := watchers(t, "code-review:skill")
	payload := `{"tool_name":"ReportFindings","tool_input":{"findings":[{"file":"a.go","line":1,` +
		`"summary":"Wrap errors with %w so callers can errors.Is them.","short_summary":"Wrap with %w"}],"level":"high"}}`

	a, w, ok := FromHook([]byte(payload), ws, fixed)
	if !ok {
		t.Fatal("a ReportFindings call is review output and should be captured")
	}
	if w.ID != "code-review" || a.Format != FormatFindings || len(a.Findings) != 1 {
		t.Errorf("watcher=%q format=%q findings=%d", w.ID, a.Format, len(a.Findings))
	}
	if a.Findings[0].Title != "Wrap with %w" {
		t.Errorf("findings not parsed: %+v", a.Findings[0])
	}
}

// A watcher designated under another name still owns a reporting tool's
// output — the payload names no skill, and discarding it would silently drop a
// review the user asked to be watched.
func TestFromHook_reportFindingsFallsBackToTheSkillWatcher(t *testing.T) {
	ws := watchers(t, "security-review:skill")
	payload := `{"tool_name":"ReportFindings","tool_input":{"findings":[{"file":"a.go","summary":"Never log tokens."}]}}`
	_, w, ok := FromHook([]byte(payload), ws, fixed)
	if !ok || w.ID != "security-review" {
		t.Errorf("ok=%v watcher=%q", ok, w.ID)
	}

	// With only command-line tools designated there is no skill to attribute
	// it to, so nothing is captured.
	if _, _, ok := FromHook([]byte(payload), watchers(t, "semgrep:tool"), fixed); ok {
		t.Error("a tool-only designation must not absorb a skill's findings")
	}
}

// For a non-reporting tool the input is the request, not the review. Capturing
// it would file what someone asked for as what the reviewer said.
func TestFromHook_ordinaryToolInputIsNotAReview(t *testing.T) {
	ws := watchers(t, "code-review:skill")
	payload := `{"tool_name":"Skill","tool_input":{"skill":"code-review","prompt":"review the diff for bugs"}}`
	if _, _, ok := FromHook([]byte(payload), ws, fixed); ok {
		t.Error("a call with no response carries no review")
	}
}

// A slash command carries the skill in `command`. Without this a watcher
// designated --kind skill — which is what the docs recommend for /code-review
// — matched nothing, forever, with no error to show for it.
func TestFromHook_slashCommandMatchesASkillWatcher(t *testing.T) {
	ws := watchers(t, "code-review:skill")
	payload := `{"tool_name":"SlashCommand","tool_input":{"command":"/code-review --fix"},` +
		`"tool_response":"## A finding\n\nUse the shared logger everywhere."}`
	if _, _, ok := FromHook([]byte(payload), ws, fixed); !ok {
		t.Error("a skill watcher must match its slash-command invocation")
	}
	if Match(ws, "SlashCommand", "", "/code-review --fix") == nil {
		t.Error("Match should resolve the slash command to the skill")
	}
	// A different slash command is still not this reviewer.
	if Match(ws, "SlashCommand", "", "/commit -m x") != nil {
		t.Error("an unrelated slash command must not match")
	}
}

// The invariant the whole capture path exists to hold: nothing is captured
// from a tool that was not designated. Splitting on separators regardless of
// quoting manufactured a command position inside quoted data, so an unrelated
// tool's output was filed as the watched reviewer's.
func TestMatch_quotedTextIsDataNotACommand(t *testing.T) {
	ws := watchers(t, "semgrep:tool", "code-review:tool")
	notRuns := []string{
		`git commit -m 'fix; semgrep noise'`,
		`git commit -m "fix; code-review feedback applied"`,
		`echo "(semgrep is noisy)"`,
		`git commit -F- <<'EOF'` + "\nsemgrep now runs on every PR\nEOF",
		`printf '%s\n' "a | semgrep b"`,
	}
	for _, cmd := range notRuns {
		if Match(ws, "Bash", "", cmd) != nil {
			t.Errorf("not a run of the watched tool, but matched: %q", cmd)
		}
	}
	// Real invocations still match, including after a genuine separator.
	realRuns := []string{
		`semgrep --json .`,
		`git add -A; semgrep --json .`,
		`echo "hi" && semgrep scan`,
	}
	for _, cmd := range realRuns {
		if Match(ws, "Bash", "", cmd) == nil {
			t.Errorf("a real invocation was missed: %q", cmd)
		}
	}
}

// A command line routinely carries credentials — `SEMGREP_APP_TOKEN=… semgrep`
// is the documented way to run that tool — and the label is persisted to an
// artifact inside the repository, where it can be committed and shared. The
// label names the reviewer, never the command.
func TestFromHook_labelDoesNotRecordTheCommandLine(t *testing.T) {
	ws := watchers(t, "semgrep:tool")
	const secret = "sk-secret-abc123"
	payload := `{"tool_name":"Bash","tool_input":{"command":"SEMGREP_APP_TOKEN=` + secret + ` semgrep --json ."},` +
		`"tool_response":{"stdout":"{\"results\":[{\"check_id\":\"c\",\"path\":\"a.py\",` +
		`\"extra\":{\"message\":\"Use the shared client please.\"}}]}"}}`

	a, _, ok := FromHook([]byte(payload), ws, fixed)
	if !ok {
		t.Fatal("a designated tool's output should be captured")
	}
	if a.Label != "hook capture: semgrep" {
		t.Errorf("label should name the reviewer; got %q", a.Label)
	}
	blob, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), secret) {
		t.Error("a credential on the command line must not reach the stored artifact")
	}
}

func TestSkillFromCommand(t *testing.T) {
	cases := map[string]string{
		"/code-review":       "/code-review",
		"/code-review --fix": "/code-review",
		"  /code-review ":    "/code-review",
		"/usr/local/bin/x":   "", // a path runs a program
		"/":                  "", // names nothing
		"//x":                "",
		"semgrep --json .":   "",
		"":                   "",
	}
	for cmd, want := range cases {
		if got := SkillFromCommand(cmd); got != want {
			t.Errorf("SkillFromCommand(%q) = %q, want %q", cmd, got, want)
		}
	}
}
