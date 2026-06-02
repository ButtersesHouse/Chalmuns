// Package pipeline provides deterministic, testable subcommands that handle
// the algorithmic steps of the learn-patterns SKILL. These steps were
// previously described as prose and were prone to incorrect agent
// reimplementation. Each subcommand reads JSON from stdin (or flags) and
// writes JSON to stdout, matching the existing state-write pattern.
package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// LeanComment is the preprocessed view of a single PR comment sent to the
// signal-extraction subagent. Only fields relevant to pattern extraction are kept.
type LeanComment struct {
	ID          int    `json:"id"`
	User        string `json:"user"`
	IsPRAuthor  bool   `json:"is_pr_author"`
	CreatedAt   string `json:"created_at,omitempty"`
	InReplyToID *int   `json:"in_reply_to_id,omitempty"`
	Type        string `json:"type"` // "review_comment", "issue_comment", "review_body"
	Path        string `json:"path,omitempty"`
	Body        string `json:"body"`
	CodeBefore  string `json:"code_before,omitempty"`
}

// LeanPR is the lean view of a single PR.
type LeanPR struct {
	PRNumber     int           `json:"pr_number"`
	FilesTouched []string      `json:"files_touched"`
	Comments     []LeanComment `json:"comments"`
}

// RunExtractLean reads PR cache files and writes a JSON array of LeanPR
// objects to stdout. The output is ready to insert directly into the
// extraction subagent prompt (Step 6.3).
//
// Usage: extract-lean --cache-dir <dir> [--prs 1,2,3]
func RunExtractLean(args []string) error {
	cacheDir := flagVal(args, "--cache-dir", "")
	if cacheDir == "" {
		return fmt.Errorf("--cache-dir required")
	}
	prsFlag := flagVal(args, "--prs", "")

	var prNums []int
	if prsFlag != "" {
		for _, s := range strings.Split(prsFlag, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			n, err := strconv.Atoi(s)
			if err != nil {
				return fmt.Errorf("invalid PR number %q: %w", s, err)
			}
			prNums = append(prNums, n)
		}
	}

	lean, err := ExtractLean(cacheDir, prNums)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(lean)
}

// ExtractLean reads PR cache files from cacheDir and returns lean views.
// If prs is non-empty only those PR numbers are processed; otherwise all
// pr-N.json files in cacheDir are processed (sorted by filename).
func ExtractLean(cacheDir string, prs []int) ([]LeanPR, error) {
	var files []string
	if len(prs) > 0 {
		for _, n := range prs {
			files = append(files, filepath.Join(cacheDir, fmt.Sprintf("pr-%d.json", n)))
		}
	} else {
		matches, err := filepath.Glob(filepath.Join(cacheDir, "pr-*.json"))
		if err != nil {
			return nil, err
		}
		files = matches
	}

	var out []LeanPR
	for _, f := range files {
		lean, err := extractFromCacheFile(f)
		if err != nil {
			// Warn and skip — one bad cache file should not abort the batch.
			fmt.Fprintf(os.Stderr, "warn: skip %s: %v\n", f, err)
			continue
		}
		out = append(out, lean)
	}
	return out, nil
}

// --- internal types for parsing GitHub API responses ---

type rawCache struct {
	PRNumber int             `json:"pr_number"`
	Raw      json.RawMessage `json:"raw"`
}

// rawPR mirrors the GitHub REST API PR response shape stored in cache files.
// Field names match the GitHub API; missing fields are silently ignored.
type rawPR struct {
	Number         int                `json:"number"`
	User           rawUser            `json:"user"`
	ReviewComments []rawReviewComment `json:"review_comments"`
	Reviews        []rawReview        `json:"reviews"`
	IssueComments  []rawIssueComment  `json:"issue_comments"`
	Files          []rawFile          `json:"files"`
}

type rawUser struct {
	Login string `json:"login"`
}

type rawReviewComment struct {
	ID          int     `json:"id"`
	User        rawUser `json:"user"`
	Body        string  `json:"body"`
	CreatedAt   string  `json:"created_at"`
	InReplyToID *int    `json:"in_reply_to_id"`
	Path        string  `json:"path"`
	DiffHunk    string  `json:"diff_hunk"`
}

