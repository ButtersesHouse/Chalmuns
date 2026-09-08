---
name: learn-patterns
description: "Extract this repo's coding conventions from PR review history, from code-review skills or tools you designate to watch, or from a rule the developer states, and write approved rules into per-domain skill files under .claude/skills/ (never the top-level CLAUDE.md — that is a separate opt-in promote step). Reviewer preferences count even when indirect — polite questions (\"could we use X?\"), skeptical critique, hedged suggestions — and one occurrence is enough. ALSO USE THIS SKILL whenever the user wants a coding standard remembered (\"add a rule that…\", \"remember we always/never…\", \"let's standardize on…\"): pass --add so it persists in a skill file, not session memory. ALSO USE IT to watch a code reviewer (\"keep an eye on /code-review\", \"turn what semgrep flags into rules\", \"stop my agent repeating what review keeps catching\"): --watch designates one, --learn-reviews mines what it flagged. Modes: --refresh, --review [--all], --auto [--auto-threshold], --discover, --add, --watch, --learn-reviews."
argument-hint: "[--refresh | --review [--all] | --auto [--refresh] [--auto-threshold] | --discover [domain ...] | --add [rule text] | --watch [add|list|remove] [name] | --learn-reviews [--auto]]"
---

# learn-patterns

Extracts coding conventions and developer preferences from merged PR review comments and writes approved rules into domain-specific skill files under `.claude/skills/`. It never writes to the repo's top-level `CLAUDE.md`/`AGENTS.md`; publishing there is an explicit, opt-in `promote` step (Step 12.6). Treats reviewer preferences — including indirect, hedged, and skeptical language — as spoken-word rules captured at face value, regardless of occurrence count.

Merged PRs are one source of review; they are not the only one. A **designated
code reviewer** — Claude Code's own `/code-review` skill, a linter, an analyzer,
a review bot — reviews code long before it reaches a PR, and what it flags is
the fastest feedback there is on how an agent's generated code diverges from
this codebase. `--watch` designates such a reviewer; from then on its output is
captured, and `--learn-reviews` mines the captured reviews through the same
pipeline into the same skills. That closes the loop: an agent generates, the
reviewer flags, the rule lands in a skill, and the next generation does not
repeat it.

## Tooling policy (read first)

Every deterministic step in this pipeline is implemented as a subcommand of the
`pattern-learner` binary (`$BIN`): `detect-repo`, `state-read`, `state-write`,
`write-outputs`, `extract-lean`, `verify-grounding`, `classify`, `triage`,
`watch`, `capture-review`, `extract-review`, `audit-format`, `promote`,
`version`. **These subcommands are the only sanctioned implementations of their
logic.**

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
- Two non-Go edges are sanctioned by design and are not policy violations: **PR fetch**
  (GitHub MCP, or the `gh`/`jq`/`xargs` inline path in the Step 5 large-repo note — the
  binary is zero-network) and **cursor-agent** (optional enhancement for `--discover`,
  `--rag`, and RAG hints; every cursor-agent feature degrades gracefully when absent).
  Everything else the pipeline runs is the Go binary.
- **Parsing a watched reviewer's output is `capture-review`, never your own reading of
  it.** The formats are plural (`findings`, `sarif`, `eslint`, `semgrep`, `markdown`)
  and each has a parser; reconstructing one by hand is exactly the reimplementation
  this policy exists to prevent. This skill also never *runs* the designated reviewer —
  it reads what that reviewer already produced. If the user wants a review run, they
  run it; the capture hook records the output either way.

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
- `--watch [add|list|remove] [name]` → designation mode: manage which code-review skills or tools pattern-learner keeps eyes on. No fetching, no mining — this only records the designation. `--watch` is incompatible with every other mode flag; if combined, abort with: "Error: --watch manages designations and cannot be combined with other modes."
- `--learn-reviews` → review-learning mode: mine the reviews already captured from designated reviewers. Skips PR fetching entirely. Combines with `--auto` (unattended approval, same predicate as elsewhere); combining it with `--refresh`, `--review`, `--discover`, or `--add` is invalid — abort with: "Error: --learn-reviews mines captured reviews and cannot be combined with <flag>."
- (nothing) → full mode: fetch all merged PRs

