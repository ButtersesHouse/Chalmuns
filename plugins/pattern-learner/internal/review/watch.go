package review

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ButtersesHouse/Chalmuns/internal/state"
)

// Watch kinds. A designation says what sort of thing is being watched so a
// tool and a skill that share a name cannot be confused for each other.
const (
	KindSkill = "skill" // a Claude Code skill invocation, e.g. /code-review
	KindTool  = "tool"  // a command line, e.g. semgrep or eslint
	KindAny   = "any"   // either
)

// Kinds lists every legal Watcher.Kind, for validation and error messages.
var Kinds = []string{KindSkill, KindTool, KindAny}

var reWatcherSlug = regexp.MustCompile(`[^a-z0-9]+`)

// WatcherID derives the stable handle `watch --remove` takes. Two watchers
// whose names differ only in punctuation or case would collide here, which is
// what AddWatcher's duplicate check is for.
func WatcherID(name string) string {
	id := reWatcherSlug.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	return strings.Trim(id, "-")
}

// NewWatcher validates a designation and returns it. Validation is strict on
// purpose: a watcher that never matches anything records nothing, and silence
// is indistinguishable from "the tool found no conventions".
func NewWatcher(name, kind, format string, now time.Time) (state.Watcher, error) {
	// A leading slash is how the user says a skill's name; accept it and
	// store the bare name so matching does not have to strip it twice.
	name = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), "/"))
	if name == "" {
		return state.Watcher{}, fmt.Errorf("a watcher needs a name — the skill or tool to keep eyes on, e.g. code-review")
	}
	id := WatcherID(name)
	if id == "" {
		return state.Watcher{}, fmt.Errorf("%q has no letters or digits to form a watcher id from", name)
	}
	if kind == "" {
		kind = KindAny
	}
	if !validKind(kind) {
		return state.Watcher{}, fmt.Errorf("unknown kind %q; accepts %s", kind, strings.Join(Kinds, ", "))
	}
	if format == "" {
		format = FormatAuto
	}
	if !ValidFormat(format) {
		return state.Watcher{}, fmt.Errorf("unknown format %q; accepts %s", format, strings.Join(Formats, ", "))
	}
	return state.Watcher{
		ID:      id,
		Name:    name,
		Kind:    kind,
		Format:  format,
		AddedAt: now.UTC().Format(time.RFC3339),
	}, nil
}

func validKind(k string) bool {
	for _, known := range Kinds {
		if k == known {
			return true
		}
	}
	return false
}

// AddWatcher appends w to ws, refusing a duplicate id. Re-designating the
// same source is an error rather than a silent replace so a typo'd second
// entry cannot quietly shadow a working one.
func AddWatcher(ws []state.Watcher, w state.Watcher) ([]state.Watcher, error) {
	for _, existing := range ws {
		if existing.ID == w.ID {
			return nil, fmt.Errorf("%q is already watched (id %q, kind %q, format %q); remove it first to change it",
				existing.Name, existing.ID, existing.Kind, existing.Format)
		}
	}
	return append(ws, w), nil
}

// RemoveWatcher drops the watcher with this id, reporting whether it was
// there. Removing a designation never touches artifacts already captured:
// rules already mined from them keep their provenance.
func RemoveWatcher(ws []state.Watcher, id string) ([]state.Watcher, bool) {
	out := make([]state.Watcher, 0, len(ws))
	found := false
	for _, w := range ws {
		if w.ID == id {
			found = true
			continue
		}
		out = append(out, w)
	}
	return out, found
}

// Match picks the watcher a tool invocation belongs to, given the tool name
// the hook reported, the skill name it invoked (empty when it was not a skill
// invocation), and the command line it ran (empty when it was not a command).
// It returns the first match in designation order; nil when nothing is watched
// or nothing matches, which is the common case and must stay cheap.
func Match(ws []state.Watcher, toolName, skill, command string) *state.Watcher {
	// A slash command names a skill, not a program: "/code-review --fix" is an
	// invocation of the code-review skill. Deriving it here rather than in the
	// caller keeps one answer to "what did this call invoke" — a caller that
	// forgot to derive it made a `--kind skill` watcher unmatchable, which is
	// exactly the designation the docs recommend for /code-review.
	if skill == "" {
		skill = SkillFromCommand(command)
	}
	for i := range ws {
		w := &ws[i]
		if w.Kind != KindTool && matchesSkill(w.Name, toolName, skill) {
			return w
		}
		if w.Kind != KindSkill && matchesCommand(w.Name, command) {
			return w
		}
	}
	return nil
}

