package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ButtersesHouse/Chalmuns/internal/output"
	"github.com/ButtersesHouse/Chalmuns/internal/state"
)

// globFiles resolves one glob through a matcher built for it alone.
func globFiles(root, glob string) []string {
	return newGlobMatcher(root, []string{glob}).files(glob)
}

// writeTree creates files (with contents) under root, making parent dirs.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGlobFiles_doubleStar(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"src/api/handlers/users.go":      "package handlers\n",
		"src/api/handlers/deep/more.go":  "package deep\n",
		"src/api/users.go":               "package api\n",
		"src/web/handlers/other.go":      "package web\n",
		"src/api/handlers/users_test.js": "// js\n",
	})

	got := globFiles(root, "src/api/**/*.go")
	want := map[string]bool{
		filepath.Join(root, "src/api/handlers/users.go"):     true,
		filepath.Join(root, "src/api/handlers/deep/more.go"): true,
		filepath.Join(root, "src/api/users.go"):              true, // ** matches zero dirs
	}
	if len(got) != len(want) {
		t.Fatalf("want %d matches, got %d: %v", len(want), len(got), got)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected match: %s", g)
		}
	}
}

func TestGlobFiles_singleStarUnchanged(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"a/x.go":   "x\n",
		"a/b/y.go": "y\n",
	})
	got := globFiles(root, "a/*.go")
	if len(got) != 1 || got[0] != filepath.Join(root, "a/x.go") {
		t.Errorf("single-star glob should not recurse; got %v", got)
	}
}

func TestAnchorSingleRule_doubleStarGlob(t *testing.T) {
	root := t.TempDir()
	code := "ctx, cancel := context.WithTimeout(ctx, 5*time.Second)"
	writeTree(t, root, map[string]string{
		"src/api/handlers/users.go": "package handlers\n\nfunc f() {\n\t" + code + "\n}\n",
	})
	r := &state.Rule{
		Status:     "approved",
		DoExamples: []state.Example{{Code: code, Language: "go"}},
		Target:     state.Target{Location: "api", FileGlob: []string{"src/api/**/*.go"}},
	}
	anchorSingleRule(r, root, newGlobMatcher(root, r.Target.FileGlob))
	want := "src/api/handlers/users.go:L4"
	if r.DoExamples[0].FileRef != want {
		t.Errorf("FileRef: want %q, got %q", want, r.DoExamples[0].FileRef)
	}
}

func TestRefExists(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"pkg/a.go": "l1\nl2\nl3\n",
	})
	cases := []struct {
		ref  string
		want bool
	}{
		{"pkg/a.go:L2", true},
		{"pkg/a.go:L3", true},
		{"pkg/a.go:L4", false},   // beyond EOF
		{"pkg/b.go:L1", false},   // no such file
		{"pkg/a.go", false},      // no line ref
		{"../a.go:L1", false},    // escapes root
		{"/etc/hosts:L1", false}, // absolute
	}
	for _, c := range cases {
		if got := refExists(c.ref, root); got != c.want {
			t.Errorf("refExists(%q) = %v, want %v", c.ref, got, c.want)
		}
	}
}

func TestExtractFileRef(t *testing.T) {
	out := "Sure! Here it is:\ninternal/api/handler.go:L42\n"
	if got := extractFileRef(out); got != "internal/api/handler.go:L42" {
		t.Errorf("got %q", got)
	}
	if got := extractFileRef("no ref here"); got != "" {
		t.Errorf("want empty, got %q", got)
	}
}

// --- promote subcommand ---

func promoteState(t *testing.T, dir string) string {
	t.Helper()
	s := state.Empty()
	s.Rules = []state.Rule{{
		ID: "r1", Title: "No abbreviations", Rule: "Never abbreviate identifiers.",
		Status: "approved", Confidence: "stated",
		Target:  state.Target{Location: "CLAUDE.md"},
		Sources: []state.Signal{{PRNumber: 1, Reviewer: "alice", Snippet: "q", Strength: "explicit"}},
	}}
	path := filepath.Join(dir, "state.json")
	if err := state.Write(path, s); err != nil {
		t.Fatal(err)
	}
	return path
}

