package review

import (
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Deciding whether a command line ran a designated tool.
//
// This is a real shell parser rather than a scanner of this package's own.
// There was a scanner, and sixteen rounds of adversarial review took it apart
// one shell construct at a time: quoting, here-documents, comments, line
// continuations, command substitution, arithmetic, backticks, arrays,
// function definitions, `case` patterns. Each fix was correct and each one
// left another shape wrong, in both directions — a heredoc body or a commit
// message read as an invocation, or a real `$(pwd)/bin/semgrep` missed. Both
// directions matter here: a review attributed to a tool that never ran
// corrupts provenance silently, and a missed capture is silence, which is
// indistinguishable from "the tool found no conventions".
//
// A parser answers the question the scanner was approximating: bash's own
// grammar says what is a command and what is a word. Everything the scanner
// needed special cases for falls out of the AST — a here-document body is a
// redirect's word, a `case` pattern is a pattern, an array element is an array
// element, and none of them is a call.
//
// One known divergence from bash, in the safe direction: this parser reads a
// `#` immediately after an expansion (`echo $(date)#1 && semgrep .`) as
// opening a comment, where bash reads it as part of the word and runs what
// follows. The cost is a missed capture on an unusual shape, never a review
// attributed to a tool that did not run.

// matchesCommand reports whether a command line invokes the watched tool. The
// name must be the command of a call that actually runs: a tool merely named
// in an argument is not a run of it, because `grep semgrep notes.txt` searches
// for the word, and capturing its output as a review would attribute grep's
// output to semgrep.
func matchesCommand(name, command string) bool {
	if command == "" {
		return false
	}
	for _, invoked := range invocations(command) {
		if strings.EqualFold(commandBase(invoked), name) {
			return true
		}
	}
	return false
}

// invocations lists the program names a command line calls.
//
// A line the parser rejects yields none. bash sometimes salvages such a line —
// it warns about an unclosed backtick and carries on — so this is a missed
// capture rather than a correct reading, and it is the safe direction: a
// command line this package cannot make sense of is not one it should be
// naming a reviewer from.
func invocations(command string) (found []string) {
	// The parser is third-party code on a fail-open path: the capture hook
	// runs inside someone else's tool call, and this package's contract is
	// that a payload it cannot handle costs a capture and nothing else. A
	// panic here would take the tool call with it, and v3.8.0 does panic on
	// some inputs — six bytes of quote, backtick, dollar and backslash reach
	// `slice bounds out of range` in its lexer. The exact input is pinned in
	// watch_test.go.
	defer func() {
		if recover() != nil {
			found = nil
		}
	}()

	// A CRLF *script* is a file-format artifact rather than shell syntax, and
	// a here-document whose terminator carries a CR parses as unterminated.
	// Only when every line ends that way: a lone `\r` inside an LF script is
	// data — a here-document body line spelled `EOF\r` is not its terminator.
	//
	// bash itself chokes on a CRLF script with a control structure, so this
	// reads some lines bash would refuse. That is the same trade the parser
	// makes everywhere it is stricter or looser than one shell build: what it
	// buys is that a here-document, a `case` pattern and a function body are
	// never read as invocations, which is where the harm was.
	if isCRLF(command) {
		command = strings.ReplaceAll(command, "\r\n", "\n")
	}

	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return nil
	}
	var out []string
	syntax.Walk(file, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.FuncDecl:
			// A function body does not run where it is defined.
			return false
		case *syntax.CallExpr:
			out = append(out, calledProgram(n)...)
		}
		return true
	})
	return out
}

// calledProgram returns the program a call invokes, plus the programs behind
// any wrappers it goes through — `npx eslint .` runs eslint, and `npm run
// lint` runs lint. Assignments are not the command: the parser keeps them in
// their own field, so `SEMGREP_RULES=x semgrep .` needs no special handling.
func calledProgram(call *syntax.CallExpr) []string {
	var out []string
	wrapper := ""
	operands := 0
	for i := 0; i < len(call.Args); i++ {
		word := literalWord(call.Args[i])
		if word == "" {
			// An argument with nothing literal in it — `$out`, `$(cmd)` — is
			// not a name this can check. It is still a word, so it ends the
			// search rather than letting the next argument stand in for it.
			return out
		}
		if wrapper != "" {
			// Inside a wrapper's own arguments, which are not the program.
			switch {
			case inquiryFlags[wrapper][word]:
				// `command -v semgrep` prints a path; it runs nothing.
				return out
			case valueFlags[wrapper][word]:
				i++
				continue
			case strings.HasPrefix(word, "-"), reAssignArg.MatchString(word):
				continue
			case operands > 0:
				// `timeout 60s semgrep .` — the duration is the wrapper's, not
				// a program.
				operands--
				continue
			}
		}
		out = append(out, word)
		base := strings.ToLower(commandBase(word))
		if !commandWrappers[base] {
			return out
		}
		wrapper, operands = base, wrapperOperands[base]
	}
	return out
}

