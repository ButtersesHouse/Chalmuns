# Right-Sizing Rubric

## Validated principles (read first)

These five are grounded — tested against 55 skills/agents across two corpora,
9 author-pinned models (ground truth), and one apply-and-measure experiment.

1. **Tier from the residual, not the surface.** Judge what's left for the model
   *after* bundled scripts/tools absorb the rote work — not skill length, domain,
   or "sounds mechanical." Bundled `scripts/` argue *against* haiku as often as
   for it: they offload the rote part and leave the model the judgment.
2. **Archetype sets the lens; a stakes/depth dial sets the tier.** Within each
   archetype one dial moves it up — Transform: mechanical→haiku vs
   semantic/behavior-preserving→sonnet/opus; Reasoning: pattern-extending→sonnet
   vs novel/adversarial→opus; Orchestration: sequencing→haiku vs
   interpretation→sonnet+; Analysis: cost-of-miss (below). Flat baselines mis-tier.
3. **Cost-of-miss is the Analysis/Reasoning tie-breaker, and it's a user dial.**
   Identical work can deserve sonnet or opus depending on how expensive a miss is
   (two near-identical reviewers were author-pinned sonnet vs opus). Surface both
   options; don't pretend one is objectively right.
4. **`inherit` ≠ right-sized.** Absent/`inherit` model means *unsized*, not
   endorsed — classify from scratch (every deep-judgment agent in the corpus was
   left unpinned).
5. **Mechanical-core + narrative-garnish → haiku (measured).** When a script does
   the parsing, haiku is sufficient for *correctness*; tier up or add
   `effort: medium` only when *editorial quality* is the goal.

Two method cautions: **read the body's workflow before pinning** (description-only
systematically *under*-tiers — cap at med confidence, never pin from a blurb); and
**the trap is inverting the signal** (scripts/mechanical domains look cheap while
the residual is judgment).

## Step 1 — Identify the archetype (do this first)

The archetype sets the *lens* and whether decomposition applies. It does **not**
by itself fix the tier — each archetype has an intra-archetype **dial** (Step 2)
that moves the tier up. Using a flat per-archetype baseline mis-tiers; the dial
is what makes classification track real author choices.

| Archetype | What it does | Scan→synth seam? | Baseline · **dial that moves it up** |
|-----------|--------------|------------------|--------------------------------------|
| **Transform** | Input → output form-change | no | haiku · **semantic/behavior-preserving work → sonnet/opus** |
| **Authoring** | Generate an artifact by applying guidance to the user's *specific* case | **no** (no scan-delegation) | sonnet · rarely moves (breadth of situational judgment) |
| **Analysis** | Scan existing artifacts → verdict/report | **yes** | sonnet · **high cost-of-miss → opus** |
| **Orchestration** | Drive tools, interpret output, adapt, coach | partial | depends · **sequencing→haiku/sonnet-low; live interpretation/recovery→sonnet+** |
| **Reasoning** | Verify, prove, critique, judge, design | rarely | sonnet · **novel/adversarial/high-stakes → opus**; creative → `fable` |

Getting the archetype right prevents the classic errors: treating an
Orchestration skill as Transform because it ships scripts; treating an Authoring
skill as Analysis and proposing a scan-delegation that has no scan phase; and
treating a *semantic* Transform (code simplification) as a mechanical one.

## Step 2 — Set the tier from the residual + the dial

Tier from what's left for the model *after* tools/scripts absorb the rote work,
then apply the archetype's dial and the cost-of-miss check below.

### `haiku` (+ `effort: low`/`medium`)
Residual is deterministic or fill-a-fixed-template work: parsing, extraction,
**mechanical** reformatting, scaffolding a fixed layout, running bundled scripts
and reporting, validation against explicit rules, pure script-sequencing
orchestration. A skill can be long and still be haiku if the length is a
template, not reasoning. A **single narrow judgment seam** (one regex, one schema
pick) doesn't force sonnet — note it, keep haiku, or decompose it out.

### `sonnet` (+ `effort: medium`/`high`) — the default
Real but bounded judgment: ordinary code edits, applying decision matrices to a
specific case, most Authoring, routine Analysis/review, pattern-extending design,
**semantic** transforms whose stakes are moderate, orchestration that interprets
tool output.

### `opus` (+ `effort: high`/`max`)
Deep or high-stakes residual: architecture under ambiguity, wide-blast-radius or
**behavior-preserving** refactors, security/exploit reasoning, silent-defect
hunting, and any **self-critique / adversarial-verification / anti-generic-default
/ blind-judging** loop. Also: routine-looking work whose **cost-of-miss is high**
(see below). Never down-tier these.

### `fable`
Creative-generative work — aesthetic/visual design, distinctive copy, novel
generation. Distinct from analytical opus; trial it where the value is taste.