// runPromote defaults to AGENTS.md and, without --create, must not bring a
// repo-root file into existence.
func TestRunPromote_defaultTargetRequiresCreate(t *testing.T) {
	dir := t.TempDir()
	statePath := promoteState(t, dir)

	if err := runPromote([]string{"--state", statePath, "--output-dir", dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); !os.IsNotExist(err) {
		t.Error("promote must not create AGENTS.md without --create")
	}

	if err := runPromote([]string{"--state", statePath, "--output-dir", dir, "--create"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); err != nil {
		t.Errorf("--create should have written AGENTS.md: %v", err)
	}
}

// Both targets can be promoted in one invocation.
func TestRunPromote_bothTargets(t *testing.T) {
	dir := t.TempDir()
	statePath := promoteState(t, dir)
	agents := filepath.Join(dir, "AGENTS.md")
	claude := filepath.Join(dir, "CLAUDE.md")

	if err := runPromote([]string{
		"--state", statePath, "--output-dir", dir,
		"--agents-md", agents, "--claude-md", claude, "--create",
	}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{agents, claude} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s should have been promoted: %v", filepath.Base(p), err)
		}
	}
}

func TestRunPromote_requiresState(t *testing.T) {
	if err := runPromote([]string{}); err == nil {
		t.Error("promote should require --state")
	}
}

func TestResolveOwner(t *testing.T) {
	s := state.Empty()
	if got, err := resolveOwner("Acme/Alpha/", s, t.TempDir()); err != nil || got != "acme/alpha" {
		t.Errorf("flag: got %q, %v", got, err)
	}
	for _, bad := range []string{"acme", "a/b/c", "any owner", "github.com/acme/alpha"} {
		if _, err := resolveOwner(bad, s, t.TempDir()); err == nil {
			t.Errorf("--repo %q should be rejected", bad)
		}
	}
	s.Repo = state.RepoInfo{Owner: "Acme", Repo: "Beta"}
	if got, err := resolveOwner("", s, t.TempDir()); err != nil || got != "acme/beta" {
		t.Errorf("state: got %q, %v", got, err)
	}
	// A state repo that cannot become a key is an error, not a silent
	// unstamped run.
	s.Repo = state.RepoInfo{Owner: "group/sub", Repo: "svc"}
	if _, err := resolveOwner("", s, t.TempDir()); err == nil || !strings.Contains(err.Error(), "not a usable owner/repo") {
		t.Errorf("unusable state.repo should be an error, got %v", err)
	}
	// No flag, no state repo, no git remote: unowned, not an error. The
	// directory is its own git repository (with no origin) so the git
	// fallback cannot walk up into whatever checkout TMPDIR happens to be
	// under.
	noRemote := t.TempDir()
	if out, err := exec.Command("git", "-C", noRemote, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init unavailable: %v: %s", err, out)
	}
	if got, err := resolveOwner("", state.Empty(), noRemote); err != nil || got != "" {
		t.Errorf("fallback: got %q, %v", got, err)
	}
}

// repoRoot judges by the state file's git top level, not the current
// directory, so a run from an ancestor of a user-level skills directory
// cannot mistake that directory for the repo's own.
func TestRepoRoot(t *testing.T) {
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init unavailable: %v: %s", err, out)
	}
	stateDir := filepath.Join(repo, ".claude", "pattern-learner")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	got, ok := repoRoot(filepath.Join(stateDir, "state.json"))
	want, _ := filepath.EvalSymlinks(repo)
	if gotResolved, _ := filepath.EvalSymlinks(got); !ok || gotResolved != want {
		t.Errorf("repoRoot = %q, %v; want %q", got, ok, want)
	}
	// State outside any git repo: its own directory, and inGit false
	// (unless TMPDIR itself sits inside a checkout).
	plain := t.TempDir()
	if root, ok := repoRoot(filepath.Join(plain, "state.json")); !ok {
		wantPlain, _ := filepath.EvalSymlinks(plain)
		if gotPlain, _ := filepath.EvalSymlinks(root); gotPlain != wantPlain {
			t.Errorf("repoRoot outside git = %q, want %q", root, wantPlain)
		}
	}

	// A .claude/ that is a symlink into another repository must not make
	// that repository the root: git is asked from the directory above
	// .claude/, the project itself.
	dotfiles := t.TempDir()
	if out, err := exec.Command("git", "-C", dotfiles, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init unavailable: %v: %s", err, out)
	}
	if err := os.MkdirAll(filepath.Join(dotfiles, "claude", "pattern-learner"), 0755); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	if out, err := exec.Command("git", "-C", project, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init unavailable: %v: %s", err, out)
	}
	if err := os.Symlink(filepath.Join(dotfiles, "claude"), filepath.Join(project, ".claude")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got, ok = repoRoot(filepath.Join(project, ".claude", "pattern-learner", "state.json"))
	wantProject, _ := filepath.EvalSymlinks(project)
	if gotResolved, _ := filepath.EvalSymlinks(got); !ok || gotResolved != wantProject {
		t.Errorf("repoRoot through a symlinked .claude = %q, %v; want the project %q", got, ok, wantProject)
	}
}

func TestGlobFiles_braceGroups(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"src/api/h.go": "a\n",
		"src/web/h.go": "b\n",
		"src/db/h.go":  "c\n",
	})
	got := globFiles(root, "src/{api,web}/**/*.go")
	want := map[string]bool{
		filepath.Join(root, "src/api/h.go"): true,
		filepath.Join(root, "src/web/h.go"): true,
	}
	if len(got) != len(want) {
		t.Fatalf("want %d matches, got %d: %v", len(want), len(got), got)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected match: %s", g)
		}
	}
}