Store `IS_AUTO` = true when `--auto` is present. Store `IS_AUTO_THRESHOLD` = true when `--auto-threshold` is present (only meaningful with `IS_AUTO`). Store `IS_SHOW_ALL` = true when `--all` is present (only meaningful with `--review`).

If `--discover` is set, jump to the **Discover Mode** section after Step 4.
If `--add` is set, jump to the **Add Mode** section after Step 4.
If `--watch` is set, jump to the **Watch Mode** section after Step 4.
If `--learn-reviews` is set, jump to the **Review Mode** section after Step 4.

---

### Step 2: Pre-flight checks and binary build

**Pre-flight checks** (run first, abort with a clear message on failure):
- `go version` — Go **1.21+** must be installed to build the binary. Parse the `goX.Y` token from the output; if the command fails or the version is below 1.21, tell the user: "Go (1.21+) is required to build the pattern-learner binary. Install Go from https://go.dev/dl/ and retry." (An older Go may appear to build the binary today but fails confusingly on modern syntax — check the version, not just presence.)
- Confirm GitHub MCP tools are available in the session by checking that `mcp__github__list_pull_requests` is present. If not, tell the user: "The GitHub MCP server is not configured in this session. Add the GitHub MCP server to your Claude Code config and retry." (Skip this check in `--review`, `--discover`, `--add`, `--watch`, and `--learn-reviews` modes since no PR fetching happens.)
- `which cursor-agent` — check if cursor-agent is available. Store result as `HAS_CURSOR_AGENT` (true/false). In `--discover` mode, if cursor-agent is not found, abort: "cursor-agent is required for --discover mode. Install Cursor and ensure cursor-agent is on your PATH." In other modes, cursor-agent is optional — absence is not an error.

**Binary build**: check whether `.claude/pattern-learner/bin/pattern-learner` exists in the current working directory (the target repo root).

If it does not exist:
1. Find the plugin root — the directory holding this plugin's `go.mod` and
   `plugin.json`. Claude Code exports it directly, so prefer that:
   ```
   ROOT="$CLAUDE_PLUGIN_ROOT"
   ```
   If `$CLAUDE_PLUGIN_ROOT` is empty (the skill was invoked outside a plugin
   install, or by an agent that does not set it), search for it. Match on the
   **plugin** directory, not the repository name — the module lives at
   `plugins/pattern-learner/go.mod`, so a pattern anchored on the repo name
   matches nothing:
   ```
   ROOT=$(find ~/.claude . -name go.mod -path "*/pattern-learner/go.mod" 2>/dev/null | head -1 | xargs -r dirname)
   ```
   If `ROOT` is still empty, **STOP** and report that the plugin source could
   not be located; do not continue. (`xargs -r` is what keeps an empty result
   from becoming a confusing `dirname: missing operand`.)

2. Create the output directory:
   ```
   mkdir -p .claude/pattern-learner/bin
   ```

3. Build the binary (CWD = `$ROOT` from step 1):
   ```
   cd "$ROOT" && go build -o <absolute_path_to_target_repo>/.claude/pattern-learner/bin/pattern-learner ./cmd/pattern-learner
   ```

If the build fails, stop and report the error. Do not continue.

Refer to the binary as `BIN=.claude/pattern-learner/bin/pattern-learner` for the rest of these steps.

**Binary self-check**: run `$BIN version` and confirm it prints exactly `0.4.0`. If the command errors (an older binary reports `unknown subcommand: version`) or prints any other value, the binary was built from older source and its output format differs from what these steps describe — delete it and rebuild from Step 2, then re-run the check. If it still does not print `0.4.0` after a clean rebuild, STOP and report it: the plugin source found in step 1 is not the version this skill file belongs to. Do not proceed with a mismatched binary.

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

