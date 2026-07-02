package detect

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseGitHubURL(t *testing.T) {
	cases := []struct {
		input     string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{"git@github.com:ButtersesHouse/Chalmuns.git", "ButtersesHouse", "Chalmuns", false},
		{"https://github.com/ButtersesHouse/Chalmuns.git", "ButtersesHouse", "Chalmuns", false},
		{"https://github.com/ButtersesHouse/Chalmuns", "ButtersesHouse", "Chalmuns", false},
		{"https://github.com/org/repo.git", "org", "repo", false},
		// local proxy URL — the actual format used in this environment
		{"http://local_proxy@127.0.0.1:41885/git/ButtersesHouse/Chalmuns", "ButtersesHouse", "Chalmuns", false},
		// proxy with .git suffix
		{"http://local_proxy@127.0.0.1:41885/git/acme/widgets.git", "acme", "widgets", false},
		// unparseable
		{"not-a-url", "", "", true},
	}

	for _, tc := range cases {
		owner, repo, err := parseGitHubURL(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%q: expected error, got nil", tc.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error: %v", tc.input, err)
			continue
		}
		if owner != tc.wantOwner || repo != tc.wantRepo {
			t.Errorf("%q: want %s/%s, got %s/%s", tc.input, tc.wantOwner, tc.wantRepo, owner, repo)
		}
	}
}

func TestDetectStackGo(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "go.mod")

	stack := detectStack(dir)
	if len(stack) != 1 || stack[0] != "go" {
		t.Errorf("want [go], got %v", stack)
	}
}

func TestDetectStackMultiple(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "go.mod")
	touch(t, dir, "package.json")

	stack := detectStack(dir)
	if len(stack) != 2 {
		t.Errorf("want 2 stacks, got %v", stack)
	}
	found := map[string]bool{}
	for _, s := range stack {
		found[s] = true
	}
	if !found["go"] || !found["nodejs"] {
		t.Errorf("expected go and nodejs in stack, got %v", stack)
	}
}

func TestDetectStackDeduplicatesPython(t *testing.T) {
	// both requirements.txt and pyproject.toml map to "python" — should appear once
	dir := t.TempDir()
	touch(t, dir, "requirements.txt")
	touch(t, dir, "pyproject.toml")

	stack := detectStack(dir)
	count := 0
	for _, s := range stack {
		if s == "python" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("python should appear once, got %d times in %v", count, stack)
	}
}

func TestDetectStackEmpty(t *testing.T) {
	dir := t.TempDir()
	stack := detectStack(dir)
	if len(stack) != 0 {
		t.Errorf("want empty stack, got %v", stack)
	}
}

func touch(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
}