func TestGlobFiles_singleAlternativeBrace(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"src/a/h.go": "a\n"})
	got := globFiles(root, "src/{a}/*.go")
	if len(got) != 1 || got[0] != filepath.Join(root, "src/a/h.go") {
		t.Errorf("single-alternative brace group should expand like the paths gate does; got %v", got)
	}
}

// A missing --state must be an error, not an empty state: write-outputs
// prunes every generated skill absent from state.
func TestWriteOutputsRefusesMissingState(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "skills"), 0755); err != nil {
		t.Fatal(err)
	}
	err := runWriteOutputs([]string{"--state", filepath.Join(dir, "missing.json"), "--output-dir", dir})
	if err == nil {
		t.Fatal("expected an error for a missing state file")
	}
}

// One matcher resolves every glob with a single walk, and globs it was
// not built with still resolve.
func TestGlobMatcher(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"src/api/h.go":   "a\n",
		"src/web/h.go":   "b\n",
		"docs/readme.md": "c\n",
	})
	m := newGlobMatcher(root, []string{"src/{api,web}/**/*.go", "src/api/**/*.go", "src/api/**/*.go"})
	if got := m.files("src/{api,web}/**/*.go"); len(got) != 2 {
		t.Errorf("brace glob: got %v", got)
	}
	if got := m.files("src/api/**/*.go"); len(got) != 1 || got[0] != filepath.Join(root, "src/api/h.go") {
		t.Errorf("plain glob: got %v", got)
	}
	if got := m.files("docs/*.md"); got != nil {
		t.Errorf("an unregistered glob has no matches rather than a walk of its own: got %v", got)
	}
}

// A relative --skills-dir resolves against the output directory, and the
// default lives under it.
func TestResolveSkillsDir(t *testing.T) {
	if got := output.ResolveSkillsDir("", "/repo"); got != filepath.Join("/repo", ".claude", "skills") {
		t.Errorf("default: %q", got)
	}
	if got := output.ResolveSkillsDir(".claude/skills", "/repo"); got != filepath.Join("/repo", ".claude", "skills") {
		t.Errorf("relative: %q", got)
	}
	if got := output.ResolveSkillsDir("/shared/skills", "/repo"); got != "/shared/skills" {
		t.Errorf("absolute: %q", got)
	}
}

