---
name: learn-patterns
description: Extract coding conventions and developer preferences from this repo's PR review history and write approved rules to CLAUDE.md and skill files. Explicit preferences ("we prefer X", "we always Y") are captured regardless of occurrence count. Use --refresh for incremental since last run, --review to re-open approval without re-fetching.
argumentHint: "[--refresh | --review]"
---

# learn-patterns

Extracts coding conventions and developer preferences from merged PR review comments and writes approved rules to `CLAUDE.md` and domain-specific skill files. Explicit preferences are captured regardless of how many times they appear.

## Instructions

Follow all steps in order. Do not skip steps unless the mode explicitly says to.

---

### Step 1: Mode detection

Parse `$ARGUMENTS`:
- `--refresh` → incremental mode: only fetch PRs newer than last run
- `--review` → approval-only mode: skip all fetching, go straight to Step 10
- (nothing) → full mode: fetch all merged PRs

---

### Step 2: Locate and build the binary

Check whether `.claude/pattern-learner/bin/pattern-learner` exists in the current working directory (the target repo root).

If it does not exist:
1. Find the plugin root by running:
   ```
   find ~/.claude -name "go.mod" -path "*/Chalmuns/go.mod" 2>/dev/null | head -1 | xargs dirname
   ```
   If nothing found, try: `find . -name "go.mod" -path "*/Chalmuns/go.mod" 2>/dev/null | head -1 | xargs dirname`

2. Create the output directory:
   ```
   mkdir -p .claude/pattern-learner/bin
   ```

3. Build the binary (CWD = plugin root found above):
   ```
   go build -o <absolute_path_to_target_repo>/.claude/pattern-learner/bin/pattern-learner ./cmd/pattern-learner
   ```

If the build fails, stop and report the error. Do not continue.

Refer to the binary as `BIN=.claude/pattern-learner/bin/pattern-learner` for the rest of these steps.

---

### Step 3: Detect repo

Run:
```
$BIN detect-repo
```

Save the JSON output: `{"owner": "...", "repo": "...", "stack": [...]}`.

---

### Step 4: Read current state

Run:
```
$BIN state-read --state .claude/pattern-learner/state.json
```

Save the state JSON. Note `last_extracted_pr_number` (will be 0 for a fresh repo).

---

### Step 5: Fetch PRs (skip entirely if `--review`)

Determine `since_pr`:
- Full mode: `since_pr = 0`
- Refresh mode: `since_pr = last_extracted_pr_number` from Step 4

Create the cache directory if needed:
```
mkdir -p .claude/pattern-learner/raw-cache
```

The sort order and stop condition differ by mode. Keep a running `max_pr_seen = 0` as you page; update it for every PR encountered (merged or not).

**Full mode** — sort by last activity, newest first. No early stop; page all results.
```
sort: "updated", direction: "desc"
```
Keep PRs where `merged_at != null`. The cache check below deduplicates any PR already seen in a prior partial run.

**Refresh mode** — sort by creation order, newest first. This aligns PR numbers with page order, making the stop condition reliable.
```
sort: "created", direction: "desc"
```
Keep PRs where `merged_at != null` AND `number > since_pr`. Stop when an entire page consists only of PRs with `number <= since_pr` — all of these were processed in a prior run. Fetch one additional page after the first all-below-watermark page to guard against any gap, then stop.

**For each qualifying PR number N** (both modes):
1. Update `max_pr_seen = max(max_pr_seen, N)`.
2. Check if `.claude/pattern-learner/raw-cache/pr-N.json` exists. If it does, skip (already cached).
3. If not cached: call `mcp__github__pull_request_read` to get reviews and review comments. If issue-level comments (general PR discussion) are available via a separate call, fetch those too.
4. Count comment types: `review_comments` (diff-level), `issue_comments` (discussion thread), `review_bodies` (text in approve/request-changes reviews with non-empty body).
5. Write to cache:
   ```json
   {
     "pr_number": N,
     "comment_sources": {
       "review_comments": <count>,
       "issue_comments": <count>,
       "review_bodies": <count>
     },
     "raw": <full PR data including all comments and reviews>
   }
   ```

Collect newly-cached PR numbers into batches of up to 20. Carry `max_pr_seen` forward to Step 11.

---

### Step 6: Extract signals via subagents

