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
	"runtime"
	"strconv"
	"strings"

	"github.com/ButtersesHouse/Chalmuns/internal/cliflags"
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
	if err := cliflags.Check(args, []string{"--state"}, nil); err != nil {
		return err
	}
	path := cliflags.Value(args, "--state", "")
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
	if err := cliflags.Check(args, []string{"--state"}, nil); err != nil {
		return err
	}
	path := cliflags.Value(args, "--state", "")
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
	if err := cliflags.Check(args,
		[]string{"--state", "--output-dir", "--skills-dir", "--repo"},
		[]string{"--rag", "--rag-hints"}); err != nil {
		return err
	}
	statePath := cliflags.Value(args, "--state", "")
	if statePath == "" {
		return fmt.Errorf("--state required")
	}
	// state.Read treats a missing file as an empty state, which is right for
	// a first run of the pipeline but not here: write-outputs prunes every
	// generated skill absent from state, so a mistyped --state would delete
	// them all. Require the file to exist, before any other work.
	if _, err := os.Stat(statePath); err != nil {
		return fmt.Errorf("--state %s: %w (write-outputs needs an existing state file; a missing one would prune every generated skill)", statePath, err)
	}
	// The output directory (default skills location, anchoring root) is
	// the repository the state belongs to, not wherever the command was
	// run from; an explicit --output-dir overrides it. The repository root
	// is resolved once and reused for the shared-directory judgement.
	// The output directory is the project the state belongs to (the
	// directory above its .claude/), which in a monorepo is not the git
	// top level; the top level only decides whether a skills directory is
	// the repository's own or shared.
	outputDir := cliflags.Value(args, "--output-dir", "")
	if outputDir == "" {
		outputDir = projectDir(statePath)
	}
	root, inGit := repoRoot(statePath)
	if !inGit {
		// No git top level: the output dir is the best root.
		root = outputDir
	}
	ragHints := cliflags.Has(args, "--rag-hints")
	ragAnchor := cliflags.Has(args, "--rag")

	s, err := state.Read(statePath)
	if err != nil {
		return err
	}

	// Every cheap, deterministic check first: a bad flag, domain name,
	// glob, or a skill directory the run would refuse to overwrite. The
	// anchoring below walks the repository (or calls cursor-agent per
	// rule) and should not run for a state the write would then reject.
	owner, err := resolveOwner(cliflags.Value(args, "--repo", ""), s, root)
	if err != nil {
		return err
	}
	opts := output.Options{
		RAGHints:  ragHints,
		SkillsDir: output.ResolveSkillsDir(cliflags.Value(args, "--skills-dir", ""), outputDir),
		Owner:     owner,
		RepoRoot:  root,
	}
	prepared, err := output.Validate(&s, outputDir, opts)
	if err != nil {
		return err
	}

	// Anchoring enriches s in place; Write reads it through the pointer
	// Validate holds.
	if ragAnchor {
		anchorExamplesRAG(&s, outputDir, opts.SkillsDir)
	} else {
		anchorExamples(&s, outputDir, opts.SkillsDir)
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
func repoRoot(statePath string) (root string, inGit bool) {
	// Ask git from the state's logical location, not through whatever the
	// path resolves to physically: a .claude/ that is a symlink into a
	// dotfiles repository would otherwise name that repository.
	project := projectDir(statePath)
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = project
	if out, err := cmd.Output(); err == nil {
		if top := strings.TrimSpace(string(out)); top != "" {
			return top, true
		}
	}
	// Not under git: the project directory is still the repository this
	// run is for, never the process cwd.
	return project, false
}

// projectDir is the directory a state file belongs to: by convention the
// state lives at <project>/.claude/pattern-learner/state.json, so the
// directory above .claude/; any other layout means the state's own
// directory. It is absolute so later joins do not depend on the cwd.
func projectDir(statePath string) string {
	dir := filepath.Dir(statePath)
	if filepath.Base(dir) == "pattern-learner" && filepath.Base(filepath.Dir(dir)) == ".claude" {
		dir = filepath.Dir(filepath.Dir(dir))
	}
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

// resolveOwner picks the "owner/repo" identity stamped into generated skills,
// in a fixed order so it cannot flip between runs: an explicit --repo flag,
// then the state's repo field, then the git remote of the repository root
// (the same detection detect-repo performs, run where the state lives
// rather than in the current directory, which may be an unrelated
// ancestor repository). Every branch produces the value through
// output.NormalizeOwnerKey so stamps compare equal across runs. Empty when
// none is available; an unusable value in state or the flag is an error
// rather than a silent unstamped run, since unstamped skills in a shared
// directory are never pruned.
func resolveOwner(flag string, s state.State, repoRoot string) (string, error) {
	if flag != "" {
		key := output.NormalizeOwnerKey(flag)
		if key == "" {
			return "", fmt.Errorf("--repo must be \"owner/repo\" (got %q)", flag)
		}
		return key, nil
	}
	if s.Repo.Owner != "" || s.Repo.Repo != "" {
		key := output.OwnerFromState(s)
		if key == "" {
			return "", fmt.Errorf("state.repo {owner: %q, repo: %q} is not a usable owner/repo identity (one segment each, no whitespace); set it from detect-repo's output (Step 11) or pass --repo", s.Repo.Owner, s.Repo.Repo)
		}
		return key, nil
	}
	if r, err := detect.Detect(repoRoot); err == nil {
		key := output.OwnerKey(r.Owner, r.Repo)
		if key == "" {
			return "", fmt.Errorf("the git remote of %s parses to {owner: %q, repo: %q}, which is not a usable owner/repo identity; pass --repo owner/repo", repoRoot, r.Owner, r.Repo)
		}
		return key, nil
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
	if err := cliflags.Check(args,
		[]string{"--state", "--output-dir", "--skills-dir", "--agents-md", "--claude-md"},
		[]string{"--create"}); err != nil {
		return err
	}
	statePath := cliflags.Value(args, "--state", "")
	if statePath == "" {
		return fmt.Errorf("--state required")
	}
	// Same default as write-outputs: the repository the state lives in.
	outputDir := cliflags.Value(args, "--output-dir", "")
	if outputDir == "" {
		outputDir = projectDir(statePath)
	}

	s, err := state.Read(statePath)
	if err != nil {
		return err
	}

	skillsDir := output.ResolveSkillsDir(cliflags.Value(args, "--skills-dir", ""), outputDir)
	promoteRoot, inGit := repoRoot(statePath)
	if !inGit {
		promoteRoot = outputDir
	}

	// Default to AGENTS.md (the cross-agent convention) when no target is
	// named; agents other than Claude Code have no other entry point.
	// Relative targets are taken against the output directory, like the
	// default, so the file lands in the repository wherever the command
	// was run from.
	agentsMD := cliflags.Value(args, "--agents-md", "")
	claudeMD := cliflags.Value(args, "--claude-md", "")
	var targets []string
	for _, t := range []string{agentsMD, claudeMD} {
		if t == "" {
			continue
		}
		if !filepath.IsAbs(t) {
			t = filepath.Join(outputDir, t)
		}
		targets = append(targets, t)
	}
	if len(targets) == 0 {
		targets = append(targets, filepath.Join(outputDir, "AGENTS.md"))
	}

	opts := output.PromoteOptions{
		SkillsDir: skillsDir,
		Create:    cliflags.Has(args, "--create"),
		// Inside the repository a link must hold on every checkout, so it
		// is written relative to the target file. The judgement is made
		// against the git top level, exactly as write-outputs makes it
		// (repoRoot, and Options.RepoRoot's doc on why outputDir alone is
		// not safe): outputDir is the project the state belongs to, which
		// in a monorepo service — or under any --output-dir below the root
		// — is not the root, so a skills directory write-outputs correctly
		// calls the repository's own was called shared here and promoted
		// with absolute, machine-local links.
		RelativeLinks: output.IsInside(skillsDir, promoteRoot),
	}
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
func anchorExamplesRAG(s *state.State, outputDir, skillsDir string) {
	if !isCursorAgentAvailable() {
		anchorExamples(s, outputDir, skillsDir)
		return
	}

	matcher := newGlobMatcher(outputDir, approvedGlobs(s), skillsDir)
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
func anchorExamples(s *state.State, outputDir, skillsDir string) {
	matcher := newGlobMatcher(outputDir, approvedGlobs(s), skillsDir)
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
	// skipDirs are absolute directories the walk does not descend into,
	// beyond the fixed skipNames below.
	skipDirs map[string]bool
}

// skipNames are directories whose contents are never a real instance of the
// team's own convention: version-control metadata, and the two conventional
// dependency trees. Anchoring a rule to third-party code would put someone
// else's file under "Real instance" and in "Exemplary Files", which tells
// the reader to imitate it.
var skipNames = map[string]bool{".git": true, "node_modules": true, "vendor": true}

func newGlobMatcher(root string, globs []string, skip ...string) *globMatcher {
	skipped := map[string]bool{}
	for _, dir := range skip {
		if dir == "" {
			continue
		}
		if abs, err := filepath.Abs(dir); err == nil {
			skipped[abs] = true
		}
	}
	return &globMatcher{root: root, globs: globs, skipDirs: skipped}
}

// files returns the files under root matching glob. The walk happens on
// the first call and covers every glob the matcher was built with.
func (m *globMatcher) files(glob string) []string {
	if !m.walked {
		m.walkAll()
	}
	// Every caller registers all its globs up front; an unregistered one
	// simply has no matches rather than costing a walk of its own.
	return m.matches[glob]
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
			// Globs are repository-relative; a leading "/" or "./" the
			// model sometimes emits means the same thing.
			cleaned := strings.TrimPrefix(path.Clean(filepath.ToSlash(g)), "/")
			if output.EscapesRoot(g) {
				// A glob that climbs out of the repository anchors nothing:
				// a reference outside the repo is refused on the RAG path
				// (refExists) and would be no use to a reader here either.
				continue
			}
			if !strings.Contains(cleaned, "**") {
				// A plain glob is a few directory listings; only "**"
				// needs the tree walk. The root is escaped so that glob
				// metacharacters in the repository path ("proj[1]") are
				// not read as a pattern.
				found, _ := filepath.Glob(filepath.Join(escapeGlobMeta(m.root), filepath.FromSlash(cleaned)))
				m.matches[glob] = append(m.matches[glob], found...)
				continue
			}
			walkPatterns = append(walkPatterns, walkPattern{glob, strings.Split(cleaned, "/")})
		}
	}
	defer m.dedupMatches()
	if len(walkPatterns) == 0 {
		return
	}
	filepath.WalkDir(m.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipNames[d.Name()] || m.skipDirs[p] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(m.root, p)
		if err != nil {
			return nil
		}
		segs := strings.Split(filepath.ToSlash(rel), "/")
		// A file matched by several alternatives of one glob is listed
		// once by the deferred dedupMatches.
		for _, wp := range walkPatterns {
			if matchSegments(wp.segs, segs) {
				m.matches[wp.glob] = append(m.matches[wp.glob], p)
			}
		}
		return nil
	})
}

// dedupMatches lists each file once per glob: a group with both a plain
// and a "**" alternative can find the same file by both routes.
func (m *globMatcher) dedupMatches() {
	for glob, files := range m.matches {
		kept := files[:0]
		for _, f := range files {
			if !m.skipped(f) {
				kept = append(kept, f)
			}
		}
		m.matches[glob] = output.DedupeStrings(kept)
	}
}

// skipped reports whether a matched file lies under a directory anchoring
// must not cite. The tree walk already prunes those, but the plain-glob
// route goes through filepath.Glob, which does no walking and would still
// return e.g. `.claude/skills/api/examples/x.md` for "**"-free patterns.
func (m *globMatcher) skipped(file string) bool {
	rel, err := filepath.Rel(m.root, file)
	if err != nil {
		return false
	}
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if skipNames[seg] {
			return true
		}
	}
	if len(m.skipDirs) == 0 {
		return false
	}
	// skipDirs holds absolute paths, so compare on absolute ones: root (and
	// therefore every match built from it) may be relative.
	abs, err := filepath.Abs(file)
	if err != nil {
		return false
	}
	for d := filepath.Dir(abs); ; {
		if m.skipDirs[d] {
			return true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return false
		}
		d = parent
	}
}

// escapeGlobMeta quotes the glob metacharacters in a literal path so it can
// be joined with a pattern for filepath.Glob. Outside Windows a backslash
// escape quotes every metacharacter, the backslash itself included (a
// bracket class cannot hold one: "[\]" reads as an escaped "]"). On
// Windows the backslash is the separator and no escape character exists,
// so the three remaining metacharacters go in bracket classes.
func escapeGlobMeta(p string) string {
	var b strings.Builder
	for _, r := range p {
		switch {
		case runtime.GOOS != "windows" && (r == '*' || r == '?' || r == '[' || r == '\\'):
			b.WriteRune('\\')
			b.WriteRune(r)
		case runtime.GOOS == "windows" && (r == '*' || r == '?' || r == '['):
			b.WriteRune('[')
			b.WriteRune(r)
			b.WriteRune(']')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
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

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}