// promote links relatively when the skills dir is inside the repository,
// whatever form the path took, so the committed file holds on any checkout.
func TestPromoteLinksRelativeInsideRepo(t *testing.T) {
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init unavailable: %v: %s", err, out)
	}
	stateDir := filepath.Join(repo, ".claude", "pattern-learner")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	s := state.Empty()
	s.Rules = []state.Rule{{
		ID: "r1", Title: "Rule", Rule: "Do it.", Status: "approved", Confidence: "stated",
		Target:  state.Target{Location: "api", FileGlob: []string{"src/**/*.go"}},
		Sources: []state.Signal{{PRNumber: 1, Reviewer: "a", Snippet: "q", Strength: "explicit"}},
	}}
	statePath := filepath.Join(stateDir, "state.json")
	if err := state.Write(statePath, s); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(repo, "packages", "web", "CLAUDE.md")
	if err := runPromote([]string{"--state", statePath, "--claude-md", target, "--create"}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	want := "`" + filepath.Join("..", "..", ".claude", "skills", "api", "SKILL.md") + "`"
	if !strings.Contains(string(content), want) {
		t.Errorf("expected a relative link %s; got:\n%s", want, content)
	}
	if strings.Contains(string(content), repo) {
		t.Error("the committed file must not carry a machine-specific absolute path")
	}
}

// Without --output-dir, write-outputs writes into the repository the state
// lives in, not the current directory.
func TestWriteOutputsDefaultsToStateRepo(t *testing.T) {
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init unavailable: %v: %s", err, out)
	}
	stateDir := filepath.Join(repo, ".claude", "pattern-learner")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	s := state.Empty()
	s.Rules = []state.Rule{{
		ID: "r1", Title: "Rule", Rule: "Do it.", Status: "approved", Confidence: "stated",
		Target:  state.Target{Location: "api", FileGlob: []string{"src/**/*.go"}},
		Sources: []state.Signal{{PRNumber: 1, Reviewer: "a", Snippet: "q", Strength: "explicit"}},
	}}
	statePath := filepath.Join(stateDir, "state.json")
	if err := state.Write(statePath, s); err != nil {
		t.Fatal(err)
	}
	if err := runWriteOutputs([]string{"--state", statePath}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".claude", "skills", "api", "SKILL.md")); err != nil {
		t.Errorf("skill should be written under the state's repository: %v", err)
	}
	if _, err := os.Stat(filepath.Join(".claude", "skills", "api")); err == nil {
		t.Error("nothing should be written under the current directory")
	}
}

// Glob metacharacters in the repository path are not read as a pattern,
// and a group with a plain and a "**" alternative lists a file once.
func TestGlobMatcherRootMetacharsAndDedup(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proj[1]")
	writeTree(t, root, map[string]string{"src/a.go": "a\n", "src/deep/b.go": "b\n"})
	m := newGlobMatcher(root, []string{"src/*.go", "src/{*.go,**/*.go}"})
	if got := m.files("src/*.go"); len(got) != 1 || got[0] != filepath.Join(root, "src/a.go") {
		t.Errorf("plain glob under a metachar root: got %v", got)
	}
	if got := m.files("src/{*.go,**/*.go}"); len(got) != 2 {
		t.Errorf("a file matched by two alternatives must be listed once: got %v", got)
	}
}

// A git remote that parses to an unusable identity is an error rather
// than a silent unstamped run.
func TestResolveOwnerRejectsUnusableRemote(t *testing.T) {
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init unavailable: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", repo, "remote", "add", "origin", "https://host/git/a b/c").CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v: %s", err, out)
	}
	if _, err := resolveOwner("", state.Empty(), repo); err == nil || !strings.Contains(err.Error(), "not a usable owner/repo") {
		t.Errorf("expected an error for an unusable remote identity, got %v", err)
	}
}

// A repo-relative glob written with a leading "/" or "./" matches the same
// files as the plain form.
func TestGlobMatcherLeadingSlash(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"src/api/h.go": "a\n"})
	for _, g := range []string{"/src/api/*.go", "./src/api/*.go", "/src/**/*.go"} {
		if got := globFiles(root, g); len(got) != 1 {
			t.Errorf("%s: got %v", g, got)
		}
	}
}