Save the JSON output: `{"owner": "...", "repo": "...", "stack": [...]}`. It is written
into the state's `repo` field in Step 11 and identifies this repo in every generated
skill file, so carry it through unchanged on every run.

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
   The `raw` object is the contract `extract-lean` parses. It MUST use exactly these
   top-level keys, each in its GitHub API shape:
   - `review_comments[]` — diff-level comments: `id`, `user.login`, `body`, `created_at`,
     `in_reply_to_id`, `path`, `diff_hunk`
   - `reviews[]` — review submissions: `id`, `user.login`, `body`, `submitted_at`
   - `issue_comments[]` — discussion comments: `id`, `user.login`, `body`, `created_at`
   - `files[]` — touched files: `filename`

   Nesting these under different names silently yields zero extracted comments.
   `extract-lean` prints a warning when a cache's `comment_sources` counts promise
   comments but none parse — treat that warning as a cache bug and fix the cache,
   do not proceed.

Collect newly-cached PR numbers into batches of up to 20. Carry `max_pr_seen` forward to Step 11.

> **Large repo note** (1,000+ merged PRs): one-at-a-time MCP calls will be slow. Bulk-fetching with `gh api --paginate` piped through `jq` is the supported alternative — `gh`, `jq`, and `xargs` are permitted by the guard. Run these as **inline commands**, not as an authored `.sh`/`.py` script file (the guard blocks script creation during a run, and a fetch script is exactly the kind of off-pipeline tooling this prevents). Each fetched PR must be written as a cache file in the same format:
> ```json
> { "pr_number": N, "comment_sources": {"review_comments": <count>, "issue_comments": <count>, "review_bodies": <count>}, "raw": <full PR data> }
> ```
> The cache contract is identical; Steps 6–13 are unaffected regardless of how the cache was populated. Parallelize with `xargs -P8`. Output must follow the `.claude/pattern-learner/raw-cache/pr-N.json` naming convention. (Fetch is the one network step the binary cannot perform — it is zero-network by design — so this inline `gh` path is sanctioned.)

---

### Step 6: Extract signals via subagents

For each batch of up to 20 newly-cached PR numbers, preprocess the raw cache into the lean view:

```
$BIN extract-lean --cache-dir .claude/pattern-learner/raw-cache --prs <comma-separated batch PR numbers>
```

Then launch one subagent per batch with the **verbatim prompt** in [references/pr-signal-extraction.md](references/pr-signal-extraction.md), which also carries the lean-view schema and the signal output schema. Do not paraphrase the prompt or reconstruct the schema from memory — read the file. Collect all signals returned by all subagent runs.

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

**Which corpus a signal is grounded against is decided by the signal**, not by
the flags: one carrying `raw_signal.pr_number` is checked against that PR's
cache file, one carrying `raw_signal.review_id` against that captured review
(Review Mode). Pass `--review-cache-dir .claude/pattern-learner/review-cache`
whenever the batch contains review signals — both flags together when it
contains both. A batch with review signals and no `--review-cache-dir` is
refused with an error naming the flag rather than reporting every rule
ungrounded, so if you see that error, add the flag and re-run; do not treat it
as the signals having failed.

---

### Step 8: Deduplicate, normalize, and aggregate

Run semantic dedup in domain-sharded reasoning passes over the verified signals and the current state from Step 4. For small signal sets (under ~100 signals from a single domain) a single reasoning pass is fine. For larger sets, shard by `suggested_target.location` and run one reasoning pass per domain shard to prevent context overload — then combine the per-shard candidates for Steps 8C–D. The sharding threshold is a practical concern, not a correctness one; the output of each approach is the same candidates list.

