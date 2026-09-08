package review

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode"

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

	// Credentials are stripped by Capture, which every path into an artifact
	// goes through. Doing it here as well cost a second pass over text that can
	// be megabytes, and left the other paths unprotected when this one was the
	// only place anybody thought to look.
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
	// Prose is exempt, but only when there is prose. A response that is just
	// the runner's account of the call — `{"cwd":"/home/me/client-project",
	// "exit_code":0}` — sniffs as markdown because no parser claims it, so
	// exempting every markdown capture wrote one content-free artifact per run
	// into the repository forever, private paths included.
	if len(art.Findings) == 0 && (art.Format != FormatMarkdown || isOnlyBookkeeping(text)) {
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
		considerMap := func(text string) {
			if strings.TrimSpace(text) == "" {
				return
			}
			format := Detect([]byte(text))
			if format != FormatMarkdown {
				consider(text, false)
				return
			}
			// Prose only as a last resort. A runner key this package does not
			// know — `{"meta":{"host":"ci-runner-7"}}` — would otherwise
			// outrank the tool's own empty report and be recorded instead of
			// it.
			if aside == "" {
				aside = text
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
			// Offered as prose as well as as a report. A decoded payload may
			// use a vocabulary the parsers do not know — Snyk, Trivy and
			// Security Hub all spell findings their own way, and one may sit
			// a level down under a key of the runner's choosing — and
			// refusing all of that recorded nothing at all for a designated
			// run of any of them. What is left after the runner's own fields
			// are dropped is either a report or nothing; nothing marshals to
			// "" and is not considered, and isOnlyBookkeeping backstops the
			// rest at the end of FromHook.
			considerMap(reportPayload(t))
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

// redactSecrets removes credentials from review text, in place, leaving every
// other byte exactly as the reviewer wrote it.
//
// It works on the text rather than on a decoded document, and that is the
// whole point. A structural scrub — delete the fields that hold an invocation
// — has to be right about the shape of every payload it will ever see, and it
// never was: a report truncated mid-document was skipped entirely, an
// invocation log carried as a string field inside the report was invisible to
// it, an argv under one key was stripped while the same array under another
// was not, and re-encoding what it kept reordered keys, escaped angle brackets
// and rewrote large integers in text this package documents as byte-for-byte.
// A pattern over the text has none of those failure modes: JSON, JSON Lines, a
// truncated document, prose quoting a config, a nested log — all the same.
//
// It has the opposite risk, and the patterns are written against it: matching
// the reviewer's own words. A rule that fired on any word containing "token"
// followed by a colon rewrote `- The credential: check is missing` into a
// finding that said something else, in the text verify-grounding matches a
// signal's quote against. So a name is only a credential's name where the
// syntax says it is holding a value — `NAME=`, or a quoted JSON key — and a
// value that is a container or a number is left alone, which also keeps the
// document valid for the parser that reads it next.
//
// What it catches is a list, and a list is never complete; it is a backstop,
// not a guarantee, which is why the label a capture records still names the
// reviewer and never the command line.
func redactSecrets(text string) string {
	// One scan to decide whether any pattern can match. Each replacement below
	// copies the whole text whether or not it changes anything, and this runs
	// after every matching tool call, on output that can be megabytes.
	if !reSecretHint.MatchString(text) {
		return text
	}
	// A JSON document answers the question the patterns below have to guess
	// at. See redactJSON.
	if out, ok := redactJSON(text); ok {
		return out
	}
	return redactText(text)
}

// redactText is the pattern pass: what runs on text that is not a JSON
// document, and on the individual string values inside one.
func redactText(text string) string {
	// Each pass writes a sentinel the value classes cannot match, so a later
	// pattern cannot consume part of an earlier replacement: `--token=x` was
	// rewritten by the assignment rule and then again by the flag rule, which
	// ate all but the closing bracket and left `[redacted]]` behind.
	//
	// The order is load-bearing for the same reason. reBareSecret's token
	// classes run to the end of a word, so letting it go first swallowed a
	// following `NAME=` whole and the assignment rule never saw it.
	//
	// Each rule sees the whole text, and the cost is real — about 1.3 seconds
	// per megabyte, inside someone else's tool call. Confining the
	// name-and-value rules to the lines carrying a secret-ish word looked like
	// the fix and measured worse on every report shape but one: review prose
	// says "token" and "auth" on nearly every line, so the windows saved almost
	// no bytes while multiplying the per-call cost of twelve regexes by the
	// line count. What bounds this in practice is maxArtifactBytes.
	for _, re := range secretPatterns {
		text = re.ReplaceAllString(text, "${1}"+redactedSentinel+"${2}")
	}
	text = reBareSecret.ReplaceAllString(text, redactedSentinel)
	text = redactColonForm(text)
	return strings.ReplaceAll(text, redactedSentinel, redactedMarker)
}

// redactedSentinel stands in for a credential while the passes run. It holds
// no character any value class matches, so a replacement cannot be re-matched;
// redactedMarker is what the reader sees.
const (
	redactedSentinel = "\x00redacted\x00"
	redactedMarker   = "[redacted]"
)

// redactJSON redacts a response that is a JSON document, and reports whether
// it was one.
//
// Inside a document the ambiguity the patterns below spend four heuristics on
// does not exist. A `password` *key's* value is a credential because the key
// says so; a `message` key's value is the reviewer's prose because that key
// says so too. Nothing has to be inferred from where the text sits on a line.
//
// What is left ambiguous is a config line a tool echoed into a string —
// semgrep's `extra.lines` — and that is where the patterns still run. They run
// per string value rather than over the document, which is what stops a match
// from crossing out of one finding into the next, from consuming the quote
// that ends a string, and from leaving the document unparseable. Every one of
// those was a defect this file has carried.
//
// A truncated or non-JSON response — which is common, and is why the patterns
// exist at all — is reported as not-JSON and falls through to them.
func redactJSON(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return "", false
	}
	var doc interface{}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	// Numbers keep the spelling the tool wrote: a line number past 2^53
	// re-encoded through float64 came back changed, in text this package
	// documents as byte-for-byte.
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return "", false
	}
	// The *whole* text, or none of it. A decoder stops at the first value, so
	// accepting one silently threw away everything after it: a JSON Lines
	// response, or a report with a banner before it, came back as its first
	// object with every finding gone.
	if dec.More() || strings.TrimSpace(trimmed[dec.InputOffset():]) != "" {
		return "", false
	}

	var scrub jsonScrub
	redacted, changed := scrub.value(doc, "", 0)
	if scrub.tooDeep {
		// The walk stopped short of the bottom, so this pass cannot claim to
		// have scrubbed the document. Reporting it as not-JSON hands the whole
		// text to the patterns, which have no depth to run out of — where
		// replacing the subtree destroyed whatever the tool had nested there,
		// and passing it through left a credential behind it unscrubbed.
		return "", false
	}
	if !changed {
		// Nothing to remove, so nothing is rewritten. Re-encoding reorders
		// keys, compacts the tool's own formatting and rewrites escapes and
		// invalid UTF-8 — in text this package documents as byte-for-byte, and
		// which verify-grounding matches a signal's quote against.
		return text, true
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// Without this a reviewer's `a < b && c > d` comes back as escape
	// sequences, and a signal quoting it fails grounding.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(redacted); err != nil {
		return "", false
	}
	return strings.TrimRight(buf.String(), "\n"), true
}

// jsonScrub walks a decoded document. It carries one bit of state: whether the
// walk ran out of depth before reaching the bottom, which makes the whole
// structural pass inconclusive rather than partially applied.
type jsonScrub struct{ tooDeep bool }

// maxScrubDepth bounds the walk. A review report nests a handful of levels; a
// thousand is far enough past that the fallback is reachable only by something
// built to reach it.
const maxScrubDepth = 1000

// value rewrites one decoded value, reporting whether it changed anything. key
// is the field it was found under, which is what decides whether a string is a
// credential or the review.
func (s *jsonScrub) value(v interface{}, key string, depth int) (interface{}, bool) {
	if depth > maxScrubDepth {
		// Recorded rather than acted on: replacing the subtree destroyed
		// whatever was nested there and changed its type, and passing it
		// through is a hole a credential can be nested behind. redactJSON
		// abandons the pass instead, and the patterns take the whole text.
		//
		// Falling back costs the rules that only exist here — the key-aware
		// ones — so the bound is set where no report reaches it rather than
		// where recursion gets uncomfortable. The document has already been
		// decoded by the time this walks it, at the same depth, so anything
		// this can be handed is something the standard decoder just walked.
		s.tooDeep = true
		return v, false
	}
	switch t := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		changed := false
		for k, val := range t {
			// A key can be the credential itself — a tool that counts what it
			// found keys the map by each secret.
			outKey := redactSecretsInValue(k)
			if outKey != k {
				changed = true
			}
			redacted, hit := s.value(val, k, depth+1)
			changed = changed || hit
			out[outKey] = redacted
		}
		if !changed {
			return v, false
		}
		return out, true
	case []interface{}:
		out := make([]interface{}, len(t))
		changed := false
		for i, e := range t {
			// An element does not inherit its field's name outright, and does
			// not lose it either. A `secrets` key on a summary object usually
			// names which secret *rules* fired, and redacting every element of
			// it made the review unreadable — while `"passwords":["a8f3d9e2"]`
			// really is a list of credentials, and dropping the key committed
			// them. So the name is carried in and the element is taken on its
			// own shape: an opaque token is a credential, a rule name is not.
			//
			// An element that is not opaque still goes through the patterns
			// below. Short-circuiting it here — the key and the shape have
			// spoken, let the value be — lost every credential embedded in a
			// longer string: `["Hardcoded key AKIA… in app.py"]` is what a
			// scanner's list of findings actually looks like.
			if str, ok := e.(string); ok && reSecretKey.MatchString(key) && looksOpaque(str) {
				out[i], changed = redactedMarker, true
				continue
			}
			// A nested array is the same list one level down, so it keeps the
			// name; anything else is a value of its own and starts afresh.
			inner := ""
			if _, ok := e.([]interface{}); ok {
				inner = key
			}
			redacted, hit := s.value(e, inner, depth+1)
			changed = changed || hit
			out[i] = redacted
		}
		if !changed {
			return v, false
		}
		return out, true
	case string:
		if reSecretKey.MatchString(key) {
			return redactedMarker, true
		}
		if out := redactSecretsInValue(t); out != t {
			return out, true
		}
		return t, false
	}
	return v, false
}