For each batch of up to 20 PR numbers:
1. Read each `.claude/pattern-learner/raw-cache/pr-N.json`.
2. Concatenate the raw data.
3. Launch a subagent with this exact prompt (fill in the PR data at the end):

---
**SUBAGENT PROMPT:**

You are analyzing PR review comments to discover how this team prefers to write code.

Your output becomes the coding rules that AI assistants follow in this repository. Extract the team's stated preferences, naming conventions, architectural choices, and coding standards as expressed in code review.

Below is raw JSON for a batch of merged pull requests including reviews and comments.

---

**PRIORITY 1 — Explicit preferences (extract even from a single occurrence)**

Look for any comment where a reviewer states a general convention or team rule using language such as:
- "we prefer / we like / we always / we never"
- "in this codebase / in this project / our convention is / our pattern is"
- "please always / always use / never use / going forward"
- "the way we do this is / the pattern here is / we do X by"

A single occurrence is enough. These are spoken-word rules. Mark `"strength": "explicit"`.

**PRIORITY 2 — Recurring corrections**

A reviewer consistently correcting the same thing suggests a convention worth capturing, even without an explicit statement. Mark `"strength": "implicit"`. These are subject to a recurrence threshold in a later step.

---

**DO NOT extract:**

- **Bug fixes**: the comment points to a specific wrong value, missing null check for a particular input, failing test assertion, or concrete runtime error. Ask yourself: "Is this about a general principle, or a specific mistake in this PR?" Only general principles qualify.
- **Product/feature correctness**: "this feature doesn't handle X scenario", "the API returns the wrong response for Y", "this breaks in production when Z". These describe what the code should *do*, not how it should be *written*.
- **Mechanical style enforced by tooling**: whitespace, semicolons, brace placement where a linter or formatter already handles it.
- **Bot comments**: skip any comment where `user.login` ends in `[bot]`.
- **Open-ended questions**: "have you considered X?" without a clear directive.

---

**Output schema** — a JSON array, each element:

```json
{
  "title": "Short rule title (5-8 words)",
  "rule": "The convention as a clear imperative instruction",
  "strength": "explicit",
  "do_example": {"code": "...", "language": "go"},
  "dont_example": {"code": "...", "language": "go"},
  "suggested_target": {
    "location": "CLAUDE.md or domain-name (e.g. api, auth, models)",
    "file_glob": ["src/api/**/*.go"]
  },
  "raw_signal": {
    "pr_number": 42,
    "comment_id": 12345,
    "reviewer": "username",
    "date": "2024-01-15",
    "snippet": "EXACT VERBATIM QUOTE"
  }
}
```

`strength`: `"explicit"` if the reviewer stated a general rule or preference; `"implicit"` if it is a correction or suggestion without a stated general principle.

`snippet`: the reviewer's exact verbatim words. Do not paraphrase. If you cannot quote the reviewer directly supporting the rule, omit the signal entirely.

`do_example` / `dont_example`: include when code examples are present in the comment or clearly implied. Omit rather than invent.

Output only the JSON array, no other text.

PR data:
[INSERT PR JSON HERE]

---

Collect all signals returned by all subagent runs.

---

### Step 7: Grounding verification

For each signal returned in Step 6:
1. Open `.claude/pattern-learner/raw-cache/pr-<raw_signal.pr_number>.json`.
2. Search the entire file content for `raw_signal.snippet` as a case-insensitive substring.
3. If the snippet is NOT found anywhere in that file: **discard the signal**. Count it as dropped.
4. If found: keep the signal.

Track: total signals extracted, signals dropped by grounding check.

---

### Step 8: Deduplicate and aggregate

In a single reasoning pass over all verified signals and the current state from Step 4:

**A. Intra-batch dedup**: Find signals across the batch that express semantically equivalent conventions (same intent, even if worded differently). Merge them into one candidate with a combined `sources` list. If any of the merged signals has `strength: "explicit"`, the merged candidate is explicit.

**B. Against existing state rules**: For each candidate:
- If semantically equivalent to an existing rule in state → append the new signal to that rule's `sources`, increment `signal_count`, update `last_seen_pr`. Then recompute the rule's confidence using the Step 9 logic (check whether any source is explicit; if so, apply the explicit path — `"established"` if 3+ signals total, `"stated"` if fewer; otherwise apply the implicit path). Do NOT create a new rule.
- If semantically distinct → treat as a new candidate rule.