**A. Intra-batch dedup**: Find signals across the batch that express semantically equivalent conventions (same intent, even if worded differently). Merge them into one candidate with a combined `sources` list. If any of the merged signals has `strength: "explicit"`, the merged candidate is explicit. Also merge their `do_examples` and `dont_examples` arrays: deduplicate by code content (exact string match after trimming whitespace), then cap each array at 4 entries. The result is a richer set of real examples accumulated across multiple PRs that all express the same convention.

**Preserve `review_id` in every intermediate representation too**, by exactly
the same rule and for the same reason. A signal mined in Review Mode carries
`raw_signal.review_id`, and it must be copied onto the `sources` entry you
build from it. Two binary behaviours read it and both fail silently without it:
`triage` holds back a convention seen in only one review by counting distinct
`review_id`s, so dropping the field lets `--learn-reviews --auto` approve
single-review rules unread; and `write-outputs` decides a rule's provenance
line from it, so a merged PR+review rule renders as PRs alone and a pure review
rule as "—". Neither reports an error.

**Preserve `strength` in every intermediate representation**: when candidates are stored to disk or passed between steps, carry `sources[].strength` through verbatim. Do NOT compute strength from raw count alone or reconstruct it later. Losing `strength` in intermediate storage causes Step 9 to mis-assign confidence — all rules will appear implicit and zero `stated` rules will be emitted. When batches are aggregated across multiple runs, re-merge their `sources` lists preserving each entry's `strength` before applying 8C/8D.

**At scale (300+ signals from multiple batch runs)**: Step 8A is intra-batch only. After all batches complete, collect post-8A candidates and run a second cross-domain dedup pass before Step 8C. For very large signal sets, shard by `suggested_target.location` and run one subagent per domain shard — a narrower scope prevents context overload and produces sharper dedup. Combine each shard's output, then run Steps 8C–D on the unified candidate list.

**A-cross. Cross-batch contradiction detection**: After all batches complete their per-batch 8A pass, collect all post-8A candidates from all batches into one pool and check them against each other for contradictions. This is necessary because Step 8A is intra-batch only — a new convention from recent PRs and the old convention it replaced, extracted from different batches, would otherwise both surface as proposals.

For each pair of candidates that are semantically contradictory:
- Compute `max_pr(X)` = highest PR number across candidate X's `sources`; same for Y.
- The candidate with the **higher** `max_pr` is the current convention (winner). The other is the outdated convention (loser).
- Discard the loser as a standalone candidate. Attach a `prior_convention` note to the winner: `{title: loser.title, min_pr: min(loser.sources[].pr_number), max_pr: max(loser.sources[].pr_number)}`. This note is used in the Step 10 approval display only — it is NOT written to state.
- If both candidates' `max_pr` values are within 5 of each other (genuine ambiguity — the team may have been actively debating the convention), keep both as proposals and set `conflicted: true` on each. This field is persisted to state so that it survives `--auto` deferral and re-surfaces correctly in a subsequent `--review` run. The approval UI displays a `[CONFLICT]` marker for rules with `conflicted: true`.

**B. Domain normalization**: For each candidate's `suggested_target.location`, normalize variants of the same domain to a single canonical name. Treat `"api"`, `"API"`, `"rest-api"`, `"endpoints"`, `"http"` as the same domain (pick one canonical form, e.g. `"api"`); `"auth"`, `"authentication"`, `"authn"` as the same; etc. Also unify against existing rule domain names already in state — if state already uses `"api"`, normalize new candidates' `"endpoints"` to `"api"`. The goal is one skill file per logical domain, not fragmented files.

