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

	s, err := state.Read(statePath)
	if err != nil {
		return err
	}
	anchorExamples(&s, outputDir)
	return output.Write(s, outputDir)
}

// anchorExamples does a best-effort search for real codebase instances of each
// approved rule's first do_example and sets FileRef when found. Errors are
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
		token := firstMeaningfulLine(r.DoExamples[0].Code)
		if len(token) < 10 {
			continue
		}
		for _, glob := range r.Target.FileGlob {
			matches, err := filepath.Glob(filepath.Join(outputDir, glob))
			if err != nil {
				continue
			}
			found := false
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
				found = true
				break
			}
			if found {
				break
			}
		}
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