// looksOpaque reports whether a string could be nothing but a credential: one
// token of some length, carrying a digit, and not a path the reviewer cited.
// It is what an array element under a secret-ish name is judged on, where the
// key alone would redact a list of rule names — the same "could be nothing
// else" test the prose branch of isAssignedCredential applies, for the same
// reason.
func looksOpaque(s string) bool {
	return len([]rune(s)) >= 8 &&
		!strings.ContainsAny(s, " \t\n") &&
		strings.ContainsAny(s, "0123456789") &&
		!isFileReference(s) &&
		// A list under such a key holds names as often as values: a scanner's
		// rule ids (`gitleaks:generic-api-key:v8.18.0`), the *names* of the
		// variables it found (`OAUTH2_CLIENT_ID`), an endpoint. Destroying
		// those loses the finding, and every one of them names something.
		!namesSomething(s)
}

// reSecretKey matches a field name that says its value is a credential.
//
// `auth` has to end a name segment, as it does in secretPatterns and for the
// same reason: as a substring it matches `author`, `authors`, `authenticated`
// and `unauthorized`, and replacing those values deleted the reviewer's own
// text — a commit author, a rule's metadata — from the artifact.
var reSecretKey = regexp.MustCompile(`(?i)^["']?(?:[A-Za-z0-9_.-]*` + secretName +
	`[A-Za-z0-9_.-]*|authorization|[A-Za-z0-9]*auth(?:[_-][A-Za-z0-9]+)*)["']?$`)

// redactSecretsInValue runs the text patterns over one string value, where a
// match cannot reach anything outside it.
func redactSecretsInValue(text string) string {
	if !reSecretHint.MatchString(text) {
		return text
	}
	return redactText(text)
}

// secretName is the part of a field name that says what the field holds. auth
// is here only for the `=` form, where the syntax leaves no doubt; as a bare
// word before a colon it is ordinary English.
const secretName = `(?:token|secret|password|passwd|api[_-]?key|access[_-]?key|private[_-]?key|credential)`

// reSecretHint is the cheap pre-filter: if none of these appears, no pattern
// below can match, so it has to be a superset of all of them: a hint narrower
// than a pattern silently switches that pattern off, which is how `X_AUTH_HDR=`
// went unredacted while the corpus row for `auth=` passed. Each replacement
// copies the whole text whether or not it changes anything, and this runs
// after every matching tool call, on output that can be megabytes.
var reSecretHint = regexp.MustCompile(`(?i)` + secretName +
	`|auth|\bsk-|\bghp_|\bgho_|\bgithub_pat_|\bxox|\bAKIA|\bAIza|BEGIN [A-Z ]*PRIVATE KEY` +
	`|` + cryptPrefix)

// quotedValue matches a JSON or shell string body, consuming `\"` so a quote
// escaped inside the value does not end the match early and leave a dangling
// one behind — which turned the document invalid, and a watcher pinned to a
// format then lost the whole review.
// A value stops at its delimiter, escaped or not, and never crosses a line.
// `\\.` consuming a `\"` ran the match past the credential to the end of the
// enclosing JSON string and deleted the report content in between; matching a
// newline let one unbalanced quote after a secret-ish name delete every
// finding up to the next quote in the review.
const quotedValue = `(?:[^"\\\n]|\\[^"\n])*`