// promote defaults its output directory to the state's repository, like
// write-outputs, so the two agree on where AGENTS.md and the skills live.
func TestPromoteDefaultsToStateRepo(t *testing.T) {
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init unavailable: %v: %s", err, out)
	}
	stateDir := filepath.Join(repo, ".claude", "pattern-learner")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	statePath := promoteState(t, stateDir)
	if err := runPromote([]string{"--state", statePath, "--create"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "AGENTS.md")); err != nil {
		t.Errorf("AGENTS.md should be created at the state's repository root: %v", err)
	}
	if _, err := os.Stat("AGENTS.md"); err == nil {
		t.Error("nothing should be written under the current directory")
	}
}

func TestEscapeGlobMeta(t *testing.T) {
	names := []string{"p[1]", "a*b?c"}
	if runtime.GOOS != "windows" {
		names = append(names, `a\b`)
	}
	for _, name := range names {
		root := filepath.Join(t.TempDir(), name)
		writeTree(t, root, map[string]string{"a.go": "x\n"})
		found, err := filepath.Glob(filepath.Join(escapeGlobMeta(root), "*.go"))
		if err != nil || len(found) != 1 || found[0] != filepath.Join(root, "a.go") {
			t.Errorf("%s: escaped root should glob normally and yield the real path: %v, %v", name, found, err)
		}
	}
}

// A state outside git still resolves to its own project directory, never
// the process cwd.
func TestRepoRootOutsideGitUsesStateProject(t *testing.T) {
	project := t.TempDir()
	stateDir := filepath.Join(project, ".claude", "pattern-learner")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	root, inGit := repoRoot(filepath.Join(stateDir, "state.json"))
	if inGit {
		// TMPDIR is inside a checkout; the fallback branch is not reachable.
		t.Skip("temp dir is inside a git checkout")
	}
	want, _ := filepath.EvalSymlinks(project)
	if got, _ := filepath.EvalSymlinks(root); got != want {
		t.Errorf("repoRoot outside git = %q, want the project %q", root, want)
	}
}

// A relative explicit target lands in the output directory, like the
// default target does, wherever the command was run from.
func TestRunPromote_relativeTargetJoinsOutputDir(t *testing.T) {
	dir := t.TempDir()
	statePath := promoteState(t, dir)
	if err := runPromote([]string{"--state", statePath, "--output-dir", dir, "--claude-md", "docs/CLAUDE.md", "--create"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "docs", "CLAUDE.md")); err != nil {
		t.Errorf("relative target should be created under the output dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join("docs", "CLAUDE.md")); err == nil {
		t.Error("nothing should be written under the current directory")
	}
}

// A relative --output-dir is not doubled into the skills path.
func TestWriteOutputsRelativeOutputDir(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	if err := os.Chdir(base); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)
	if err := os.MkdirAll(filepath.Join("proj", ".claude", "pattern-learner"), 0755); err != nil {
		t.Fatal(err)
	}
	s := state.Empty()
	s.Rules = []state.Rule{{
		ID: "r1", Title: "Rule", Rule: "Do it.", Status: "approved", Confidence: "stated",
		Target:  state.Target{Location: "api", FileGlob: []string{"src/**/*.go"}},
		Sources: []state.Signal{{PRNumber: 1, Reviewer: "a", Snippet: "q", Strength: "explicit"}},
	}}
	statePath := filepath.Join("proj", ".claude", "pattern-learner", "state.json")
	if err := state.Write(statePath, s); err != nil {
		t.Fatal(err)
	}
	if err := runWriteOutputs([]string{"--state", statePath, "--output-dir", "proj", "--skills-dir", ".claude/skills"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join("proj", ".claude", "skills", "api", "SKILL.md")); err != nil {
		t.Errorf("skill should be under proj/.claude/skills: %v", err)
	}
	if _, err := os.Stat(filepath.Join("proj", "proj")); err == nil {
		t.Error("the relative output dir must not be doubled")
	}
}
