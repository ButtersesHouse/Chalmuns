package review

import (
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
// A line the parser rejects yields none: bash would refuse it too, so nothing
// in it ran, and reading a command out of a line that cannot execute is how a
// syntax error got filed as a review.
func invocations(command string) []string {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		// CRLF is a file-format artifact rather than shell syntax, and a
		// here-document whose terminator carries a CR parses as unterminated.
		// bash runs such a script; refusing it cost the capture.
		if !strings.Contains(command, "\r\n") {
			return nil
		}
		file, err = syntax.NewParser().Parse(
			strings.NewReader(strings.ReplaceAll(command, "\r\n", "\n")), "")
		if err != nil {
			return nil
		}
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
	for _, arg := range call.Args {
		word := literalWord(arg)
		if word == "" {
			// An argument with nothing literal in it — `$out`, `$(cmd)` — is
			// not a name this can check. It is still a word, so it ends the
			// search rather than letting the next argument stand in for it.
			return out
		}
		out = append(out, word)
		if !commandWrappers[strings.ToLower(commandBase(word))] {
			return out
		}
	}
	return out
}

// literalWord renders the parts of a word that are literal text, dropping the
// parts that are not. `$(pwd)/bin/semgrep` yields "/bin/semgrep", which
// commandBase reduces to semgrep — the form a CI script uses to run a
// locally-installed linter — while `$TOOL` yields nothing, because nothing in
// it says what will run.
func literalWord(w *syntax.Word) string {
	var b strings.Builder
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
				}
			}
		}
	}
	return b.String()
}