**C. Against rejected signals**: If semantically equivalent to any entry in `rejected_signals` → discard silently.

New candidates get `status: "proposed"`. IDs will be assigned by `state-write` in Step 11.

---

### Step 9: Signal threshold and confidence

Apply rules based on signal strength:

**Explicit candidates** — at least one source has `strength: "explicit"`:
Keep unconditionally regardless of occurrence count. Assign confidence:
- `"established"` — 3+ signals total
- `"stated"` — 1–2 signals total (explicitly declared preference, fewer occurrences)

**Implicit candidates** — all sources have `strength: "implicit"` (or empty):
Keep only if:
- 3+ distinct PR numbers in `sources`, OR
- 2+ distinct reviewers in `sources`

Assign confidence:
- `"established"` — 5+ signals total
- `"emerging"` — 3–4 signals total

Discard implicit candidates below threshold.

---

### Step 10: Approval UI

Present each rule for user decision. Show new candidates first (status: "proposed"), then any existing proposed rules from prior runs.

Group by target: `CLAUDE.md` rules first, then alphabetically by domain.

For each rule, display:

```
────────────────────────────────────────────────────
Rule: <title>
Target: <CLAUDE.md | domain>
Confidence: <stated (explicit preference) | established | emerging> (<N> signals across <M> PRs)

Convention:
  <rule text>

✓ Do:
  <language>
  <do_example.code>

✗ Don't:
  <language>
  <dont_example.code>

Evidence:
  PR #<N> (@<reviewer>) [explicit]: "<snippet>"
  PR #<N> (@<reviewer>): "<snippet>"
  [... up to 3 examples shown; tag [explicit] when signal strength is "explicit"]

[a]pprove  [r]eject  [e]dit  [s]kip (defer)
────────────────────────────────────────────────────
```

Wait for user input per rule:
- `a` → set `status: "approved"`
- `r` → set `status: "rejected"`; this rule will move to `rejected_signals` in state
- `e` → prompt user to edit title, rule text, or examples inline; re-display updated rule for confirmation
- `s` → leave as `status: "proposed"` (will appear again on next `--review`)

---

### Step 11: Persist state

Build the complete updated state JSON:
- All rules (approved, rejected, proposed, superseded) with updated statuses, signal counts, sources
- Updated `last_extracted_pr_number` = `max_pr_seen` from Step 5 (the highest PR number encountered on any page, merged or not — this sets the watermark so the next refresh only fetches newer PRs). Leave unchanged if `--review`.
- Updated `last_run`, `repo`, `stats`
- Rules with `status: "rejected"` should also appear in `rejected_signals` with their rule text preserved for future matching

Write the JSON to a staging file using the Write tool (avoids shell command-line length limits):
```
.claude/pattern-learner/state-pending.json
```

Then pipe it to state-write:
```
cat .claude/pattern-learner/state-pending.json | $BIN state-write --state .claude/pattern-learner/state.json
```

The binary assigns UUIDs to new rules and writes atomically. Delete `state-pending.json` after a successful write.

---

### Step 12: Generate output files

Run:
```
$BIN write-outputs --state .claude/pattern-learner/state.json --output-dir .
```

This writes:
- `CLAUDE.md` — approved rules targeting `CLAUDE.md`, max 30, stated first then established then emerging
- `.claude/skills/<domain>/SKILL.md` — one file per domain with approved rules

---

### Step 13: Summary

Report to the user:

```
── Pattern Learner Summary ──────────────────────────
PRs scanned this run:       <N>
  review comments:          <N>
  issue comments:           <N>
  review bodies:            <N>
Signals extracted:          <N>
  explicit (spoken-word):   <N>
  implicit (corrections):   <N>
Signals dropped (grounding):<N>
New rules proposed:         <N>
  Approved:                 <N>
    stated (explicit):      <N>
    established:            <N>
    emerging:               <N>
  Rejected:                 <N>
  Skipped (deferred):       <N>
Files written:
  CLAUDE.md                 (<N> rules)
  .claude/skills/<domain>/SKILL.md  (<N> rules)
  [...]
Stale rules (last_seen_pr is 200+ below current watermark):
  <list titles or "none">
─────────────────────────────────────────────────────
```
