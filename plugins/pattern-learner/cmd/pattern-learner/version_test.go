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
	var pm struct {
		Version     string `json:"version"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(manifest, &pm); err != nil {
		t.Fatal(err)
	}
	if pm.Version != Version {
		t.Errorf("plugin.json version %q != main.Version %q", pm.Version, Version)
	}

	// The marketplace manifest and plugin.json describe the same plugin;
	// keep the two descriptions from drifting apart (they already did once).
	// The marketplace manifest lives at the repository root, outside this Go
	// module. An installed copy of the plugin has only the module, so its
	// absence is not a failure.
	market, err := os.ReadFile("../../../../.claude-plugin/marketplace.json")
	if os.IsNotExist(err) {
		t.Log("marketplace.json not found (plugin checked out on its own); skipping the description check")
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	var mk struct {
		Plugins []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(market, &mk); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range mk.Plugins {
		if p.Name == "pattern-learner" {
			found = true
			if strings.TrimSuffix(p.Description, ".") != strings.TrimSuffix(pm.Description, ".") {
				t.Errorf("marketplace.json description differs from plugin.json:\n  %q\n  %q", p.Description, pm.Description)
			}
		}
	}
	if !found {
		t.Error("marketplace.json has no pattern-learner entry")
	}

}

// SKILL.md Step 2 tells the agent which version string to expect from the
// binary; it must be the one the binary prints.
func TestSkillExpectsThisVersion(t *testing.T) {
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