// SkillFromCommand returns the skill a slash command invokes, or "" when the
// command is an ordinary program.
func SkillFromCommand(command string) string {
	command = strings.TrimSpace(command)
	if !strings.HasPrefix(command, "/") {
		return ""
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	// A path is not a slash command: "/usr/local/bin/semgrep" runs a program.
	if strings.Contains(strings.TrimPrefix(fields[0], "/"), "/") {
		return ""
	}
	// A bare "/" names nothing.
	if strings.Trim(fields[0], "/") == "" {
		return ""
	}
	return fields[0]
}

// matchesSkill compares a designation against a skill invocation. A skill may
// be addressed bare ("code-review"), plugin-qualified ("myplugin:code-review"),
// or with a leading slash, and the tool name itself is the skill name for a
// slash-command invocation, so all three are accepted.
func matchesSkill(name, toolName, skill string) bool {
	for _, candidate := range []string{skill, toolName} {
		candidate = strings.TrimPrefix(strings.TrimSpace(candidate), "/")
		if candidate == "" {
			continue
		}
		if strings.EqualFold(candidate, name) {
			return true
		}
		// plugin-qualified: "plugin:skill"
		if i := strings.LastIndex(candidate, ":"); i >= 0 && strings.EqualFold(candidate[i+1:], name) {
			return true
		}
	}
	return false
}

var (
	// Shell separators that start a new command within one line.
	reCmdSeparator = regexp.MustCompile(`\|\||&&|[;|&\n()]`)
	// A leading VAR=value assignment precedes the command, it is not the
	// command: `SEMGREP_RULES=x semgrep .` is a run of semgrep.
	reEnvAssign = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
)

// commandWrappers run another program and are skipped when looking for the
// command actually being invoked, so watching "eslint" matches `npx eslint .`
// and watching "lint" matches `npm run lint`.
var commandWrappers = map[string]bool{
	"sudo": true, "env": true, "time": true, "nice": true, "exec": true,
	"npx": true, "bunx": true, "uvx": true, "pnpm": true, "yarn": true,
	"npm": true, "bun": true, "poetry": true, "uv": true, "run": true,
}

// matchesCommand reports whether a command line invokes the watched tool. The
// name must be in command position — the first word of the line or of a
// pipeline stage, after any environment assignments and wrappers — so a tool
// merely named in an argument is not a run of it: `grep semgrep notes.txt`
// searches for the word, it does not review anything, and capturing its output
// as a review would attribute grep's output to semgrep.
func matchesCommand(name, command string) bool {
	if command == "" {
		return false
	}
	for _, segment := range reCmdSeparator.Split(sanitizeCommand(command), -1) {
		fields := strings.Fields(segment)
		i := 0
		for i < len(fields) && (reEnvAssign.MatchString(fields[i]) || commandWrappers[strings.ToLower(fields[i])]) {
			i++
		}
		if i < len(fields) && strings.EqualFold(commandBase(fields[i]), name) {
			return true
		}
	}
	return false
}

// sanitizeCommand blanks out the parts of a command line that are data rather
// than commands, so the separator split cannot manufacture a command position
// inside them.
//
// Without this, every `;` `|` `&` `(` and newline was a separator regardless
// of context, so the first word after one was read as a program being run.
// `git commit -m 'fix; semgrep noise'` therefore counted as a run of semgrep,
// and a heredoc commit message whose body line began "semgrep now runs on
// every PR" did too — capturing git's output and filing it as semgrep's
// review. That breaks the invariant this whole path exists to hold: nothing is
// captured from a tool that was not designated.
func sanitizeCommand(command string) string {
	var out strings.Builder
	out.Grow(len(command))
	lines := strings.Split(command, "\n")
	for i := 0; i < len(lines); i++ {
		masked, delim := maskLine(lines[i])
		out.WriteString(masked)
		out.WriteByte('\n')
		if delim == "" {
			continue
		}
		// A here-document's body is input to a command, not more commands.
		// Skip to its terminator and carry on scanning after it — truncating
		// the script there instead meant a designated tool run later in the
		// same call was never captured.
		for i+1 < len(lines) && strings.TrimSpace(lines[i+1]) != delim {
			i++
		}
		if i+1 < len(lines) {
			i++
		}
	}
	return out.String()
}

// maskLine blanks the quoted spans of one line and reports the here-document
// delimiter the line opens, if any.
//
// The two jobs share one scan because they depend on each other: a `<<` inside
// a quoted string is text, not an operator, and a delimiter written `<<'EOF'`
// is itself quoted. Doing either pass first gets the other wrong — masking
// first erases the delimiter, scanning first treats quoted text as an opener
// and swallows every line after it.
func maskLine(line string) (masked, delimiter string) {
	var b strings.Builder
	b.Grow(len(line))
	runes := []rune(line)
	var quote rune
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case quote == 0 && (r == '\'' || r == '"'):
			quote = r
			b.WriteRune(' ')
		case quote != 0 && r == '\\' && quote != '\'':
			// The character after a backslash is literal, so an escaped quote
			// does not end the string. Treating it as if it did re-exposed the
			// rest of the argument as command positions.
			b.WriteRune(' ')
			if i+1 < len(runes) {
				i++
				b.WriteRune(' ')
			}
		case quote != 0:
			if r == quote {
				quote = 0
			}
			b.WriteRune(' ')
		case r == '\\':
			b.WriteRune(' ')
			if i+1 < len(runes) {
				i++
				b.WriteRune(' ')
			}
		case r == '<' && i+1 < len(runes) && runes[i+1] == '<' && delimiter == "":
			// An operator, because we are outside quotes. Read its delimiter
			// from the raw text, where its own quotes are still intact.
			delimiter = heredocDelimiter(string(runes[i:]))
			b.WriteString("<<")
			i++
		default:
			b.WriteRune(r)
		}
	}
	return b.String(), delimiter
}

