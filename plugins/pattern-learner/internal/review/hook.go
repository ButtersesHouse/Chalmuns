package review

import (
	"bytes"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/ButtersesHouse/Chalmuns/internal/state"
)

// Capture from a hook payload.
//
// This is the automatic half of "keep eyes on": once a source is designated,
// its output is recorded as it happens rather than being imported by hand.
// It follows internal/guard's discipline exactly — a payload it cannot parse,
// a field it does not recognise, or a tool nobody designated all mean "do
// nothing", never "fail". A capture hook that errored would surface as a
// broken tool call in the user's session, which is far worse than a missed
// review.
//
// What this code does NOT assume: it does not depend on any one field name
// carrying the tool's output. The pre-invocation payload shape is pinned by
// internal/guard (tool_name, cwd, tool_input.command, tool_input.file_path),
// but the field a post-invocation payload delivers a result under is not
// something this repository can verify, and inventing one would produce a
// feature that silently never fires. So FromHook reads whichever of several
// plausible fields actually carries text, and the explicit
// `capture-review --file` path — which depends on none of this — remains the
// supported way to feed a review in. If the hook never fires, the feature
// still works; the user just runs one command.

// responseKeys are the payload fields that may carry a completed tool's
// output, most specific first.
var responseKeys = []string{"tool_response", "tool_result", "tool_output", "response", "result", "output"}

// reportingTools are tools whose *input* is the review. Claude Code's
// /code-review skill does not return its findings as the skill call's result —
// it reports them by calling ReportFindings, whose payload carries the
// findings array as that call's arguments. Reading only a tool's response
// therefore missed the one format this package names as its house shape, and
// captured the skill's own prose instead. Each entry maps such a tool to the
// reviewer it belongs to, used when the payload names no skill of its own.
var reportingTools = map[string]string{
	"ReportFindings": "code-review",
}

// reportingSkills are the skills whose findings arrive through a reporting
// tool rather than as their own call's result. Their Skill/SlashCommand call
// is deliberately NOT captured: that call's response is the skill's own
// instructions, not a review, and capturing both routes recorded one review
// twice — as two artifacts with two ids, which reads downstream as two
// independent reviews agreeing and so defeats the single-review hold-back
// exactly where it matters most.
//
// Only the skills whose reporting tool is known belong here. Any other skill
// that behaves the same way is caught structurally instead, by
// looksLikeSkillDefinition — guessing at a list would be how the wrong review
// gets attributed.
var reportingSkills = map[string]bool{}

func init() {
	for _, skill := range reportingTools {
		reportingSkills[skill] = true
	}
}

// responseTextKeys are the fields that may carry a review inside a structured
// response. stderr is last and read on different terms — see valueText.
var responseTextKeys = []string{"content", "output", "stdout", "text", "body", "message", "stderr"}