**Universal-rule qualification**: a rule belongs in the `"CLAUDE.md"` universal sentinel (generated as the `conventions` skill) only when it applies to every file in the repository regardless of technology or context — e.g. "no abbreviations in identifiers", "prefix commits with the ticket number", "never log PII". Rules that depend on file path, language, framework, or layer belong in domain skills, even if they appeared across many PRs. Coarse locations like `"backend"`, `"frontend"`, or `"general"` are not valid domain names — re-normalize these to the most specific subdomain the rule's file globs imply. When in doubt between `"CLAUDE.md"` and a domain, choose the domain.

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
- **Implicit** (all sources implicit or empty): `"established"` (5+) or `"emerging"` (1–4). Recency downgrade: if the candidate's most-recent source PR is below the midpoint of the scanned range (`max_pr_seen − (max_pr_seen − since_pr) × 0.5`) the confidence is downgraded one tier (`established` → `emerging`; `emerging` → dropped). A candidate with no PR source, or with any review source, is exempt — recency is a judgement on the PR number line and those candidates have no point on it.

`signal_count` is set authoritatively by the tool, ending manual drift. It counts `len(sources)` for a candidate mined only from PRs; once any source carries a `review_id` it counts distinct *occasions* instead — one per review, one per PR, one per source naming neither — so a single linter run that tripped one check in five files is one signal, not five.

Output: `{"kept": [...candidates with confidence set...], "dropped": N}`. Use `kept` as input to Step 10.

---

### Step 10: Approval UI

**If `IS_AUTO` is true**, skip the interactive loop: extract the `status: "proposed"` rules and run `cat proposed.json | $BIN triage --mode auto [--auto-threshold]`, then replace those rules in state with its output and print the auto-approve summary line.

**Otherwise** run the interactive approval loop, which begins with the unchanged-emerging pre-pass (`$BIN triage --mode review-filter [--all]`).

The display format, the per-rule keys (`a`/`r`/`e`/`s`) and exactly what each one writes to state are in [references/approval-ui.md](references/approval-ui.md). Read it before presenting any rule — the `s` snapshot and the supersession bookkeeping are easy to get wrong from memory.

### Step 11: Persist state

Build the complete updated state JSON:
- All rules (approved, rejected, proposed, superseded) with updated statuses, signal counts, sources
- `reviewed_snapshot` and `conflicted` per rule: both fields are part of the `Rule` struct and round-trip through `state-write` automatically. `reviewed_snapshot` is set by the `s` action and cleared on approve/reject (not on edit). `conflicted` is set by Step 8A-cross and cleared when the user resolves the conflict during review.
- Updated `last_extracted_pr_number` = `max_pr_seen` from Step 5 (the highest PR number encountered on any page, merged or not — this sets the watermark so the next refresh only fetches newer PRs). Leave unchanged if `--review` or `--learn-reviews`.
- `last_ingested_review_at`: the review-cache watermark. Set it only in Review Mode (Step R5.4), to the `captured_at` of the newest review mined this run; leave it untouched in every other mode.
- `watchers`: carry through unchanged. Designations are managed by `watch` (Watch Mode) and no other mode may add, drop, or reorder them — dropping one silently stops every future capture from that reviewer.
- Updated `last_run` and `stats`
- `repo`: **always** set to the Step 3 `detect-repo` output (`{owner, repo, stack}`),
  on every run and in every mode — never leave it empty or copy it from memory.
  `write-outputs` stamps `owner/repo` into each generated skill and uses it to tell
  this repo's skills from another repo's in a shared skills directory; a value that
  changes between runs leaves stale skills unpruned.
- `updated_at` per rule: `state-write` stamps only rules whose `updated_at` is **empty**.
  Clear the field on every rule you created or modified this run so it gets a fresh
  timestamp; leave untouched rules' existing values in place (that is their change history).
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

Build the flags from `HAS_CURSOR_AGENT` (add `--rag-hints` and `--rag` when true), then:

```
$BIN write-outputs \
  --state .claude/pattern-learner/state.json \
  --skills-dir .claude/skills \
  [--rag-hints] [--rag]
```

`write-outputs` writes **only** inside the skills directory — never `CLAUDE.md`, `AGENTS.md`, or anything else at the repo root. Publishing there is Step 12.6. Do not run two write-outputs against one skills directory at once.

