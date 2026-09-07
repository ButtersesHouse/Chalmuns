package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ButtersesHouse/Chalmuns/internal/cliflags"
	"github.com/ButtersesHouse/Chalmuns/internal/review"
	"github.com/ButtersesHouse/Chalmuns/internal/state"
)

// The three subcommands behind "keep eyes on a code reviewer":
//
//	watch           designate a review skill or tool, list, or undesignate
//	capture-review  record one review's output into the review cache
//	extract-review  hand captured reviews to the extraction subagent
//
// They are the review path's counterparts to detect-repo (identify the
// source), the PR fetch (record it), and extract-lean (preprocess it). From
// extract-review onward the review path is the PR path: the same signal
// schema, the same verify-grounding, classify, triage and approval, the same
// state, and the same generated per-domain skills. Nothing here writes a rule.

// reviewCacheRel is where captured artifacts live inside a project, the
// review-side sibling of raw-cache/.
const reviewCacheRel = ".claude/pattern-learner/review-cache"

// statePathRel is where the capture hook looks for the designations, matching
// the path SKILL.md uses everywhere else.
const statePathRel = ".claude/pattern-learner/state.json"

// runWatch manages the designations. Every mode prints the resulting watcher
// list as JSON, so the caller never has to re-read state to see what changed.
//
// Usage: watch --state <path> (--list | --add <name> | --remove <id>)
//
//	[--kind skill|tool|any] [--format auto|findings|sarif|eslint|semgrep|markdown]
//	[--cache-dir <dir>]
func runWatch(args []string) error {
	if err := cliflags.Check(args,
		[]string{"--state", "--add", "--remove", "--kind", "--format", "--cache-dir"},
		[]string{"--list"}); err != nil {
		return err
	}
	statePath := cliflags.Value(args, "--state", "")
	if statePath == "" {
		return fmt.Errorf("--state required")
	}
	add := cliflags.Value(args, "--add", "")
	remove := cliflags.Value(args, "--remove", "")
	list := cliflags.Has(args, "--list")

	chosen := 0
	for _, set := range []bool{add != "", remove != "", list} {
		if set {
			chosen++
		}
	}
	if chosen != 1 {
		return fmt.Errorf("watch takes exactly one of --add <name>, --remove <id>, --list")
	}

	s, err := state.Read(statePath)
	if err != nil {
		return err
	}

	switch {
	case add != "":
		w, err := review.NewWatcher(add, cliflags.Value(args, "--kind", ""), cliflags.Value(args, "--format", ""), time.Now())
		if err != nil {
			return err
		}
		updated, err := review.AddWatcher(s.Watchers, w)
		if err != nil {
			return err
		}
		s.Watchers = updated
		if err := writeStateFile(statePath, s); err != nil {
			return err
		}
	case remove != "":
		updated, found := review.RemoveWatcher(s.Watchers, review.WatcherID(remove))
		if !found {
			return fmt.Errorf("no watcher %q; run watch --list to see the designations", remove)
		}
		s.Watchers = updated
		if err := writeStateFile(statePath, s); err != nil {
			return err
		}
	}

	// The tally comes from the cache, not from state — see state.Watcher.
	cacheDir := cliflags.Value(args, "--cache-dir", "")
	if cacheDir == "" {
		cacheDir = filepath.Join(projectDir(statePath), reviewCacheRel)
	}
	artifacts, err := review.ListArtifacts(cacheDir)
	if err != nil {
		// A missing cache is simply "nothing captured yet", which is the
		// state every designation starts in.
		artifacts = nil
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(review.Status(s.Watchers, artifacts))
}

// writeStateFile persists a state the CLI itself modified. It goes through
// state.Write for the atomic rename and the recomputed stats, and creates the
// state directory first so designating a watcher works in a repo that has
// never run the pipeline.
func writeStateFile(path string, s state.State) error {
	if err := os.MkdirAll(dirOf(path), 0755); err != nil {
		return err
	}
	return state.Write(path, s)
}

// captureResult is what capture-review prints. Written reports whether the
// artifact was new: capture is content-addressed, so re-recording one review
// is a no-op and must not read as a second, corroborating review.
type captureResult struct {
	ReviewID   string `json:"review_id"`
	Source     string `json:"source"`
	Format     string `json:"format"`
	CapturedAt string `json:"captured_at"`
	Findings   int    `json:"findings"`
	Written    bool   `json:"written"`
	Path       string `json:"path"`
}

// runCaptureReview records one review's output into the review cache.
//
// Two input modes:
//
//	capture-review --cache-dir D --source NAME [--format F] [--label L] [--file P]
//	  reads the review from --file, or from stdin when --file is absent.
//
//	capture-review --hook [--project-dir D]
//	  reads a Claude Code hook payload from stdin and records it only if it
//	  belongs to a designated watcher. This mode never fails and never writes
//	  to stdout: it runs inside someone's tool call, where an error would
//	  surface as a broken session for a feature that is only ever additive.
func runCaptureReview(args []string) error {
	if err := cliflags.Check(args,
		[]string{"--cache-dir", "--source", "--format", "--label", "--file", "--project-dir"},
		[]string{"--hook"}); err != nil {
		return err
	}
	if cliflags.Has(args, "--hook") {
		captureFromHook(args)
		return nil
	}

	cacheDir := cliflags.Value(args, "--cache-dir", "")
	if cacheDir == "" {
		return fmt.Errorf("--cache-dir required")
	}
	source := cliflags.Value(args, "--source", "")
	if source == "" {
		return fmt.Errorf("--source required: name the reviewer whose output this is (e.g. code-review)")
	}
	format := cliflags.Value(args, "--format", review.FormatAuto)
	if !review.ValidFormat(format) {
		return fmt.Errorf("unknown --format %q; accepts %s", format, strings.Join(review.Formats, ", "))
	}

	var data []byte
	var err error
	if file := cliflags.Value(args, "--file", ""); file != "" {
		data, err = os.ReadFile(file)
		if err != nil {
			return err
		}
	} else {
		data, err = io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
	}

	art, err := review.Capture(review.Input{
		Data:   data,
		Source: source,
		Format: format,
		Label:  cliflags.Value(args, "--label", ""),
		Now:    time.Now(),
	})
	if err != nil {
		return err
	}
	written, err := review.WriteArtifact(cacheDir, art)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(captureResult{
		ReviewID:   art.ReviewID,
		Source:     art.Source,
		Format:     art.Format,
		CapturedAt: art.CapturedAt,
		Findings:   len(art.Findings),
		Written:    written,
		Path:       review.ArtifactPath(cacheDir, art.ReviewID),
	})
}

// captureFromHook is the passive half of the feature. It follows
// internal/guard's fail-open discipline to the letter: every failure path
// returns silently, so a malformed payload, an unreadable state file, or a
// full disk costs a captured review and nothing else.
func captureFromHook(args []string) {
	payload, err := io.ReadAll(os.Stdin)
	if err != nil {
		return
	}

	// Resolve the project exactly as the guard does — CLAUDE_PROJECT_DIR, then
	// the cwd the payload reports, then the process cwd. The payload step is
	// not optional: a hook wired by hand, or run by anything that does not
	// export CLAUDE_PROJECT_DIR, otherwise falls back to the tool call's own
	// working directory, and a command run from a subdirectory then finds no
	// state.json and captures nothing.
	base := cliflags.Value(args, "--project-dir", "")
	if base == "" {
		base = os.Getenv("CLAUDE_PROJECT_DIR")
	}
	if base == "" {
		base = review.PayloadCWD(payload)
	}
	if base == "" {
		if cwd, err := os.Getwd(); err == nil {
			base = cwd
		}
	}
	if base == "" {
		return
	}

	s, err := state.Read(filepath.Join(base, statePathRel))
	if err != nil || len(s.Watchers) == 0 {
		return
	}

	art, _, ok := review.FromHook(payload, s.Watchers, time.Now())
	if !ok {
		return
	}
	cacheDir := cliflags.Value(args, "--cache-dir", "")
	if cacheDir == "" {
		cacheDir = filepath.Join(base, reviewCacheRel)
	}
	// The error is deliberately dropped: there is no one to report it to, and
	// a hook that fails a tool call to complain about its own bookkeeping is
	// worse than a hook that misses a review.
	_, _ = review.WriteArtifact(cacheDir, art)
}

// runExtractReview prints the lean views of captured reviews for the
// extraction subagent — the review path's extract-lean.
//
// Usage: extract-review --cache-dir <dir> [--reviews id1,id2] [--since <RFC3339>]
func runExtractReview(args []string) error {
	if err := cliflags.Check(args, []string{"--cache-dir", "--reviews", "--since"}, nil); err != nil {
		return err
	}
	cacheDir := cliflags.Value(args, "--cache-dir", "")
	if cacheDir == "" {
		return fmt.Errorf("--cache-dir required")
	}

	var ids []string
	if raw := cliflags.Value(args, "--reviews", ""); raw != "" {
		for _, id := range strings.Split(raw, ",") {
			if id = strings.TrimSpace(id); id != "" {
				ids = append(ids, id)
			}
		}
	}

	lean, err := review.ExtractLean(cacheDir, ids, cliflags.Value(args, "--since", ""))
	if err != nil {
		return err
	}
	if lean == nil {
		lean = []review.LeanReview{}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(lean)
}
