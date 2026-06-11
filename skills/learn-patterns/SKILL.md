---
name: learn-patterns
description: Extract coding conventions and developer preferences from this repo's PR review history and write approved rules to CLAUDE.md and skill files. Treats reviewer preferences as authoritative spoken-word rules — including indirect language like polite questions ("could we use X?"), skeptical critique ("interesting choice"), and hedged suggestions — and captures them regardless of occurrence count. ALSO USE THIS SKILL to record a coding rule a developer states while working — whenever the user says things like "add a rule that…", "remember we always/never…", "make a convention that…", "save this as a rule", "let's standardize on…", or otherwise wants to persist a coding standard: invoke with --add so the rule is written into the right skill file (portable across Claude Code instances) instead of being lost in local session memory. Use --refresh for incremental since last run, --review to re-open approval without re-fetching (skips unchanged emerging rules already seen; add --all to force-show all), --auto to run without any interactive approval (defers supersessions, conflicts, and single-implicit singletons for human review; add --auto-threshold to also auto-approve singletons), --discover to find patterns directly from the codebase using cursor-agent, --add to manually record a single human-authored rule.
argumentHint: "[--refresh | --review [--all] | --auto [--refresh] [--auto-threshold] | --discover [domain ...] | --add [rule text]]"
---

# learn-patterns

Extracts coding conventions and developer preferences from merged PR review comments and writes approved rules to `CLAUDE.md` and domain-specific skill files. Treats reviewer preferences — including indirect, hedged, and skeptical language — as spoken-word rules captured at face value, regardless of occurrence count.

## Tooling policy (read first)

Every deterministic step in this pipeline is implemented as a subcommand of the
`pattern-learner` binary (`$BIN`): `detect-repo`, `state-read`, `state-write`,
`write-outputs`, `extract-lean`, `verify-grounding`, `classify`, `triage`. **These
subcommands are the only sanctioned implementations of their logic.**

- **Do NOT** write or run ad-hoc scripts (Python, Node, Ruby, Perl, shell scripts, etc.)
  to fetch, preprocess, ground-check, deduplicate by rule, score confidence, or triage.
  These were the source of past correctness failures (lost `strength`, dropped signals,
  fabricated rule text).
- The only work you perform directly is genuinely semantic: signal extraction (via the
  Step 6 subagent), semantic dedup, contradiction detection, and domain canonicalization
  (Step 8). Everything else is a `$BIN` call.
- **If a subcommand is missing, errors, or seems unable to do what you need: STOP and
  report it to the user. Do not write a workaround script.** A gap in the tooling is a
  bug to fix in the binary, not something to paper over.

This policy is mechanically enforced: while a run is in progress (Step 2 creates a
run-lock at `.claude/pattern-learner/.run-lock`), a PreToolUse hook blocks interpreter
invocations and script file creation. If you find yourself blocked, it means you are
trying to reimplement a sanctioned subcommand — use the subcommand instead.

## Instructions

Follow all steps in order. Do not skip steps unless the mode explicitly says to.

---

### Step 1: Mode detection

Parse `$ARGUMENTS`:
- `--refresh` → incremental mode: only fetch PRs newer than last run
- `--review` → approval-only mode: skip all fetching, go straight to Step 10
- `--auto` → unattended mode: run the full pipeline (or combine with `--refresh` for incremental) and auto-approve rules at Step 10 without any interactive prompts. Supersessions, conflicts, and single-implicit singletons are auto-deferred for human review. `--auto` + `--review` is invalid — abort with: "Error: --auto and --review are incompatible. --review requires human approval; --auto skips it."
- `--auto-threshold` → modifier for `--auto` only: also auto-approve single-implicit singletons that would normally be deferred. Has no effect without `--auto`.
- `--all` → modifier for `--review` only: force-show all proposed emerging rules in the approval loop, including ones the user previously skipped that have not received new signals since. Has no effect without `--review`.
- `--discover [domain ...]` → codebase-discovery mode: use cursor-agent to find patterns directly from code, skip PR fetching. Optional domain names after `--discover` target specific domains (e.g. `--discover api auth`). If no domains given, discover for all domains that already have approved rules.
- `--add [rule text]` → manual-add mode: record a single human-authored rule the developer wants to persist (no PR fetching, no cursor-agent). Any text after `--add` is the rule statement; if absent, infer the rule from the user's request in the conversation. `--add` is incompatible with every other mode flag — if combined, abort with: "Error: --add records one manual rule and cannot be combined with other modes."
- (nothing) → full mode: fetch all merged PRs

