package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ButtersesHouse/Chalmuns/internal/detect"
	"github.com/ButtersesHouse/Chalmuns/internal/guard"
	"github.com/ButtersesHouse/Chalmuns/internal/output"
	"github.com/ButtersesHouse/Chalmuns/internal/pipeline"
	"github.com/ButtersesHouse/Chalmuns/internal/state"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: pattern-learner <subcommand> [flags]")
		fmt.Fprintln(os.Stderr, "subcommands: detect-repo, state-read, state-write, write-outputs,")
		fmt.Fprintln(os.Stderr, "             extract-lean, verify-grounding, classify, triage, guard")
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
	case "extract-lean":
		err = pipeline.RunExtractLean(os.Args[2:])
	case "verify-grounding":
		err = pipeline.RunVerifyGrounding(os.Args[2:])
	case "classify":
		err = pipeline.RunClassify(os.Args[2:])
	case "triage":
		err = pipeline.RunTriage(os.Args[2:])
	case "guard":
		err = guard.Run()
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

	opts := output.Options{
		RAGHints:     ragHints,
		ClaudeMDPath: flagValue(args, "--claude-md", ""),
		SkillsDir:    flagValue(args, "--skills-dir", ""),
	}
	return output.Write(s, outputDir, opts)
}

// anchorExamplesRAG uses cursor-agent to semantically find real codebase instances
// of each approved rule's pattern and sets FileRef when found. Falls back to
// grep-based anchorExamples if cursor-agent is unavailable or returns no result.
func anchorExamplesRAG(s *state.State, outputDir string) {
	if !isCursorAgentAvailable() {
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

		// Extract the first token that looks like path:Lnum, and only trust
		// it once verified against the actual file — cursor-agent output is
		// not provenance until it is grounded.
		ref := extractFileRef(out)
		if ref != "" && refExists(ref, outputDir) {
			r.DoExamples[0].FileRef = ref
		} else {
			anchorSingleRule(r, outputDir)
		}
	}
}

// isCursorAgentAvailable checks whether cursor-agent is on PATH.
func isCursorAgentAvailable() bool {
	_, err := exec.LookPath("cursor-agent")
	return err == nil
}

// runCursorAgent runs `cursor-agent -p --mode=ask <prompt>` and returns stdout.
func runCursorAgent(prompt string) (string, error) {
	cmd := exec.Command("cursor-agent", "-p", "--mode=ask", prompt)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("cursor-agent: %w", err)
	}
	return string(out), nil
}

// refExists reports whether ref ("path/file.go:L42") names an existing file
// under root with at least that many lines.
func refExists(ref, root string) bool {
	idx := strings.LastIndex(ref, ":L")
	if idx <= 0 {
		return false
	}
	n, err := strconv.Atoi(ref[idx+2:])
	if err != nil || n < 1 {
		return false
	}
	rel := filepath.Clean(ref[:idx])
	if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	f, err := os.Open(filepath.Join(root, rel))
	if err != nil {
		return false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	lines := 0
	for scanner.Scan() {
		lines++
		if lines >= n {
			return true
		}
	}
	return false
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
		for _, file := range globFiles(outputDir, glob) {
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

// globFiles returns files under root matching glob. Unlike filepath.Glob it
// supports "**" for any number of directories — the form SKILL.md instructs
// subagents to emit (e.g. "src/api/**/*.go").
func globFiles(root, glob string) []string {
	if !strings.Contains(glob, "**") {
		matches, _ := filepath.Glob(filepath.Join(root, glob))
		return matches
	}
	pat := strings.Split(path.Clean(filepath.ToSlash(glob)), "/")
	var out []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		if matchSegments(pat, strings.Split(filepath.ToSlash(rel), "/")) {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// matchSegments matches path segments against pattern segments where "**"
// matches zero or more segments and other segments use path.Match rules.
func matchSegments(pat, segs []string) bool {
	if len(pat) == 0 {
		return len(segs) == 0
	}
	if pat[0] == "**" {
		if matchSegments(pat[1:], segs) {
			return true
		}
		return len(segs) > 0 && matchSegments(pat, segs[1:])
	}
	if len(segs) == 0 {
		return false
	}
	if ok, err := path.Match(pat[0], segs[0]); err != nil || !ok {
		return false
	}
	return matchSegments(pat[1:], segs[1:])
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