Relay every `warning:` line it prints on stderr verbatim in the Step 13 summary.

What it generates (progressive-disclosure skill bodies, `examples/`, chunked `rules/`), how it stamps and prunes stale skills, and every condition under which it refuses the whole run are in [references/output-generation.md](references/output-generation.md) — read it when the command warns or errors, or when you need to explain a generated file.

### Step 12.5: Format check

If this run wrote **zero** skill files, skip this step entirely. Otherwise audit every skill file it wrote or touched (including `<skills-dir>/conventions/SKILL.md` when a universal rule was approved):

```
$BIN audit-format <skills-dir>/<domain1>/SKILL.md <skills-dir>/<domain2>/SKILL.md ...
```

This verifies the one thing the generator does not check: that the frontmatter it emitted is valid YAML and within the documented field rules. How to act on each finding — and which findings are generator bugs to **stop and report** rather than fix by hand — is in [references/format-check.md](references/format-check.md). This step never edits a file or state on its own.

### Step 12.6: Promote to a top-level file (only when explicitly asked)

**Do not run this step as part of a normal run.** Claude Code auto-loads `.claude/skills/`, so nothing needs copying to the repo root for this repo's own use. Run it only when the user explicitly asks for a top-level `AGENTS.md`/`CLAUDE.md` — usually so agents that never read `.claude/skills/` (Codex, Cursor, Gemini CLI) can see the conventions.

The invocation, the marked-block contract, the outcomes it reports, and the rule that `--create` is never passed unless the user asked for the file to be created are in [references/promote.md](references/promote.md).

### Step 13: Summary

The "Watched reviewers" line needs a count the state does not carry (watchers
store no counters by design — the cache is the source of truth), so read it
live before printing the summary. Skip this when Step 4's state had no
`watchers`:

```
$BIN watch --state .claude/pattern-learner/state.json --list
```

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
Files written (skills only — nothing at the repo root):
  .claude/skills/<domain>/SKILL.md  (<N> rules, <inline | chunked index> + <N> examples/rules companion files)
  [...]
Promoted to top level:      <"not requested" | "<path> — <created|updated|appended|unchanged|skipped>">
Stale rules (last_seen_pr is 200+ below current watermark):
  <list titles or "none">
Watched reviewers:          <"none designated" | "<name>, <name> — <N> captured
                             (run /learn-patterns --learn-reviews to mine them)">
RAG anchoring:              <"cursor-agent (semantic)" | "grep (fallback)" | "none">
RAG hints in skill files:   <yes | no>
Format check (audit-format):
  <"skipped — no domain skill files touched this run" |
   "frontmatter valid for all <N> domain skills" |
   list per flagged domain: "<domain>: frontmatter issue — <finding> — <fixed | declined>"
   and/or "<domain>: BUG — <N> lines, over budget despite Step 12 chunking (reported, not hand-fixed)">
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

Invoked by `--discover`. Mines conventions from the codebase with cursor-agent instead of PR history (Steps D1–D5 replace Steps 5–9), then rejoins at Step 10. **Read [references/discover-mode.md](references/discover-mode.md) when Step 1 routed here** — do not run it from memory.

## Add Mode

Invoked by `--add`. Records one human-authored rule (Steps A1–A6 replace Steps 5–10), then rejoins at Step 11. **Read [references/add-mode.md](references/add-mode.md) when Step 1 routed here.**

## Watch Mode

Invoked by `--watch`. Designates which code-review skills and tools pattern-learner keeps eyes on; it writes no rules and replaces every step after Step 4. **Read [references/watch-mode.md](references/watch-mode.md) when Step 1 routed here.**

## Review Mode

Invoked by `--learn-reviews`. Mines the reviews captured from designated reviewers (Steps R1–R5 replace Steps 5–7), then rejoins at Step 8 and runs 8–13 as written. **Read [references/review-mode.md](references/review-mode.md) when Step 1 routed here.**