Store `IS_AUTO` = true when `--auto` is present. Store `IS_AUTO_THRESHOLD` = true when `--auto-threshold` is present (only meaningful with `IS_AUTO`). Store `IS_SHOW_ALL` = true when `--all` is present (only meaningful with `--review`).

If `--discover` is set, jump to the **Discover Mode** section after Step 4.
If `--add` is set, jump to the **Add Mode** section after Step 4.

---

### Step 2: Pre-flight checks and binary build

**Pre-flight checks** (run first, abort with a clear message on failure):
- `which go` — Go must be installed to build the binary. If missing, tell the user: "Go (1.21+) is required to build the pattern-learner binary. Install Go from https://go.dev/dl/ and retry."
- Confirm GitHub MCP tools are available in the session by checking that `mcp__github__list_pull_requests` is present. If not, tell the user: "The GitHub MCP server is not configured in this session. Add the GitHub MCP server to your Claude Code config and retry." (Skip this check in `--review`, `--discover`, and `--add` modes since no PR fetching happens.)
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

**Binary self-check**: run `$BIN` with no arguments and confirm the usage output lists every expected subcommand: `extract-lean`, `verify-grounding`, `classify`, `triage`, `guard` (in addition to `detect-repo`, `state-read`, `state-write`, `write-outputs`). If any are missing, the binary is stale — delete it and rebuild from Step 2. If they are still missing after a clean rebuild, STOP and report it. Do not proceed with a binary that lacks the pipeline subcommands.

**Create the run-lock** (enables the off-script guard for the duration of this run):
```
mkdir -p .claude/pattern-learner && touch .claude/pattern-learner/.run-lock
```
The lock activates the PreToolUse hook that blocks interpreter/script usage (see the Tooling policy). **If the skill aborts at any later step, remove the lock** (`rm -f .claude/pattern-learner/.run-lock`) before exiting so the guard does not linger.

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

> **Large repo note** (1,000+ merged PRs): one-at-a-time MCP calls will be slow. Bulk-fetching with `gh api --paginate` piped through `jq` is the supported alternative — `gh`, `jq`, and `xargs` are permitted by the guard. Run these as **inline commands**, not as an authored `.sh`/`.py` script file (the guard blocks script creation during a run, and a fetch script is exactly the kind of off-pipeline tooling this prevents). Each fetched PR must be written as a cache file in the same format:
> ```json
> { "pr_number": N, "comment_sources": {"review_comments": <count>, "issue_comments": <count>, "review_bodies": <count>}, "raw": <full PR data> }
> ```
> The cache contract is identical; Steps 6–13 are unaffected regardless of how the cache was populated. Parallelize with `xargs -P8`. Output must follow the `.claude/pattern-learner/raw-cache/pr-N.json` naming convention. (Fetch is the one network step the binary cannot perform — it is zero-network by design — so this inline `gh` path is sanctioned.)

---

### Step 6: Extract signals via subagents

For each batch of up to 20 PR numbers:
1. Read each `.claude/pattern-learner/raw-cache/pr-N.json`.
2. **Preprocess the raw data into a lean view** by running the `extract-lean` subcommand. This deterministically parses `code_before` from each review comment's `diff_hunk`, computes `is_pr_author`, and drops all noise fields:

   ```
   $BIN extract-lean --cache-dir .claude/pattern-learner/raw-cache --prs <comma-separated batch PR numbers>
   ```

   The output is a JSON array of lean PR views ready to insert into the subagent prompt. The lean view schema (for reference — do not reconstruct it by hand):
   ```json
   {
     "pr_number": 42,
     "files_touched": ["internal/api/handlers/users.go"],
     "comments": [
       {
         "id": 12345,
         "user": "bob",
         "is_pr_author": false,
         "created_at": "2024-01-15T...",
         "in_reply_to_id": null,
         "type": "review_comment",
         "path": "internal/api/handlers/users.go",
         "body": "Could we use context.WithTimeout here?",
         "code_before": "ctx := context.Background()"
       }
     ]
   }
   ```

