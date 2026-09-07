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
	"github.com/ButtersesHouse/Chalmuns/internal/format"
	"github.com/ButtersesHouse/Chalmuns/internal/guard"
	"github.com/ButtersesHouse/Chalmuns/internal/output"
	"github.com/ButtersesHouse/Chalmuns/internal/pipeline"
	"github.com/ButtersesHouse/Chalmuns/internal/state"
)

// Version identifies the binary's output contract. SKILL.md Step 2 compares
// it against the version it expects and rebuilds on mismatch, so a binary
// built from older source cannot silently keep producing the old output
// (the usage listing alone cannot tell two builds apart when the subcommand
// set is unchanged). Bump it whenever generated output or a subcommand's
// behaviour changes, and update the expected value in SKILL.md and the
// plugin manifest to match.
const Version = "0.3.0"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: pattern-learner <subcommand> [flags]")
		fmt.Fprintln(os.Stderr, "subcommands: detect-repo, state-read, state-write, write-outputs,")
		fmt.Fprintln(os.Stderr, "             extract-lean, verify-grounding, classify, triage,")
		fmt.Fprintln(os.Stderr, "             audit-format, promote, guard, version")
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "version":
		fmt.Println(Version)
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
	case "audit-format":
		err = format.RunAuditFormat(os.Args[2:])
	case "promote":
		err = runPromote(os.Args[2:])
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

	// state.Read treats a missing file as an empty state, which is right for
	// a first run of the pipeline but not here: write-outputs prunes every
	// generated skill absent from state, so a mistyped --state would delete
	// them all. Require the file to exist.
	if _, err := os.Stat(statePath); err != nil {
		return fmt.Errorf("--state %s: %w (write-outputs needs an existing state file; a missing one would prune every generated skill)", statePath, err)
	}
	s, err := state.Read(statePath)
	if err != nil {
		return err
	}

	// Every cheap, deterministic check first: a bad flag, domain name,
	// glob, or a skill directory the run would refuse to overwrite. The
	// anchoring below walks the repository (or calls cursor-agent per
	// rule) and should not run for a state the write would then reject.
	owner, err := resolveOwner(flagValue(args, "--repo", ""), s, outputDir)
	if err != nil {
		return err
	}
	opts := output.Options{
		RAGHints:  ragHints,
		SkillsDir: flagValue(args, "--skills-dir", ""),
		Owner:     owner,
		RepoRoot:  repoRoot(statePath, outputDir),
	}
	prepared, err := output.Validate(&s, outputDir, opts)
	if err != nil {
		return err
	}

	// Anchoring enriches s in place; Write reads it through the pointer
	// Validate holds.
	if ragAnchor {
		anchorExamplesRAG(&s, outputDir)
	} else {
		anchorExamples(&s, outputDir)
	}

	err = prepared.Write()
	for _, w := range prepared.Warnings() {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	return err
}

// repoRoot finds the root of the repository a run writes for, which decides
// whether the skills directory is the repo's own or a shared one. The state
// file lives inside the repository, so its git top level is authoritative;
// when the state is not under git, fall back to outputDir. Using the
// current directory alone would misclassify a user-level skills directory
// as repo-owned whenever the command is run from one of its ancestors.
func repoRoot(statePath, outputDir string) string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = filepath.Dir(statePath)
	if out, err := cmd.Output(); err == nil {
		if top := strings.TrimSpace(string(out)); top != "" {
			return top
		}
	}
	return outputDir
}

// resolveOwner picks the "owner/repo" identity stamped into generated skills,
// in a fixed order so it cannot flip between runs: an explicit --repo flag,
// then the state's repo field, then the git remote of the output directory
// (the same detection detect-repo performs). Every branch produces the value
// through output.OwnerKey so stamps compare equal across runs. Empty when
// none is available.
func resolveOwner(flag string, s state.State, outputDir string) (string, error) {
	if flag != "" {
		key := output.NormalizeOwnerKey(flag)
		if key == "" {
			return "", fmt.Errorf("--repo must be \"owner/repo\" (got %q)", flag)
		}
		return key, nil
	}
	if key := output.OwnerFromState(s); key != "" {
		return key, nil
	}
	if r, err := detect.Detect(outputDir); err == nil {
		return output.OwnerKey(r.Owner, r.Repo), nil
	}
	return "", nil
}

