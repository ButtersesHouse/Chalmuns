package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ButtersesHouse/Chalmuns/internal/detect"
	"github.com/ButtersesHouse/Chalmuns/internal/output"
	"github.com/ButtersesHouse/Chalmuns/internal/state"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: pattern-learner <subcommand> [flags]")
		fmt.Fprintln(os.Stderr, "subcommands: detect-repo, state-read, state-write, write-outputs")
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "detect-repo":
		err = runDetectRepo()
	case "state-read":
		err = runStateRead(os.Args[2:])
	case "state-write":
		err = runStateWrite(os.Args[2:])
	case "write-outputs":
		err = runWriteOutputs(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runDetectRepo() error {
	return detect.Run()
}

func runStateRead(args []string) error {
	path := flagValue(args, "--state", "")
	if path == "" {
		return fmt.Errorf("--state required")
	}
	s, err := state.Read(path)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}

func runStateWrite(args []string) error {
	path := flagValue(args, "--state", "")
	if path == "" {
		return fmt.Errorf("--state required")
	}

	var s state.State
	if err := json.NewDecoder(os.Stdin).Decode(&s); err != nil {
		return fmt.Errorf("decode stdin: %w", err)
	}

	if err := os.MkdirAll(dirOf(path), 0755); err != nil {
		return err
	}
	return state.Write(path, s)
}

func runWriteOutputs(args []string) error {
	statePath := flagValue(args, "--state", "")
	outputDir := flagValue(args, "--output-dir", ".")
	if statePath == "" {
		return fmt.Errorf("--state required")
	}
	ragHints := hasFlag(args, "--rag-hints")
	ragAnchor := hasFlag(args, "--rag")

	s, err := state.Read(statePath)
	if err != nil {
		return err
	}

	if ragAnchor {
		anchorExamplesRAG(&s, outputDir)
	} else {
		anchorExamples(&s, outputDir)
	}

	return output.Write(s, outputDir, output.Options{RAGHints: ragHints})
}

// anchorExamplesRAG uses cursor-agent to semantically find real codebase instances
// of each approved rule's pattern and sets FileRef when found. Falls back to
// grep-based anchorExamples if cursor-agent is unavailable or returns no result.
func anchorExamplesRAG(s *state.State, outputDir string) {
	// Check cursor-agent is available before looping over rules.
	if _, err := os.Stat("/dev/null"); err != nil { // dummy — actual check below
	}
	cursorAvail := isCursorAgentAvailable()
	if !cursorAvail {
		anchorExamples(s, outputDir)
		return
	}

	for i := range s.Rules {
		r := &s.Rules[i]
		if r.Status != "approved" || len(r.DoExamples) == 0 {
			continue
		}
		if r.DoExamples[0].FileRef != "" {
			continue
		}

		prompt := fmt.Sprintf(
			"Find one real example of the pattern '%s' in this codebase. "+
				"Return ONLY: the file path relative to the repo root and the line number, "+
				"formatted exactly as: FILE:LLINE (e.g. internal/api/handler.go:L42). "+
				"No prose, no explanation — just FILE:Lline on a single line.",
			r.Title,
		)

		out, err := runCursorAgent(prompt)
		if err != nil || strings.TrimSpace(out) == "" {
			// Fall back to grep for this rule.
			anchorSingleRule(r, outputDir)
			continue
		}

		// Extract the first token that looks like path:Lnum.
		ref := extractFileRef(out)
		if ref != "" {
			r.DoExamples[0].FileRef = ref
		} else {
			anchorSingleRule(r, outputDir)
		}
	}
}

// isCursorAgentAvailable checks whether cursor-agent is on PATH.
func isCursorAgentAvailable() bool {
	_, err := findExecutable("cursor-agent")
	return err == nil
}

// findExecutable walks PATH to find an executable — stdlib equivalent of exec.LookPath
// without importing os/exec (keeps zero-dep policy).
func findExecutable(name string) (string, error) {
	pathEnv := os.Getenv("PATH")
	for _, dir := range strings.Split(pathEnv, ":") {
		full := filepath.Join(dir, name)
		info, err := os.Stat(full)
		if err == nil && info.Mode()&0111 != 0 {
			return full, nil
		}
	}
	return "", fmt.Errorf("%s not found in PATH", name)
}

// runCursorAgent runs cursor-agent with -p --mode=ask and returns stdout.
// Uses os.StartProcess to avoid importing os/exec.
func runCursorAgent(prompt string) (string, error) {
	bin, err := findExecutable("cursor-agent")
	if err != nil {
		return "", err
	}

	// Write prompt to a temp file to avoid shell escaping issues.
	tmp, err := os.CreateTemp("", "cursor-prompt-*.txt")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(prompt); err != nil {
		tmp.Close()
		return "", err
	}
	tmp.Close()

	// cursor-agent -p --mode=ask "<prompt>"
	outFile, err := os.CreateTemp("", "cursor-out-*.txt")
	if err != nil {
		return "", err
	}
	defer os.Remove(outFile.Name())
	outPath := outFile.Name()
	outFile.Close()

	proc, err := os.StartProcess(bin, []string{bin, "-p", "--mode=ask", prompt},
		&os.ProcAttr{
			Files: []*os.File{nil, func() *os.File { f, _ := os.Create(outPath); return f }(), os.Stderr},
		})
	if err != nil {
		return "", err
	}
	ps, err := proc.Wait()
	if err != nil || !ps.Success() {
		return "", fmt.Errorf("cursor-agent exited non-zero")
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// extractFileRef scans text for the first token matching path:Lnum.
func extractFileRef(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		// Must contain :L followed by digits.
		if idx := strings.Index(line, ":L"); idx > 0 {
			candidate := line[0 : idx+2]
			rest := line[idx+2:]
			digits := ""
			for _, ch := range rest {
				if ch >= '0' && ch <= '9' {
					digits += string(ch)
				} else {
					break
				}
			}
			if len(digits) > 0 && !strings.Contains(candidate, " ") {
				return candidate + digits
			}
		}
	}
	return ""
}

// anchorSingleRule is the grep fallback for one rule.
func anchorSingleRule(r *state.Rule, outputDir string) {
	if len(r.DoExamples) == 0 || len(r.Target.FileGlob) == 0 {
		return
	}
	token := firstMeaningfulLine(r.DoExamples[0].Code)
	if len(token) < 10 {
		return
	}
	for _, glob := range r.Target.FileGlob {
		matches, err := filepath.Glob(filepath.Join(outputDir, glob))
		if err != nil {
			continue
		}
		for _, file := range matches {
			lineNum, ok := findInFile(file, token)
			if !ok {
				continue
			}
			rel, err := filepath.Rel(outputDir, file)
			if err != nil {
				rel = file
			}
			r.DoExamples[0].FileRef = fmt.Sprintf("%s:L%d", rel, lineNum)
			return
		}
	}
}

// anchorExamples does a best-effort grep search for real codebase instances of
// each approved rule's first do_example and sets FileRef when found. Errors are
// silently ignored — this is advisory metadata only.
func anchorExamples(s *state.State, outputDir string) {
	for i := range s.Rules {
		r := &s.Rules[i]
		if r.Status != "approved" || len(r.DoExamples) == 0 || len(r.Target.FileGlob) == 0 {
			continue
		}
		if r.DoExamples[0].FileRef != "" {
			continue
		}
		anchorSingleRule(&s.Rules[i], outputDir)
	}
}

// firstMeaningfulLine returns the first non-blank, non-comment line from code.
func firstMeaningfulLine(code string) string {
	for _, line := range strings.Split(code, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "//") || strings.HasPrefix(t, "#") ||
			strings.HasPrefix(t, "*") || strings.HasPrefix(t, "/*") {
			continue
		}
		return t
	}
	return ""
}

// findInFile searches filename line-by-line for substring and returns the line number.
func findInFile(filename, substring string) (int, bool) {
	f, err := os.Open(filename)
	if err != nil {
		return 0, false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if strings.Contains(scanner.Text(), substring) {
			return lineNum, true
		}
	}
	return 0, false
}

// hasFlag reports whether a boolean flag appears in args.
func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// flagValue extracts --flag value from an args slice.
func flagValue(args []string, flag, def string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return def
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}
