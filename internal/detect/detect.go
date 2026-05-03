package detect

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type RepoResult struct {
	Owner string   `json:"owner"`
	Repo  string   `json:"repo"`
	Stack []string `json:"stack"`
}

func Run() error {
	r, err := Detect(".")
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func Detect(dir string) (RepoResult, error) {
	owner, repo, err := parseRemote(dir)
	if err != nil {
		return RepoResult{}, err
	}
	stack := detectStack(dir)
	return RepoResult{Owner: owner, Repo: repo, Stack: stack}, nil
}

func parseRemote(dir string) (owner, repo string, err error) {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("git remote get-url origin: %w", err)
	}
	return parseGitHubURL(strings.TrimSpace(string(out)))
}

func parseGitHubURL(raw string) (owner, repo string, err error) {
	// SSH: git@github.com:owner/repo.git
	if strings.HasPrefix(raw, "git@github.com:") {
		path := strings.TrimPrefix(raw, "git@github.com:")
		path = strings.TrimSuffix(path, ".git")
		parts := strings.SplitN(path, "/", 2)
		if len(parts) != 2 {
			return "", "", fmt.Errorf("unexpected SSH remote: %s", raw)
		}
		return parts[0], parts[1], nil
	}

	// HTTPS: https://github.com/owner/repo[.git]
	if strings.Contains(raw, "github.com/") {
		idx := strings.Index(raw, "github.com/")
		path := raw[idx+len("github.com/"):]
		path = strings.TrimSuffix(path, ".git")
		parts := strings.SplitN(path, "/", 2)
		if len(parts) != 2 {
			return "", "", fmt.Errorf("unexpected HTTPS remote: %s", raw)
		}
		return parts[0], parts[1], nil
	}

	// Generic fallback: extract last two path segments as owner/repo.
	// Handles proxied URLs like http://proxy@host/git/owner/repo
	parts := strings.Split(strings.TrimRight(raw, "/"), "/")
	var clean []string
	for _, p := range parts {
		p = strings.TrimSuffix(p, ".git")
		if idx := strings.LastIndex(p, "@"); idx >= 0 {
			p = p[idx+1:]
		}
		// skip scheme, empty, and host:port segments
		if p == "" || p == "http:" || p == "https:" || p == "git:" || strings.Contains(p, ":") {
			continue
		}
		clean = append(clean, p)
	}
	if len(clean) >= 2 {
		return clean[len(clean)-2], clean[len(clean)-1], nil
	}

	return "", "", fmt.Errorf("cannot parse owner/repo from remote: %s", raw)
}

var stackManifests = []struct {
	file  string
	stack string
}{
	{"go.mod", "go"},
	{"package.json", "nodejs"},
	{"pyproject.toml", "python"},
	{"requirements.txt", "python"},
	{"setup.py", "python"},
	{"Cargo.toml", "rust"},
	{"pom.xml", "java"},
	{"build.gradle", "java"},
	{"Gemfile", "ruby"},
}

func detectStack(dir string) []string {
	seen := map[string]bool{}
	var stack []string
	for _, m := range stackManifests {
		path := dir + "/" + m.file
		if _, err := os.Stat(path); err == nil && !seen[m.stack] {
			seen[m.stack] = true
			stack = append(stack, m.stack)
		}
	}
	return stack
}