// runPromote publishes the conventions into a top-level agent instruction
// file. Separate from write-outputs on purpose: a pipeline run never writes
// above the skills directory, so escalating to the repo root stays an
// explicit act. The write is confined to a marked block, leaving any
// hand-written content in the file intact.
//
// Usage: promote --state <path> [--agents-md P] [--claude-md P]
//
//	[--skills-dir D] [--create]
func runPromote(args []string) error {
	statePath := flagValue(args, "--state", "")
	if statePath == "" {
		return fmt.Errorf("--state required")
	}
	outputDir := flagValue(args, "--output-dir", ".")

	s, err := state.Read(statePath)
	if err != nil {
		return err
	}

	skillsDir := flagValue(args, "--skills-dir", "")
	if skillsDir == "" {
		skillsDir = filepath.Join(outputDir, ".claude", "skills")
	}

	// Default to AGENTS.md (the cross-agent convention) when no target is
	// named; agents other than Claude Code have no other entry point.
	agentsMD := flagValue(args, "--agents-md", "")
	claudeMD := flagValue(args, "--claude-md", "")
	var targets []string
	if agentsMD != "" {
		targets = append(targets, agentsMD)
	}
	if claudeMD != "" {
		targets = append(targets, claudeMD)
	}
	if len(targets) == 0 {
		targets = append(targets, filepath.Join(outputDir, "AGENTS.md"))
	}

	opts := output.PromoteOptions{SkillsDir: skillsDir, Create: hasFlag(args, "--create")}
	results := make([]output.PromoteResult, 0, len(targets))
	for _, t := range targets {
		res, err := output.Promote(s, t, opts)
		if err != nil {
			return err
		}
		results = append(results, res)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(results)
}

// anchorExamplesRAG uses cursor-agent to semantically find real codebase instances
// of each approved rule's pattern and sets FileRef when found. Falls back to
// grep-based anchorExamples if cursor-agent is unavailable or returns no result.
func anchorExamplesRAG(s *state.State, outputDir string) {
	if !isCursorAgentAvailable() {
		anchorExamples(s, outputDir)
		return
	}

	matcher := newGlobMatcher(outputDir, approvedGlobs(s))
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
			anchorSingleRule(r, outputDir, matcher)
			continue
		}

		// Extract the first token that looks like path:Lnum, and only trust
		// it once verified against the actual file — cursor-agent output is
		// not provenance until it is grounded.
		ref := extractFileRef(out)
		if ref != "" && refExists(ref, outputDir) {
			r.DoExamples[0].FileRef = ref
		} else {
			anchorSingleRule(r, outputDir, matcher)
		}
	}
}

// approvedGlobs collects every file glob an approved rule carries, so one
// matcher can resolve them all in a single walk.
func approvedGlobs(s *state.State) []string {
	var globs []string
	for _, r := range s.Rules {
		if r.Status == "approved" {
			globs = append(globs, r.Target.FileGlob...)
		}
	}
	return globs
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
	if filepath.IsAbs(rel) || output.IsOutsideRel(rel) {
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
func anchorSingleRule(r *state.Rule, outputDir string, matcher *globMatcher) {
	if len(r.DoExamples) == 0 || len(r.Target.FileGlob) == 0 {
		return
	}
	token := firstMeaningfulLine(r.DoExamples[0].Code)
	if len(token) < 10 {
		return
	}
	for _, glob := range r.Target.FileGlob {
		for _, file := range matcher.files(glob) {
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
	matcher := newGlobMatcher(outputDir, approvedGlobs(s))
	for i := range s.Rules {
		r := &s.Rules[i]
		if r.Status != "approved" || len(r.DoExamples) == 0 || len(r.Target.FileGlob) == 0 {
			continue
		}
		if r.DoExamples[0].FileRef != "" {
			continue
		}
		anchorSingleRule(&s.Rules[i], outputDir, matcher)
	}
}

// globMatcher resolves the files matching each of a fixed set of globs
// with a single walk of the tree, however many rules share a glob or how
// many "**" globs there are; the previous per-rule, per-glob walks grew
// linearly with the rule count. Unlike filepath.Glob it supports "**" for
// any number of directories — the form SKILL.md instructs subagents to emit
// (e.g. "src/api/**/*.go") — and brace groups, expanded the same way the
// skill frontmatter expands them.
type globMatcher struct {
	root    string
	globs   []string
	matches map[string][]string
	walked  bool
}

func newGlobMatcher(root string, globs []string) *globMatcher {
	return &globMatcher{root: root, globs: globs}
}

// files returns the files under root matching glob. The walk happens on
// the first call and covers every glob the matcher was built with; a glob
// it was not built with costs a walk of its own.
func (m *globMatcher) files(glob string) []string {
	if !m.walked {
		m.walkAll()
	}
	if files, ok := m.matches[glob]; ok {
		return files
	}
	return newGlobMatcher(m.root, []string{glob}).files(glob)
}

// walkAll resolves every registered glob: plain globs through
// filepath.Glob, "**" globs together in one traversal of the tree.
func (m *globMatcher) walkAll() {
	m.walked = true
	m.matches = map[string][]string{}
	type walkPattern struct {
		glob string
		segs []string
	}
	var walkPatterns []walkPattern
	for _, glob := range m.globs {
		if _, seen := m.matches[glob]; seen {
			continue
		}
		m.matches[glob] = nil
		expanded, err := output.ExpandBraces(glob)
		if err != nil {
			// A malformed glob is refused by write-outputs itself;
			// anchoring is advisory and simply finds nothing for it.
			continue
		}
		for _, g := range expanded {
			if !strings.Contains(g, "**") {
				found, _ := filepath.Glob(filepath.Join(m.root, g))
				m.matches[glob] = append(m.matches[glob], found...)
				continue
			}
			walkPatterns = append(walkPatterns, walkPattern{glob, strings.Split(path.Clean(filepath.ToSlash(g)), "/")})
		}
	}
	if len(walkPatterns) == 0 {
		return
	}
	lastMatched := map[string]int{}
	generation := 0
	filepath.WalkDir(m.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(m.root, p)
		if err != nil {
			return nil
		}
		segs := strings.Split(filepath.ToSlash(rel), "/")
		// A glob with several "**" alternatives must list a file once;
		// the generation counter dedups without allocating per file.
		generation++
		for _, wp := range walkPatterns {
			if lastMatched[wp.glob] == generation {
				continue
			}
			if matchSegments(wp.segs, segs) {
				m.matches[wp.glob] = append(m.matches[wp.glob], p)
				lastMatched[wp.glob] = generation
			}
		}
		return nil
	})
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