// singleQuotedValue is the same for a single-quoted value. It excludes the
// newline for the reason quotedValue does, which the single-quote rules were
// missing: one unbalanced `'` after a secret-ish name — an apostrophe in the
// reviewer's prose is enough — let the value run to the next quote anywhere
// below and deleted every finding in between.
const singleQuotedValue = `[^'\n]*`

// escapedQuote matches the quote delimiter as it arrives in hook text, which
// is usually JSON: a review's own `"` reaches this package as `\"`, and rules
// that required a literal one missed every credential inside a nested string —
// semgrep's `extra.lines` echoing the offending source line, for one.
const escapedQuote = `\\?"`

// Each pattern keeps group 1 (the name and the syntax that introduces the
// value) and group 2 (the closing quote, where there is one), replacing only
// what lies between them. `[ \t]*` rather than `\s*` throughout: a rule that
// crossed newlines matched a quoted phrase two lines below a secret-ish word
// and rewrote the reviewer's finding.
var secretPatterns = []*regexp.Regexp{
	// An Authorization header, whose value is the credential itself. First,
	// because the name rules below would stop at the scheme word and leave the
	// credential after it. The name may be quoted, as it is in a JSON object.
	regexp.MustCompile(`(?i)(["']?authorization["']?[ \t]*[:=][ \t]*")(?:bearer |basic |token )?` + quotedValue + `(")`),
	// Unquoted, the scheme word is required: without it, `- Authorization: the
	// middleware is skipped` is a sentence, and redacting its next word
	// rewrote the reviewer's finding into a different one.
	regexp.MustCompile(`(?i)(\bauthorization[ \t]*[:=][ \t]*(?:bearer|basic|token) )[^\s"',;)\]}]+()`),
	// A quoted value, whose end is its closing quote rather than the first
	// space: `MY_PASSWORD='p@ss w0rd'` leaked everything after the space.
	//
	// Where the name is itself quoted, or joined to the value by `=`, the
	// syntax is unambiguous and the value is taken as-is. A *bare* name before
	// a colon is not: `- The credential: "auth.go" is missing` is a sentence,
	// so there the value has to look like a credential too.
	regexp.MustCompile(`(?i)(` + escapedQuote + `[A-Za-z0-9_.-]*` + secretName + `[A-Za-z0-9_.-]*` + escapedQuote + `[ \t]*[:=][ \t]*` + escapedQuote + `)` + quotedValue + `(` + escapedQuote + `)`),
	regexp.MustCompile(`(?i)(["'][A-Za-z0-9_.-]*` + secretName + `[A-Za-z0-9_.-]*["'][ \t]*[:=][ \t]*')` + singleQuotedValue + `(')`),
	regexp.MustCompile(`(?i)(\b[A-Za-z0-9_.-]*` + secretName + `[A-Za-z0-9_.-]*[ \t]*=[ \t]*` + escapedQuote + `)` + quotedValue + `(` + escapedQuote + `)`),
	regexp.MustCompile(`(?i)(\b[A-Za-z0-9_.-]*` + secretName + `[A-Za-z0-9_.-]*[ \t]*=[ \t]*')` + singleQuotedValue + `(')`),
	// An unquoted shell assignment. The `=` is what marks it as one, so `auth`
	// is safe to name here.
	regexp.MustCompile(`(?i)(\b[A-Za-z0-9_.-]*` + secretName + `[A-Za-z0-9_.-]*=)[^\s"',;)\]}]+()`),
	// `auth` on its own is a word that appears inside ordinary identifiers, so
	// it has to end a name segment: `OAUTH=` and `X_AUTH_HDR=` are variables
	// holding a credential, while `IsAuthenticated=true` and `authorized=true`
	// are a reviewer describing code, and rewriting those rewrote the finding.
	regexp.MustCompile(`(?i)((?:^|[^A-Za-z0-9])[A-Za-z0-9]*auth(?:[_-][A-Za-z0-9]+)*=)[^\s"',;)\]}]+()`),
	// A flag naming the value that follows it.
	regexp.MustCompile(`(?i)(--?(?:token|password|secret|api[_-]?key|access[_-]?key)[= ])[^\s"',;)\]}]+()`),
}

// The `name: value` shape — how YAML, an env dump and a header dump all spell
// it, in each of its quotings. Group 1 is the name and its delimiter, group 2
// the value, group 3 the closing delimiter. Only group 2 is replaced.
//
// The value classes are bounded to a line and to their own quote, and exclude
// the backslash. A class that fell back to "anything but a quote" ran to the
// end of the review whenever the next character was a newline, taking every
// finding after it; one that swallowed a trailing `\` ate the escape of the
// `\"` that ended the enclosing JSON string and left the document invalid.
// bareValue is an unquoted value. It stops at a markdown delimiter as well as
// at shell and JSON punctuation: replacing a span that had swallowed the
// closing backtick of “ `api_key: abcdef` “ left the rest of the write-up
// rendering as code.
//
// The two closing brackets it does take are an index, `[0]`, and an expansion,
// `${VAR:-default}` — and inside the expansion it excludes exactly what it
// excludes outside one, or an unclosed `${` ran to a brace further down the
// line and ate the quote that ended the enclosing JSON string. Both are taken
// because a reference — `creds[0].password` — and stopping at the
// bracket left `creds[0` to be judged on its own, which reads as a credential
// and rewrote the reference into `[redacted]].password`. Both alternatives come
// before the general class: the engine takes the first branch that lets the
// whole pattern succeed, and the general class matching a lone `[` or `$` was
// one of them.
const bareValue = "((?:\\$\\{[^\\s\"',;)\\]}\\\\]*\\}|\\[[0-9]+\\]|[^\\s\"',;)\\]}\\\\])+)"

var colonForms = []*regexp.Regexp{
	regexp.MustCompile(`(?i)([A-Za-z0-9_.-]*` + secretName + `[A-Za-z0-9_.-]*[ \t]*:[ \t]*` + escapedQuote + `)(` + quotedValue + `)(` + escapedQuote + `)`),
	regexp.MustCompile(`(?i)([A-Za-z0-9_.-]*` + secretName + `[A-Za-z0-9_.-]*[ \t]*:[ \t]*')([^'\n]*)(')`),
	regexp.MustCompile(`(?i)([A-Za-z0-9_.-]*` + secretName + `[A-Za-z0-9_.-]*[ \t]*:[ \t]*)` + bareValue + `()`),
}

