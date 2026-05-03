package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadMissing(t *testing.T) {
	s, err := Read("/nonexistent/path/state.json")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got %v", err)
	}
	if s.SchemaVersion != SchemaVersion {
		t.Errorf("expected schema version %q, got %q", SchemaVersion, s.SchemaVersion)
	}
}

func TestWriteAssignsIDs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s := Empty()
	s.Rules = []Rule{
		{Title: "no id yet", Rule: "do the thing", Status: "proposed", Confidence: "emerging"},
		{ID: "rule_existing", Title: "has id", Rule: "already set", Status: "approved", Confidence: "established"},
	}

	if err := Write(path, s); err != nil {
		t.Fatal(err)
	}

	out, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(out.Rules))
	}
	if !strings.HasPrefix(out.Rules[0].ID, "rule_") {
		t.Errorf("expected generated ID, got %q", out.Rules[0].ID)
	}
	if out.Rules[1].ID != "rule_existing" {
		t.Errorf("expected existing ID preserved, got %q", out.Rules[1].ID)
	}
	if out.Rules[0].CreatedAt == "" {
		t.Error("expected created_at set for new rule")
	}
}

func TestWriteStatsRecomputed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s := Empty()
	s.Rules = []Rule{
		{Status: "approved", Confidence: "established"},
		{Status: "approved", Confidence: "established"},
		{Status: "approved", Confidence: "emerging"},
		{Status: "rejected", Confidence: "emerging"},
		{Status: "proposed", Confidence: "emerging"},
	}

	if err := Write(path, s); err != nil {
		t.Fatal(err)
	}

	out, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.Stats.TotalRules != 5 {
		t.Errorf("total_rules: want 5, got %d", out.Stats.TotalRules)
	}
	if out.Stats.EstablishedRules != 2 {
		t.Errorf("established: want 2, got %d", out.Stats.EstablishedRules)
	}
	if out.Stats.EmergingRules != 3 {
		t.Errorf("emerging: want 3, got %d", out.Stats.EmergingRules)
	}
	if out.Stats.ApprovedRules != 3 {
		t.Errorf("approved: want 3, got %d", out.Stats.ApprovedRules)
	}
	if out.Stats.RejectedRules != 1 {
		t.Errorf("rejected: want 1, got %d", out.Stats.RejectedRules)
	}
}

func TestWriteAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	if err := Write(path, Empty()); err != nil {
		t.Fatal(err)
	}
	// tmp file should be cleaned up
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("tmp file still exists after write")
	}
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	original := Empty()
	original.Repo = RepoInfo{Owner: "acme", Repo: "widgets", Stack: []string{"go"}}
	original.Rules = []Rule{
		{
			Title:       "Use errors.As",
			Rule:        "Use errors.As for type-checking errors",
			Confidence:  "established",
			Status:      "approved",
			SignalCount: 5,
			Sources:     []Signal{{PRNumber: 1, Reviewer: "alice", Snippet: "please use errors.As"}},
			Target:      Target{Location: "CLAUDE.md"},
		},
	}

	if err := Write(path, original); err != nil {
		t.Fatal(err)
	}
	out, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.Rules[0].Title != "Use errors.As" {
		t.Errorf("title not preserved")
	}

	// verify valid JSON
	data, _ := os.ReadFile(path)
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Errorf("output is not valid JSON: %v", err)
	}
}
