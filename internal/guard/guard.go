// Package guard implements the PreToolUse hook decision logic that prevents
// the learn-patterns SKILL from going off-script. During a learn-patterns run
// (signalled by the presence of a run-lock file) it denies Bash commands that
// invoke general-purpose interpreters or create/execute ad-hoc scripts, and
// denies Write/Edit of script files. The sanctioned path is the pattern-learner
// binary's own subcommands, which are never blocked.
//
// The guard is intentionally fail-open: any parse error, a missing run-lock, or
// an unrecognised tool results in "allow" so the hook can never brick a session.
package guard

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

// hookInput is the subset of the PreToolUse hook stdin payload we read.
type hookInput struct {
	ToolName  string `json:"tool_name"`
	CWD       string `json:"cwd"`
	ToolInput struct {
		Command  string `json:"command"`   // Bash
		FilePath string `json:"file_path"` // Write / Edit
	} `json:"tool_input"`
}

// runLockRelPath is the marker the SKILL creates while a run is in progress.
const runLockRelPath = ".claude/pattern-learner/.run-lock"

// exitBlock is the PreToolUse exit code that blocks the tool call and feeds
// stderr back to the model.
const exitBlock = 2

// Run reads the hook payload from stdin and decides whether to allow or block
// the pending tool call. It manages its own process exit code (0 = allow,
// 2 = block) and therefore never returns a value the caller must act on.
func Run() error {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return nil // fail-open
	}
	decide(data, allowExit, blockExit)
	return nil
}

// allowExit / blockExit are indirections so tests can capture the decision
// without terminating the test process.
func allowExit() { os.Exit(0) }
func blockExit(reason string) {
	fmt.Fprintf(os.Stderr, "[learn-patterns guard] %s\n", reason)
	os.Exit(exitBlock)
}

// decide parses the payload and calls allow() or block(reason). Pure enough to
// unit-test by passing capturing callbacks.
func decide(payload []byte, allow func(), block func(string)) {
	var in hookInput
	if err := json.Unmarshal(payload, &in); err != nil {
		allow()
		return
	}

	// Only enforce while a learn-patterns run is active.
	base := in.CWD
	if base == "" {
		base, _ = os.Getwd()
	}
	if _, err := os.Stat(filepath.Join(base, runLockRelPath)); err != nil {
		allow()
		return
	}

	switch in.ToolName {
	case "Bash":
		if reason := forbiddenCommand(in.ToolInput.Command); reason != "" {
			block(reason)
			return
		}
	case "Write", "Edit", "NotebookEdit":
		if reason := forbiddenFile(in.ToolInput.FilePath); reason != "" {
			block(reason)
			return
		}
	}
	allow()
}

var (
	// Interpreter invoked as a command (after start, whitespace, '/', ';', '&', '|', '(').
	reInterpreter = regexp.MustCompile(`(?i)(?:^|[\s/;&|(])(python3?|nodejs|node|deno|bun|ruby|perl|php|rscript)\b`)
	// Piping into a shell (curl ... | sh, etc.).
	rePipeToShell = regexp.MustCompile(`(?i)\|\s*(sh|bash|zsh|fish)\b`)
	// Executing a local script file (./foo.sh).
	reRunLocal = regexp.MustCompile(`(?i)(?:^|\s)\./\S+\.(sh|py|rb|pl|bash|zsh|js|mjs|cjs|ts)\b`)
	// Redirecting output into a script file (cat > cluster.py <<EOF).
	reRedirectScript = regexp.MustCompile(`(?i)[12]?>>?\s*\S*\.(py|sh|rb|pl|bash|zsh|js|mjs|cjs|ts)\b`)
	// tee into a script file.
	reTeeScript = regexp.MustCompile(`(?i)\btee\s+\S*\.(py|sh|rb|pl|bash|zsh|js|mjs|cjs|ts)\b`)
	// Making a file executable.
	reChmodX = regexp.MustCompile(`(?i)\bchmod\s+\+?x`)
	// Write/Edit of a script file.
	reScriptFile = regexp.MustCompile(`(?i)\.(py|sh|rb|pl|bash|zsh|js|mjs|cjs|ts)$`)
)

const sanctioned = "Use the pattern-learner subcommands (extract-lean, verify-grounding, classify, triage, state-read/write, write-outputs) instead. If a needed capability is missing or a subcommand is broken, STOP and report it — do not write a workaround script."

// forbiddenCommand returns a non-empty reason if the Bash command would
// reimplement pipeline logic via an interpreter or ad-hoc script.
func forbiddenCommand(cmd string) string {
	switch {
	case reInterpreter.MatchString(cmd):
		return "Running a general-purpose interpreter (python/node/ruby/perl/…) is blocked during a learn-patterns run. " + sanctioned
	case rePipeToShell.MatchString(cmd):
		return "Piping into a shell is blocked during a learn-patterns run. " + sanctioned
	case reRunLocal.MatchString(cmd):
		return "Executing a local script file is blocked during a learn-patterns run. " + sanctioned
	case reRedirectScript.MatchString(cmd):
		return "Writing a script file via redirection is blocked during a learn-patterns run. " + sanctioned
	case reTeeScript.MatchString(cmd):
		return "Writing a script file via tee is blocked during a learn-patterns run. " + sanctioned
	case reChmodX.MatchString(cmd):
		return "Making a file executable is blocked during a learn-patterns run. " + sanctioned
	}
	return ""
}

// forbiddenFile returns a non-empty reason if the Write/Edit target is a script.
func forbiddenFile(path string) string {
	if reScriptFile.MatchString(path) {
		return "Creating or editing a script file (" + filepath.Base(path) + ") is blocked during a learn-patterns run. " + sanctioned
	}
	return ""
}