// FromHook decides whether a hook payload belongs to a designated watcher and,
// if so, normalizes the reviewed output into an Artifact. The second return is
// the watcher that matched. ok is false whenever there is nothing to record,
// which includes every payload from an undesignated tool.
func FromHook(payload []byte, watchers []state.Watcher, now time.Time) (a Artifact, w state.Watcher, ok bool) {
	if len(watchers) == 0 {
		return Artifact{}, state.Watcher{}, false
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(payload, &doc); err != nil {
		return Artifact{}, state.Watcher{}, false
	}

	toolName, _ := doc["tool_name"].(string)
	skill, command := invocation(doc)
	reporter, isReporting := reportingTools[toolName]
	if isReporting && skill == "" {
		skill = reporter
	}

	// A skill that reports through a reporting tool is captured from that tool
	// alone; its own call carries prompt text, not findings.
	//
	// Both spellings are checked because Match accepts both: a skill call may
	// name the skill in tool_input, or the payload may carry it as the
	// tool_name itself. Testing only tool_input let the same review through as
	// two artifacts with two ids, which downstream reads as two independent
	// reviewers agreeing.
	if !isReporting && (reportingSkills[bareSkillName(skill)] || reportingSkills[bareSkillName(toolName)]) {
		return Artifact{}, state.Watcher{}, false
	}

	// A reporting tool's payload is review output but names no skill of its
	// own, so it is attributed through reportingTools above and nowhere else.
	// Falling back to "some designated skill watcher" instead filed
	// /code-review's findings under whatever else happened to be watched — a
	// semgrep designation, say — so the artifact claimed a tool that never ran
	// and the rules it produced cited it. A missed capture is recoverable; a
	// review attributed to the wrong reviewer corrupts provenance silently.
	//
	matched := Match(watchers, toolName, skill, command)
	if matched == nil {
		return Artifact{}, state.Watcher{}, false
	}

	text := responseText(doc, isReporting)
	if strings.TrimSpace(text) == "" {
		return Artifact{}, state.Watcher{}, false
	}
	// A skill's own body is not a review. Some review skills deliver findings
	// through a reporting tool and return their instructions as the call's
	// result; capturing that would mine standing conventions out of a prompt,
	// and it grounds perfectly because the text really is in the artifact.
	// The check is structural rather than a guess: only a skill or command
	// definition opens with that frontmatter.
	if looksLikeSkillDefinition(text) {
		return Artifact{}, state.Watcher{}, false
	}

	art, err := Capture(Input{
		Data:   []byte(text),
		Source: matched.Name,
		Format: matched.Format,
		Label:  hookLabel(matched.Name, toolName, skill),
		Now:    now,
	})
	if err != nil {
		return Artifact{}, state.Watcher{}, false
	}
	// A structured report that parsed cleanly and found nothing has said
	// nothing, and the hook is the unattended path: a linter wired to a
	// designation fires on every run, and a tool that varies its incidental
	// fields between clean runs — eslint echoes `source`, semgrep varies
	// `paths.scanned` — would otherwise write one content-free artifact per run
	// into the repository forever, inflating the capture tally `watch --list`
	// reports and the work of every mining run. `capture-review --file` still
	// records whatever it is handed: there someone asked for it.
	//
	// Prose is exempt. A markdown review's whole content is its text, so
	// "no parsed findings" says nothing about whether it has anything to say.
	if len(art.Findings) == 0 && art.Format != FormatMarkdown {
		return Artifact{}, state.Watcher{}, false
	}
	return art, *matched, true
}

// bareSkillName reduces a skill reference to the name a designation carries:
// no leading slash, no plugin qualifier. matchesSkill accepts all three forms,
// so anything deciding "is this that skill" must too — comparing only the bare
// spelling let a plugin-qualified /code-review past the double-capture filter
// while still matching the watcher.
func bareSkillName(skill string) string {
	skill = strings.TrimPrefix(strings.TrimSpace(skill), "/")
	if i := strings.LastIndex(skill, ":"); i >= 0 {
		skill = skill[i+1:]
	}
	return strings.ToLower(skill)
}

// reSkillFrontmatter matches the opening of a skill or slash-command
// definition: a YAML block naming the skill, which no review output has.
var reSkillFrontmatter = regexp.MustCompile(`(?s)\A\s*---\r?\n.*?\bname:\s*\S`)

func looksLikeSkillDefinition(text string) bool {
	return reSkillFrontmatter.MatchString(text)
}

// invocation pulls the skill name and command line out of a payload's
// tool_input, which is the shape internal/guard already reads.
func invocation(doc map[string]interface{}) (skill, command string) {
	input, _ := doc["tool_input"].(map[string]interface{})
	if input == nil {
		return "", ""
	}
	// A skill invocation names the skill; different runners have called the
	// field both things, and neither is expensive to check.
	for _, key := range []string{"skill", "skill_name", "name"} {
		if s, _ := input[key].(string); s != "" {
			skill = s
			break
		}
	}
	command, _ = input["command"].(string)
	// A slash command carries the skill in `command`. Match derives this too;
	// resolving it here as well is what lets the artifact's label name the
	// skill rather than the raw command line.
	if skill == "" {
		skill = SkillFromCommand(command)
	}
	return skill, command
}

// PayloadCWD reads the working directory a hook payload reports, so a capture
// can find the project when CLAUDE_PROJECT_DIR is not exported — the same
// fallback internal/guard applies, for the same reason. It decodes only that
// one field: the hook runs after every matching tool call, and a full decode
// of a payload the caller may never use again is work multiplied by tool-call
// frequency.
func PayloadCWD(payload []byte) string {
	dec := json.NewDecoder(bytes.NewReader(payload))
	if _, err := dec.Token(); err != nil { // opening brace
		return ""
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return ""
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return ""
		}
		if name, ok := key.(string); ok && name == "cwd" {
			var cwd string
			if json.Unmarshal(v, &cwd) == nil {
				return cwd
			}
			return ""
		}
	}
	return ""
}

// hookLabel records what produced the artifact, for the approval display.
//
// It names the designated reviewer, never the raw command line. A command line
// routinely carries credentials — `SEMGREP_APP_TOKEN=… semgrep --json .` is the
// documented way to run that tool — and the label is persisted to an artifact
// inside the repository, where it can be committed and shared. The command adds
// nothing the watcher's name does not already say, so recording it is all risk
// and no benefit.
func hookLabel(watcher, toolName, skill string) string {
	switch {
	case skill != "":
		return "hook capture: /" + strings.TrimPrefix(skill, "/")
	case watcher != "":
		return "hook capture: " + watcher
	case toolName != "":
		return "hook capture: " + toolName
	}
	return "hook capture"
}