// redactColonForm decides each `name: value` from three things: what precedes
// the name in its line or string, what the value looks like, and what follows
// it. No one of them settles it, and four rounds of trying to make one of them
// settle it alone oscillated between leaking credentials and rewriting the
// reviewer's own words:
//
//   - The value alone cannot tell `password: "p@ss w0rd"` from `- The
//     credential: "auth.go" is missing`.
//   - The surroundings alone leak `Using api_key: abcdef123456`, and every
//     credential that is not the last thing on its line.
//
// Together they are decidable. A name that *begins* its line or string is an
// assignment, and its value is taken as written; a name with words before it
// is a sentence, and only a value that could be nothing but a credential is
// taken from it. Either way a file the reviewer cited is left alone, and words
// after the value mean the line was prose all along.
func redactColonForm(text string) string {
	for _, re := range colonForms {
		locs := re.FindAllStringSubmatchIndex(text, -1)
		if locs == nil {
			continue
		}
		var b strings.Builder
		b.Grow(len(text))
		last := 0
		for _, loc := range locs {
			if !isAssignedCredential(text, loc) {
				continue
			}
			// A closing `` ` `` or `**` belongs to the write-up, not to the
			// value: swallowing it left the rest of the review rendering as
			// code. Only the value itself is replaced.
			b.WriteString(text[last:loc[4]])
			b.WriteString(redactedSentinel)
			last = loc[4] + len(strings.TrimRight(text[loc[4]:loc[5]], "`*_"))
		}
		if last == 0 {
			continue
		}
		b.WriteString(text[last:])
		text = b.String()
	}
	return text
}

// isAssignedCredential judges one match in its context. loc is a
// FindAllStringSubmatchIndex entry: loc[0:2] is the whole match, loc[4:6] the
// value.
func isAssignedCredential(text string, loc []int) bool {
	value := strings.TrimRight(text[loc[4]:loc[5]], "`*_")
	rest := text[loc[4]+len(value):]
	if len([]rune(strings.TrimSpace(value))) < 6 {
		// `token: yes` is a sentence.
		return false
	}
	if isFileReference(value) {
		// A path the reviewer cited. Rewriting it makes the finding name a
		// file that does not exist, in the text grounding checks against.
		return false
	}
	if isIndirection(value) {
		// A reference standing where the credential would be. This is the one
		// value test that outranks the assignment syntax below, because
		// `password: $DB_PASSWORD` is the commonest line in a scanned config
		// and there is no reading of it that holds a secret.
		//
		// It is deliberately this narrow. Letting the whole name test outrank
		// the syntax was tried, and it excused `password: myPassword123`,
		// `password: sup3rS3cret` and a scanner echoing `lines: "  password:
		// dbHunter2pass"` — which is the module's primary input shape. Where
		// the line says it is an assignment, the assignment wins.
		return false
	}
	if beginsItsUnit(text, loc[0]) {
		// An assignment. What follows is an annotation — `# rotate this`,
		// `(line 4)`, the punctuation closing a field — unless it is a word,
		// which means the line was a sentence that happened to start with a
		// secret-ish name.
		return !startsWithWord(rest)
	}
	// Inside a markdown span, the sentence around it says nothing about the
	// value: `- **DB_PASSWORD: hunter2trustno1** is committed in
	// docker-compose.yml` is a reviewer quoting an assignment and then
	// describing it, and reading "is committed" as words after the value left
	// the credential in the artifact.
	//
	// Dropping those words leaves the value to settle it alone, so a value that
	// names something is let go first — here, below the assignment branch,
	// where the surroundings really have left the judgement to the value.
	if end := markdownSpanEnd(text, loc[0]); end >= loc[4]+len(value) {
		rest = text[loc[4]+len(value) : end]
	}
	if namesSomething(value) {
		// `cfg.OAuth2Token`, `SHA256_DIGEST`, `sessionToken2`, a scanner's own
		// rule id. The same test the array branch uses, so a value is judged
		// the same way wherever the surroundings leave the judgement to it.
		return false
	}
	// A name with words before it is prose. Only a value that could be
	// nothing else is taken from it: one token, carrying a digit — an
	// identifier the reviewer named (`sessionToken`, `change_me_now`) does
	// not — and with nothing but punctuation after it on the line.
	return !strings.ContainsAny(value, " \t\n") &&
		strings.ContainsAny(value, "0123456789") &&
		!followedByWords(rest)
}

// isIndirection reports whether a value points at a credential rather than
// being one: a shell or template expansion, an environment lookup, or a
// reference to the field that holds it. These are the only shapes allowed to
// overrule a line that spells out an assignment, because they are unambiguous —
// nothing spells a literal secret `$DB_PASSWORD` — and because a config that
// does this right is the config a scanner flags least and quotes most.
func isIndirection(value string) bool {
	if isVarExpansion(value) || strings.HasPrefix(value, "{{") ||
		(strings.HasPrefix(value, "<") && strings.HasSuffix(value, ">")) ||
		reEnvLookup.MatchString(value) {
		return true
	}
	// A qualified reference whose last piece names the credential:
	// `cfg.DBPassword`, `settings.API_KEY`, `dbConfig.password`. That last
	// piece is what says the value points at a secret rather than being one —
	// a literal does not end in the word "password". This is the shape a
	// scanner echoes out of source in `extra.lines`, which is the commonest
	// text this package sees.
	//
	// The pieces in front of it have to look like a container, because this
	// clause outranks the assignment syntax and testing the tail alone was
	// weaker than the name test it overrules: `password:
	// hunter2trustno1.password` and `api_key: wJalrXUtnFEM….secret` both read
	// as references to it.
	i := strings.LastIndexAny(value, ".[")
	if i <= 0 {
		return false
	}
	last := strings.Trim(value[i+1:], `[]"'`)
	if last == "" || !reSecretKey.MatchString(last) {
		return false
	}
	for _, piece := range strings.FieldsFunc(value[:i], func(r rune) bool {
		return r == '.' || r == '[' || r == ']'
	}) {
		if !isContainerPiece(piece) {
			return false
		}
	}
	return true
}

// isContainerPiece reports whether a piece names the thing a credential is
// reached through: `cfg`, `settings`, `dbConfig`, `process`, `env`. Those are
// short and word-shaped; `hunter2trustno1` is neither.
func isContainerPiece(piece string) bool {
	if piece == "" || len([]rune(piece)) >= maxContainerRunes {
		return false
	}
	// An index into a collection of them: `creds[0].password`. Bounded, or a
	// nineteen-digit payload reads as one.
	if strings.IndexFunc(piece, func(r rune) bool { return !unicode.IsDigit(r) }) < 0 {
		return len([]rune(piece)) <= maxIndexDigits
	}
	if !strings.ContainsAny(piece, "0123456789") {
		return true
	}
	// A piece carrying digits keeps the tighter bound. The names that needed
	// the longer one — `databaseConfig`, `serviceAccount`, `connectionSettings`
	// — are all digit-free, and hasWordBreak was already excusing a camelCase
	// piece, so widening this side let the corpus's own canonical credential
	// walk out the moment `.password` was appended to it.
	return hasWordBreak(piece) && len([]rune(piece)) < maxDigitPieceRunes
}

