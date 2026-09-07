# Review Mode

## Contents

- Review Step R1: Select the reviews to mine
- Review Step R2: Batch the lean views
- Review Step R3: Extract signals via subagents
- Review Step R4: Grounding verification
- Review Step R5: Rejoin the main pipeline

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

## Review Step R1: Select the reviews to mine

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

## Review Step R2: Batch the lean views

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

## Review Step R3: Extract signals via subagents

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

## Review Step R4: Grounding verification

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

## Review Step R5: Rejoin the main pipeline

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
