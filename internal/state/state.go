package state

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

const SchemaVersion = "1"

type State struct {
	SchemaVersion         string            `json:"schema_version"`
	Repo                  RepoInfo          `json:"repo"`
	LastRun               string            `json:"last_run"`
	LastExtractedPRNumber int               `json:"last_extracted_pr_number"`
	Stats                 Stats             `json:"stats"`
	Rules                 []Rule            `json:"rules"`
	RejectedSignals       []RejectedSignal  `json:"rejected_signals"`
	// DomainDescriptions maps a skill domain name to a human-written description
	// used in the skill file's frontmatter. SKILL.md generates these during
	// approval; output.go reads them when present, falls back to a generic
	// template otherwise. Drives Claude Code's skill auto-loading.
	DomainDescriptions map[string]string `json:"domain_descriptions,omitempty"`
}

type RepoInfo struct {
	Owner string   `json:"owner"`
	Repo  string   `json:"repo"`
	Stack []string `json:"stack"`
}

type Stats struct {
	TotalPRsScanned    int `json:"total_prs_scanned"`
	TotalSignals       int `json:"total_signals"`
	DroppedByGrounding int `json:"dropped_by_grounding"`
	TotalRules         int `json:"total_rules"`
	StatedRules        int `json:"stated_rules"`
	EstablishedRules   int `json:"established_rules"`
	EmergingRules      int `json:"emerging_rules"`
	ApprovedRules      int `json:"approved_rules"`
	RejectedRules      int `json:"rejected_rules"`
}

type Rule struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Rule         string    `json:"rule"`
	DoExample    *Example  `json:"do_example,omitempty"`
	DontExample  *Example  `json:"dont_example,omitempty"`
	DoExamples   []Example `json:"do_examples,omitempty"`
	DontExamples []Example `json:"dont_examples,omitempty"`
	Target       Target    `json:"target"`
	Confidence   string   `json:"confidence"`
	SignalCount  int      `json:"signal_count"`
	Sources      []Signal `json:"sources"`
	Status       string   `json:"status"`
	Supersedes   []string `json:"supersedes,omitempty"`
	SupersededBy *string  `json:"superseded_by,omitempty"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
	LastSeenPR   int      `json:"last_seen_pr"`
}

type Example struct {
	Code     string `json:"code"`
	Language string `json:"language"`
	FileRef  string `json:"file_ref,omitempty"` // "path/to/file.go:L45" — set by write-outputs
	Context  string `json:"context,omitempty"`  // surrounding function context from suggestion blocks
}

// Target.Location is either "CLAUDE.md" or a skill domain name (e.g. "api", "auth").
type Target struct {
	Location string   `json:"location"`
	FileGlob []string `json:"file_glob,omitempty"`
}

type Signal struct {
	PRNumber  int    `json:"pr_number"`
	CommentID int    `json:"comment_id"`
	Reviewer  string `json:"reviewer"`
	Date      string `json:"date"`
	Snippet   string `json:"snippet"`
	// Strength is "explicit" when the reviewer stated a general preference or convention
	// ("we prefer X", "we always Y"); "implicit" for corrections without a stated rule.
	// Empty is treated as "implicit" for backward compatibility.
	Strength string `json:"strength,omitempty"`
}

type RejectedSignal struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Rule       string   `json:"rule"`
	RejectedAt string   `json:"rejected_at"`
	Sources    []Signal `json:"sources"`
}

func Empty() State {
	return State{SchemaVersion: SchemaVersion}
}

func Read(path string) (State, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Empty(), nil
	}
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return s, nil
}

// Write finalizes an incoming state (assigns IDs, sets timestamps) then atomically writes it.
func Write(path string, s State) error {
	now := time.Now().UTC().Format(time.RFC3339)
	s.SchemaVersion = SchemaVersion
	s.LastRun = now

	for i := range s.Rules {
		if s.Rules[i].ID == "" {
			s.Rules[i].ID = newRuleID()
		}
		if s.Rules[i].CreatedAt == "" {
			s.Rules[i].CreatedAt = now
		}
		s.Rules[i].UpdatedAt = now
	}

	// recompute stats
	s.Stats.TotalRules = len(s.Rules)
	s.Stats.StatedRules = 0
	s.Stats.EstablishedRules = 0
	s.Stats.EmergingRules = 0
	s.Stats.ApprovedRules = 0
	s.Stats.RejectedRules = 0
	for _, r := range s.Rules {
		switch r.Confidence {
		case "stated":
			s.Stats.StatedRules++
		case "established":
			s.Stats.EstablishedRules++
		case "emerging":
			s.Stats.EmergingRules++
		}
		switch r.Status {
		case "approved":
			s.Stats.ApprovedRules++
		case "rejected":
			s.Stats.RejectedRules++
		}
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func newRuleID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return fmt.Sprintf("rule_%x", b)
}