3. Launch a subagent with this exact prompt (fill in the `extract-lean` output at the end):

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

**Rule text provenance (required)**: `title` and `rule` MUST be synthesized from the reviewer's actual words, not from a pre-authored category or taxonomy. If the reviewer wrote "we prefer returning errors as values here, not panicking", the rule should say "Return errors as values; do not panic for expected error cases" — not a generic error-handling rule invented by the subagent. A reviewer must be able to read the rule and recognize their own feedback. Rules that are disconnected from reviewer language are fabricated — discard them and omit the signal.

`do_examples` / `dont_examples`: arrays — include all examples available. Use this priority order for sourcing them:

1. **Priority 0 suggestion blocks**: `dont_examples[0].code` = original lines replaced; `do_examples[0].code` = suggested replacement. Populate `context` with ±5 lines of surrounding diff context.
2. **`code_before` field** (present on `review_comment` type): use as `dont_examples[0].code` when no suggestion block is present. This is verbatim code from the diff the reviewer was looking at — the highest-fidelity dont example available. Use the comment's body, inline code, or your inference to construct the corresponding `do_example`.
3. **Inline code in comment body**: backtick-quoted code in the comment text.
4. **Inference from reviewer wording**: only when none of the above are available.

A single comment may produce multiple examples; return them all. Omit rather than invent when no example source is available. Use the file paths the PR touched to infer the language.

`suggested_target`: use the file paths the PR touches as a hint. Pick the **most specific** domain the PR's file paths suggest — e.g. `"migrations"` not `"database"`, `"mutations"` not `"backend"`, `"workers"` not `"jobs"`. Never use coarse buckets like `"backend"`, `"frontend"`, or `"general"` as a location — always go one level more specific (e.g. `"api"`, `"auth"`, `"models"`, `"migrations"`, `"workers"`, `"components"`, `"hooks"`). Reserve `"CLAUDE.md"` **only** for rules that apply universally regardless of which file is being edited: naming conventions, commit message format, anti-patterns true everywhere in the codebase. When in doubt, prefer a specific domain over `"CLAUDE.md"`.

Output only the JSON array, no other text.

PR data (lean preprocessed view — comments + metadata + file paths; review comments include `code_before` extracted from diff_hunk):
[INSERT LEAN PR DATA HERE]

---

Collect all signals returned by all subagent runs.

---

### Step 7: Grounding verification

Pipe the signals array from Step 6 through the `verify-grounding` subcommand:

```
cat signals.json | $BIN verify-grounding --cache-dir .claude/pattern-learner/raw-cache
```

The tool enforces:
- **≥20-char minimum** (trimmed rune count): short fragments substring-match too easily and provide no real provenance.
- **Normalized substring match**: lowercases both snippet and cached file content, collapses whitespace runs to single spaces, then checks containment. Tolerates minor formatting differences without admitting genuine paraphrases.

Output shape: `{"kept": [...signals...], "stats": {"too_short": N, "not_found": N, "kept": N}}`.

Read `stats.too_short` and `stats.not_found` for the Step 13 summary. Use `kept` as the verified signals array for Step 8.

---

### Step 8: Deduplicate, normalize, and aggregate

Run semantic dedup in domain-sharded reasoning passes over the verified signals and the current state from Step 4. For small signal sets (under ~100 signals from a single domain) a single reasoning pass is fine. For larger sets, shard by `suggested_target.location` and run one reasoning pass per domain shard to prevent context overload — then combine the per-shard candidates for Steps 8C–D. The sharding threshold is a practical concern, not a correctness one; the output of each approach is the same candidates list.

