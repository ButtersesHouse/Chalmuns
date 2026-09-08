package review

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

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

// shellKeywords introduce a command without being one, so `then semgrep .` is
// a run of semgrep. Without this a designated tool invoked from a conditional
// or a loop — `if semgrep --error .; then …`, `for f in *; do semgrep $f; done`
// — matched nothing at all, which is the silence a designation exists to
// avoid. Gating a build on a linter's exit status is the idiomatic conditional
// invocation, so the words that introduce a *condition* belong here as much as
// the ones that introduce a body.
//
// Matched case-sensitively, unlike commandWrappers on the same line: these are
// bash reserved words, and bash does not recognise `Then`.
var shellKeywords = map[string]bool{
	"if": true, "while": true, "until": true, "then": true, "do": true,
	"else": true, "elif": true, "!": true, "{": true,
	"command": true, "builtin": true,
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
		for i < len(fields) && (reEnvAssign.MatchString(fields[i]) ||
			commandWrappers[strings.ToLower(fields[i])] || shellKeywords[fields[i]]) {
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
	return sanitize(command, 0)
}

// sanitize is sanitizeCommand with the recursion depth a quoted command
// substitution needs: its contents are a command line and are sanitized in
// turn, and a hand-written `"$( "$( … )" )"` must not spin.
func sanitize(command string, depth int) string {
	// A hand-written `"$( "$( … )" )"` must not spin, and each level re-copies
	// what is left of the command, so the cost of nesting is quadratic. Past
	// the bound the text is data: blanked, never scanned.
	if depth > 4 {
		return strings.Repeat(" ", len([]rune(command)))
	}
	var b strings.Builder
	b.Grow(len(command))
	runes := []rune(command)

	var quote rune
	var span []rune
	// inSubWord records that the word being scanned already contains a command
	// substitution, so nothing else in that word is a program name of its own.
	// A substitution is emitted with parens so the separator split can see the
	// command inside it, and those parens then made the *adjacent* text look
	// like a command too: `echo $(date)semgrep`, `` echo `date`semgrep `` and
	// `echo "$(date)"semgrep` are each one argument that bash hands to echo,
	// and each read as a run of semgrep. The flag survives the quote around a
	// word, because `echo "$(date)"'semgrep'` is one argument as well.
	inSubWord := false
	// pending holds the here-documents opened on the current line, in the
	// order bash reads their bodies. One slot was not enough: with
	// `cat <<A <<B`, B's body was scanned as commands, so a body line reading
	// `note; semgrep now runs on every PR` manufactured a command position.
	var pending []heredoc
	// parens records what each open paren was, because `)` means different
	// things to the rest of the scan: `$(date +%Y)#1` closes a substitution
	// inside a word and carries no comment, while `(true)#note` closes a
	// subshell and does.
	var parens []parenKind
	// prev is the previous character as the shell sees it — after line
	// continuations are joined, and never a character inside quotes. Reading
	// the raw text instead made the `#` in `echo hello\<newline>#1` a comment
	// and dropped the rest of the joined command.
	var prev rune
	// spanPrev is prev's counterpart inside a quoted span: an escaped `$` is a
	// literal dollar, not the first half of a `$$`, so `"\$$(semgrep .)"` is a
	// real substitution and reading the raw text made it look like a pid.
	var spanPrev rune
	closedSubshell := false

	// closeSpan decides what a finished quoted span was. A span is a program
	// name only if it is a single bare word: `"semgrep" --json .` is an
	// ordinary invocation, `"semgrep found nothing"` is prose. Keeping the
	// words of a span that holds whitespace let quoted data manufacture a
	// command position it never had — `MSG="semgrep found nothing" && git
	// commit -m "$MSG"` read as a run of semgrep, and git's output was filed
	// as semgrep's review. Blanking every span instead lost the quoted program
	// name, which is silence, so neither extreme will do.
	closeSpan := func(closed bool) {
		switch {
		case closed && !inSubWord && isBareWord(span):
			b.WriteString(string(span))
		default:
			b.WriteRune(' ')
		}
		span = span[:0]
	}

	// atNewline runs the here-document bodies opened on the line just ended.
	// A body is input to a command, not more commands, and scanning resumes
	// after the last terminator.
	atNewline := func(i int) int {
		for _, h := range pending {
			i = skipHeredocBody(runes, i+1, h) - 1
		}
		pending = pending[:0]
		return i
	}

	for i := 0; i < len(runes); i++ {
		r := runes[i]

		// A word that already holds a substitution is finished as far as
		// command position goes: blank the rest of it, quotes included, until
		// a real word break arrives.
		if inSubWord && quote == 0 {
			if isWordBreak(r) {
				inSubWord = false
			} else if r != '\'' && r != '"' {
				b.WriteRune(' ')
				prev = midWord
				continue
			}
		}

		// Quote state is carried across newlines, because a shell string is:
		// `git commit -m "fix things<newline>semgrep now runs on every PR"` is
		// one argument, and scanning each line from a clean state read its
		// second line as a command of its own.
		if quote != 0 {
			// A command substitution inside double quotes runs commands, and
			// quotes nest inside it: `out="$(semgrep --config "p/ci" .)"` is
			// one argument holding one real invocation. Treating the span as
			// plain text and hunting for substitutions in it afterwards got
			// both halves wrong — the inner quote ended the span, so a genuine
			// run was lost, and the tail that escaped read as unquoted shell,
			// where a `;` in it manufactured a command position.
			if quote == '"' {
				// `$((…))` is arithmetic: a value, not a command. `$$(` is the
				// process id followed by literal parens. Neither opens a
				// substitution, and reading them as one put their operands in
				// command position.
				if r == '$' && spanPrev != '$' && i+2 < len(runes) && runes[i+1] == '(' && runes[i+2] == '(' {
					if end, ok := matchSubstitution(runes, i); ok {
						closeSpan(false)
						inSubWord = true
						// Arithmetic is a value, not a command, so its
						// operands must not land in command position —
						// `echo "$(( semgrep ))"` reads a variable. But a
						// substitution nested inside it really does run, so
						// the interior is kept for those alone.
						b.WriteString(substitutionsOnly(string(runes[i+3:end-2]), depth))
						i = end - 1
						prev = midWord
						continue
					}
				}
				// Inside a span prev is not maintained — it tracks the word
				// structure outside quotes — so the `$$` test reads the raw
				// text here, where a span holds no continuations to see past.
				if opensSubstitution(runes, i, spanPrev) {
					if end, ok := matchSubstitution(runes, i); ok {
						// Whatever preceded it in the span is still data.
						closeSpan(false)
						inSubWord = true
						b.WriteString(" (")
						b.WriteString(sanitize(string(runes[i+2:end-1]), depth+1))
						b.WriteString(") ")
						i = end - 1
						continue
					}
				}
				if r == '`' {
					if end, ok := matchBacktick(runes, i); ok {
						closeSpan(false)
						inSubWord = true
						b.WriteString(" (")
						b.WriteString(sanitize(string(runes[i+1:end-1]), depth+1))
						b.WriteString(") ")
						i = end - 1
						continue
					}
				}
			}
			switch {
			case r == '\\' && quote != '\'':
				// The character after a backslash is literal, so an escaped
				// quote does not end the string. Treating it as if it did
				// re-exposed the rest of the argument as command positions.
				span = append(span, ' ')
				if i+1 < len(runes) {
					i++
					span = append(span, ' ')
				}
				spanPrev = midWord
				continue
			case r == quote:
				closeSpan(true)
				quote = 0
				prev = '"'
			default:
				span = append(span, r)
			}
			spanPrev = r
			continue
		}

		switch {
		case r == '#' && startsWord(prev, closedSubshell):
			// A comment, and its text is not a command. Skipping it is not a
			// nicety: an apostrophe in one (`# see the tool's docs`) opened a
			// quote that, now that quote state crosses newlines, swallowed
			// every command after it.
			for i+1 < len(runes) && runes[i+1] != '\n' {
				i++
			}
			continue
		case r == '\'' || r == '"':
			quote = r
			spanPrev = 0
			continue
		case r == '\\':
			// A backslash-newline is a line continuation: bash joins the two
			// lines into one command, so it is neither a separator nor the
			// boundary a here-document body starts at. Emitting a real newline
			// read `npm install --save-dev \<newline>  eslint` as a run of
			// eslint, and running the body skip here swallowed the commands on
			// the joined line. prev is left alone so the joined text reads as
			// one word, which is what bash does with it.
			b.WriteRune(' ')
			if i+1 < len(runes) {
				i++
				if runes[i] != '\n' {
					b.WriteRune(neutralize(runes[i]))
					// The escaped character is still a character, whatever it
					// is: `echo a\b#c` prints `ab#c` and `echo a\ #c` prints
					// `a #c` — in both the `#` is mid-word. Recording a space
					// made it a word start, and copying the character through
					// did the same for an escaped space or separator, so the
					// rest of the line was dropped, `&&` and all.
					prev = midWord
				}
			}
			continue
		case r == '$' && i+1 < len(runes) && runes[i+1] == '$':
			// `$$` is the process id. `$$(cmd)` is a syntax error unquoted and
			// literal parens when quoted; either way nothing in it runs, and
			// leaving the parens for the separator split read the text inside
			// as a command.
			b.WriteString("  ")
			i++
			if i+1 < len(runes) && runes[i+1] == '(' {
				end, ok := matchSubstitution(runes, i)
				if !ok {
					// bash refuses the line outright; nothing after it runs.
					end = len(runes)
				}
				for j := i + 1; j < end; j++ {
					b.WriteRune(' ')
				}
				i = end - 1
			}
		case r == '$' && prev != '$' && i+2 < len(runes) && runes[i+1] == '(' && runes[i+2] == '(':
			// Consumed whole: the operands of `$(( 1 << x ))` are neither
			// commands nor a here-document operator — `echo $(( semgrep ))`
			// reads a variable — and only a substitution nested inside really
			// runs. `$((…))` is an expansion, so the word continues after it.
			if end, ok := matchSubstitution(runes, i); ok {
				b.WriteString(substitutionsOnly(string(runes[i+3:end-2]), depth))
				i = end - 1
				inSubWord = true
				prev = midWord
				continue
			}
			parens = append(parens, parenArithExpansion)
			b.WriteString("  ")
			i += 2
		case r == '$' && prev != '$' && i+1 < len(runes) && runes[i+1] == '(':
			// The paren is kept: `$(semgrep --version)` runs semgrep, and the
			// separator split is what gives the substitution its own command
			// position. Blanking it left `echo` as the only command on the
			// line and a designated run inside went uncaptured. Only the `$`
			// is dropped, and the stack remembers what this paren was.
			parens = append(parens, parenSubstitution)
			b.WriteString(" (")
			i++
		case r == '(' && i+1 < len(runes) && runes[i+1] == '(':
			// `((…))` is a command rather than an expansion, so it ends the
			// word it is in; its interior is arithmetic all the same.
			if end, ok := matchParens(runes, i); ok {
				b.WriteString(substitutionsOnly(string(runes[i+2:end-2]), depth))
				i = end - 1
				continue
			}
			parens = append(parens, parenArithCommand)
			b.WriteString("  ")
			i++
		case r == '(':
			parens = append(parens, parenSubshell)
			b.WriteRune(r)
		case r == ')':
			kind := parenSubshell
			if n := len(parens); n > 0 {
				kind = parens[n-1]
				parens = parens[:n-1]
			}
			// A `)` that closed a command substitution leaves the scan
			// mid-word — `echo $(date +%Y)#1` prints `2026#1` — while one that
			// closed a subshell starts a new word, and `(true)#note` really is
			// a comment.
			// `$(…)` and `$((…))` are expansions: they sit inside a word, so
			// `echo $((1+2))#1` prints `3#1` and carries no comment. `(…)` and
			// `((…))` are commands, and `(true)#note` really is a comment.
			closedSubshell = kind == parenSubshell || kind == parenArithCommand
			if kind == parenSubstitution {
				inSubWord = true
			}
			if kind == parenArithExpansion || kind == parenArithCommand {
				b.WriteRune(' ')
				if i+1 < len(runes) && runes[i+1] == ')' {
					b.WriteRune(' ')
					i++
				}
			} else {
				b.WriteRune(r)
			}
			prev = ')'
			continue
		case r == '<' && i+1 < len(runes) && runes[i+1] == '<' && !inArithmetic(parens):
			// A here-document operator, because we are outside quotes and
			// outside `$(( ))` — where `<<` is a left shift, and reading its
			// operand as a delimiter discarded everything after it.
			if h, ok := parseHeredoc(runes, i); ok {
				pending = append(pending, h)
			}
			b.WriteString("<<")
			i++
		case r == '`':
			// The older spelling of a command substitution. Backticks are not
			// shell metacharacters to the separator split, so without this
			// `echo ` + "`" + `semgrep .` + "`" + ` left echo as the only command
			// position and a designated run was never captured.
			if end, ok := matchBacktick(runes, i); ok {
				b.WriteString(" (")
				b.WriteString(sanitize(string(runes[i+1:end-1]), depth+1))
				b.WriteString(") ")
				i = end - 1
				inSubWord = true
				prev = midWord
				continue
			}
			b.WriteRune(' ')
		case r == '\n':
			b.WriteRune('\n')
			i = atNewline(i)
		default:
			b.WriteRune(r)
		}
		prev = r
		closedSubshell = false
	}
	// An unterminated quote runs to the end of the command. Nothing closed it,
	// so its content was never a program name.
	if quote != 0 {
		// Nothing closed it, so its content was never a program name.
		closeSpan(false)
	}
	return b.String()
}

// parenKind records what an open paren was, so the scan can tell a subshell
// from a command substitution when the matching `)` arrives.
type parenKind int

const (
	parenSubshell       parenKind = iota // `( … )`, a command
	parenSubstitution                    // `$( … )`, an expansion inside a word
	parenArithExpansion                  // `$(( … ))`, an expansion inside a word
	parenArithCommand                    // `(( … ))`, a command
)

// inArithmetic reports whether the scan is inside `$(( ))` or `(( ))`, where
// `<<` is a left shift rather than a here-document operator. It walks the
// stack rather than keeping a counter beside it: the stack is never more than
// a few frames deep, and a counter has to be kept in step by hand at every
// push and pop, where this cannot disagree with the state it reads.
func inArithmetic(parens []parenKind) bool {
	for _, k := range parens {
		if k == parenArithExpansion || k == parenArithCommand {
			return true
		}
	}
	return false
}

// midWord is the value prev takes for a character that carries no word-break
// meaning of its own — an escaped one. It is not a character any command line
// contains, so it can never collide with a real previous character.
const midWord rune = '\uFFFF'

// startsWord reports whether a `#` following prev opens a comment.
//
// bash starts a comment at the beginning of a word: after whitespace, after a
// control operator, or after a redirection. It does not start one mid-word,
// which is where a command substitution leaves it — `echo $(date +%Y)#1`
// prints `2026#1`, while `(true)#note` really is a comment. Getting either
// wrong costs a command: the first drops a designated run that followed a
// `&&`, the second scans comment text for one.
func startsWord(prev rune, closedSubshell bool) bool {
	switch prev {
	case 0:
		return true
	case ';', '|', '&', '(', '<', '>':
		return true
	case ')':
		return closedSubshell
	default:
		return unicode.IsSpace(prev)
	}
}

// isBareWord reports whether a quoted span is a single unadorned word — the
// only shape in which quoting a program name is ordinary.
func isBareWord(span []rune) bool {
	if len(span) == 0 {
		return false
	}
	for _, r := range span {
		if unicode.IsSpace(r) || neutralize(r) == ' ' {
			return false
		}
	}
	return true
}

// openerIsCRLF reports whether the line carrying the here-document operator at
// i ends with a carriage return, which is what makes its terminator carry one.
func openerIsCRLF(runes []rune, i int) bool {
	for ; i < len(runes); i++ {
		if runes[i] == '\n' {
			return i > 0 && runes[i-1] == '\r'
		}
	}
	return false
}

// heredoc is one pending here-document: the terminator to look for, and
// whether the operator was `<<-`, which lets the terminator be indented.
type heredoc struct {
	delimiter string
	dashed    bool
	// crlf records that the opener's own line ended CRLF, which is the only
	// case in which the terminator carries a CR too. Trimming one from every
	// body line instead let a body line reading `EOF\r` inside an LF script
	// end a here-document early, and the data after it was scanned as
	// commands.
	crlf bool
}

// skipHeredocBody returns the index just past the line terminating h, or the
// end of the command when no line terminates it.
//
// Skipping to the end on an unterminated here-document is the safe answer, not
// the tidy one. bash reads an unterminated body to end-of-input as data too,
// and the alternative — scanning the body as commands — is how a commit
// message beginning "semgrep now runs on every PR" got captured as semgrep's
// review. A missed capture is recoverable; a review attributed to a tool that
// never ran corrupts provenance silently.
func skipHeredocBody(runes []rune, start int, h heredoc) int {
	for i := start; i < len(runes); {
		end := i
		for end < len(runes) && runes[end] != '\n' {
			end++
		}
		line := string(runes[i:end])
		if end < len(runes) {
			end++
		}
		// bash strips leading tabs from the terminator only for `<<-`, and
		// never strips spaces. Comparing a trimmed line let an indented `EOF`
		// inside a plain here-document end it, and the body after it was then
		// scanned as commands. On a CRLF script bash's delimiter word carries
		// the CR too, so it comes off there and only there.
		if h.crlf {
			line = strings.TrimSuffix(line, "\r")
		}
		if h.dashed {
			line = strings.TrimLeft(line, "\t")
		}
		if line == h.delimiter {
			return end
		}
		i = end
	}
	return len(runes)
}

// neutralize strips a character of shell meaning while keeping it as text, so
// quoted data cannot manufacture a command position but a quoted program name
// still reads as one word.
func neutralize(r rune) rune {
	switch r {
	case ';', '|', '&', '(', ')', '<', '>', '\n':
		return ' '
	}
	return r
}

// reHeredoc matches a here-document operator and its delimiter word in every
// form bash accepts it: `<<EOF`, `<<'EOF'`, `<<-"EOF"`, `<<\EOF`, `<<EOF-1`,
// `<<1EOF`. A form this misses is a body scanned as commands, which is how a
// here-document body could manufacture a command position.
var reHeredoc = regexp.MustCompile(`^<<(-?)[ \t]*(?:'([^'\n]*)'|"([^"\n]*)"|((?:\\?[^\s;|&<>()'"])+))`)

// heredocWindow bounds how much of the command the delimiter match looks at.
// Handing it the whole tail copied the rest of the command into a fresh string
// for every `<<` on the line.
const heredocWindow = 128

func parseHeredoc(runes []rune, i int) (heredoc, bool) {
	end := i + heredocWindow
	if end > len(runes) {
		end = len(runes)
	}
	window := string(runes[i:end])
	loc := reHeredoc.FindStringSubmatchIndex(window)
	if loc == nil {
		return heredoc{}, false
	}
	dashed := loc[2] >= 0 && loc[3] > loc[2]
	// Group order: single-quoted, double-quoted, bare. Which one participated
	// is read from the indices rather than from emptiness, because `<<''` is a
	// legal here-document whose terminator is a blank line — treating its
	// empty match as "no delimiter" left the body to be scanned as commands.
	for group := 2; group <= 4; group++ {
		lo, hi := loc[2*group], loc[2*group+1]
		if lo < 0 {
			continue
		}
		word := window[lo:hi]
		if group == 4 {
			// A backslash quotes the next character in an *unquoted*
			// delimiter, so `<<\EOF` terminates at `EOF`. Inside quotes a
			// backslash is literal, and stripping it there left the terminator
			// of `<<'EO\F'` unmatchable.
			word = strings.ReplaceAll(word, `\`, "")
		}
		return heredoc{delimiter: word, dashed: dashed, crlf: openerIsCRLF(runes, i)}, true
	}
	return heredoc{}, false
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

// matchSubstitution returns the index just past the `)` closing the command
// substitution that starts at i, and whether one was found. It carries its own
// quote state, because quotes nest inside a substitution however the text
// around it is quoted, and an unbalanced one means bash would reject the line
// rather than run anything in it.
func matchSubstitution(runes []rune, i int) (int, bool) {
	return matchParens(runes, i+1)
}

// matchParens returns the index just past the `)` matching an opening paren at
// or after i, carrying its own quote state.
func matchParens(runes []rune, i int) (int, bool) {
	depth := 0
	var quote rune
	for j := i; j < len(runes); j++ {
		r := runes[j]
		if quote != 0 {
			switch {
			case r == '\\' && quote != '\'':
				j++
			case r == quote:
				quote = 0
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case '\\':
			j++
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return j + 1, true
			}
		}
	}
	return 0, false
}

// opensSubstitution reports whether the `$` at i begins a command
// substitution. `$$(` is the process id followed by literal parens, not a
// substitution, and reading it as one put its contents in command position.
func opensSubstitution(runes []rune, i int, prev rune) bool {
	return runes[i] == '$' && prev != '$' &&
		i+1 < len(runes) && runes[i+1] == '(' &&
		!(i+2 < len(runes) && runes[i+2] == '(')
}

// priorRune is the character before i, or 0 at the start of the command.
func priorRune(runes []rune, i int) rune {
	if i == 0 {
		return 0
	}
	return runes[i-1]
}

// isWordBreak reports whether a character ends a shell word.
func isWordBreak(r rune) bool {
	return unicode.IsSpace(r) || neutralize(r) == ' '
}

// matchBacktick returns the index just past the backtick closing the
// substitution that opens at i. A backslash escapes the next character, as it
// does inside a backtick substitution in bash.
func matchBacktick(runes []rune, i int) (int, bool) {
	for j := i + 1; j < len(runes); j++ {
		switch runes[j] {
		case '\\':
			j++
		case '`':
			return j + 1, true
		}
	}
	return 0, false
}

// substitutionsOnly blanks a stretch of text except for the command
// substitutions inside it, which are emitted with their parens and their own
// sanitized text. It is what arithmetic gets: `$(( semgrep ))` reads a
// variable and must not look like a run, while `$(( $(semgrep --count) + 1 ))`
// really does run one and must.
func substitutionsOnly(text string, depth int) string {
	runes := []rune(text)
	var b strings.Builder
	b.Grow(len(runes) + 2)
	for i := 0; i < len(runes); i++ {
		var end int
		var ok bool
		switch {
		case opensSubstitution(runes, i, priorRune(runes, i)):
			if end, ok = matchSubstitution(runes, i); ok {
				b.WriteString(" (")
				b.WriteString(sanitize(string(runes[i+2:end-1]), depth+1))
				b.WriteString(") ")
			}
		case runes[i] == '`':
			if end, ok = matchBacktick(runes, i); ok {
				b.WriteString(" (")
				b.WriteString(sanitize(string(runes[i+1:end-1]), depth+1))
				b.WriteString(") ")
			}
		}
		if ok {
			i = end - 1
			continue
		}
		b.WriteRune(' ')
	}
	return b.String()
}
