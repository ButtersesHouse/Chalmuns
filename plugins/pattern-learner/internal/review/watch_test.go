package review

import (
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
