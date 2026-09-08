package review

import (
	"bytes"
	"encoding/json"
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

// textKeys are the fields that may carry text inside a structured response.
var textKeys = []string{"content", "output", "stdout", "text", "body", "message"}

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
	if !isReporting && reportingSkills[bareSkillName(skill)] {
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
		return marshalText(v)
	case map[string]interface{}:
		// A findings payload is data, not prose — hand it over whole so the
		// findings parser can read it rather than digging out a text field.
		if _, ok := t["findings"]; ok {
			return marshalText(v)
		}
		// A response envelope that carries text fields has already said what it
		// has: if they are all empty the tool produced no output, and falling
		// through to marshal the envelope would record `{"stdout":"", …}` as
		// the reviewer's words.
		envelope := false
		for _, key := range textKeys {
			inner, present := t[key]
			if !present {
				continue
			}
			envelope = true
			if text := valueText(inner, depth+1); strings.TrimSpace(text) != "" {
				return text
			}
		}
		if envelope {
			return ""
		}
		return marshalText(v)
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