## The cost-of-miss dimension (the tie-breaker within Analysis & Reasoning)

Two skills can do structurally identical work yet deserve different tiers because
a wrong/missed answer costs more in one. Validation evidence: two near-identical
code-reviewer agents were pinned by their authors to **sonnet** and **opus**
respectively — the opus one prioritizes catching every real bug (high recall,
expensive miss). So:

- Advisory/reversible/low-stakes Analysis → **sonnet** (a miss is cheap to fix).
- Security, correctness-critical, silent-failure, or gate-keeping Analysis →
  **opus** (a miss ships a vulnerability or a silent bug).

Cost-of-miss is **partly a user preference**, not a rubric constant. Surface it in
the report ("opus if bug-recall matters here; sonnet to economize") rather than
pretending one answer is objectively right.

## Empirically validated: mechanical-core + narrative-garnish → haiku

A/B tested (haiku vs sonnet, same task, graded against a deterministic oracle):
a skill whose parsing is done by a bundled script and whose model residual is
"paste the data + compute simple %s + write a few terse findings" runs correctly
on **haiku** — 5/5 arithmetic correct, valid output, even caught a subtle
per-subagent anomaly, at ~10× lower per-token cost and lower latency. Sonnet's
only edge was *editorial*: richer good/bad/info framing and threshold multiples.

Rule: when a script absorbs the mechanical core, down-tier to haiku for
correctness. Tier up (or add `effort: medium`) **only if narrative/editorial
quality is itself a priority** — that, not correctness, is what the bigger model
buys here.

## `inherit` is not evidence of right-sizing

Most skills and agents ship no `model:` (i.e. `inherit`) even for deep judgment
work — in one validation corpus, *all* the security/architecture/grading agents
were left unpinned. Treat an absent or `inherit` model as **unsized**: classify
from scratch and propose the right tier. Never read `inherit` as an author's
endorsement of the session model.

## The residual-load rules (where surface features mislead)

- **Bundled scripts absorb mechanical load — read the residual, don't down-tier
  for having them.** `scripts/` means the rote part is already offloaded to
  deterministic code; what remains for the model is the *non-scriptable*
  judgment. Presence of scripts argues *against* haiku as often as for it.
- **Orchestration is judgment even when the tools are mechanical.** A skill that
  launches deterministic scripts but whose model-facing role is routing,
  interpreting logs/tool output, and recovering from failure is sonnet+, not
  haiku.
- **Situational application is judgment.** Authoring skills apply general
  principles to the user's specific artifact — that resists haiku even when the
  file format is fixed.

## Effort as an independent lever

Effort tunes thinking budget separately from model. **Drop `effort` before
dropping `model`** when a skill is on the right tier but over-deliberating (a
sonnet skill doing a rote procedure → keep sonnet, `effort: low`). A genuinely
hard task on a small model is a mis-fit effort cannot fix — move the tier.

## Whole-skill `context: fork` signals

Recommend running the entire skill in an isolated subagent when **all** hold:
- Bounded input → bounded output; a clear deliverable
- No dependence on live conversation history
- Verbose intermediate output worth isolating from the main context
- Read-only search/extraction → use `agent: Explore` (haiku); writes → a general
  cheap subagent

Fork buys a cheaper model *and* context isolation at once. Transform-archetype
skills (e.g. transcript → HTML report) are the prime candidates. Do **not** fork
interactive skills that need live user approval mid-flow.

## Decomposition (in-body delegation) — Analysis archetype only

Only Analysis/Orchestration skills have a real seam. Propose delegating the
collect phase to a haiku `Explore` subagent that returns *only the distilled
result*, keeping synthesis on the session model — **but only when the scan is
token-heavy** (reads many files / large output that can be distilled).

Do **not** propose delegation for:
- a one-line shell command (`find`, `git log`) — no tokens to save
- a **user interview** collect phase — the conversation can't be handed off
- a phase already driven by scripts or subagents

Sketch to insert at the seam:
```
Use the Agent tool (subagent_type: Explore) to gather <X>:
  "Scan <scope> for <pattern>; return only <distilled fields>, no prose."
Then do <the judgment step> on the session model.
```
For non-read-only mechanical sub-steps, delegate to a general Agent call with
`model: haiku` instead of `Explore`.

## Confidence calibration

- **high** — body read; the residual is unambiguous (clearly mechanical, or
  clearly hard). Safe to propose the change.
- **med** — body skimmed, or a judgment seam of uncertain weight. Propose but
  note the seam; **description-only reads are capped here.**
- **low** — vague body or open-ended judgment implied. **Do not down-tier.** Flag
  for human review, or offer decomposition as the safer path to savings.

Never trade correctness for cost on a low-confidence read, and never on a
description alone. The goal is to free expensive models for impactful work — not
to starve work that needs them.
