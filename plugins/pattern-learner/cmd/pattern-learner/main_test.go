package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ButtersesHouse/Chalmuns/internal/state"
)

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
	anchorSingleRule(r, root)
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