// isVarExpansion reports whether a value is a variable being expanded rather
// than a literal that opens with a dollar sign. The bare `$` prefix excused
// every crypt-format hash there is — `$2b$12$…`, `$1$salt$…`, `$argon2id$…` —
// which are exactly what a `password_hash` field holds, and `$Tr0ub4dor3xK`
// besides. What this accepts is what a shell, a compose file or PowerShell
// actually writes, defaults included.
func isVarExpansion(value string) bool {
	if !strings.HasPrefix(value, "$") {
		// The sequence has to *be* the value, not end it. Without this a
		// digit-free literal with any variable appended walked out whole:
		// `password: changeme${DB}`, `correcthorsebatterystaple$FOO`.
		return false
	}
	rest, saw, bare := value, false, false
	for {
		i := strings.IndexByte(rest, '$')
		if i < 0 {
			break
		}
		if bare && i == 0 {
			// One unbraced expansion running straight into the next is not a
			// composition, it is a crypt hash: `$apr1$saltsalt$hashhash`,
			// `$argon2id$salt$hash`. No compose file writes `$A$B`; every
			// composition worth reading uses braces.
			return false
		}
		bare = !strings.HasPrefix(rest[i:], "${")
		// The literal text joining two expansions. A composed value is still an
		// expansion — `${DB_USER}:${DB_PASS}`, `${VAR}-suffix`, `${PREFIX}${SUFFIX}`
		// — as long as what joins them is not itself carrying a secret. Reading
		// only a value that is exactly one expansion rewrote every one of those,
		// and a compose file is written that way.
		if strings.ContainsAny(rest[:i], "0123456789") {
			return false
		}
		name, width := readExpansion(rest[i:])
		if width == 0 || !isVarName(name) {
			return false
		}
		saw, rest = true, rest[i+width:]
	}
	return saw && !strings.ContainsAny(rest, "0123456789")
}

// readExpansion reads one `$NAME` or `${NAME…}` at the start of s, returning the
// variable's name and how many bytes the expansion occupies. A width of zero
// means it is not one, or its default holds something that is not a placeholder.
func readExpansion(s string) (string, int) {
	body := s[1:]
	if !strings.HasPrefix(body, "{") {
		n := scanVarRef(body)
		return body[:n], 1 + n
	}
	inner := body[1:]
	// The closing brace is optional because the unquoted value class excludes
	// it, so what reaches this is often `${POSTGRES_PASSWORD` with the brace
	// still in the reviewer's text.
	width := 2 + len(inner)
	if end := strings.IndexByte(inner, '}'); end >= 0 {
		inner, width = inner[:end], 2+end+1
	}
	name, ok := stripDefault(inner)
	if !ok {
		return "", 0
	}
	return name, width
}

// scanVarRef returns how many bytes at the start of s form a variable
// reference, PowerShell's `env:` drive prefix included.
func scanVarRef(s string) int {
	n := 0
	if len(s) > 4 && strings.EqualFold(s[:4], "env:") {
		n = 4
	}
	for n < len(s) && (s[n] == '_' || isAlnumByte(s[n])) {
		n++
	}
	return n
}

func isAlnumByte(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// stripDefault removes `:-changeme` and its relatives from inside `${…}`. The
// default belongs to the expansion — taking the value up to it left a dangling
// brace — but it is also where a compose file keeps the password it hardcoded,
// so a digit in it means this is not an expansion to wave through.
//
// PowerShell's drive separator is a colon too, and `${env:API_KEY2}` is a name
// rather than a name and a default. What tells them apart is the character
// after the colon: a default's colon is always paired with one of `-+?=`.
func stripDefault(inner string) (string, bool) {
	i := strings.IndexAny(inner, ":-+?")
	if i <= 0 {
		return inner, true
	}
	if inner[i] == ':' && i+1 < len(inner) && !strings.ContainsAny(inner[i+1:i+2], "-+?=") {
		return inner, true
	}
	if strings.ContainsAny(inner[i:], "0123456789") {
		return "", false
	}
	return inner[:i], true
}

// isVarName reports whether a string is spelled the way a variable's name is:
// one case throughout, or camelCase opening in lowercase, and nothing in it but
// letters, digits and the underscore.
func isVarName(name string) bool {
	// PowerShell reaches the environment through a drive prefix.
	if len(name) > 4 && strings.EqualFold(name[:4], "env:") {
		name = name[4:]
	}
	if name == "" || strings.IndexFunc(name, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	}) >= 0 {
		return false
	}
	first := []rune(name)[0]
	if !unicode.IsLetter(first) && first != '_' {
		return false
	}
	if name == strings.ToUpper(name) || name == strings.ToLower(name) {
		return true
	}
	// A mixed-case name has to be digit-free as well. Without that the camel
	// branch excused `$dbHunter2pass`, `$myPassword123` and `$sup3rS3cret` —
	// the very strings the assignment branch exists to catch, wearing a dollar
	// sign. A variable named after a number is rare; a password with one in it
	// is the common case.
	return unicode.IsLower(first) && isWordPiece(name) &&
		!strings.ContainsAny(name, "0123456789")
}

var reEnvLookup = regexp.MustCompile(`(?i)^(?:process\.env|os\.environ|import\.meta\.env|env)[.\[]`)

// How long the name of a thing a credential is reached through may run.
// maxContainerRunes is twenty because the names real code uses are long —
// `databaseConfig`, `serviceAccount`, `connectionSettings` — and every one of
// them is digit-free; a piece that carries digits keeps the tighter bound, and
// an index is bounded shorter still.
const (
	maxContainerRunes  = 20
	maxDigitPieceRunes = 14 // `oauth2_config`, `my_db2_config`, `api_v2_config`
	maxIndexDigits     = 4
)