**A. Intra-batch dedup**: Find signals across the batch that express semantically equivalent conventions (same intent, even if worded differently). Merge them into one candidate with a combined `sources` list. If any of the merged signals has `strength: "explicit"`, the merged candidate is explicit. Also merge their `do_examples` and `dont_examples` arrays: deduplicate by code content (exact string match after trimming whitespace), then cap each array at 4 entries. The result is a richer set of real examples accumulated across multiple PRs that all express the same convention.

**Preserve `strength` in every intermediate representation**: when candidates are stored to disk or passed between steps, carry `sources[].strength` through verbatim. Do NOT compute strength from raw count alone or reconstruct it later. Losing `strength` in intermediate storage causes Step 9 to mis-assign confidence — all rules will appear implicit and zero `stated` rules will be emitted. When batches are aggregated across multiple runs, re-merge their `sources` lists preserving each entry's `strength` before applying 8C/8D.

**At scale (300+ signals from multiple batch runs)**: Step 8A is intra-batch only. After all batches complete, collect post-8A candidates and run a second cross-domain dedup pass before Step 8C. For very large signal sets, shard by `suggested_target.location` and run one subagent per domain shard — a narrower scope prevents context overload and produces sharper dedup. Combine each shard's output, then run Steps 8C–D on the unified candidate list.

**A-cross. Cross-batch contradiction detection**: After all batches complete their per-batch 8A pass, collect all post-8A candidates from all batches into one pool and check them against each other for contradictions. This is necessary because Step 8A is intra-batch only — a new convention from recent PRs and the old convention it replaced, extracted from different batches, would otherwise both surface as proposals.

For each pair of candidates that are semantically contradictory:
- Compute `max_pr(X)` = highest PR number across candidate X's `sources`; same for Y.
- The candidate with the **higher** `max_pr` is the current convention (winner). The other is the outdated convention (loser).
- Discard the loser as a standalone candidate. Attach a `prior_convention` note to the winner: `{title: loser.title, min_pr: min(loser.sources[].pr_number), max_pr: max(loser.sources[].pr_number)}`. This note is used in the Step 10 approval display only — it is NOT written to state.
- If both candidates' `max_pr` values are within 5 of each other (genuine ambiguity — the team may have been actively debating the convention), keep both as proposals and set `conflicted: true` on each. This field is persisted to state so that it survives `--auto` deferral and re-surfaces correctly in a subsequent `--review` run. The approval UI displays a `[CONFLICT]` marker for rules with `conflicted: true`.

**B. Domain normalization**: For each candidate's `suggested_target.location`, normalize variants of the same domain to a single canonical name. Treat `"api"`, `"API"`, `"rest-api"`, `"endpoints"`, `"http"` as the same domain (pick one canonical form, e.g. `"api"`); `"auth"`, `"authentication"`, `"authn"` as the same; etc. Also unify against existing rule domain names already in state — if state already uses `"api"`, normalize new candidates' `"endpoints"` to `"api"`. The goal is one skill file per logical domain, not fragmented files.

**CLAUDE.md qualification**: a rule belongs in `"CLAUDE.md"` only when it applies to every file in the repository regardless of technology or context — e.g. "no abbreviations in identifiers", "prefix commits with the ticket number", "never log PII". Rules that depend on file path, language, framework, or layer belong in domain skills, even if they appeared across many PRs. Coarse locations like `"backend"`, `"frontend"`, or `"general"` are not valid domain names — re-normalize these to the most specific subdomain the rule's file globs imply. When in doubt between `"CLAUDE.md"` and a domain, choose the domain.

