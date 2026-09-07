---
name: learn-patterns
description: "Extract coding conventions and developer preferences from this repo's PR review history and write approved rules into per-domain skill files under .claude/skills/ (never the repo's top-level CLAUDE.md — publishing there is a separate, opt-in promote step). Treats reviewer preferences as authoritative spoken-word rules — including indirect language like polite questions (\"could we use X?\"), skeptical critique (\"interesting choice\"), and hedged suggestions — and captures them regardless of occurrence count. ALSO USE THIS SKILL to record a coding rule a developer states while working — whenever the user says things like \"add a rule that…\", \"remember we always/never…\", \"make a convention that…\", \"save this as a rule\", \"let's standardize on…\", or otherwise wants to persist a coding standard: invoke with --add so the rule is written into the right skill file (portable across Claude Code instances) instead of being lost in local session memory. ALSO USE THIS SKILL when the user wants a code-review skill or tool watched for conventions — \"keep an eye on /code-review\", \"learn from what the reviewer flags\", \"watch semgrep/eslint and turn its findings into rules\", \"stop my agent making the mistake review keeps catching\": --watch designates a reviewer whose output is captured from then on, and --learn-reviews mines the captured reviews into rules so generated code stops repeating what review already rejected. Use --refresh for incremental since last run, --review to re-open approval without re-fetching (skips unchanged emerging rules already seen; add --all to force-show all), --auto to run without any interactive approval (defers supersessions, conflicts, and single-implicit singletons for human review; add --auto-threshold to also auto-approve singletons), --discover to find patterns directly from the codebase using cursor-agent, --add to manually record a single human-authored rule."
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
    "location": "CLAUDE.md (the universal-rule sentinel) or domain-name (e.g. api, auth, models)",
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

`suggested_target`: use the file paths the PR touches as a hint. Pick the **most specific** domain the PR's file paths suggest — e.g. `"migrations"` not `"database"`, `"mutations"` not `"backend"`, `"workers"` not `"jobs"`. Never use coarse buckets like `"backend"`, `"frontend"`, or `"general"` as a location — always go one level more specific (e.g. `"api"`, `"auth"`, `"models"`, `"migrations"`, `"workers"`, `"components"`, `"hooks"`). Reserve `"CLAUDE.md"` **only** for rules that apply universally regardless of which file is being edited: naming conventions, commit message format, anti-patterns true everywhere in the codebase. When in doubt, prefer a specific domain over `"CLAUDE.md"`. Note that `"CLAUDE.md"` is a **sentinel value, not a file path** — nothing is ever written to a file by that name. Rules carrying it are generated into `.claude/skills/conventions/SKILL.md`, an ungated skill that auto-loads on every file.

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
Target: <universal (conventions skill) | domain>
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

Build the flags based on available tools:
- If `HAS_CURSOR_AGENT` is true: add `--rag-hints` (embeds live query hints in skill files) and `--rag` (uses cursor-agent for semantic anchoring instead of grep)
- Otherwise: no extra flags

`write-outputs` writes **only** into the skills directory. It never creates or
modifies a file above it — no `CLAUDE.md`, no `AGENTS.md`, nothing at the repo
root. That tree is generator-owned and safe to regenerate wholesale, whereas a
top-level instruction file is hand-maintained and lives in the user's git
history. Publishing there is a separate, explicitly-requested step (Step 12.6).

Use `--skills-dir` to target a non-default skills directory; when omitted,
`--output-dir` provides the base (`<dir>/.claude/skills`). A relative `--skills-dir`
is resolved against `--output-dir`, and `--output-dir` itself defaults to the root of
the repository the state file lives in — never the shell's current directory — so the
invocation below behaves the same from any directory.

**Typical invocation:**
```
$BIN write-outputs \
  --state .claude/pattern-learner/state.json \
  --skills-dir .claude/skills \
  [--rag-hints] [--rag]
```

Do not run `write-outputs` twice at once against one skills directory: there is no
lock, and two runs can swap a domain out from under each other. The single-agent
pipeline these steps describe never does this.

