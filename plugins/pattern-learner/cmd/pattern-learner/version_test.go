package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The version literal lives in three places that nothing links at build
// time: the Version constant the binary prints, the plugin manifest, and
// the value SKILL.md Step 2 tells the agent to expect. If they drift, every
// run rebuilds and then stops on the mismatch, so drift must fail here
// instead.
func TestVersionLiteralsAgree(t *testing.T) {
	manifest, err := os.ReadFile("../../plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	if m.Version != Version {
		t.Errorf("plugin.json version %q != main.Version %q", m.Version, Version)
	}

	skill, err := os.ReadFile("../../skills/learn-patterns/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	want := "confirm it prints exactly `" + Version + "`"
	if !strings.Contains(string(skill), want) {
		t.Errorf("SKILL.md Step 2 must expect %q from `version`; %q not found", Version, want)
	}
	if strings.Contains(string(skill), "$BIN version") && !strings.Contains(string(skill), "does not print `"+Version+"`") {
		t.Errorf("SKILL.md Step 2 rebuild instruction must name %q", Version)
	}
}
