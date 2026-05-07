---
name: learn-patterns
description: Extract coding conventions and developer preferences from this repo's PR review history and write approved rules to CLAUDE.md and skill files. Treats reviewer preferences as authoritative spoken-word rules — including indirect language like polite questions ("could we use X?"), skeptical critique ("interesting choice"), and hedged suggestions — and captures them regardless of occurrence count. Use --refresh for incremental since last run, --review to re-open approval without re-fetching, --discover to find patterns directly from the codebase using cursor-agent.
argumentHint: "[--refresh | --review | --discover [domain ...]]"
---

# learn-patterns

Extracts coding conventions and developer preferences from merged PR review comments and writes approved rules to `CLAUDE.md` and domain-specific skill files. Treats reviewer preferences — including indirect, hedged, and skeptical language — as spoken-word rules captured at face value, regardless of occurrence count.

## Instructions

Follow all steps in order. Do not skip steps unless the mode explicitly says to.

---

### Step 1: Mode detection

Parse `$ARGUMENTS`:
- `--refresh` → incremental mode: only fetch PRs newer than last run
- `--review` → approval-only mode: skip all fetching, go straight to Step 10
- `--discover [domain ...]` → codebase-discovery mode: use cursor-agent to find patterns directly from code, skip PR fetching. Optional domain names after `--discover` target specific domains (e.g. `--discover api auth`). If no domains given, discover for all domains that already have approved rules.
- (nothing) → full mode: fetch all merged PRs

If `--discover` is set, jump to the **Discover Mode** section after Step 4.

---

### Step 2: Pre-flight checks and binary build

**Pre-flight checks** (run first, abort with a clear message on failure):
- `which go` — Go must be installed to build the binary. If missing, tell the user: "Go (1.21+) is required to build the pattern-learner binary. Install Go from https://go.dev/dl/ and retry."
- Confirm GitHub MCP tools are available in the session by checking that `mcp__github__list_pull_requests` is present. If not, tell the user: "The GitHub MCP server is not configured in this session. Add the GitHub MCP server to your Claude Code config and retry." (Skip this check in `--review` and `--discover` modes since no PR fetching happens.)
- `which cursor-agent` — check if cursor-agent is available. Store result as `HAS_CURSOR_AGENT` (true/false). In `--discover` mode, if cursor-agent is not found, abort: "cursor-agent is required for --discover mode. Install Cursor and ensure cursor-agent is on your PATH." In other modes, cursor-agent is optional — absence is not an error.

**Binary build**: check whether `.claude/pattern-learner/bin/pattern-learner` exists in the current working directory (the target repo root).

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
2. **Preprocess the raw data into a lean view before sending to the subagent.** Sending the full PR JSON wastes context on diffs, file changes, labels, and PR descriptions that are noise for pattern extraction. From each PR's `raw` field, extract only:
   ```json
   {
     "pr_number": 42,
     "files_touched": ["internal/api/handlers/users.go", "..."],
     "comments": [
       {
         "id": 12345,
         "user": "alice",
         "is_pr_author": false,
         "created_at": "2024-01-15T...",
         "in_reply_to_id": null,
         "type": "review_comment",
         "path": "internal/api/handlers/users.go",
         "body": "Could we use context.WithTimeout here?"
       },
       ...
     ]
   }
   ```
   - `is_pr_author`: true when the comment author equals the PR author. Used by the subagent to detect author acknowledgment vs reviewer feedback.
   - `type`: one of `review_comment`, `issue_comment`, `review_body`.
   - `path`: file path for review comments; null for issue comments and review bodies.
   - Drop diff hunks, position info, blob URLs, reactions, and any field not listed above.

   Concatenate the lean views into a single JSON array per batch.

3. Launch a subagent with this exact prompt (fill in the lean PR data at the end):

---
**SUBAGENT PROMPT:**

You are analyzing PR review comments to discover how this team prefers to write code.

Your output becomes the coding rules that AI assistants follow in this repository. Extract the team's stated preferences, naming conventions, architectural choices, and coding standards as expressed in code review.

Below is raw JSON for a batch of merged pull requests including reviews, comments, and reply threads.