// responseText finds the review text in the payload. A structured response is
// re-encoded as JSON rather than flattened, so a findings payload reaches the
// findings parser intact.
//
// For a reporting tool the review is the call's *input*, so tool_input is read
// first; for every other tool the input is the request, not the review, and is
// never read — capturing it would record what someone asked for as what the
// reviewer said.
func responseText(doc map[string]interface{}, reporting bool) string {
	if reporting {
		if text := valueText(doc["tool_input"], 0); strings.TrimSpace(text) != "" {
			return text
		}
	}
	for _, key := range responseKeys {
		v, present := doc[key]
		if !present {
			continue
		}
		if text := valueText(v, 0); strings.TrimSpace(text) != "" {
			return text
		}
	}
	return ""
}

// valueText renders one payload value as the review text. depth bounds the
// recursion so a self-referential decoded document cannot spin.
func valueText(v interface{}, depth int) string {
	if depth > 4 {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case []interface{}:
		// A content-block array ([{type: "text", text: "..."}]) is joined; any
		// other array is structured data the parsers should see whole.
		var parts []string
		blocks := len(t) > 0
		for _, e := range t {
			m, isMap := e.(map[string]interface{})
			if !isMap {
				blocks = false
				break
			}
			text, hasText := m["text"].(string)
			if !hasText {
				blocks = false
				break
			}
			parts = append(parts, text)
		}
		if blocks && len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
		// Scrubbed like a map is: eslint's native shape is an array, so a
		// response that decodes to one reached the artifact unscrubbed and a
		// credential in it was written straight into the repository.
		return marshalText(scrub(v, 0))
	case map[string]interface{}:
		// One pass over everything in this map that could be the review, then
		// the best of it, in a fixed order of preference. Returning the first
		// non-empty candidate instead let whichever key happened to be checked
		// first win, and the loser was sometimes the actual review:
		//
		//   - A report beats prose wherever it sits, so a runner that adds
		//     `"message":"Command exited with code 1"` beside a linter's JSON
		//     does not have its own bookkeeping recorded as the review.
		//   - A report with findings beats one without. An empty
		//     `{"results":[]}` on stderr used to shadow a prose write-up on
		//     stdout, and FromHook's clean-run guard then dropped the empty
		//     report — losing the review entirely.
		//   - Prose that reads as a review — one with sections — beats prose
		//     that does not, for the same reason: `"output":"Command exited
		//     with code 1"` is not the write-up under `"body"`.
		//   - Prose on stderr is not a candidate at all. A usage error and a
		//     write-up share that stream and nothing tells them apart; mining
		//     standing conventions out of a crash message is the worse
		//     mistake.
		var report, prose, aside, empty string
		consider := func(text string, allowProse bool) {
			if strings.TrimSpace(text) == "" {
				return
			}
			format := Detect([]byte(text))
			if format == FormatMarkdown {
				// Checked before parsing: prose on stderr is discarded
				// whatever it holds, and parsing it first spent a full
				// markdown scan on a candidate that was never eligible.
				if !allowProse {
					return
				}
				// A write-up has sections; a runner's status line does not.
				if findings, _ := parse(format, []byte(text)); len(findings) > 0 {
					if prose == "" {
						prose = text
					}
					return
				}
				// Structureless prose is still a review — a reviewer may say
				// "the auth package looks right" and nothing more. It ranks
				// last, so it cannot outrank a write-up or a report; the cost
				// is that a runner's own status line, when it is the only
				// thing in the response, is recorded as a short artifact with
				// no findings. That is cache clutter, and the alternative —
				// a length or shape floor — throws away real short reviews.
				if aside == "" {
					aside = text
				}
				return
			}
			if findings, err := parse(format, []byte(text)); err == nil && len(findings) > 0 {
				if report == "" {
					report = text
				}
			} else if empty == "" {
				empty = text
			}
		}
		for _, key := range responseTextKeys {
			inner, present := t[key]
			if !present {
				continue
			}
			consider(valueText(inner, depth+1), key != "stderr")
			if report != "" {
				break
			}
		}
		// The map itself may be the report rather than an envelope around one.
		// It is offered stripped of the fields already considered above and
		// only when it carries none of the runner's own bookkeeping: handing
		// the whole object over wrote a `command` field — `SEMGREP_APP_TOKEN=…
		// semgrep --json .` — into an artifact inside the repository, and put
		// stderr prose into the grounding corpus two lines after ruling it out
		// as review text.
		if report == "" {
			// A map carrying a `findings` key is a review payload by
			// construction, even when its vocabulary is one the findings
			// parser does not know — Snyk, Trivy and Security Hub all use the
			// key with fields of their own. Refusing it as prose recorded
			// nothing at all for a designated run of any of them.
			_, named := t["findings"]
			consider(reportPayload(t), named)
		}

		switch {
		case report != "":
			return report
		case prose != "":
			return prose
		case aside != "":
			return aside
		default:
			// An empty structured report is still what the tool said. It
			// reaches FromHook's clean-run guard, which is where "the tool
			// found nothing" is decided — not here.
			return empty
		}
	}
	return ""
}