If the command prints `warning:` lines on stderr (it succeeded, but found something
it could not resolve on its own, such as a leftover copy of a skill in a shared
directory whose slot another repo now holds), relay each warning to the user
verbatim in the Step 13 summary.

This writes:
- `<skills-dir>/<domain>/SKILL.md` — one skill per domain, generated with progressive
  disclosure so the always-loaded body stays lean (skill bodies are a recurring token
  cost once loaded):
  - **Examples live in companion files**, never inline in SKILL.md: each rule with
    examples gets `<domain>/examples/<slug>.md` (all do/don't pairs, real-instance
    refs, context) and the SKILL.md rule entry links it — the consuming agent reads
    it at its discretion when it wants the code.
  - **Each rule states its own scope when the domain's rules differ.** The
    `paths:` gate is the union of every rule's `file_glob`, so a skill loaded
    for one file can carry rules scoped to others. When the domain's rules do
    not all share one scope, each rule entry carries an `**Applies to:**` line
    naming its own globs (a repo-wide rule says so in words). When they do all
    share one scope the frontmatter already states it and no per-rule lines are
    emitted.
  - **Very large skills are chunked** (when the rendered body would reach the
    400-line warn threshold `audit-format` applies, honoring the documented
    "keep SKILL.md under 500 lines" limit): SKILL.md becomes a **rule index** (title + globs, grouped by confidence,
    each entry linking `<domain>/rules/<slug>.md`), and each rule file carries the
    full rule with its examples inline. The index also tells the agent it can
    `grep -ril "<keyword>" rules/` for full-text lookup.
  - `SKILL.md`, `examples/` and `rules/` are **generator-owned**: `write-outputs`
    renders the whole domain directory afresh each run (into a staging sibling that is
    swapped into place, so the directory is only ever complete or absent), so never
    hand-edit those (edit rules via the pipeline instead). Any other file or directory
    a user keeps at the top level of a generated domain directory is carried over
    unchanged.
  - **Frontmatter is emitted as quoted YAML** (`name`, `description`, and the
    comma-separated `paths` string are all double-quoted), so descriptions
    containing `: ` and globs starting with `*` load correctly. Do not strip the
    quotes.
  - **Stale skills are pruned.** Every generated SKILL.md starts with a
    `<!-- Generated by pattern-learner (write-outputs). ... -->` marker line plus a
    `<!-- pattern-learner:repo=<owner>/<repo> -->` stamp naming the repo that wrote
    it. After every skill has been written, `write-outputs` removes any
    `<skills-dir>/<domain>/` whose SKILL.md carries that marker (or the fixed header
    sentence older versions of the generator wrote) but whose domain no longer has
    approved rules (renamed, merged, or all rejected), so an outdated skill cannot
    keep auto-loading. Only the generated entries (`SKILL.md`, `examples/`, `rules/`)
    are removed; any other file a user kept in that directory stays, and the
    directory is removed only once it is empty. Inside the repo's own tree, unstamped
    generated skills and those stamped for this repo are pruned; one stamped for a
    *different* repo (a leftover from before a fork or rename, or a skill the user
    copied in from another repo on purpose) is left in place with a `warning:` line
    naming it, since the two cases cannot be told apart — except when the run has no
    identity of its own (no `--repo`, empty `state.repo`, no git remote), in which case
    it cannot tell a foreign stamp from its former one and prunes every stale generated
    skill in its tree. In a skills directory **outside** the repo, only skills carrying
    this repo's stamp are pruned. Skills the generator did not write (hand-written ones sharing the
    directory) and skills another repo generated into a shared skills directory are
    never touched. A skill directory written by a very
    old build (no marker, none of the generator's fixed body sentences) carries no
    fingerprint and is left alone too; if the Step 13 file list no longer names a
    `<skills-dir>/<domain>/` that still exists, tell the user it is a leftover from
    an older version and can be deleted.
  - **`write-outputs` never overwrites a skill it did not write.** If
    `<skills-dir>/<domain>/SKILL.md` already exists without the generator's marker
    (a hand-written skill whose name collides with a domain, or one generated by a
    very old build), the whole run is refused with nothing written. Ask the user
    whether to re-target the domain in state or move/delete that directory, then
    rerun. Inside the repo's own tree a generated skill is regenerated whichever repo
    stamp it carries (a fork or renamed repo re-stamps it). In a skills directory
    **outside** the repo (one shared by several repos), a domain stamped by another
    repo is refused instead — two repos cannot share one domain name there; ask the
    user to rename the domain in one of them or use separate skills directories. The
    universal `conventions` skill has a fixed name, so two repos with universal rules
    can only share a skills directory by giving each its own; and a run with no repo
    identity (no `--repo`, empty `state.repo`, no git remote) can neither overwrite
    nor prune any stamped skill there. Skills written into a shared directory by a
    pre-0.3.0 build carry no stamp and are never pruned by any repo (they cannot be
    attributed); each repo's next run re-stamps the ones it still owns, and any left
    over must be removed by hand.
  - **Globs must not contain commas.** `paths` is one comma-separated string, so
    `write-outputs` expands brace groups (`src/{a,b}/**` → two globs) and refuses
    the run for any glob that still contains a comma (e.g. a `[a,b]` character
    class). Rewrite the offending `target.file_glob` in state and rerun.
  - **Domain names must be single directory segments.** `write-outputs` refuses the
    whole run, writing nothing, if any approved rule's `target.location` contains a
    path separator, is `.`/`..`, exceeds 222 bytes (255 minus the staging-directory
    suffix and tail), contains control characters or
    any of `< > : " | ? *`, ends in a dot or space, is a Windows reserved device name
    (`con`, `aux`, `nul`, `com1`…), or differs from another domain only by letter
    case. Re-target those rules in state and rerun.
  - When `--rag-hints` is set, each rule (wherever its body lands) includes a
    `cursor-agent` command for retrieving live codebase examples at skill-use time.

---

### Step 12.5: Format check

`write-outputs` already keeps generated skills inside the documented body-line
budget itself (inline rules while the body fits under the 400-line warn
threshold, otherwise the chunked `rules/` index described in Step 12). So this step is **not** a
size-management step — it verifies the one thing the generator does not check:
that the **frontmatter it emitted is valid**. The domain name is written to
`name:` verbatim and the description comes from your `domain_descriptions`
entry; neither is validated or sanitized by the generator. `audit-format`
parses the block as real YAML (the way Claude Code loads it) and then checks
the documented field rules, so a file that would fail to load is reported as
`frontmatter is not valid YAML` rather than passing silently.

If this run wrote **zero** skill files (e.g. a `--review` run that approved nothing), skip this step entirely — do not invoke `audit-format` with no paths. `audit-format` requires at least one path and its usage error is not a sign the subcommand is broken; there is simply nothing to check this run.

Otherwise, run it against every skill file this run wrote or touched. That includes `<skills-dir>/conventions/SKILL.md` when any universal (`CLAUDE.md`-sentinel) rule was approved — it is a real skill with frontmatter like every other domain:

```
$BIN audit-format <skills-dir>/<domain1>/SKILL.md <skills-dir>/<domain2>/SKILL.md ...
```

The thresholds it applies are the ones documented in Anthropic's
Skill-authoring guidance; the `skill-right-sizing` plugin's
`right-format-skills` skill checks hand-authored skills against the same
rubric (`references/rubric.md` there carries the citations).

For each result:

- **`frontmatter_issues` non-empty — act on this.** The domain name violates
  a documented frontmatter rule (e.g. Step 8B/A3 canonicalized a domain to
  `Legacy_API`, which breaks the lowercase/digits/hyphens charset rule). A
  skill whose `name` breaks the charset rule may fail to register, so this
  is worth fixing. A `not valid YAML` finding, or any finding about the
  *description*, on a file `write-outputs` just wrote is a generator bug (it
  quotes every value it emits, and it truncates every description — your
  `domain_descriptions` override included — well under the length limit, so
  a length finding cannot come from state) — STOP and report it, as with
  `over_budget` below.

  Fix it at the **state** level, never by hand-editing the generated file —
  `write-outputs` rewrites that file wholesale from state on every run, so a
  hand-edit is silently discarded next run. Propose to the user a corrected
  domain name (valid charset). On approval, re-target the affected rules'
  `target.location`, then re-run **Step 11** (state-write) and **Step 12**
  (write-outputs), and re-run this check to confirm it comes back clean. On
  decline, note it in the Step 13 summary and continue.

- **`over_budget` / `approaching_budget` — this is a generator bug, not
  something to fix by hand.** Step 12's chunking is supposed to make this
  unreachable: a domain large enough to exceed the budget should already
  have been chunked into a `rules/` index. If a generated skill still
  reports over budget, `write-outputs`' chunking did not do its job.
  Per the Tooling policy, **STOP and report it** — do not re-split the
  domain by hand and do not edit the emitted file. Include the domain name
  and the reported `body_lines` so the generator can be fixed.

- **A non-zero exit — a path was not audited.** `audit-format` fails when it
  cannot read one of the paths you gave it, and names them. That is a
  mistake in the path list, not a finding about the skill: the file was
  never opened, so its empty `frontmatter_issues` means nothing. Re-check
  the paths against what Step 12 reported it wrote and run it again. Do not
  report the run's skills as format-clean until every path audited.

This step never edits a file or state on its own.

---

### Step 12.6: Promote to a top-level file (only when explicitly asked)

**Do not run this step as part of a normal run.** The pipeline's outputs are
the per-domain skills; Claude Code auto-loads `.claude/skills/` on its own, so
nothing needs to be copied to the repo root for this repo's own use.

Run it **only** when the user explicitly asks for a top-level `AGENTS.md` or
`CLAUDE.md` — e.g. "publish these to AGENTS.md", "I want Codex to see these",
"promote the conventions". The usual reason is agents other than Claude Code
(Codex, Cursor, Gemini CLI) which never read `.claude/skills/`.

```
$BIN promote --state .claude/pattern-learner/state.json \
  --skills-dir .claude/skills \
  [--agents-md AGENTS.md] [--claude-md CLAUDE.md] [--create]
```

Defaults to `AGENTS.md` when neither target flag is given. Pass both to write
both. The command prints a JSON array of `{path, outcome, reason}`.

What it writes is confined to a marked block:

```
<!-- pattern-learner:begin -->  ...generated...  <!-- pattern-learner:end -->
```

Everything outside those markers is preserved byte-for-byte, so a
hand-written file keeps its content and a re-run only refreshes our section.
Outcomes: `created` (with `--create`), `updated` (block replaced), `appended`
(no block yet — added at the end, nothing overwritten), `unchanged`
(identical, no write), `skipped` (nothing to promote, or the file is absent
and `--create` was not passed).

Two rules for this step:

- **Never pass `--create` unless the user asked for the file to be created.**
  Without it, a missing target is skipped rather than created, which is what
  keeps `promote` from introducing a repo-root file nobody asked for. If the
  result comes back `skipped` for that reason, report it and ask before
  re-running with `--create`.
- **If it errors about malformed markers**, do not try to repair the file by
  hand — report it. A half-present marker pair means someone edited the
  delimiters, and guessing the block bounds risks destroying their content.

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
Files written (skills only — nothing at the repo root):
  .claude/skills/<domain>/SKILL.md  (<N> rules, <inline | chunked index> + <N> examples/rules companion files)
  [...]
Promoted to top level:      <"not requested" | "<path> — <created|updated|appended|unchanged|skipped>">
Stale rules (last_seen_pr is 200+ below current watermark):
  <list titles or "none">
Watched reviewers:          <"none designated" |
                             "<name>, <name> — <N> captured, <N> not yet mined
                              (run /learn-patterns --learn-reviews)">
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
- `sources`: one synthetic Signal — `{"reviewer": "cursor-agent", "date": "<today>",
  "snippet": "<the rule statement verbatim>", "strength": "explicit", "pr_number": 0}`.
  The rule schema has no top-level strength field — explicitness lives on the source
  signal (same pattern as Add mode). An empty `sources` list would make any later
  re-classification treat the rule as implicit and `--auto` triage defer it as a
  singleton, losing the "authoritative" status this mode intends.
- `signal_count`: 1
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

All discover candidates carry an explicit source signal (D3) so they are kept unconditionally regardless of count. Confidence was already set in D3 from cursor-agent's rating.

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

A manual rule must land in a specific skill. Decide **with** the user:

1. Build the list of candidate targets: every domain that already has rules in state (read
   their `target.location` and `file_glob`s), plus **universal** (stored as the
   `"CLAUDE.md"` sentinel, generated as the ungated `conventions` skill).
2. Form a suggestion. Use the rule's content and the existing domains' file globs to pick
   the most specific fitting domain. Reserve **universal** **only** for rules that apply to
   every file regardless of language/layer (naming conventions, commit format, repo-wide
   anti-patterns) — the same qualification as Step 8. When unsure between a domain and
   universal, prefer the domain.
3. Ask the user to confirm the target, using the existing domains as options and your
   suggestion marked as recommended — e.g. via `AskUserQuestion` with the candidate domains,
   **universal (repo-wide)**, and a "new skill" choice. Present it as "universal", not as a
   file path — no rule is ever written to a top-level `CLAUDE.md`.

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
(`write-outputs` regenerates the affected `.claude/skills/<domain>/SKILL.md` using
`--skills-dir` as described there; it writes nothing at the repo root).
`last_extracted_pr_number` is unchanged in this mode — manual add does not touch the PR
watermark.

---

### Add Step A6: Summary

Replace the Step 13 summary with a short confirmation:
```
── Manual Rule Added ────────────────────────────────
Rule:        <title>
Target:      <universal | domain>  (<new skill created | existing skill>)
Action:      <added new rule | strengthened existing rule | replaced existing rule | superseded "<old title>">
File:        .claude/skills/<domain>/SKILL.md  <or "universal rule — stored in state; appears in a promoted file only if you run Step 12.6">
─────────────────────────────────────────────────────
```

Then **release the run-lock** (`rm -f .claude/pattern-learner/.run-lock`) as in Step 13.

---

## Watch Mode

Invoked when `--watch` is set. This records **which code-review skills and tools
pattern-learner keeps eyes on**. It runs after Step 4 (state read) and replaces
every later step: designating a reviewer writes no rules and generates no
skills, so Steps 5–13 do not apply. Release the run-lock when done.

A designation is what makes capture happen at all. The plugin ships a
PostToolUse hook that runs `capture-review --hook` after every Bash, Skill,
SlashCommand and Task call; with no watchers designated it reads the payload,
matches nothing, and exits. Once a reviewer is designated, that reviewer's
output is written into `.claude/pattern-learner/review-cache/` as it happens,
and `--learn-reviews` mines it. Nothing is captured from a tool nobody asked
for, and nothing is ever captured from a tool that is not designated.

---

### Watch Step W1: Determine the action

Parse what follows `--watch`:

- `add <name>` (or just a bare name) → designate a reviewer.
- `list` (or nothing at all) → show the current designations.
- `remove <id>` → undesignate.

If the user's intent is a designation but no name is recoverable ("watch my
code review tool"), ask which skill or tool they mean, offering `code-review`
as the likely answer when the session has that skill.

---

### Watch Step W2: Run the subcommand

```
$BIN watch --state .claude/pattern-learner/state.json --list
$BIN watch --state .claude/pattern-learner/state.json --add <name> [--kind skill|tool|any] [--format auto|findings|sarif|eslint|semgrep|markdown]
$BIN watch --state .claude/pattern-learner/state.json --remove <id>
```

Every form prints the resulting designations as JSON, each with a live
`captures` tally read from the review cache.

Choosing the flags:

- `--kind` — `skill` for a Claude Code skill (`/code-review`, `/security-review`),
  `tool` for a command (`semgrep`, `eslint`, `golangci-lint`), `any` when the
  user is not distinguishing. Default `any`. A `tool` watcher matches only
  when the tool is the command actually being run: `semgrep --json .` matches,
  `grep semgrep notes.txt` does not.
- `--format` — leave at the default `auto` unless the user knows the tool emits
  a shape they want forced. `auto` sniffs each artifact, so a tool that emits
  JSON on one run and prose on the next is handled per capture.

`--add` refuses a name already designated rather than replacing it, so changing
a watcher's kind or format is `--remove` then `--add`. Removing a designation
never deletes captured artifacts, and never touches rules already mined from
them — their provenance stands.

---

### Watch Step W3: Report

```
── Watched Reviewers ────────────────────────────────
<name>  (<kind>, format <format>)  — <N> captured, last <timestamp | "none yet">
[...]
─────────────────────────────────────────────────────
Captured reviews are mined by: /learn-patterns --learn-reviews
```

When a designation was just added and nothing has been captured yet, say so
plainly and tell the user what triggers the first capture: running the
designated reviewer. If they have a review output on hand already — a saved
report, a file of findings — mention they can feed it directly:

```
$BIN capture-review --cache-dir .claude/pattern-learner/review-cache \
  --source <name> [--format F] [--label "what was reviewed"] --file <path>
```

Then release the run-lock and stop.

---

## Review Mode

Invoked when `--learn-reviews` is set. Mines the reviews captured from
designated reviewers into conventions. It runs after Step 4 (state read) and
replaces **Steps 5–7** (fetch, PR signal extraction, grounding) with the four
steps below. **Steps 8–13 run exactly as written** — the same semantic dedup,
the same `classify`, the same approval, the same `state-write`, the same
`write-outputs`, the same summary. A rule mined from a review is a rule like
any other by the time it reaches Step 8; only its provenance differs.

Why this is worth a mode of its own: a designated reviewer sees the code an
agent just generated, long before that code reaches a PR. What it flags is the
most direct evidence available of where generated code diverges from this
codebase — and once that divergence is a rule in a domain skill, the next
generation is loaded with it. That is the alignment loop this mode exists to
close.

---

### Review Step R1: Select the reviews to mine

Read the watermark `last_ingested_review_at` from the Step 4 state (absent on a
first run). List what is available:

```
$BIN extract-review --cache-dir .claude/pattern-learner/review-cache [--since <last_ingested_review_at>]
```

Omit `--since` on a first run to mine everything captured so far; pass it
otherwise so a re-run does not re-mine reviews already turned into rules. To
mine specific reviews regardless of the watermark, pass
`--reviews <id1,id2>`.

If the output is an empty array, stop here and tell the user plainly which case
it is:

- **No watchers designated** (`$BIN watch --state … --list` prints `[]`) →
  "Nothing is being watched yet. Designate a reviewer with
  `/learn-patterns --watch add code-review`, then run the reviewer."
- **Watchers designated but nothing captured** → "Watching `<names>`, but no
  review output has been captured yet. Run the reviewer, or feed an existing
  report with `capture-review --file`."
- **Everything already mined** (watermark is current) → "All <N> captured
  reviews have already been mined; nothing new since <timestamp>."

Release the run-lock in every one of these cases before stopping.

---

### Review Step R2: Batch the lean views

`extract-review` returns a JSON array of lean review views, ready to insert
into the subagent prompt below. Each element:

```json
{
  "review_id": "rev-70af1cd25b01",
  "source": "code-review",
  "format": "findings",
  "captured_at": "2026-01-15T10:04:00Z",
  "label": "hook capture: /code-review",
  "files_touched": ["internal/api/handler.go"],
  "findings": [
    {
      "index": 0,
      "file": "internal/api/handler.go",
      "line": 42,
      "category": "correctness",
      "severity": "",
      "title": "Errors wrapped without %w",
      "body": "This codebase wraps errors with %w so callers can errors.Is them.\n\nA caller doing errors.Is(err, ErrNotFound) gets false and falls into the generic 500 branch.",
      "code_before": "return fmt.Errorf(\"lookup: %v\", err)",
      "code_after": "return fmt.Errorf(\"lookup: %w\", err)",
      "verdict": "CONFIRMED"
    }
  ]
}
```

A review whose format carries no structure (a prose write-up with no headings)
has no `findings` and carries the review verbatim in `text` instead; the
subagent reads that directly.

Batch up to **10 reviews** per subagent run — findings are denser than PR
comments, so the batches are smaller than Step 6's twenty.

---

### Review Step R3: Extract signals via subagents

For each batch, launch a subagent with this exact prompt (fill in the
`extract-review` output at the end):

---
**SUBAGENT PROMPT:**

You are reading the output of a code-review skill or tool that this team runs on
their codebase. Your job is to find the **conventions** behind what it flagged,
so an AI coding assistant stops generating code that gets flagged again.

**The central distinction — read this twice.** A review finding is usually a
report about *one piece of code*, not a statement of a rule. Your output becomes
standing instructions applied to every future edit, so a one-off defect promoted
to a convention is worse than a convention missed.

- **A convention** generalizes: it would still be true about code that does not
  exist yet. "Wrap errors with %w so callers can errors.Is them." "Use the shared
  http client, never requests directly." "Table-driven tests go in the same file
  as the code under test."
- **A defect report** does not: it is about this code, here, now. "This returns
  nil when the list is empty." "Off-by-one in the retry loop." "This test asserts
  the wrong field."

Extract the first. Discard the second, however severe — a critical bug is still
not a convention, and this pipeline is not a bug tracker.

**Recurrence is the strongest evidence you have.** The same finding raised in
several reviews is a convention the team's reviewer enforces; a finding raised
once may be a defect that happened to be phrased generally. When you see the
same underlying rule in more than one review, emit it once with one entry in
`sources` per review it appeared in.

---

**Strength**

- `"strength": "explicit"` — the finding **states the rule itself**: it says what
  this codebase does, names the convention, cites a configured lint rule, or
  explains the general principle ("this codebase wraps errors with %w so callers
  can errors.Is them"). A named rule id from a tool the team configured
  (`no-console`, `py.no-requests`) is a stated convention: the team chose to
  enforce it.
- `"strength": "implicit"` — the finding shows the correction without stating a
  rule, and you inferred the convention from it.

Do not mark a finding explicit merely because the tool sounded confident. A
tool's `verdict` or `severity` says how sure it is that this is a *problem*, not
whether a *convention* was stated.

---

**DO NOT extract:**

- **One-off defects**: nil dereferences, off-by-ones, wrong assertions, a bad
  regex — the whole "defect report" category above.
- **Product or feature correctness**: what the code should *do*, not how it
  should be *written*.
- **Generic language advice** the model already knows: "handle errors", "avoid
  global state", "add tests". If the rule would be true in any repository in this
  language, it teaches nothing about *this* codebase — drop it.
- **Findings whose fix is already automated**: formatting a formatter applies,
  a lint rule that autofixes on save.
- **Anything you cannot quote.** See below.

---

**Output schema** — a JSON array, each element:

```json
{
  "title": "Short rule title (5-8 words)",
  "rule": "The convention as a clear imperative instruction",
  "strength": "explicit",
  "do_examples": [
    {"code": "...", "language": "go", "context": "optional"}
  ],
  "dont_examples": [
    {"code": "...", "language": "go", "context": "flagged at internal/api/handler.go:42"}
  ],
  "suggested_target": {
    "location": "CLAUDE.md (the universal-rule sentinel) or domain-name (e.g. api, auth, models)",
    "file_glob": ["internal/api/**/*.go"]
  },
  "raw_signal": {
    "review_id": "rev-70af1cd25b01",
    "reviewer": "code-review",
    "date": "2026-01-15",
    "snippet": "EXACT VERBATIM QUOTE FROM THE FINDING"
  }
}
```

`raw_signal.review_id` is the `review_id` of the review the finding came from,
and `reviewer` is that review's `source`. Both are copied verbatim from the
input — a signal whose `review_id` does not name a real captured review is
dropped in Step R4.

`snippet` — the reviewer's exact words, at least 20 characters, copied from the
finding's `title`, `body`, or (for an unstructured review) its `text`. **Do not
paraphrase, do not stitch words from different places, do not summarize.** Step
R4 checks every snippet against the stored review and silently drops any that is
not found. If you cannot quote the finding in support of the rule, the rule is
yours rather than the reviewer's — omit it.

A rule seen in several reviews gets one `raw_signal` per review; emit it as
several array elements sharing a `title` and `rule`, and Step 8 will merge them.

`do_examples` / `dont_examples`: use the finding's own code. `code_before` is
the flagged code — that is the `dont_example`. `code_after` (the suggested fix)
is the `do_example`. Where a finding gives only one side, supply the other only
if the finding's text states it; otherwise omit it. Never invent code.

**The finding's `file` and `line` say where the problem was found.** Put that in
a `dont_example`'s `context`, never on a `do_example` — a file the reviewer
flagged is the last file a reader should be pointed at to imitate.

`suggested_target`: pick the most specific domain the finding's file paths
suggest, by exactly the rules in Step 6 — never a coarse bucket like `backend`
or `general`, and reserve the `"CLAUDE.md"` sentinel for rules that hold for
every file in the repository regardless of language or layer. Derive
`file_glob` from the finding's `file` (e.g. `internal/api/handler.go` →
`internal/api/**/*.go`). A finding with no file path and no repo-wide claim is
not targetable — omit it.

Output only the JSON array, no other text.

Captured reviews:
[INSERT extract-review OUTPUT HERE]

---

Collect all signals returned by all subagent runs.

---

### Review Step R4: Grounding verification

Same check, same subcommand, different corpus — a review signal is verified
against the review it names:

```
cat signals.json | $BIN verify-grounding --review-cache-dir .claude/pattern-learner/review-cache
```

The rules are the ones Step 7 documents (≥20 runes, normalized substring match),
applied against the captured artifact rather than a PR cache file. Read
`stats.too_short` and `stats.not_found` for the summary, and use `kept` as the
verified signals array.

---

### Review Step R5: Rejoin the main pipeline

Continue at **Step 8** (dedup, normalize, aggregate) with the verified signals,
and run Steps 8 through 13 as written, with these five differences:

1. **Step 8A-cross contradiction detection** compares candidates by
   `max_pr(X)`, which every review candidate reports as 0. Order them by their
   reviews' `captured_at` instead — the newest capture is the current
   convention. A review candidate contradicting a *PR* candidate is genuinely
   ambiguous (two different clocks): set `conflicted: true` on both and let a
   human decide.
2. **Step 9 `classify`** is run with `--max-pr-seen <last_extracted_pr_number>
   --since-pr <same value>` when the batch is review-only, so no PR range is
   implied. Review candidates carry no PR number and are exempt from the
   recency downgrade by construction — a convention has not gone stale merely
   because it was never in a PR.
3. **Every new rule gets `origin: "code-review"`** in Step 11. This drives its
   provenance line in the generated skill (`_Source: code review (code-review)_`)
   and keeps it out of the PR-watermark staleness check, which would otherwise
   mark every review rule stale.
4. **Step 11 also sets `last_ingested_review_at`** to the `captured_at` of the
   newest review mined this run (the last element of the `extract-review`
   output). Leave `last_extracted_pr_number` untouched — this mode does not
   read PRs.
5. **Step 13's summary** replaces the PR counters with the Review Mode block
   below.

Approval is unchanged and still required: `--learn-reviews` alone presents
every candidate for decision, and `--learn-reviews --auto` applies the same
`triage --mode auto` predicate as everywhere else. Note that a convention a
reviewer raised exactly once, without stating it as a rule, is a single
implicit signal and so is **deferred** rather than auto-approved — which is the
behaviour you want, because that is precisely the shape a one-off defect takes
if one slips through R3.

In the Step 13 summary, replace the PR-related counters with:

```
Reviews mined this run:     <N>  (sources: <name>, <name>)
  findings read:            <N>
Signals extracted:          <N>
  explicit (stated rule):   <N>
  implicit (inferred):      <N>
Signals dropped (grounding):<N>
  too short (<20 chars):    <N>
  not found in review:      <N>
Review watermark:           <last_ingested_review_at>
```