---

**Default assumption**: in code review, reviewers rarely state preferences as blunt directives. Most conventions are communicated through polite questions, hedged suggestions, or skeptical critique. **Questions are usually directives.** A reviewer wouldn't ask if they didn't have an opinion. Take indirect feedback at face value as a real preference signal.

---

**PRIORITY 0 — GitHub code suggestions (strongest possible signal)**

Reviewer comments containing ` ```suggestion ` blocks. The reviewer literally wrote the code they want — this IS the convention. The diff between the original line(s) and the suggested replacement shows the pattern. Extract as `"strength": "explicit"`.

**PRIORITY 1 — Stated preferences (explicit OR indirect; capture from a single occurrence)**

Treat all of the following as explicit preference signals (`"strength": "explicit"`):

- **Direct preferences**: "we prefer / we like / we always / we never / our convention is / always use / never use / going forward / in this codebase / our pattern is"
- **Polite directives framed as questions**: "could we use X?", "what about X?", "have you considered X?", "why not X?", "should this be X?", "any reason not to X?"
- **Skeptical or passive-aggressive critique**: "is there a reason for this approach?", "I usually see this done with Y", "I would have done X", "hmm, this is unusual", "interesting choice", "this works but...", "this is fine I guess but..."
- **Hedged preferences**: "maybe consider X", "I think we usually X", "we tend to X here", "it might be cleaner to X", "wouldn't it be better to X?"

**Author acknowledgment** strengthens a signal. Walk the comment threads (`in_reply_to_id` linkage). When the PR author responds to a reviewer's comment with confirmation language — "good catch", "you're right", "ok fixed", "updated", "thanks", "sgtm", "done", "addressed" — the reviewer's feedback was a real ask. Treat such confirmed comments as the highest-quality explicit signals.

**PRIORITY 2 — Recurring corrections without explicit framing** (`"strength": "implicit"`)

Reviewers fixing the same thing across multiple comments without stated preference.

---

**DO NOT extract:**

- **Bug fixes for specific code**: "this returns null when X is empty", "missing nil check on line 42", "wrong assertion in this test", "this throws when input has trailing spaces". The comment points to a concrete defect in *this* PR, not a general principle.
- **Product or feature correctness**: "this doesn't handle X user scenario", "the API returns the wrong response for Y", "this breaks in production when Z". These describe what the code should *do*, not how it should be *written*.
- **Genuine clarification questions** (information-seeking): "what does this variable represent?", "is this still used anywhere?", "what's the use case for this case?", "is this intentional?". Distinguish from **rhetorical questions** (preference signals): "why are we doing it this way?", "why not just X?", "do we really need this?".
- **Author asking reviewer**: when the PR author is asking the reviewer a question, not the other way around. Look at who is the author of each comment.
- **Mechanical style enforced by tooling**: whitespace, semicolons, brace placement when a linter or formatter already handles it.
- **Bot comments**: skip any comment where `user.login` ends in `[bot]`, or matches one of: `dependabot`, `renovate`, `coderabbitai`, `copilot`, `github-actions`, `sonarqube`, `codecov`.

---

**Output schema** — a JSON array, each element:

```json
{
  "title": "Short rule title (5-8 words)",
  "rule": "The convention as a clear imperative instruction",
  "strength": "explicit",
  "do_examples": [
    {"code": "...", "language": "go", "context": "optional: surrounding function (±5 lines from diff)"}
  ],
  "dont_examples": [
    {"code": "...", "language": "go", "context": "optional: surrounding function (±5 lines from diff)"}
  ],
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

`strength`: `"explicit"` for any Priority 0 or Priority 1 signal (direct, indirect, polite, skeptical, hedged, or code-suggestion); `"implicit"` only for Priority 2 recurring corrections without stated framing.

`snippet`: the reviewer's exact verbatim words. Do not paraphrase. If you cannot quote the reviewer directly supporting the rule, omit the signal entirely. For code-suggestion signals, the snippet may be the suggested code block itself.

`do_examples` / `dont_examples`: arrays — include all examples available for this signal. A single comment may produce multiple examples; return them all. For **Priority 0 suggestion blocks**: set `dont_examples[0].code` to the original lines being replaced and `do_examples[0].code` to the suggested replacement; populate `context` with ±5 lines of surrounding diff context so the AI can see the pattern in situ. For other comment types, include any inline code blocks or referenced before/after changes. Omit rather than invent. Use the file paths the PR touched to infer the language.

`suggested_target`: use the file paths the PR touches as a hint. PRs touching only `internal/api/**` should suggest `"location": "api"` rather than `"CLAUDE.md"`. Reserve `"CLAUDE.md"` for project-wide rules that span multiple domains.

Output only the JSON array, no other text.

PR data (lean preprocessed view — comments + metadata + file paths only, diffs and other PR metadata stripped):
[INSERT LEAN PR DATA HERE]

---

Collect all signals returned by all subagent runs.

---

### Step 7: Grounding verification

For each signal returned in Step 6:
1. **Snippet length check**: if `raw_signal.snippet` (after trimming whitespace) is fewer than 20 characters, discard the signal — short fragments substring-match too easily and provide no real provenance. Count as dropped.
2. Open `.claude/pattern-learner/raw-cache/pr-<raw_signal.pr_number>.json`.
3. Normalize both the cached file content and `raw_signal.snippet` for comparison: lowercase both, then collapse runs of whitespace (spaces, tabs, newlines) into single spaces. This tolerates minor formatting differences like wrapped lines or escaped newlines without admitting genuine paraphrases.
4. Check whether the normalized snippet appears as a substring of the normalized file content.
5. If the snippet is NOT found: **discard the signal**. Count it as dropped.
6. If found: keep the signal.

Track: total signals extracted, signals dropped by grounding check (broken down by reason: too-short vs not-found).

---

### Step 8: Deduplicate, normalize, and aggregate

In a single reasoning pass over all verified signals and the current state from Step 4:

**A. Intra-batch dedup**: Find signals across the batch that express semantically equivalent conventions (same intent, even if worded differently). Merge them into one candidate with a combined `sources` list. If any of the merged signals has `strength: "explicit"`, the merged candidate is explicit. Also merge their `do_examples` and `dont_examples` arrays: deduplicate by code content (exact string match after trimming whitespace), then cap each array at 4 entries. The result is a richer set of real examples accumulated across multiple PRs that all express the same convention.

**B. Domain normalization**: For each candidate's `suggested_target.location`, normalize variants of the same domain to a single canonical name. Treat `"api"`, `"API"`, `"rest-api"`, `"endpoints"`, `"http"` as the same domain (pick one canonical form, e.g. `"api"`); `"auth"`, `"authentication"`, `"authn"` as the same; etc. Also unify against existing rule domain names already in state — if state already uses `"api"`, normalize new candidates' `"endpoints"` to `"api"`. The goal is one skill file per logical domain, not fragmented files.

**C. Against existing state rules**: For each candidate:
- **Equivalent**: semantically the same convention → append the new signal to that rule's `sources`, increment `signal_count`, update `last_seen_pr`. Recompute confidence via Step 9 logic (any source explicit → explicit path: `"established"` 3+ signals, `"stated"` fewer; else implicit path). Preserve the existing rule's text and `status`. Merge the candidate's `do_examples`/`dont_examples` into the existing rule's arrays (deduplicate by code content, cap each at 4). Do NOT create a new rule.
- **Contradicts**: semantically *opposite* to an existing rule (e.g., existing says "use X", new says "we always use Y instead"). Do NOT merge. Create a new candidate rule with `supersedes: ["<existing_rule_id>"]`. The user will see both in the approval UI and decide whether to accept the supersession (which then sets the existing rule's `status: "superseded"` and `superseded_by: "<new_rule_id>"`).
- **Semantically distinct**: not equivalent and not contradicting → treat as a new candidate rule.

**D. Against rejected signals**: If a candidate is semantically equivalent to any entry in `rejected_signals` → discard silently. Do this AFTER contradiction check so an explicit reversal of a rejected rule still has a chance to surface (rare but possible).

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
[Supersedes: rule_<id> "<superseded rule title>" — only shown when supersedes is non-empty]

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
- `a` → set `status: "approved"`. If the rule has non-empty `supersedes`, also set the superseded rule's `status: "superseded"` and `superseded_by: "<this_rule_id>"` (the binary will fill `<this_rule_id>` at write time if not yet assigned — pass the rule's index for now).
- `r` → set `status: "rejected"`; this rule will move to `rejected_signals` in state
- `e` → prompt user to edit title, rule text, or examples inline; re-display updated rule for confirmation
- `s` → leave as `status: "proposed"` (will appear again on next `--review`)

After all rules are reviewed, display a summary of decisions:
```
You approved <N>, rejected <N>, edited <N>, skipped <N>. Proceed to write state? [y/n]
```
Wait for confirmation before continuing to Step 11. If `n`, exit without modifying state.

---

### Step 11: Persist state

Build the complete updated state JSON:
- All rules (approved, rejected, proposed, superseded) with updated statuses, signal counts, sources
- Updated `last_extracted_pr_number` = `max_pr_seen` from Step 5 (the highest PR number encountered on any page, merged or not — this sets the watermark so the next refresh only fetches newer PRs). Leave unchanged if `--review`.
- Updated `last_run`, `repo`, `stats`
- Rules with `status: "rejected"` should also appear in `rejected_signals` with their rule text preserved for future matching
- **`domain_descriptions`**: for each domain that now has approved rules, set or refresh the entry. Read all approved rules in that domain and synthesize a 1–2 sentence description (max 200 chars) that:
  - Names what the skill is for (e.g. "HTTP API endpoint conventions")
  - Lists 2–3 concrete topics from the rules (e.g. "error responses, validation, auth middleware")
  - Includes a "Use when editing" hint based on the file globs

  Example: `"Conventions for HTTP API endpoints: error response format, validation patterns, auth middleware. Use when editing src/api/."`

  This description goes into the generated skill file's frontmatter and drives Claude Code's auto-loading. Generic descriptions ("Coding conventions for api") won't trigger loading at the right times — be specific.

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

Build the flags based on available tools:
- If `HAS_CURSOR_AGENT` is true: add `--rag-hints` (embeds live query hints in skill files) and `--rag` (uses cursor-agent for semantic anchoring instead of grep)
- Otherwise: no extra flags

Run:
```
$BIN write-outputs --state .claude/pattern-learner/state.json --output-dir . [--rag-hints] [--rag]
```

This writes:
- `CLAUDE.md` — approved rules targeting `CLAUDE.md`, max 30, stated first then established then emerging
- `.claude/skills/<domain>/SKILL.md` — one file per domain with approved rules; when `--rag-hints` is set, each rule includes a `cursor-agent` command for retrieving live codebase examples at skill-use time

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
  too short (<20 chars):    <N>
  not found in source:      <N>
New rules proposed:         <N>
  Approved:                 <N>
    stated (explicit):      <N>
    established:            <N>
    emerging:               <N>
  Rejected:                 <N>
  Skipped (deferred):       <N>
Supersessions accepted:     <N>  (existing rules replaced)
Files written:
  CLAUDE.md                 (<N> rules)
  .claude/skills/<domain>/SKILL.md  (<N> rules)
  [...]
Stale rules (last_seen_pr is 200+ below current watermark):
  <list titles or "none">
RAG anchoring:              <"cursor-agent (semantic)" | "grep (fallback)" | "none">
RAG hints in skill files:   <yes | no>
─────────────────────────────────────────────────────
```

---

## Discover Mode

Invoked when `--discover` is set. Runs after Step 4 (state read) and replaces Steps 5–9 with codebase analysis via cursor-agent. Steps 10–13 (approval, state write, output generation, summary) run as normal.

---

### Discover Step D1: Determine target domains

If domain names were passed after `--discover` (e.g. `--discover api auth`), use those.

Otherwise, collect target domains from the current state:
- All domains that have at least one approved rule (from Step 4 state)
- If the state is empty (no approved rules yet), ask the user: "No domains found in state. Which domains would you like to discover? (e.g. `api auth models`)"

For each domain, gather its file globs from the approved rules in state. If a domain has no rules yet (user-specified domain not in state), ask: "What file patterns should I search for the `{domain}` domain? (e.g. `internal/api/**/*.go`)"

---

### Discover Step D2: Run cursor-agent discovery per domain

For each target domain, run the following cursor-agent query. Use the domain's file globs to focus the search.

```
cursor-agent -p --mode=ask "<DISCOVERY PROMPT>"
```

**Discovery prompt** (fill in `{domain}` and `{globs}`):

---
Analyze this codebase and identify coding conventions the team consistently follows in files matching: {globs}

This is for the `{domain}` domain. Your job is to find conventions a new developer or AI coding assistant would need to know — the things that make code in this domain "fit in" with existing code.

Focus on:
- Naming conventions (functions, types, variables, files) specific to this domain
- Error handling and propagation patterns
- Structural/architectural patterns (how things are organized, what calls what)
- Data flow conventions (how data is passed, transformed, returned)
- What NOT to do — patterns that would look wrong to experienced contributors

Do NOT extract:
- General language best practices (these are already known)
- Conventions enforced by linters or formatters
- Project-wide rules that apply everywhere (not domain-specific)

For each convention you find, return a JSON object with these exact fields:
- `title`: short rule title, 5-8 words
- `rule`: imperative instruction, one sentence
- `do_examples`: array of real code from the codebase — take actual snippets verbatim, with file path and line number. At least one example required; up to 3.
  Each: `{"code": "...", "language": "...", "file_ref": "path/file.go:L42", "context": "optional surrounding function"}`
- `dont_examples`: array of what NOT to do (can be constructed from the "before" side of common mistakes you see, or clearly wrong alternatives). Each: `{"code": "...", "language": "..."}`
- `suggested_target`: `{"location": "{domain}", "file_glob": ["{globs}"]}`
- `confidence`: `"high"` (pattern in 5+ places), `"medium"` (2-4 places), `"low"` (1 place but clearly intentional)

Return a JSON array of 5–15 conventions. Real examples only — copy actual code from the files verbatim. Output only the JSON array, no other text.
---

Capture cursor-agent's output. Extract the JSON array from the response (cursor-agent may wrap it in prose — find the `[` ... `]` block).

---

### Discover Step D3: Parse and normalize candidates

Read each extracted candidate and construct a candidate rule:
- `title`: from cursor-agent output
- `rule`: from cursor-agent output
- `do_examples`: from cursor-agent output (already include FileRef and Context)
- `dont_examples`: from cursor-agent output
- `target`: `{location: domain, file_glob: globs}`
- `confidence`: map cursor-agent confidence → rule confidence: `"high"` → `"established"`, `"medium"` → `"emerging"`, `"low"` → `"stated"`
- `sources`: `[]` (empty — no PR source; codebase-derived)
- `signal_count`: 1
- `strength`: `"explicit"` (AI-curated from real code; treat as authoritative)
- `status`: `"proposed"`

---

### Discover Step D4: Deduplicate against existing state

Run the same dedup logic as Step 8 (C and D):
- **Equivalent to existing approved rule**: merge examples (cap at 4 per array), note the discovery confirmed the rule. Do not create a new rule.
- **Contradicts existing rule**: create supersession candidate with `supersedes: ["<id>"]`.
- **Semantically distinct and not in rejected_signals**: add as new proposed candidate.
- **Equivalent to rejected_signals entry**: discard silently.

Skip Step 8A (intra-batch dedup) — run it across all domains' candidates combined before the above.

---

### Discover Step D5: Apply threshold

All discover candidates have `strength: "explicit"` so they are kept unconditionally regardless of count. Confidence was already set in D3 from cursor-agent's rating.

Then continue with **Step 10** (approval UI), **Step 11** (state write), **Step 12** (generate outputs), **Step 13** (summary).

In Step 13 summary, replace PR-related counters with:
```
Domains analyzed:           <N>
Candidates found:           <N>
  high confidence:          <N>
  medium confidence:        <N>
  low confidence:           <N>
Candidates merged into existing rules: <N>
New candidates proposed:    <N>
```