// namesSomething reports whether a value reads as a name the reviewer quoted
// rather than as a credential: a CONSTANT_NAME (`SHA256_DIGEST`,
// `OAUTH2_CLIENT_ID`), or a qualified one whose pieces are all short
// (`cfg.OAuth2Token`, `process.env.API_KEY`, `gitleaks:generic-api-key:v8.18.0`).
//
// Both halves are bounded on purpose. "Looks like camelCase" was the first
// attempt and it is not a test at all: more than 999 in 1000 random base64
// strings contain a lowercase letter followed by an uppercase one, so it
// excused nearly every credential it was shown. What a name has and a payload
// does not is *joints* — it is short pieces with separators between them, and
// a run of sixteen unbroken characters is not a piece of a name. That is what
// keeps a JWT, whose dots make it look qualified, on the credential side.
//
// A URL is excluded outright: `postgres://admin:sup3rS3cret@db/app` has all
// the joints of a name and a password in the middle of it.
//
// It is used only where the alternative is no test at all — where a value has
// been cut off from the sentence that would otherwise judge it, or where a
// key's own name is all there is. A credential shaped like a short qualified
// name survives it; that is the same trade isFileReference makes, for the same
// reason.
func namesSomething(value string) bool {
	if value == "" || strings.ContainsAny(value, " \t\n") {
		// A name is one word. `password: "p@ss w0rd.1"` is not one, and reading
		// the space as part of a piece made every quoted passphrase a name.
		return false
	}
	if carriesAPassword(value) {
		return false
	}
	// Every piece has to be spelled like a word, whichever shape said it was a
	// name: an underscore between two 25-character runs is not a CONSTANT_NAME,
	// it is `WJALRXUTNFEMI_K7MDENGBPXRFICYEXAMPLEKEY`.
	pieces := strings.FieldsFunc(value, isNameSeparator)
	if len(pieces) == 0 {
		return false
	}
	for _, piece := range pieces {
		if !isWordPiece(piece) {
			return false
		}
	}
	if strings.Contains(value, "_") && value == strings.ToUpper(value) {
		return true
	}
	if strings.Contains(value, ".") {
		return true
	}
	// camelCase — a *variable* name, so it starts lowercase. Without that it is
	// not a test at all at the length a piece is allowed to run: `Summer2024Rocks`
	// and `Kj9mQv3xLp7nWd` have humps as regular as any identifier's, and the
	// "one in a million" argument for the hump rule is an argument about forty
	// characters, not about fourteen. What is left is what a review actually
	// quotes: `sessionToken2`, `dbPassword1`, `stripeKey2`.
	rs := []rune(value)
	return unicode.IsLower(rs[0]) && shapeOf(value).hasUpper
}

// isWordPiece reports whether one separator-delimited piece of a value is
// spelled like a word rather than like a chunk of payload. Two things say it is
// not: a hump shorter than two runes — `aB3xY9zQ7wE` has the joints of a name
// and none of its words — and, in a piece that mixes case, a run of capitals,
// which is what base64 has and an identifier does not. A piece that is capitals
// all through is a CONSTANT piece and is bounded by its length alone.
func isWordPiece(piece string) bool {
	rs := []rune(piece)
	if len(rs) == 0 || len(rs) >= maxNamePieceRunes {
		return false
	}
	sh := shapeOf(piece)
	if sh.hasLower && sh.maxCaps >= maxCapsRun {
		return false
	}
	hump := 0
	for i := 1; i < len(rs); i++ {
		if unicode.IsUpper(rs[i]) && unicode.IsLower(rs[i-1]) {
			if i-hump < minHumpRunes {
				return false
			}
			hump = i
		}
	}
	return len(rs)-hump >= minHumpRunes || hump == 0
}

// carriesAPassword reports whether a value keeps a password in its userinfo.
// `https://oauth2@dev.azure.com/org/_git/repo` is a clone URL a review cites;
// `postgres://admin:sup3rS3cret@db.internal:5432/app` is a credential, and so
// is the same thing with the scheme left off. What separates them is the colon
// inside the userinfo, not the `@`.
func carriesAPassword(value string) bool {
	at := strings.Index(value, "@")
	if at < 0 {
		return false
	}
	userinfo := value[:at]
	if i := strings.Index(userinfo, "://"); i >= 0 {
		return strings.Contains(userinfo[i+3:], ":")
	}
	// A scheme-relative URL has no scheme to find, and skipping the leading
	// slashes is the difference between reading `//admin:pass@host` as a login
	// and not reading it at all.
	userinfo = strings.TrimPrefix(userinfo, "//")
	// With no scheme in front of it, everything before the `@` is userinfo only
	// if it reads like one — and userinfo is `user:pass`, exactly one colon and
	// no path. Testing for a dot instead was one dot away from being wrong in
	// both directions at once: `first.last:s3cr3tpassw0rd@db` was excused. The
	// slash is what keeps an image reference — `docker.io/library/nginx:1.2@sha256`
	// — from reading as a login.
	return strings.Count(userinfo, ":") == 1 && !strings.Contains(userinfo, "/")
}

func isNameSeparator(r rune) bool {
	return strings.ContainsRune("._-:/@", r)
}

const (
	// How long one piece of a qualified name runs before it stops being a word
	// and starts being a payload.
	maxNamePieceRunes = 16
	// How many capitals may run together inside a piece that has lowercase in
	// it. Four covers the acronyms a name really carries — API, HTTP, JSON,
	// UUID — and nothing covers `WJALRXUTNFEMI`.
	maxCapsRun = 5
	// How short a camelCase hump may be. A word is at least two letters; a
	// chunk of base64 that happens to change case is one.
	minHumpRunes = 2
)

// wordShape is a value's case pattern: whether it has capitals, whether it has
// lowercase, and the longest run of capitals in it.
type wordShape struct {
	hasUpper, hasLower bool
	maxCaps            int
}

func shapeOf(value string) wordShape {
	var sh wordShape
	run := 0
	for _, r := range value {
		switch {
		case unicode.IsUpper(r):
			sh.hasUpper = true
			run++
			if run > sh.maxCaps {
				sh.maxCaps = run
			}
		default:
			run = 0
			if unicode.IsLower(r) {
				sh.hasLower = true
			}
		}
	}
	return sh
}

// beginsItsUnit reports whether the match at i starts its line or its string,
// give or take the punctuation a list item, a heading, a fence or an indent
// puts first. A string counts as well as a line because the shape that matters
// most has no newline in it: a secret scanner echoes the offending source line
// and the whole report arrives as one line of JSON.
func beginsItsUnit(text string, i int) bool {
	start, end := unitBounds(text, i)
	// A `` ` `` or `*` that opens a span and closes it again on the same line
	// is a code span or an emphasis rather than a list item — but only prose
	// after the closing delimiter says which. "`token: sessionToken` is never
	// validated" is a sentence about code, and reading its backtick as a bullet
	// redacted the identifier the reviewer named; "`api_key: abcdefghijkl`" is
	// a span holding nothing but the assignment, and reading *its* backtick as
	// emphasis leaked the key.
	if close := markdownSpanEnd(text, i); close >= 0 && hasLetter(text[close:end]) {
		return false
	}
	prefix := strings.TrimLeft(text[start:i], " \t-*+>|{,[#`_")
	// An ordered list item — `1. password: …` — is a bullet too.
	return prefix == "" || reOrderedItem.MatchString(prefix)
}

// unitBounds returns the line or string the match at i sits in, as offsets into
// text.
func unitBounds(text string, i int) (start, end int) {
	start = strings.LastIndexAny(text[:i], "\n\"'") + 1
	end = len(text)
	if j := strings.IndexAny(text[i:], "\n\"'"); j >= 0 {
		end = i + j
	}
	return start, end
}

// markdownSpanEnd returns the offset of the delimiter closing the markdown span
// that the match at i sits inside, or -1 when the match does not open one that
// closes on its line. `**` is tested before `*` and the search stops at the
// delimiter that opened, or bold's second star reads as a span of its own
// closing at the end of the line.
func markdownSpanEnd(text string, i int) int {
	start, end := unitBounds(text, i)
	opener := strings.TrimLeft(text[start:i], " \t-+>|{,[#")
	for _, delim := range []string{"`", "**", "*", "_"} {
		if !strings.HasPrefix(opener, delim) {
			continue
		}
		body := i - len(opener) + len(delim)
		j := strings.Index(text[body:end], delim)
		if j < 0 {
			return -1
		}
		return body + j
	}
	return -1
}