// What a wrapper's own arguments look like. This is a small table of bounded
// knowledge about a fixed set of programs — not shell grammar — and each entry
// is a shape a scan cannot infer: whether a flag consumes the next word, and
// whether the wrapper takes an operand of its own before the program.
//
// Skipping every `-flag` without knowing which take a value promoted the value
// to the program: `command -v semgrep` reported a run of semgrep, and
// `sudo -u ci semgrep .` reported a run of ci.
var (
	valueFlags = map[string]map[string]bool{
		"sudo": {"-u": true, "-g": true, "-p": true, "-C": true, "-h": true,
			"--user": true, "--group": true, "--prompt": true, "--chdir": true,
			"--close-from": true, "--host": true, "--role": true, "--type": true,
			"-D": true, "-R": true, "-T": true, "--chroot": true,
			"--command-timeout": true},
		"env":     {"-u": true, "-C": true, "--unset": true, "--chdir": true},
		"timeout": {"-s": true, "-k": true, "--signal": true, "--kill-after": true},
		"nice":    {"-n": true, "--adjustment": true},
		"exec":    {"-a": true},
		// Only the flags GNU xargs requires an argument for. `-i`, `-l`,
		// `--replace` and `--eof` take an *optional* one, which xargs never
		// reads as a separate word, so listing them swallowed the program:
		// `xargs -i semgrep {}` really runs semgrep.
		"xargs": {"-a": true, "-I": true, "-n": true, "-P": true,
			"-d": true, "-s": true, "-L": true, "-E": true,
			"--max-args": true, "--max-procs": true, "--max-chars": true,
			"--max-lines": true, "--arg-file": true, "--delimiter": true},
		"yarn": {"--cwd": true},
		// A subcommand's own flags. `wrapper` follows the chain, so `uv run
		// --with x semgrep .` consults this rather than uv's table, and an
		// entry filed only under the top-level program was never reached.
		"run": {"--with": true, "--python": true, "-p": true, "--from": true,
			"--directory": true, "-C": true, "--cwd": true, "-w": true,
			"--workspace": true, "--prefix": true, "--filter": true},
		"tool":   {"--with": true, "--python": true, "-p": true, "--from": true},
		"npx":    {"-p": true, "--package": true, "-c": true, "--call": true},
		"npm":    {"-w": true, "--workspace": true, "--prefix": true, "--loglevel": true},
		"pnpm":   {"-C": true, "--dir": true, "--filter": true},
		"uv":     {"--with": true, "--python": true, "--directory": true},
		"uvx":    {"--with": true, "--python": true, "-p": true},
		"poetry": {"-C": true, "--directory": true},
	}
	// After these, the wrapper reports on a program rather than running it.
	inquiryFlags = map[string]map[string]bool{
		"command": {"-v": true, "-V": true},
		"builtin": {"-v": true, "-V": true},
	}
	// Operands the wrapper itself takes before the program.
	wrapperOperands = map[string]int{"timeout": 1}
)

var reAssignArg = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// literalWord renders the parts of a word that are literal text, dropping the
// parts that are not. `$(pwd)/bin/semgrep` yields "/bin/semgrep", which
// commandBase reduces to semgrep — the form a CI script uses to run a
// locally-installed linter — while `$TOOL` yields nothing, because nothing in
// it says what will run.
func literalWord(w *syntax.Word) string {
	var b strings.Builder
	expanded := false
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			b.WriteString(p.Value)
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, inner := range p.Parts {
				if lit, ok := inner.(*syntax.Lit); ok {
					b.WriteString(lit.Value)
				} else {
					expanded = true
					b.Reset()
				}
			}
		default:
			// An expansion. Whatever came before it is not the program's
			// name, so the accumulated text starts again after it.
			expanded = true
			b.Reset()
		}
	}
	text := b.String()
	if expanded && !strings.Contains(text, "/") {
		// The expansion produced part of the name itself: `${TOOL}semgrep`
		// runs whatever `$TOOL` expands to, glued to "semgrep", and reading
		// the literal half as the program named a tool that never ran. With a
		// `/` after the expansion the basename is fully literal, which is the
		// `$(pwd)/bin/semgrep` form a CI script uses.
		return ""
	}
	return text
}

// isCRLF reports whether every line of a command ends with a carriage return,
// which is what makes it a CRLF script rather than LF text with a stray CR in
// it.
func isCRLF(command string) bool {
	rest, seen := command, false
	for {
		i := strings.IndexByte(rest, '\n')
		if i < 0 {
			return seen
		}
		if i == 0 || rest[i-1] != '\r' {
			return false
		}
		seen = true
		rest = rest[i+1:]
	}
}
