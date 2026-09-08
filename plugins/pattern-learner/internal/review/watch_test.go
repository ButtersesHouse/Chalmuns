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
	ws := watchers(t, "house-review:skill", "semgrep:tool")

	t.Run("skill invocation", func(t *testing.T) {
		payload := `{"tool_name":"Skill","tool_input":{"skill":"house-review"},"tool_response":` + hookFindings + `}`
		a, w, ok := FromHook([]byte(payload), ws, fixed)
		if !ok {
			t.Fatal("designated skill output should be captured")
		}
		if w.ID != "house-review" || a.Source != "house-review" {
			t.Errorf("attribution wrong: watcher=%q source=%q", w.ID, a.Source)
		}
		// A structured response must reach the findings parser whole rather
		// than being flattened into prose.
		if a.Format != FormatFindings || len(a.Findings) != 1 {
			t.Errorf("format=%q findings=%d", a.Format, len(a.Findings))
		}
		if !strings.Contains(a.Label, "/house-review") {
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
		payload := `{"tool_name":"Skill","tool_input":{"skill":"house-review"},` +
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
	ws := watchers(t, "house-review:skill")
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
	ws := watchers(t, "house-review:skill")
	payloads := []string{
		"",
		"not json at all",
		"null",
		"[]",
		`{"tool_name":"Skill"}`,
		`{"tool_name":"Skill","tool_input":{"skill":"code-review"}}`,                       // no response
		`{"tool_name":"Skill","tool_input":{"skill":"house-review"},"tool_response":""}`,   // empty response
		`{"tool_name":"Skill","tool_input":{"skill":"house-review"},"tool_response":{}}`,   // empty object
		`{"tool_name":"Skill","tool_input":{"skill":"house-review"},"tool_response":null}`, // null
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
	ws := watchers(t, "house-review:skill")
	for _, key := range responseKeys {
		payload := `{"tool_name":"Skill","tool_input":{"skill":"house-review"},"` + key + `":` + hookFindings + `}`
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

// A reporting tool's payload names no skill of its own, so it is attributed
// through reportingTools and nowhere else. Falling back to "whatever skill
// happens to be watched" filed /code-review's findings under an unrelated
// designation — the artifact then claimed a tool that never ran, and the rules
// mined from it cited that tool. A missed capture is recoverable; a review
// attributed to the wrong reviewer corrupts provenance silently.
func TestFromHook_reportFindingsIsNotMisattributed(t *testing.T) {
	payload := `{"tool_name":"ReportFindings","tool_input":{"findings":[{"file":"a.go","summary":"Never log tokens."}]}}`

	for _, spec := range []string{"semgrep:any", "semgrep:tool", "security-review:skill", "security-review:any"} {
		if _, w, ok := FromHook([]byte(payload), watchers(t, spec), fixed); ok {
			t.Errorf("watcher %q must not absorb /code-review's findings (got %q)", spec, w.Name)
		}
	}

	// The designation the mapping names does own it.
	if _, w, ok := FromHook([]byte(payload), watchers(t, "code-review:any"), fixed); !ok || w.ID != "code-review" {
		t.Errorf("ok=%v watcher=%q", ok, w.ID)
	}
}

// For a non-reporting tool the input is the request, not the review. Capturing
// it would file what someone asked for as what the reviewer said.
func TestFromHook_ordinaryToolInputIsNotAReview(t *testing.T) {
	ws := watchers(t, "house-review:skill")
	payload := `{"tool_name":"Skill","tool_input":{"skill":"house-review","prompt":"review the diff for bugs"}}`
	if _, _, ok := FromHook([]byte(payload), ws, fixed); ok {
		t.Error("a call with no response carries no review")
	}
}

// A slash command carries the skill in `command`. Without this a watcher
// designated --kind skill — which is what the docs recommend for /code-review
// — matched nothing, forever, with no error to show for it.
func TestFromHook_slashCommandMatchesASkillWatcher(t *testing.T) {
	ws := watchers(t, "house-review:skill")
	payload := `{"tool_name":"SlashCommand","tool_input":{"command":"/house-review --fix"},` +
		`"tool_response":"## A finding\n\nUse the shared logger everywhere."}`
	if _, _, ok := FromHook([]byte(payload), ws, fixed); !ok {
		t.Error("a skill watcher must match its slash-command invocation")
	}
	if Match(ws, "SlashCommand", "", "/house-review --fix") == nil {
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

// Quoting and here-documents are data, not commands. Each of these once
// produced a false capture or a false miss.
func TestMatch_quotingAndHeredocs(t *testing.T) {
	// Both names are designated: a case asserting that `npm install … eslint`
	// is not a run needs an eslint designation to assert anything at all.
	ws := watchers(t, "semgrep:tool", "eslint:tool")
	cases := []struct {
		name, command string
		want          bool
	}{
		{"escaped quote inside a quoted arg", `git commit -m "fix \"; semgrep noise\" here"`, false},
		{"single-quoted arg", `git commit -m 'fix; semgrep noise'`, false},
		{"heredoc body", "git commit -F- <<'EOF'\nsemgrep runs on every PR\nEOF", false},
		{"unquoted heredoc body", "cat <<EOF\nsemgrep\nEOF", false},
		{"indented heredoc body", "cat <<-\"END\"\nsemgrep\nEND\necho done", false},
		// A command *after* a heredoc is still a command: truncating there
		// meant a designated tool run later in the script was never captured.
		{"run after a heredoc", "cat <<EOF > rules.yaml\nrules: []\nEOF\nsemgrep --config rules.yaml .", true},
		{"escaped quote then a real run", `echo "a \" " && semgrep .`, true},
		{"plain run", "semgrep --json .", true},
		// A quoted span is a program name only if it is one bare word.
		{"quoted program name", `"semgrep" --json .`, true},
		{"quoted path with a space", `"/opt/my tools/semgrep" .`, false},
		// Quoted prose assigned to a variable put the tool's name one field
		// after an env assignment, which reads as command position.
		{"quoted prose in an assignment", `MSG="semgrep found nothing" && git commit -m "$MSG"`, false},
		{"quoted prose after a separator", `echo hi; "semgrep is great"`, false},
		// A shell string spans newlines. Scanning each line from a clean quote
		// state read the second line of a commit message as its own command.
		{"multi-line quoted argument", "git commit -m \"fix things\nsemgrep now runs on every PR\"", false},
		{"unterminated quote", `echo "semgrep found nothing`, false},
		// An apostrophe in a comment opened a quote that, once quote state
		// crossed newlines, swallowed every command below it.
		{"apostrophe in a comment", "# see the tool's docs\nsemgrep --json .", true},
		{"comment after a command", "echo \"a\" # it's fine\nsemgrep .", true},
		{"trailing comment", "semgrep . # done", true},
		{"a fragment is not a comment", "curl host/path#frag && semgrep .", true},
		// Reading `<<'EOF-1'` as "EOF" meant the terminator was never found and
		// the skip ate the rest of the script.
		{"delimiter with punctuation", "cat <<'EOF-1' > f\nbody\nEOF-1\nsemgrep .", true},
		// A line continuation joins the two lines into one command, so it is
		// neither a separator nor where a here-document body starts. Bash runs
		// `cat <<EOF note; semgrep …` here and the body begins on the line
		// after — treating the continuation as a newline read a continued
		// argument as a command, and running the body skip at it swallowed the
		// commands on the joined line.
		{"continuation joins the line", "cat <<EOF \\\nnote; semgrep now runs on every PR\nEOF\ntrue", true},
		{"continued argument is not a command", "npm install --save-dev \\\n  eslint", false},
		{"run after a continued heredoc opener", "cat <<EOF > r.yml \\\n  && semgrep --config r.yml .\nrules: []\nEOF", true},
		{"two heredocs on one line", "cat <<A <<B\nabody\nA\nsemgrep .\nB", false},
		// A `<<` inside `$(( ))` is a left shift, not a here-document operator.
		{"arithmetic shift is not a heredoc", "n=$(( 1 << x ))\nsemgrep .", true},
		{"arithmetic shift with a later bare word", "n=$(( 1 << x ))\nsemgrep .\nx\ngit log", true},
		// An unterminated here-document is data to end of input in bash too,
		// so the safe reading is the accurate one.
		{"unterminated heredoc", "cat <<EOF\nsemgrep now runs on every PR", false},
		{"delimiter bash accepts whole", "cat <<EOF@1\nsemgrep now runs\nEOF@1\ntrue", false},
		// bash strips leading tabs from a terminator only for `<<-`.
		{"indented terminator in a plain heredoc", "git commit -F - <<EOF\nnotes:\n  EOF\nsemgrep now runs on every PR\nEOF", false},
		{"indented terminator in a dashed heredoc", "cat <<-EOF\n\tbody\n\tEOF\nsemgrep .", true},
		{"backslash-quoted delimiter", "cat <<\\EOF\nx; semgrep bad\nEOF\ntrue", false},
		{"digit-leading delimiter", "cat <<1EOF\nx; semgrep bad\n1EOF\ntrue", false},
		// `#` opens a comment only at the start of a word. A command
		// substitution leaves it mid-word; a subshell does not.
		{"hash after a substitution", "echo $(date +%Y)#1 && semgrep .", true},
		{"hash after a subshell", "(true)#note; semgrep .", false},
		// prev must be the character the shell sees, after continuations are
		// joined — the raw newline made this `#` a comment.
		{"hash after a continuation", "echo hello\\\n#1 && semgrep --config r.yml .", true},
		{"run inside a subshell", "(cd x && semgrep .)", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(ws, "Bash", "", tc.command) != nil; got != tc.want {
				t.Errorf("want %v got %v\n  command:   %q\n  sanitized: %q", tc.want, got, tc.command, sanitizeCommand(tc.command))
			}
		})
	}
}

// RFC3339Nano drops trailing zeros, so a whole-second stamp is the shorter
// string and would beat a later sub-second one in a raw string compare.
func TestStatus_lastCapturedComparesTimes(t *testing.T) {
	ws := watchers(t, "code-review:any")
	got := Status(ws, []Artifact{
		{ReviewID: "rev-a", Source: "code-review", CapturedAt: "2026-01-15T12:00:05.5Z"},
		{ReviewID: "rev-b", Source: "code-review", CapturedAt: "2026-01-15T12:00:05Z"},
	})
	if got[0].LastCapturedAt != "2026-01-15T12:00:05.5Z" {
		t.Errorf("last captured should be the later time; got %q", got[0].LastCapturedAt)
	}
}

// One review must produce one artifact. /code-review reports through
// ReportFindings, so the hook sees two calls for a single review — and
// capturing both recorded it twice, as two ids, which reads downstream as two
// independent reviews agreeing and defeats the single-review hold-back exactly
// where it matters. Its own Skill call carries prompt text, not findings.
func TestFromHook_oneReviewIsCapturedOnce(t *testing.T) {
	ws := watchers(t, "code-review:any")
	skillCall := `{"tool_name":"Skill","tool_input":{"skill":"code-review"},` +
		`"tool_response":"## Contents\n\n## Review Step R1\n\nsome skill prose"}`
	reportCall := `{"tool_name":"ReportFindings","tool_input":` + hookFindings + `}`

	if _, _, ok := FromHook([]byte(skillCall), ws, fixed); ok {
		t.Error("a reporting skill's own call carries prompt text, not a review")
	}
	a, _, ok := FromHook([]byte(reportCall), ws, fixed)
	if !ok || a.Format != FormatFindings {
		t.Errorf("the ReportFindings call is the review; ok=%v format=%q", ok, a.Format)
	}
	// The slash-command route is the same call by another name.
	slashCall := `{"tool_name":"SlashCommand","tool_input":{"command":"/code-review"},"tool_response":"prose"}`
	if _, _, ok := FromHook([]byte(slashCall), ws, fixed); ok {
		t.Error("the slash-command form must not double-capture either")
	}
}

// Designating a second reviewer must not disable capture. An earlier attempt
// treated any other non-tool watcher as a competing reporter, which meant the
// documented default kind ("any") silently switched the house format off as
// soon as anything else was watched. ReportFindings belongs to the skill that
// owns it; nothing else reports through it.
func TestFromHook_reportFindingsSurvivesOtherDesignations(t *testing.T) {
	payload := `{"tool_name":"ReportFindings","tool_input":` + hookFindings + `}`
	for _, ws := range [][]string{
		{"code-review:any"},
		{"code-review:any", "semgrep:any"},
		{"semgrep:any", "code-review:any"},
		{"code-review:skill", "security-review:skill"},
	} {
		_, w, ok := FromHook([]byte(payload), watchers(t, ws...), fixed)
		if !ok || w.ID != "code-review" {
			t.Errorf("designations %v: ok=%v watcher=%q", ws, ok, w.ID)
		}
	}
	// With no review skill designated there is nothing to attribute it to.
	if _, _, ok := FromHook([]byte(payload), watchers(t, "semgrep:tool"), fixed); ok {
		t.Error("a tool-only designation must not absorb a skill's findings")
	}
}

// A skill's own body is not a review. Some review skills report through a tool
// and return their instructions as the call's result; mining that would build
// standing conventions out of a prompt, and it grounds perfectly because the
// text really is in the artifact.
func TestFromHook_skillDefinitionIsNotAReview(t *testing.T) {
	ws := watchers(t, "security-review:skill")
	body := "---\nname: security-review\ndescription: Review the diff for vulnerabilities\n---\n\n" +
		"## Contents\n\n## Step 1\n\nDo the thing."
	payload := `{"tool_name":"Skill","tool_input":{"skill":"security-review"},"tool_response":` +
		string(mustJSON(body)) + `}`
	if _, _, ok := FromHook([]byte(payload), ws, fixed); ok {
		t.Error("a skill definition must not be captured as review output")
	}

	// A genuine prose review from the same skill still is.
	real := "## Never log tokens\n\nEverywhere else the token is redacted before logging."
	payload = `{"tool_name":"Skill","tool_input":{"skill":"security-review"},"tool_response":` +
		string(mustJSON(real)) + `}`
	if _, _, ok := FromHook([]byte(payload), ws, fixed); !ok {
		t.Error("a real review from a watched skill should still be captured")
	}
}

func mustJSON(s string) []byte {
	b, _ := json.Marshal(s)
	return b
}

// A "<<" inside a quoted string is text, not a here-document operator. Reading
// it as one meant the terminator was never found and every remaining line of
// the script was swallowed, so a designated tool run after it vanished.
func TestMatch_heredocOperatorInsideQuotesIsText(t *testing.T) {
	ws := watchers(t, "semgrep:tool")
	cmd := "git commit -m \"note << EOF style\"\nsemgrep --json ."
	if Match(ws, "Bash", "", cmd) == nil {
		t.Errorf("the run after the quoted text should still match\n  sanitized: %q", sanitizeCommand(cmd))
	}
}

// A skill invoked from a plugin arrives qualified ("pattern-learner:code-review"),
// and the double-capture filter compares bare names. Missing the qualifier let
// the Skill call through alongside the ReportFindings call, recording one
// review twice — the exact miscount TestFromHook_oneReviewIsCapturedOnce
// exists to prevent.
func TestFromHook_pluginQualifiedSkillIsStillTheReporter(t *testing.T) {
	ws := watchers(t, "code-review:any")
	payload := `{"tool_name":"Skill","tool_input":{"skill":"reviewer-pack:code-review"},` +
		`"tool_response":{"stdout":"Reviewing the diff at high effort…"}}`
	if _, _, ok := FromHook([]byte(payload), ws, fixed); ok {
		t.Error("the reporting skill's own call is prompt text, not the review")
	}
}

// A tool that ran and produced nothing comes back as an envelope of empty
// text fields beside the runner's own bookkeeping. Handing back either — the
// envelope marshalled whole, or the fields it did not recognise — recorded
// JSON punctuation as the reviewer's words, and because the first non-empty
// candidate wins it also shadowed a real review under a later response key.
func TestFromHook_emptyResponseEnvelopeIsNotAReview(t *testing.T) {
	ws := watchers(t, "semgrep:tool")
	cases := []struct{ name, response string }{
		{"empty text fields", `{"stdout":"","stderr":"","interrupted":false}`},
		{"numeric bookkeeping", `{"stdout":"","stderr":"","exit_code":0}`},
		{"string bookkeeping", `{"stdout":"","tool_use_id":"toolu_01ABC","sandbox":"none"}`},
		{"nested bookkeeping", `{"stdout":"","meta":{"tool_use_id":"toolu_01ABC"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := `{"tool_name":"Bash","tool_input":{"command":"semgrep --json ."},` +
				`"tool_response":` + tc.response + `}`
			if a, _, ok := FromHook([]byte(payload), ws, fixed); ok {
				t.Errorf("an empty response envelope should capture nothing; got %q", a.RawText)
			}
		})
	}
}

// An empty envelope must not shadow the real review either: responseText takes
// the first non-empty candidate, so residue handed back from tool_response
// discarded a review sitting under a later response key.
func TestFromHook_anEmptyEnvelopeDoesNotShadowTheReview(t *testing.T) {
	ws := watchers(t, "semgrep:tool")
	payload := `{"tool_name":"Bash","tool_input":{"command":"semgrep --json ."},` +
		`"tool_response":{"stdout":"","tool_use_id":"toolu_01ABC"},` +
		`"result":{"stdout":"## Use the shared http client\n\nThe svc package should reuse it."}}`
	a, _, ok := FromHook([]byte(payload), ws, fixed)
	if !ok {
		t.Fatal("the review under a later response key should be captured")
	}
	if !strings.Contains(a.RawText, "Use the shared http client") {
		t.Errorf("captured the wrong thing: %q", a.RawText)
	}
}

// Match accepts a skill named as the payload's tool_name, so the filter that
// keeps a reporting skill's own call out of the cache has to read that
// spelling too. Testing only tool_input let the same review through twice,
// under two ids, which downstream reads as two reviewers agreeing.
func TestFromHook_reporterNamedAsTheToolNameIsStillFiltered(t *testing.T) {
	ws := watchers(t, "code-review:any")
	payload := `{"tool_name":"code-review","tool_input":{"prompt":"review the diff"},` +
		`"tool_response":{"stdout":"I reviewed the diff and reported the findings."}}`
	if _, _, ok := FromHook([]byte(payload), ws, fixed); ok {
		t.Error("the reporting skill's own call is prompt text, not the review")
	}
}

// A linter that writes its report to stderr has still reported. Treating an
// empty stdout as an empty response made it silent, which is indistinguishable
// from "found no conventions" — the failure this package exists to avoid.
func TestFromHook_reportOnStderrIsStillAReview(t *testing.T) {
	ws := watchers(t, "semgrep:tool")
	payload := `{"tool_name":"Bash","tool_input":{"command":"semgrep --json ."},` +
		`"tool_response":{"stdout":"","stderr":"{\"results\":[{\"check_id\":\"py.no-requests\",` +
		`\"path\":\"svc/c.py\",\"extra\":{\"message\":\"Use the shared http client.\"}}]}"}}`
	a, _, ok := FromHook([]byte(payload), ws, fixed)
	if !ok {
		t.Fatal("a report on stderr should be captured")
	}
	if len(a.Findings) != 1 || a.Findings[0].Title != "py.no-requests" {
		t.Errorf("stderr report did not parse: %+v", a.Findings)
	}

	// But a diagnostic is not a report, and nothing in the stream tells them
	// apart — only the shape does. Capturing a crash message would ask the
	// extraction subagent to mine standing conventions out of it.
	for _, noise := range []string{
		"semgrep: error: unrecognized argument --jsonn",
		"Scanning 12 files with 40 rules.",
	} {
		diag := `{"tool_name":"Bash","tool_input":{"command":"semgrep --json ."},` +
			`"tool_response":{"stdout":"","stderr":` + string(mustJSON(noise)) + `}}`
		if got, _, ok := FromHook([]byte(diag), ws, fixed); ok {
			t.Errorf("a diagnostic on stderr is not a review; captured %q", got.RawText)
		}
	}

	// A report under a known text key still wins over stderr noise.
	both := `{"tool_name":"Bash","tool_input":{"command":"semgrep --json ."},` +
		`"tool_response":{"stderr":"Scanning 12 files with 40 rules.",` +
		`"text":"## Use the shared client\n\nThe auth package should reuse it."}}`
	got, _, ok := FromHook([]byte(both), ws, fixed)
	if !ok || !strings.Contains(got.RawText, "shared client") {
		t.Errorf("stderr noise shadowed the review: ok=%v raw=%q", ok, got.RawText)
	}

	// And the report wins wherever it sits. A runner that adds its own
	// `"message":"Command exited with code 1"` beside a linter's JSON had that
	// bookkeeping read as the review, purely because "message" is one of the
	// keys checked before the one the report was actually in.
	shadowed := `{"tool_name":"Bash","tool_input":{"command":"semgrep --json ."},` +
		`"tool_response":{"stdout":"","message":"Command exited with code 1","stderr":` +
		string(mustJSON(`{"results":[{"check_id":"py.no-requests","path":"svc/c.py",`+
			`"extra":{"message":"Use the shared http client."}}]}`)) + `}}`
	got, _, ok = FromHook([]byte(shadowed), ws, fixed)
	if !ok {
		t.Fatal("the structured report should have been found")
	}
	if len(got.Findings) != 1 {
		t.Errorf("the runner's bookkeeping shadowed the report: %q", got.RawText)
	}
}

// Which field of a response is the review is decided by what the field holds,
// not by which key happens to be checked first. Every case here had the wrong
// candidate win at some point: a runner's bookkeeping beating a report, an
// empty report beating a prose write-up, an envelope's punctuation beating
// nothing at all.
func TestFromHook_theReviewWinsOverWhateverSitsBesideIt(t *testing.T) {
	ws := watchers(t, "semgrep:tool")
	report := `{"check_id":"py.no-requests","path":"svc/c.py","extra":{"message":"Use the shared http client."}}`
	cases := []struct {
		name, response string
		wantFindings   int
		wantContains   string
	}{
		{"report beside bookkeeping in one map",
			`{"results":[` + report + `],"message":"Command exited with code 1"}`, 1, "py.no-requests"},
		{"report beside a null stderr",
			`{"results":[` + report + `],"stderr":null}`, 1, "py.no-requests"},
		{"structured stderr beats a message field",
			`{"stdout":"","message":"Command exited with code 1","stderr":` +
				string(mustJSON(`{"results":[`+report+`]}`)) + `}`, 1, "py.no-requests"},
		{"an empty report does not shadow prose",
			`{"stdout":"## Review\n\n- Use the shared http client everywhere.\n","stderr":"{\"results\":[]}"}`,
			1, "shared http client"},
		{"an empty findings array does not shadow prose",
			`{"content":"## Review\n\n- Use the shared http client everywhere.\n","stdout":"{\"findings\":[]}"}`,
			1, "shared http client"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := `{"tool_name":"Bash","tool_input":{"command":"semgrep --json ."},` +
				`"tool_response":` + tc.response + `}`
			a, _, ok := FromHook([]byte(payload), ws, fixed)
			if !ok {
				t.Fatal("the review should have been captured")
			}
			if len(a.Findings) != tc.wantFindings {
				t.Errorf("findings: want %d got %d (raw %q)", tc.wantFindings, len(a.Findings), a.RawText)
			}
			if !strings.Contains(a.RawText+findingText(a), tc.wantContains) {
				t.Errorf("the wrong candidate won: %q", a.RawText)
			}
		})
	}
}

func findingText(a Artifact) string {
	var b strings.Builder
	for _, f := range a.Findings {
		b.WriteString(f.Title)
		b.WriteString(f.Body)
	}
	return b.String()
}

// A response envelope that says nothing must not be recorded whatever shape
// its emptiness takes, and a map carrying only a runner's bookkeeping is not a
// review either — that JSON sniffs as prose, so the clean-run guard, which
// exempts prose, could not catch it.
func TestFromHook_bookkeepingIsNeverAReview(t *testing.T) {
	ws := watchers(t, "semgrep:tool")
	for _, response := range []string{
		`{"stderr":null}`,
		`{"stderr":false}`,
		`{"interrupted":false}`,
		`{"is_error":true,"tool_use_id":"toolu_01ABC"}`,
		`{"stdout":"","exit_code":1}`,
	} {
		payload := `{"tool_name":"Bash","tool_input":{"command":"semgrep --json ."},` +
			`"tool_response":` + response + `}`
		if a, _, ok := FromHook([]byte(payload), ws, fixed); ok {
			t.Errorf("%s should capture nothing; got %q", response, a.RawText)
		}
	}

	// A response that *is* a report, handed over as a decoded object rather
	// than a string, still has to reach the parser.
	report := `{"tool_name":"Bash","tool_input":{"command":"semgrep --json ."},` +
		`"tool_response":{"results":[{"check_id":"py.no-requests","path":"svc/c.py",` +
		`"extra":{"message":"Use the shared http client."}}]}}`
	a, _, ok := FromHook([]byte(report), ws, fixed)
	if !ok || len(a.Findings) != 1 {
		t.Errorf("a decoded report should still parse: ok=%v findings=%+v", ok, a.Findings)
	}
}

// The hook is the unattended path, so a designated linter fires on every run.
// A structured report that found nothing has said nothing, and recording it
// would write one content-free artifact per clean run into the repository
// forever. A prose review is exempt: its whole content is its text.
func TestFromHook_aCleanStructuredRunIsNotRecorded(t *testing.T) {
	ws := watchers(t, "semgrep:tool", "house-review:skill")

	clean := `{"tool_name":"Bash","tool_input":{"command":"semgrep --json ."},` +
		`"tool_response":{"stdout":"{\"results\":[],\"paths\":{\"scanned\":[\"a.py\"]}}"}}`
	if a, _, ok := FromHook([]byte(clean), ws, fixed); ok {
		t.Errorf("a clean structured run should record nothing; got %q", a.RawText)
	}

	prose := `{"tool_name":"Skill","tool_input":{"skill":"house-review"},` +
		`"tool_response":{"stdout":"The auth package looks right; nothing to flag this round."}}`
	if _, _, ok := FromHook([]byte(prose), ws, fixed); !ok {
		t.Error("a prose review with no parsed findings still has something to say")
	}
}