var reOrderedItem = regexp.MustCompile(`^[0-9]+[.)][ \t]*$`)

// startsWithWord reports whether the first thing after the value is a word. A
// comment is not — `password: x  # rotate this` is still an assignment — and
// neither is an annotation the value is followed by: `(line 4)`, a comma, the
// quote that closes a field.
func startsWithWord(rest string) bool {
	rest = strings.TrimLeft(rest, " \t")
	if strings.HasPrefix(rest, "#") || strings.HasPrefix(rest, "//") {
		return false
	}
	for _, r := range rest {
		return unicode.IsLetter(r)
	}
	return false
}

// followedByWords reports whether prose continues anywhere after the value on
// its line. The stricter test, for a name that did not begin its line: there
// the surroundings have already said "sentence", so anything word-shaped after
// the value confirms it.
func followedByWords(rest string) bool {
	// Bounded to the value's own line or string: past that is another field or
	// another finding, and what is written there says nothing about this one.
	if i := strings.IndexAny(rest, "\n\""); i >= 0 {
		rest = rest[:i]
	}
	rest = strings.TrimLeft(rest, " \t")
	if strings.HasPrefix(rest, "#") || strings.HasPrefix(rest, "//") {
		return false
	}
	// The whole remainder, not just its first character. Stopping at the first
	// rune made a comma or a period read as "the line ended here", so
	// `- The api_key: abcdefghij, hardcoded in config.go` was rewritten.
	return hasLetter(rest)
}