// reHeredoc matches a here-document operator and its delimiter word, quoted or
// not — `<<EOF`, `<<'EOF'`, `<<-"EOF"` all name the same terminator.
var reHeredoc = regexp.MustCompile(`^<<-?\s*['"]?([A-Za-z_][A-Za-z0-9_]*)['"]?`)

func heredocDelimiter(s string) string {
	if m := reHeredoc.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// commandBase reduces an invocation token to the program's name: quotes off,
// directories off, and the Windows extension off, so /usr/local/bin/semgrep,
// "semgrep" and semgrep.exe are one tool.
func commandBase(token string) string {
	token = strings.Trim(token, `"'`)
	if i := strings.LastIndexAny(token, `/\`); i >= 0 {
		token = token[i+1:]
	}
	return strings.TrimSuffix(token, ".exe")
}

// WatcherStatus is a designation plus what has actually been captured for it,
// which is counted from the review cache rather than stored: see the note on
// state.Watcher for why the capture hook does not write state.
type WatcherStatus struct {
	state.Watcher
	Captures       int    `json:"captures"`
	LastCapturedAt string `json:"last_captured_at,omitempty"`
}

// Status pairs each watcher with its capture tally from the given artifacts.
// Artifacts are matched on Source, which is the watcher's name at capture
// time, so a watcher renamed by remove-then-add loses its history — which is
// honest, since the new designation may match different output.
func Status(ws []state.Watcher, artifacts []Artifact) []WatcherStatus {
	out := make([]WatcherStatus, 0, len(ws))
	for _, w := range ws {
		st := WatcherStatus{Watcher: w}
		for _, a := range artifacts {
			if !strings.EqualFold(a.Source, w.Name) {
				continue
			}
			st.Captures++
			// Compared as times, not strings: RFC3339Nano drops trailing
			// zeros, so a whole-second stamp is the shorter string and would
			// always beat a later sub-second one in the same second, reporting
			// a stale "last captured".
			if st.LastCapturedAt == "" || capturedTime(a.CapturedAt).After(capturedTime(st.LastCapturedAt)) {
				st.LastCapturedAt = a.CapturedAt
			}
		}
		out = append(out, st)
	}
	return out
}
