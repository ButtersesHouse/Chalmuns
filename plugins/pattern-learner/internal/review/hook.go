package review

import (
	"encoding/json"
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

	matched := Match(watchers, toolName, skill, command)
	if matched == nil && isReporting {
		// A reporting tool's payload *is* review output, but it need not name
		// the skill that produced it. When the user watches a review skill
		// under some other name, attribute it to that designation rather than
		// discarding a review they asked to be kept eyes on.
		matched = firstSkillWatcher(watchers)
	}
	if matched == nil {
		return Artifact{}, state.Watcher{}, false
	}

	text := responseText(doc, isReporting)
	if strings.TrimSpace(text) == "" {
		return Artifact{}, state.Watcher{}, false
	}

	art, err := Capture(Input{
		Data:   []byte(text),
		Source: matched.Name,
		Format: matched.Format,
		Label:  hookLabel(toolName, skill, command),
		Now:    now,
	})
	if err != nil {
		return Artifact{}, state.Watcher{}, false
	}
	return art, *matched, true
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

// firstSkillWatcher returns the first designation that can own a skill's
// output, or nil when only command-line tools are watched.
func firstSkillWatcher(ws []state.Watcher) *state.Watcher {
	for i := range ws {
		if ws[i].Kind != KindTool {
			return &ws[i]
		}
	}
	return nil
}

// PayloadCWD reads the working directory a hook payload reports, so a capture
// can find the project when CLAUDE_PROJECT_DIR is not exported — the same
// fallback internal/guard applies, for the same reason.
func PayloadCWD(payload []byte) string {
	var doc struct {
		CWD string `json:"cwd"`
	}
	if err := json.Unmarshal(payload, &doc); err != nil {
		return ""
	}
	return doc.CWD
}

// hookLabel records what produced the artifact, for the approval display.
func hookLabel(toolName, skill, command string) string {
	switch {
	case skill != "":
		return "hook capture: /" + strings.TrimPrefix(skill, "/")
	case command != "":
		return "hook capture: " + truncateLabel(command)
	case toolName != "":
		return "hook capture: " + toolName
	}
	return "hook capture"
}

func truncateLabel(s string) string {
	const max = 120
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len([]rune(s)) > max {
		return string([]rune(s)[:max]) + "…"
	}
	return s
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
		for _, key := range textKeys {
			if inner, present := t[key]; present {
				if text := valueText(inner, depth+1); strings.TrimSpace(text) != "" {
					return text
				}
			}
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