// hasLetter reports whether a span holds anything word-shaped.
func hasLetter(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// isFileReference reports whether a value reads as a path the reviewer cited
// rather than as a credential: it ends in a dotted extension.
//
// A directory component is not required. `private_key: server.pem` and
// `api_key: settings.py` are files a review names, and requiring a `/` on top
// of the extension redacted both — while requiring nothing but the extension
// costs only `password: hunter2.key`, a password that happens to end that way.
// Of the two errors, rewriting a path the reviewer cited is the one that makes
// the finding name a file that does not exist, in the text grounding checks
// against.
//
// What separates the two is how long a *segment* runs, not how long the value
// is. A path is directory names and a file name, each of them short, however
// deep it goes: `src/main/java/com/example/service/authentication/
// TokenServiceImpl.java` is 70 runes and its longest piece is 21. A payload has
// no such joints — a base64 secret with slashes in it runs 26 and 33 — and a
// JWT is one unbroken run whose last dotted piece looks exactly like an
// extension. A total-length ceiling could not tell those apart: the secret
// above and the Java path are four runes apart.
//
// A long segment is allowed when it reads as words, because that is what a
// long file *name* is: `AuthenticationTokenProviderTest.java` is routine in a
// Java or TypeScript test suite, and every capital in it opens a word. The
// base64 chunk this bound is for does not —
// `AKIAIOSFODNN7EXAMPLEwJalrXUtn.key` and `WJALRXUTNFEMIKMDENG….key` are runs
// of capitals. Bounding long segments outright rewrote the first; allowing
// them on "carries no digits" let the second through with its digits stripped.
//
// Known limitation: a base64 secret whose slashes happen to fall often enough
// to keep every piece short is read as a path. The two are not separable by
// shape there, and reading a cited path as a credential is the error that
// corrupts the corpus this feature builds.
func isFileReference(value string) bool {
	if !reFileRef.MatchString(value) {
		return false
	}
	if !strings.Contains(value, "/") {
		return len([]rune(value)) < maxUnbrokenPathRunes
	}
	for _, seg := range strings.Split(value, "/") {
		if n := len([]rune(seg)); n >= maxLongSegmentRunes || (n >= maxPathSegmentRunes && !readsAsWords(seg)) {
			return false
		}
	}
	return true
}

// readsAsWords reports whether a long path segment is spelled the way a file
// name is: words, with at most an acronym's worth of capitals together, and no
// digits mixed through it.
//
// Two narrower rules were tried first and each covered half the class. Banning
// digits rewrote every long file name that carries one, and long file names
// carry them — `UserAuthenticationTokenProviderFactoryImpl2.java`,
// `Auth2FactorEnrollmentDialogContainer.tsx`. Banning a stem of nothing but hex
// caught the content hash and nothing else: strip the digits out of a base64
// key and `wjalrxutnfemik7mdengbpxrficyexamplekey.key` reads as a word again.
//
// What every long file name has is word breaks — humps, hyphens, underscores —
// and what no payload has is any. Except that Go, and Kubernetes with it,
// writes `validatingwebhookconfiguration.go`: a real 33-rune file name with no
// break in it at all. What that convention does not do is mix digits in, and
// what a key does is exactly that, so an unbroken stem is a payload only when
// it carries one.
func readsAsWords(seg string) bool {
	stem := seg
	if i := strings.LastIndexByte(stem, '.'); i > 0 {
		stem = stem[:i]
	}
	if !hasWordBreak(stem) && strings.ContainsAny(stem, "0123456789") {
		return false
	}
	sh := shapeOf(seg)
	return sh.hasLower && sh.maxCaps < maxCapsRun
}

// hasWordBreak reports whether a run of characters is divided into words by
// anything at all: a hyphen, an underscore, or a capital opening one.
func hasWordBreak(s string) bool {
	if strings.ContainsAny(s, "-_") {
		return true
	}
	rs := []rune(s)
	for i := 1; i < len(rs); i++ {
		if unicode.IsUpper(rs[i]) && unicode.IsLower(rs[i-1]) {
			return true
		}
	}
	return false
}

// How long a piece of a path may run. maxUnbrokenPathRunes is the stricter
// bound for a value with no separator at all, where the whole thing is one file
// name and nothing but the extension says "path"; it is what tells a JWT's last
// dotted segment from a real extension. The other two bound one directory or
// file name: maxPathSegmentRunes where it carries digits, maxLongSegmentRunes
// where it is letters all the way.
const (
	maxUnbrokenPathRunes = 24
	maxPathSegmentRunes  = 32
	maxLongSegmentRunes  = 64
)

var reFileRef = regexp.MustCompile(`\.[A-Za-z]{1,4}$`)

// keyLineBreak is how one line of a PEM key is separated from the next in the
// text this package sees: a real newline, indented or not, or a JSON escape.
const keyLineBreak = `(?:[ \t]*(?:\r?\n|\\n)[ \t]*)`

// reBareSecret matches the token shapes that identify themselves without a
// name beside them — an argv entry, a line of a traceback, a URL — and the
// body of a PEM key, which is the credential rather than its header.
//
// Each is anchored on a word boundary: `sk-` with none matched inside
// `task-scheduler`, rewriting a finding's own file path into one that does not
// exist. The PEM rule requires its END marker: running to the end of the text
// instead swallowed the rest of a report that merely quoted the header line.
var reBareSecret = regexp.MustCompile(
	// The body is whole lines of key material, not any run of letters and
	// whitespace: a report that merely quotes the header line mid-sentence had
	// the rest of that sentence, and the finding after it, swallowed. A key
	// truncated before its END marker still loses its body.
	//
	// Every way a reviewer quotes a key is covered by keyLineBreak: indented
	// inside a fence or a YAML block scalar, and — the likeliest of all, since
	// a structured response is re-marshalled to JSON before it gets here — with
	// its line breaks arriving as the two characters `\` and `n`.
	`-----BEGIN [A-Z ]*PRIVATE KEY-----(?:` + keyLineBreak + `[A-Za-z0-9+/=]+)*` +
		`(?:` + keyLineBreak + `-----END [A-Z ]*PRIVATE KEY-----)?` +
		`|\bsk-[A-Za-z0-9_-]{8,}|\bghp_[A-Za-z0-9]{16,}|\bgho_[A-Za-z0-9]{16,}` +
		`|\bgithub_pat_[A-Za-z0-9_]{20,}|\bxox[baprs]-[A-Za-z0-9-]{10,}` +
		`|\bAKIA[0-9A-Z]{16}\b|\bAIza[A-Za-z0-9_-]{30,}` +
		// A crypt-format hash, which names its own algorithm and is what a
		// `password_hash` column holds. It belongs here rather than with the
		// name-and-value rules because their unquoted value class stops at a
		// comma, and an argon2 hash has commas in its parameters — so those
		// rules redacted the algorithm and left the salt.
		`|` + cryptPrefix + `[^\s"'\\]{20,}`)

// cryptPrefix is the `$algorithm$` a crypt-format hash announces itself with.
// It is also what reSecretHint has to carry, or the rule above is switched off
// for every line that does not happen to say "password" as well —
// `hash: $2b$12$…`, or a line of /etc/shadow echoed out of a scanned file.
//
// The tail is required to be substantial because `$1$` on its own is also a
// regex replacement template, and `sed -e 's/(a)(b)/$1$2_and_more/'` is a
// command a review quotes. Twenty rather than eight: the shortest real crypt
// tail is md5crypt's, at about thirty.
const cryptPrefix = `\$(?:[0-9]|2[abxy]?|argon2[a-z0-9]*|scrypt|pbkdf2[a-z0-9-]*|y|gy|7|sha1)\$`

// envelopeKeys are what a tool runner wraps a result in: what tool it was, how
// it ended, what it was told to do. A map carrying them describes a call
// rather than being a report, so they are dropped before the map is offered as
// one — at the top level only, because the same words are report content one
// level down: `description` is the advisory text in Snyk, Grype and Checkov
// findings, and `file_path` is a finding's location.
var envelopeKeys = map[string]bool{
	"command": true, "cmd": true, "argv": true, "args": true,
	"env": true, "environment": true, "cwd": true, "file_path": true,
	"description": true, "tool_name": true, "tool_input": true,
	"tool_use_id": true, "is_error": true, "interrupted": true,
	"exit_code": true, "exitCode": true, "sandbox": true,
}

// noiseKeys are dropped at every level of a decoded response, not just the
// top. They are the runner's account of the call rather than any part of the
// report, and stderr is among them for the reason valueText spends a branch
// on: a crash message is not a review, and carrying it in the payload put one
// into the grounding corpus by the back door.
//
// The ambiguous names are deliberately absent: `description` is the advisory
// text in a Snyk or Checkov finding, `file_path` is a finding's location, and
// `input` may be the report itself. Those are bookkeeping only at the top
// level of a response, which is what envelopeKeys is for.
var noiseKeys = map[string]bool{
	"command": true, "cmd": true, "argv": true, "args": true,
	"env": true, "environment": true, "stderr": true,
	"tool_name": true, "tool_input": true, "tool_use_id": true,
	"is_error": true, "interrupted": true, "exit_code": true,
	"exitCode": true, "sandbox": true,
}

// reportPayload renders a decoded response as a candidate review: the map with
// the runner's bookkeeping removed, and without the fields already considered
// on their own. Credentials are not this function's job — redactSecrets covers
// every shape, including the ones no key list can reach.
func reportPayload(t map[string]interface{}) string {
	rest := make(map[string]interface{}, len(t))
	for key, val := range t {
		if isEnvelopeField(key, val) || isResponseTextKey(key) {
			continue
		}
		rest[key] = pruneNoise(val, 0)
	}
	return marshalText(rest)
}

// pruneNoise drops the runner's own fields from a decoded response at every
// level. It is not the credential defence — redactSecrets is, and covers the
// shapes no key list can reach — but a nested stderr or command belongs in the
// artifact no more than a top-level one does.
func pruneNoise(v interface{}, depth int) interface{} {
	if depth > 32 {
		return v
	}
	switch t := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for key, val := range t {
			if noiseKeys[key] {
				continue
			}
			out[key] = pruneNoise(val, depth+1)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, e := range t {
			out[i] = pruneNoise(e, depth+1)
		}
		return out
	}
	return v
}

// isEnvelopeField reports whether a top-level field of a response is the
// runner's account of the call rather than part of the report.
//
// `input` is decided by type. As text it is the command line — the thing
// hookLabel refuses to record for exactly this reason — and as a container it
// may be the report itself, which a name-only rule deleted whole.
func isEnvelopeField(key string, val interface{}) bool {
	if envelopeKeys[key] {
		return true
	}
	if key != "input" {
		return false
	}
	_, isText := val.(string)
	return isText
}

func isResponseTextKey(key string) bool {
	for _, known := range responseTextKeys {
		if key == known {
			return true
		}
	}
	return false
}

// isOnlyBookkeeping reports whether a response is the runner's account of the
// call and nothing else. Such a payload sniffs as markdown, because no parser
// claims it, so without this every one of them was exempt from the clean-run
// guard and written into the repository as a content-free artifact — one per
// run, forever, private paths included.
func isOnlyBookkeeping(text string) bool {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") {
		return false
	}
	var doc map[string]interface{}
	if json.Unmarshal([]byte(trimmed), &doc) != nil {
		return false
	}
	for key, val := range doc {
		if isEnvelopeField(key, val) || noiseKeys[key] {
			continue
		}
		switch t := val.(type) {
		case string:
			if strings.TrimSpace(t) != "" {
				return false
			}
		case []interface{}:
			if len(t) > 0 {
				return false
			}
		case map[string]interface{}:
			if len(t) > 0 {
				return false
			}
		default:
			// A number or a bool under a key this package does not know is
			// content: `{"errorCount":7,"warningCount":3}` is a linter saying
			// the run was not clean, which is the one thing worth keeping.
			return false
		}
	}
	return true
}