type rawReview struct {
	ID          int     `json:"id"`
	User        rawUser `json:"user"`
	Body        string  `json:"body"`
	SubmittedAt string  `json:"submitted_at"`
}

type rawIssueComment struct {
	ID        int     `json:"id"`
	User      rawUser `json:"user"`
	Body      string  `json:"body"`
	CreatedAt string  `json:"created_at"`
}

type rawFile struct {
	Filename string `json:"filename"`
}

func extractFromCacheFile(path string) (LeanPR, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return LeanPR{}, err
	}

	var cache rawCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return LeanPR{}, fmt.Errorf("parse cache: %w", err)
	}

	var pr rawPR
	if err := json.Unmarshal(cache.Raw, &pr); err != nil {
		return LeanPR{}, fmt.Errorf("parse raw PR: %w", err)
	}

	prNum := cache.PRNumber
	if prNum == 0 {
		prNum = pr.Number
	}
	prAuthor := pr.User.Login

	// Collect touched files from the files list, then supplement from review
	// comment paths for caches that omit the files array.
	seen := map[string]bool{}
	var files []string
	for _, f := range pr.Files {
		if f.Filename != "" && !seen[f.Filename] {
			files = append(files, f.Filename)
			seen[f.Filename] = true
		}
	}
	for _, rc := range pr.ReviewComments {
		if rc.Path != "" && !seen[rc.Path] {
			files = append(files, rc.Path)
			seen[rc.Path] = true
		}
	}

	var comments []LeanComment

	for _, rc := range pr.ReviewComments {
		if rc.Body == "" {
			continue
		}
		c := LeanComment{
			ID:         rc.ID,
			User:       rc.User.Login,
			IsPRAuthor: rc.User.Login == prAuthor,
			CreatedAt:  rc.CreatedAt,
			Type:       "review_comment",
			Path:       rc.Path,
			Body:       rc.Body,
		}
		if rc.InReplyToID != nil {
			c.InReplyToID = rc.InReplyToID
		}
		if rc.DiffHunk != "" {
			if cb := CodeBefore(rc.DiffHunk); cb != "" {
				c.CodeBefore = cb
			}
		}
		comments = append(comments, c)
	}

	for _, rv := range pr.Reviews {
		if strings.TrimSpace(rv.Body) == "" {
			continue
		}
		comments = append(comments, LeanComment{
			ID:         rv.ID,
			User:       rv.User.Login,
			IsPRAuthor: rv.User.Login == prAuthor,
			CreatedAt:  rv.SubmittedAt,
			Type:       "review_body",
			Body:       rv.Body,
		})
	}

	for _, ic := range pr.IssueComments {
		if ic.Body == "" {
			continue
		}
		comments = append(comments, LeanComment{
			ID:         ic.ID,
			User:       ic.User.Login,
			IsPRAuthor: ic.User.Login == prAuthor,
			CreatedAt:  ic.CreatedAt,
			Type:       "issue_comment",
			Body:       ic.Body,
		})
	}

	return LeanPR{
		PRNumber:     prNum,
		FilesTouched: files,
		Comments:     comments,
	}, nil
}

// CodeBefore extracts the "before" view of code from a unified diff hunk.
// It keeps context lines (space prefix) and removed lines (- prefix), stripping
// the leading prefix character. Added lines (+ prefix) and @@ headers are
// discarded. The result is what the code looked like before the change — the
// natural dont_example for a rule extracted from this comment.
func CodeBefore(diffHunk string) string {
	var b strings.Builder
	for _, line := range strings.Split(diffHunk, "\n") {
		line = strings.TrimRight(line, "\r") // normalise CRLF
		if strings.HasPrefix(line, "@@") {
			continue
		}
		if strings.HasPrefix(line, "+") {
			continue // added line — not present before the change
		}
		if strings.HasPrefix(line, "-") {
			b.WriteString(line[1:])
			b.WriteByte('\n')
		} else if strings.HasPrefix(line, " ") {
			b.WriteString(line[1:])
			b.WriteByte('\n')
		}
		// Empty or unexpected-prefix lines are skipped.
	}
	return strings.TrimRight(b.String(), "\n")
}