**C. Against existing state rules**: For each candidate:
- **Equivalent**: semantically the same convention → append the new signal to that rule's `sources`, increment `signal_count`, update `last_seen_pr`. Recompute confidence via Step 9 logic (any source explicit → explicit path: `"established"` 3+ signals, `"stated"` fewer; else implicit path). Preserve the existing rule's text and `status`. Merge the candidate's `do_examples`/`dont_examples` into the existing rule's arrays (deduplicate by code content, cap each at 4). Do NOT create a new rule.
- **Contradicts**: semantically *opposite* to an existing rule (e.g., existing says "use X", new says "we always use Y instead"). Do NOT merge. Create a new candidate rule with `supersedes: ["<existing_rule_id>"]`. The user will see both in the approval UI and decide whether to accept the supersession (which then sets the existing rule's `status: "superseded"` and `superseded_by: "<new_rule_id>"`).
- **Semantically distinct**: not equivalent and not contradicting → treat as a new candidate rule.

**D. Against rejected signals**: If a candidate is semantically equivalent to any entry in `rejected_signals` → discard silently. Do this AFTER contradiction check so an explicit reversal of a rejected rule still has a chance to surface (rare but possible).

New candidates get `status: "proposed"`. IDs will be assigned by `state-write` in Step 11.

---

### Step 9: Signal threshold and confidence

Pipe the deduplicated candidates from Step 8 through the `classify` subcommand:

```
cat candidates.json | $BIN classify --max-pr-seen <max_pr_seen> --since-pr <since_pr>
```

The tool applies the strength-aware confidence rules and recency downgrade:

- **Explicit** (any source `strength: "explicit"`): `"established"` (3+ signals) or `"stated"` (1–2 signals). Kept unconditionally — a stated preference does not expire.
- **Implicit** (all sources implicit or empty): `"established"` (5+) or `"emerging"` (1–4). Recency downgrade: if the candidate's most-recent source PR is below the midpoint of the scanned range (`max_pr_seen − (max_pr_seen − since_pr) × 0.5`) the confidence is downgraded one tier (`established` → `emerging`; `emerging` → dropped).

`signal_count` is authoritatively set to `len(sources)` by the tool, ending manual drift.

Output: `{"kept": [...candidates with confidence set...], "dropped": N}`. Use `kept` as input to Step 10.

---

### Step 10: Approval UI

**If `IS_AUTO` is true**, skip the interactive loop entirely and use the `triage` subcommand instead:

1. Extract all `status: "proposed"` rules from the state into `proposed.json`.
2. Run:
   ```
   cat proposed.json | $BIN triage --mode auto [--auto-threshold]
   ```
   The tool applies the ordered predicate and returns the same rules with `status` patched to `"approved"` or left as `"proposed"` (deferred). Defer conditions (first match wins): `supersedes` non-empty → defer; `conflicted: true` → defer; `signal_count == 1` AND all sources implicit AND NOT `--auto-threshold` → defer. Otherwise → approve.
3. Replace the proposed rules in the state with the triage output.

Skip the confirmation prompt. Proceed automatically to Step 11. Print a single summary line:
```
Auto-approved: <N> rules  |  Deferred for review: <N> (run /learn-patterns --review to decide)
```

Then continue directly to Step 11.

---

**At scale (50+ proposed rules)**: rules are already grouped by domain — lean into that structure. It is acceptable to approve by confidence tier within a domain ("approve all `established` rules in `api`") rather than reviewing every rule individually. Use `--auto` for large initial runs with no supersessions; reserve the interactive loop for supersession rules and `[CONFLICT]` tags. For first-time runs on large repos, `--auto` + `--refresh` is the recommended path.

**If `IS_AUTO` is false**, present each rule for user decision. Show new candidates first (status: "proposed"), then any existing proposed rules from prior runs.

Group by target: `CLAUDE.md` rules first, then alphabetically by domain.

**Emerging rule unchanged filter** (run before the display loop):

```
cat proposed.json | $BIN triage --mode review-filter [--all]
```

The tool compares each emerging rule's current `signal_count` and sorted source PR numbers against its `reviewed_snapshot` (set by a previous `s` action). Rules where both match are "unchanged" and suppressed — the user already saw them and nothing new has arrived. Pass `--all` when `IS_SHOW_ALL` is true.

Output: `{"show": [...rules to display...], "suppressed": N, "suppressed_ids": [...]}`.

If `suppressed > 0`, print before the loop:
```
Skipping <N> unchanged emerging rule(s) (previously reviewed, no new signals).
Run /learn-patterns --review --all to force-show all.
```

Display only the rules in `show`.

For each rule, display:

```
────────────────────────────────────────────────────
Rule: <title>
Target: <CLAUDE.md | domain>
Confidence: <stated (explicit preference) | established | emerging> (<N> signals across <M> PRs)
[Supersedes: "<superseded rule title>"                              ← only when supersedes is non-empty
   ↳ This convention: PRs #<min_new>–<max_new> (<N_new> signals)
   ↳ Replaces:        PRs #<min_old>–<max_old> (<N_old> signals)]
[Prior convention (same run, older PRs #<min>–<max>): "<title>"   ← only when prior_convention note from Step 8A-cross is present]
[CONFLICT: contradicts another candidate from overlapping PR range — review both] ← only when rule.conflicted == true

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

For the supersession block: `min_new`/`max_new` = min/max PR numbers from the current rule's sources; `min_old`/`max_old` = min/max PR numbers from the superseded rule's sources (look it up in state). This makes the temporal relationship visible at decision time — the reviewer can see at a glance which convention is newer and by how many PRs.

Wait for user input per rule:
- `a` → set `status: "approved"`; clear `reviewed_snapshot`. If the rule has non-empty `supersedes`, also set the superseded rule's `status: "superseded"` and `superseded_by: "<this_rule_id>"` (the binary will fill `<this_rule_id>` at write time if not yet assigned — pass the rule's index for now).
- `r` → set `status: "rejected"`; clear `reviewed_snapshot`; this rule will move to `rejected_signals` in state
- `e` → prompt user to edit title, rule text, or examples inline; re-display updated rule for confirmation
- `s` → leave as `status: "proposed"` and save a review snapshot: `reviewed_snapshot = {signal_count: <current signal_count>, source_pr_numbers: <sorted list of PR numbers from sources>}`. On the next run the rule will be suppressed unless new signals arrive (i.e. signal_count increases or new PR numbers are added to sources).

After all rules are reviewed, display a summary of decisions:
```
You approved <N>, rejected <N>, edited <N>, skipped <N>. Proceed to write state? [y/n]
```
Wait for confirmation before continuing to Step 11. If `n`, exit without modifying state.

---

### Step 11: Persist state

Build the complete updated state JSON:
- All rules (approved, rejected, proposed, superseded) with updated statuses, signal counts, sources
- `reviewed_snapshot` and `conflicted` per rule: both fields are part of the `Rule` struct and round-trip through `state-write` automatically. `reviewed_snapshot` is set by the `s` action and cleared on approve/reject (not on edit). `conflicted` is set by Step 8A-cross and cleared when the user resolves the conflict during review.
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

The binary assigns `rule_<hex>` IDs to new rules and writes atomically. Delete `state-pending.json` after a successful write.

---

### Step 12: Generate output files

Build the flags based on available tools:
- If `HAS_CURSOR_AGENT` is true: add `--rag-hints` (embeds live query hints in skill files) and `--rag` (uses cursor-agent for semantic anchoring instead of grep)
- Otherwise: no extra flags

`write-outputs` writes two independent outputs: `CLAUDE.md` and per-domain skill files under `.claude/skills/`. **These paths are independently controllable** — use `--claude-md` to target an existing `CLAUDE.md` at a non-root location, and `--skills-dir` to target a non-default skills directory. When both are omitted, `--output-dir` provides the base for both (for backward compat: `<dir>/CLAUDE.md` and `<dir>/.claude/skills`).

**Typical invocation** (target repo has `CLAUDE.md` at its root and skills under `.claude/skills/`):
```
$BIN write-outputs \
  --state .claude/pattern-learner/state.json \
  --claude-md CLAUDE.md \
  --skills-dir .claude/skills \
  [--rag-hints] [--rag]
```

Using `--claude-md` and `--skills-dir` directly avoids any ambiguity about where the files land — even if the project has an unusual layout — and prevents the tool from writing an unwanted `CLAUDE.md` at a wrong depth.

If `CLAUDE.md` should not be modified in this run (e.g. `--review` only touched domain skills), add `--claude-md /dev/null` to suppress that output.

This writes:
- `CLAUDE.md` at the path given by `--claude-md` — approved rules targeting `CLAUDE.md`, max 30, stated first then established then emerging
- `<skills-dir>/<domain>/SKILL.md` — one file per domain with approved rules; when `--rag-hints` is set, each rule includes a `cursor-agent` command for retrieving live codebase examples at skill-use time

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

When `IS_AUTO` is true, append to the summary:

```
Auto-approve mode:
  Auto-approved:              <N>
  Deferred (supersessions):   <N>  ← run /learn-patterns --review to decide
  Deferred (conflicts):       <N>  ← run /learn-patterns --review to decide
─────────────────────────────────────────────────────
```

**Release the run-lock**: after the summary is printed, remove the guard lock so normal tooling is unrestricted again:
```
rm -f .claude/pattern-learner/.run-lock
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
- `confidence`: map cursor-agent confidence → rule confidence: `"high"` → `"established"`, `"medium"` → `"emerging"`, `"low"` → `"emerging"` (a single observed instance, not a stated preference — treat with the same human-review level as implicit emerging rules)
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

---

## Add Mode

Invoked when `--add` is set. This records **one** rule a developer authored by hand —
a convention they decided on while working — and persists it into the right skill file
so it travels across Claude Code instances instead of living in disposable session
memory. It runs after Step 4 (state read) and replaces Steps 5–10 entirely; Steps 11
(state write), 12 (output generation), and 13 (summary) run as normal.

Manual rules bypass the fetch / grounding / classify pipeline — there is no PR to ground
against and no occurrence count to score. The rule is authoritative because a human
stated it: it is written with `origin: "manual"`, `strength: "explicit"`,
`confidence: "stated"`, and `status: "approved"`.

The two pieces of judgment in this mode — deciding whether the rule already exists, and
deciding which domain it belongs to — are genuinely semantic and interactive. You perform
them directly. Do not write a script; the only `$BIN` calls are `state-read` (already done
in Step 4), `state-write`, and `write-outputs`.

---

### Add Step A1: Capture the rule

Determine the convention the user wants to record:
- If text follows `--add`, that text is the rule statement.
- Otherwise, use the user's request from the conversation that triggered this skill.
- If the intent is too vague to turn into a concrete instruction (e.g. just "add a rule
  about errors"), ask the user to state the convention as a single imperative sentence.

Synthesize a candidate rule:
- `title`: 5–8 words.
- `rule`: one imperative sentence, in the user's own intent — not a generic textbook rule.
- `do_examples` / `dont_examples`: include them only if the user supplied code or a clear
  before/after. Manual rules may legitimately have no examples — do **not** invent code the
  user didn't provide.

Echo the synthesized rule back to the user in one line so they can catch a misread before
anything is written.

---

### Add Step A2: Check whether the rule already exists

Read every rule already in state (from Step 4) — across all domains **and** `CLAUDE.md`,
including `proposed` and `superseded` rules, not just approved ones. Semantically compare
the new rule against each existing one. This is your judgment, not a string match.

- **Equivalent** to an existing rule (same intent, even if worded differently): do NOT
  create a duplicate. Tell the user it already exists, showing the existing rule's title,
  its target location (`CLAUDE.md` or domain), and current confidence. Then ask how to
  proceed:
  - **Strengthen it** — append a manual source to the existing rule (a `Signal` with
    `reviewer` = the user, `date` = today, `snippet` = the user's rule statement,
    `strength: "explicit"`, `pr_number: 0`), bump `signal_count`, set `last_seen_pr`
    unchanged, and leave its text and status. If the existing rule was implicit/`emerging`,
    this human confirmation promotes it — re-run its confidence as explicit (`stated`).
  - **Replace its text** — keep the rule's identity and sources but update `title`/`rule`/
    examples to the new wording.
  - **Cancel** — make no change and stop (still release the run-lock).
  Skip A3 in the strengthen/replace cases — the target is already decided. Go to Step 11.
- **Contradicts** an existing rule (the new rule says the opposite): show both to the user
  and ask whether the new rule should supersede the old one. If yes, set
  `supersedes: ["<existing_rule_id>"]` on the new rule (Step 11 will mark the old one
  `superseded`). If no, stop.
- **Distinct** (neither equivalent nor contradicting): proceed to A3.

---

### Add Step A3: Choose the target skill

A manual rule must land in a specific skill file (or `CLAUDE.md`). Decide **with** the user:

1. Build the list of candidate targets: every domain that already has rules in state (read
   their `target.location` and `file_glob`s), plus `CLAUDE.md`.
2. Form a suggestion. Use the rule's content and the existing domains' file globs to pick
   the most specific fitting domain. Reserve `CLAUDE.md` **only** for rules that apply to
   every file regardless of language/layer (naming conventions, commit format, repo-wide
   anti-patterns) — the same CLAUDE.md qualification as Step 8. When unsure between a
   domain and `CLAUDE.md`, prefer the domain.
3. Ask the user to confirm the target, using the existing domains as options and your
   suggestion marked as recommended — e.g. via `AskUserQuestion` with the candidate domains,
   `CLAUDE.md`, and a "new skill" choice. The user may pick an existing domain, `CLAUDE.md`,
   or a brand-new domain.

**If the user chooses a new domain** (one not present in state):
- **Confirm before creating.** Ask explicitly: "No skill exists for `<domain>` yet — create
  a new skill at `.claude/skills/<domain>/SKILL.md`? [y/n]". Only proceed on an affirmative
  answer. If declined, return to step 3 and let them pick an existing target.
- Ask for the file globs that scope the new domain (e.g. `internal/api/**/*.go`) so the
  generated skill auto-loads at the right times. Store them on the rule's
  `target.file_glob`.
- The skill file itself is created by `write-outputs` in Step 12 — you do not author it by
  hand. You only add the rule with the new `target.location`.

Set the rule's `target` to `{location: <chosen>, file_glob: <globs>}`. For `CLAUDE.md`,
`file_glob` may be empty.

---

### Add Step A4: Assemble the rule for state

Construct the final rule object (for the distinct/new case):
- `title`, `rule`, `do_examples`, `dont_examples` from A1
- `target` from A3
- `origin: "manual"`
- `confidence: "stated"`
- `status: "approved"`
- `sources`: a single `Signal` — `{reviewer: <user>, date: <today>, snippet: <the user's
  rule statement verbatim>, strength: "explicit", pr_number: 0}`
- `signal_count: 1`
- `supersedes`: set only if A2 found a contradiction the user chose to supersede
- Leave `id`, `created_at`, `updated_at` empty — `state-write` fills them.

For the **strengthen** or **replace** path from A2, instead modify the existing rule in
place (append source / update text) rather than adding a new rule.

If a new domain was created, also add or refresh its `domain_descriptions` entry following
the same guidance as Step 11 (name what the skill is for, 2–3 concrete topics, a "Use when
editing" hint from the globs).

---

### Add Step A5: Persist and generate

Proceed to **Step 11** (write the updated state via `state-write`), then **Step 12**
(`write-outputs` regenerates `CLAUDE.md` and the affected `.claude/skills/<domain>/SKILL.md` using
`--claude-md` and `--skills-dir` as described there).
`last_extracted_pr_number` is unchanged in this mode — manual add does not touch the PR
watermark.

---

### Add Step A6: Summary

Replace the Step 13 summary with a short confirmation:
```
── Manual Rule Added ────────────────────────────────
Rule:        <title>
Target:      <CLAUDE.md | domain>  (<new skill created | existing skill>)
Action:      <added new rule | strengthened existing rule | replaced existing rule | superseded "<old title>">
File:        <CLAUDE.md | .claude/skills/<domain>/SKILL.md>
─────────────────────────────────────────────────────
```

Then **release the run-lock** (`rm -f .claude/pattern-learner/.run-lock`) as in Step 13.
