# Add Mode

## Contents

- Add Step A1: Capture the rule
- Add Step A2: Check whether the rule already exists
- Add Step A3: Choose the target skill
- Add Step A4: Assemble the rule for state
- Add Step A5: Persist and generate
- Add Step A6: Summary

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

## Add Step A1: Capture the rule

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

## Add Step A2: Check whether the rule already exists

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

## Add Step A3: Choose the target skill

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

## Add Step A4: Assemble the rule for state

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

## Add Step A5: Persist and generate

Proceed to **Step 11** (write the updated state via `state-write`), then **Step 12**
(`write-outputs` regenerates the affected `.claude/skills/<domain>/SKILL.md` using
`--skills-dir` as described there; it writes nothing at the repo root).
`last_extracted_pr_number` is unchanged in this mode — manual add does not touch the PR
watermark.

---

## Add Step A6: Summary

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
