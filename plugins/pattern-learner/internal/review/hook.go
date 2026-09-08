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

	// Redacted here rather than at the Capture call, so nothing downstream —
	// the artifact, the label, the lean view — ever sees the credential.
	text := redactSecrets(responseText(doc, isReporting))
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
	// Each pass writes a sentinel the value classes cannot match, so a later
	// pattern cannot consume part of an earlier replacement: `--token=x` was
	// rewritten by the assignment rule and then again by the flag rule, which
	// ate all but the closing bracket and left `[redacted]]` behind.
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
	`|auth|\bsk-|\bghp_|\bgho_|\bgithub_pat_|\bxox|\bAKIA|\bAIza|BEGIN [A-Z ]*PRIVATE KEY`)

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
	regexp.MustCompile(`(?i)(["'][A-Za-z0-9_.-]*` + secretName + `[A-Za-z0-9_.-]*["'][ \t]*[:=][ \t]*')[^']*(')`),
	regexp.MustCompile(`(?i)(\b[A-Za-z0-9_.-]*` + secretName + `[A-Za-z0-9_.-]*[ \t]*=[ \t]*` + escapedQuote + `)` + quotedValue + `(` + escapedQuote + `)`),
	regexp.MustCompile(`(?i)(\b[A-Za-z0-9_.-]*` + secretName + `[A-Za-z0-9_.-]*[ \t]*=[ \t]*')[^']*(')`),
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
// it, in each of its quotings. A bare name before a colon is also ordinary
// prose, so what follows is judged by looksLikeCredential rather than by the
// pattern. Group 1 is the name and its delimiter, group 2 the closing one.
//
// The value classes are bounded to a line and to their own quote, and exclude
// the backslash. A class that fell back to "anything but a quote" ran to the
// end of the review whenever the next character was a newline, taking every
// finding after it; one that swallowed a trailing `\` ate the escape of the
// `\"` that ended the enclosing JSON string and left the document invalid.
var colonForms = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(\b[A-Za-z0-9_.-]*` + secretName + `[A-Za-z0-9_.-]*[ \t]*:[ \t]*` + escapedQuote + `)(` + quotedValue + `)(` + escapedQuote + `)`),
	regexp.MustCompile(`(?i)(\b[A-Za-z0-9_.-]*` + secretName + `[A-Za-z0-9_.-]*[ \t]*:[ \t]*')([^'\n]*)(')`),
	regexp.MustCompile(`(?i)(\b[A-Za-z0-9_.-]*` + secretName + `[A-Za-z0-9_.-]*[ \t]*:[ \t]*)([^\s"',;)\]}\\]+)()`),
}

func redactColonForm(text string) string {
	for _, re := range colonForms {
		// Checked before replacing: ReplaceAllStringFunc copies the whole text
		// whether or not it matches, and this runs on output that can be
		// megabytes, after every matching tool call.
		if !re.MatchString(text) {
			continue
		}
		text = re.ReplaceAllStringFunc(text, func(m string) string {
			g := re.FindStringSubmatch(m)
			if !looksLikeCredential(g[2]) {
				return m
			}
			return g[1] + redactedSentinel + g[3]
		})
	}
	return text
}

// looksLikeCredential decides what may follow a name that says "secret". A
// credential is an unbroken run of characters; the two things that are not are
// a word too short to be one — `token: yes` — and a file reference, which is
// what a review sentence puts there: `- The credential: "auth.go" is missing`,
// `- The api_key: internal/auth/token.go is unused`. Those are the reviewer's
// own words, and rewriting them into a finding that cites a file which does
// not exist is the failure this whole pattern set is bounded against.
func looksLikeCredential(value string) bool {
	value = strings.TrimSpace(value)
	if len([]rune(value)) < 6 {
		return false
	}
	// A credential is one token. A phrase is the reviewer talking: `###
	// Hardcoded password: "must be at least 12 characters"` is a finding, and
	// rewriting it makes the artifact say something else.
	if strings.ContainsAny(value, " \t\n") {
		return false
	}
	if !reFileRef.MatchString(value) {
		return true
	}
	// It ends in something that reads as a file extension. A long unbroken run
	// with no path separator is a credential anyway — a JWT's last segment
	// looks like one — while `internal/auth/token_provider.go` is a path the
	// reviewer cited, and redacting that makes the finding name a file that
	// does not exist.
	return !strings.Contains(value, "/") && len([]rune(value)) >= 24
}

// reFileRef matches what a review sentence puts after a colon: a file the
// reviewer cited. Rewriting one into `[redacted]` makes the finding cite a
// path that does not exist, in the text grounding checks the quote against.
var reFileRef = regexp.MustCompile(`(?:^|/)[A-Za-z0-9_.-]+\.[A-Za-z]{1,4}$`)

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
		`|\bAKIA[0-9A-Z]{16}\b|\bAIza[A-Za-z0-9_-]{30,}`)

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