func marshalText(v interface{}) string {
	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	// An empty container carries no review; treat it as nothing found so the
	// caller falls through to the next candidate field.
	if s := string(data); s != "{}" && s != "[]" && s != "null" {
		return s
	}
	return ""
}

// invocationKeys carry what was run and with what — a command line, an
// argument vector, an environment. They are stripped at every level, because
// that is where credentials live and one level is not where nesting stops:
// `{"results":[…],"metadata":{"command":"SEMGREP_APP_TOKEN=… semgrep"}}` put a
// token into an artifact inside the repository just as surely as a top-level
// `command` did. stderr is here for a different reason: valueText rules stderr
// prose out as review text, and carrying it in the payload instead would put a
// crash message into the grounding corpus by the back door.
var invocationKeys = map[string]bool{
	"command": true, "cmd": true, "argv": true, "args": true,
	"env": true, "environment": true, "tool_input": true, "stderr": true,
}

// envelopeKeys are what a tool runner wraps a result in: where it ran and how
// it ended. They are stripped only at the top level, because the same words
// are report content one level down — `description` is the advisory text in
// Snyk, Grype and Checkov findings, and `file_path` is a finding's location.
// Stripping them everywhere deleted the reviewer's own prose from the corpus.
var envelopeKeys = map[string]bool{
	"cwd": true, "file_path": true, "description": true, "input": true,
	"tool_name": true, "tool_use_id": true, "is_error": true,
	"interrupted": true, "exit_code": true, "exitCode": true, "sandbox": true,
}

// reportPayload renders a decoded response as a candidate review: the map with
// the runner's bookkeeping removed, and without the fields already considered
// on their own.
//
// Removing rather than refusing. Bailing out on any bookkeeping key threw away
// a real report that happened to sit beside an `exit_code` — and a linter
// exits non-zero precisely when it has findings, so that is the ordinary
// shape, not the exception.
//
// Removing at every level, because one level is not where credentials stop:
// `{"results":[…],"metadata":{"command":"SEMGREP_APP_TOKEN=… semgrep"}}` put
// the token into an artifact inside the repository just as surely as a
// top-level `command` did.
func reportPayload(t map[string]interface{}) string {
	rest := make(map[string]interface{}, len(t))
	for key, val := range t {
		if envelopeKeys[key] || invocationKeys[key] || isResponseTextKey(key) {
			continue
		}
		rest[key] = scrub(val, 0)
	}
	return marshalText(rest)
}

// scrub removes the invocation fields from a decoded value at every level,
// returning the value itself when there was nothing to remove — which is the
// overwhelmingly common case, and rebuilding every container regardless meant
// a second full copy of a document that can be megabytes, on a path that runs
// after every matching tool call.
//
// depth only stops a runaway. Past it the value is passed through unchanged
// rather than dropped: a bound that nils out what it cannot reach corrupted
// ordinary reports — a SARIF result's location and snippet sit deeper than a
// small bound allows — and handed the corruption on as the grounding corpus.
func scrub(v interface{}, depth int) interface{} {
	if depth > 64 {
		return v
	}
	switch t := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		changed := false
		for key, val := range t {
			if invocationKeys[key] {
				changed = true
				continue
			}
			scrubbed := scrub(val, depth+1)
			// Compared by identity, not by value: a container scrub left alone
			// comes back as the same reference.
			if !sameValue(scrubbed, val) {
				changed = true
			}
			out[key] = scrubbed
		}
		if !changed {
			return v
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		changed := false
		for i, e := range t {
			out[i] = scrub(e, depth+1)
			if !sameValue(out[i], e) {
				changed = true
			}
		}
		if !changed {
			return v
		}
		return out
	}
	return v
}

// sameValue reports whether scrub returned its argument untouched. Only the
// container cases can return something new, and for those a pointer comparison
// through reflect is exact; everything else is returned as-is by definition.
func sameValue(scrubbed, original interface{}) bool {
	switch original.(type) {
	case map[string]interface{}, []interface{}:
		return reflect.ValueOf(scrubbed).Pointer() == reflect.ValueOf(original).Pointer()
	}
	return true
}

func isResponseTextKey(key string) bool {
	for _, known := range responseTextKeys {
		if key == known {
			return true
		}
	}
	return false
}
