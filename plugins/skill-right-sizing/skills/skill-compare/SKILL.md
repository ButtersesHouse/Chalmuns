---
name: skill-compare
description: This skill should be used when the user asks to "A/B test a skill", "compare two skill versions", "test a skill change", "validate a model change to a skill", "benchmark skill versions", or wants evidence that a skill edit (especially a model:/effort: change) preserves quality before adopting it. Runs both versions on their own declared models, grades blind, and returns a cost-adjusted verdict.
argument-hint: "[skillA skillB | --skill <path> --patch model=haiku | --git <refA> <refB> | --channels <skill> | --trend]"
allowed-tools: Read, Grep, Glob, Bash, Write, Edit
model: sonnet
effort: high
---

# Skill Compare

A/B test two versions of a skill and return a **cost-adjusted verdict** — adopt
the cheaper version when quality holds within noise, keep the pricier one only
when it buys real quality. Built to validate a proposed `model:`/`effort:` change
(e.g. from `right-size-skills`) before adopting it.

This skill **reads, runs, grades, and reports** — it never edits the compared
skill. It is the validator in the pipeline: `right-size-skills` proposes → this
proves → you apply.

**Callable at any point in a workflow.** Every run appends a compact record to a
rolling append-only ledger (`~/.claude/skill-compare/ledger.jsonl`), so repeated
comparisons — across different skills, changes, and sessions — accumulate into a
pool you can trend over time. Drop a `skill-compare` call wherever a change lands
(after an edit, in a loop, in CI); the history builds itself. `--trend`
summarizes the pool without running anything.

## Why this exists (not just skill-creator)

`skill-creator` already provides the blind **comparator**, **grader**,
**analyzer**, `aggregate_benchmark.py`, and an HTML viewer — reuse them. But it
runs both versions on the *executor's* model, so it cannot actually exercise a
frontmatter model change. This skill adds the missing pieces: **per-version
model/effort-pinned execution**, arbitrary **version intake**, **blinding
hygiene**, and the **cost-adjusted verdict**. The correctness-critical judge is
vendored (`references/comparator.md`) so the core never breaks; skill-creator's
grader/analyzer/viewer are used opportunistically and skipped gracefully if absent.

## Trend mode (`--trend`)

To review the accumulated pool instead of running a comparison:
```sh
python <skill-dir>/scripts/trend.py [--skill NAME] [--since YYYY-MM-DD]
```
It reads the ledger and reports: verdict mix (ADOPT_CHEAPER / KEEP_PRICIER /
INCONCLUSIVE), **cumulative tokens saved** from adopted down-tiers, per-skill
history over time, and tasks that keep coming back INCONCLUSIVE (need more reps).
Writes `~/.claude/skill-compare/trend.md`. Runs nothing else — safe any time.

## Workflow

### 1. Resolve the two versions → isolated dirs

Set `WS=<cwd>/skill-compare-workspace`. Materialize both versions:
- **`skillA skillB`** — two skill directories → `WS/versionA`, `WS/versionB`.
- **`--skill <path> --patch <k=v…>`** — `versionA` = copy of the skill;
  `versionB` = same copy with the frontmatter patched (e.g. `model=haiku`,
  `effort=low`). The headline mode for model/effort sweeps.
- **`--git <refA> <refB>`** — extract the skill dir at each ref
  (`git -C <repo> archive <ref> <skill-subpath> | tar -x -C …`) → versionA/B.
- **`--channels <skill-name>`** — for the marketplace beta framework: auto-locate
  the installed **stable** and **beta** copies. Run
  `python <skill-dir>/scripts/resolve_channels.py <skill-name>`; it returns the
  stable/beta dirs (channel inferred from marketplace name, e.g. `kara-kara` vs
  `kara-kara-beta`), which feed the `--dirs` flow (stable=versionA, beta=versionB).
  Errors if both channels aren't installed — tell the user to add the beta
  marketplace first. With no tasks supplied, use the target skill's bundled
  `evals/evals.json`.

Then **strip version-identifying markers** from both copies (version strings,
changelog comments, differing header text) so the blind comparator cannot infer
provenance from the outputs. The only intended difference is the change under test.

Also write `WS/meta.json` describing the comparison, so the ledger record is
self-contained:
```json
{"skill": "<name>", "change": "model=haiku (B) vs model=sonnet (A)",
 "versions": {"versionA": {"model": "sonnet", "effort": null},
              "versionB": {"model": "haiku", "effort": "low"}}}
```

### 2. Get eval tasks

If the skill has `evals/evals.json`, use it (`evals[].{id,prompt,expected_output,
files,expectations[]}`). Otherwise ask the user for 2–3 representative tasks and
confirm. For each task, note whether it has a **deterministic oracle** — a
checkable answer computable by a script (e.g. session-report's token math). Tasks
with an oracle get objective grading; prefer to include at least one.

### 3. Read each version's execution config

Parse `model:` and `effort:` from each version's frontmatter. `inherit`/absent →
session default. These drive step 4 — this is what makes the A/B test the change.

### 4. Run paired executions (N reps, default 3)

For each `eval × version × rep`, spawn an **executor subagent pinned to that
version's model/effort** via the Agent tool (`model`, `effort` params), giving it:
that version's SKILL.md body, the task prompt, any input files, and an output dir.
Spawn both versions' runs in the same turn (parallel). Capture `total_tokens` and
`duration_ms` from each task-completion notification.

Write this exact layout (what `scripts/verdict.py` and skill-creator's aggregator
both read):
```
WS/iteration-1/eval-<K>/
  versionA/run-<N>/{outputs/…, grading.json, timing.json}
  versionB/run-<N>/{outputs/…, grading.json, timing.json}
  comparison/run-<N>/{comparison.json, slotmap.json}
```
`timing.json` = `{"total_tokens": …, "total_duration_seconds": …}`.

Note: giving the executor the skill body approximates real Skill-tool invocation
(the same approximation skill-creator makes) — state this in the report.

### 5. Grade each run (triangulate)

- **Oracle** (strongest): run the deterministic check; write `grading.json` with
  `{"summary": {"pass_rate", "passed", "failed", "total"}, "expectations":[{text,passed,evidence}]}`.
- **Assertions** (optional): if the eval has `expectations[]` and skill-creator is
  installed, use its `agents/grader.md` to grade → same `grading.json` schema.
- **Blind quality**: for each `eval × rep`, spawn a comparator subagent with
  `references/comparator.md`, passing versionA's and versionB's outputs. **Randomize
  which version is slot A vs B** and write the mapping to
  `comparison/run-N/slotmap.json` as `{"A": "versionA"|"versionB", "B": …}`; pass
  the oracle result if available. The comparator writes `comparison.json`
  (`winner: A|B|TIE` + scores) — blind to provenance.

### 6. Aggregate + decide

Run the owned verdict engine (it also appends to the rolling ledger):
```sh
python <skill-dir>/scripts/verdict.py WS/iteration-1 --meta WS/meta.json
```
It computes per-version mean/stddev(n−1) for pass_rate, tokens, seconds, and the
comparator win-rate (de-blinded via slotmap), then applies the rule in
`references/verdict-rules.md`: **parity within the noise band + no decisive
comparator win for the pricier → ADOPT_CHEAPER**; clear quality win → KEEP_PRICIER;
noisy/thin data → **INCONCLUSIVE (raise --reps)**. It writes `verdict.json` +
`verdict.md`, and **appends one record to `~/.claude/skill-compare/ledger.jsonl`**
(disable with `--no-ledger`; relocate with `--ledger PATH`). No dependency on
skill-creator — it reads the run files directly.

### 7. Report

Summarize inline (recommendation, quality Δ, cost Δ, latency Δ, confidence) and
write the report to `~/.claude/plans/skill-compare-<YYYY-MM-DD>.md`. If
skill-creator's `eval-viewer/generate_review.py` is present (find via Glob over
`plugins/marketplaces/**/skill-creator/`), optionally render the HTML with
`--benchmark`; if absent, note it was skipped. Never edit the compared skill —
adopting the winner is a separate, user-approved step.

## Caveats to surface in every report

- **Executor approximation**: runs give the subagent the skill body rather than
  invoking via the Skill tool.
- **Power**: 3 reps is weak for close calls; trust `INCONCLUSIVE` and raise
  `--reps` rather than forcing a winner on noise.
- **Blinding holds only** if markers were stripped (step 1) and A/B slots were
  randomized + recorded (step 5).

## Resources

- **`references/comparator.md`** — vendored blind A/B judge (spawn as a subagent).
- **`references/verdict-rules.md`** — the cost-adjusted decision rule + thresholds.
- **`scripts/verdict.py`** — stats + verdict + ledger append;
  `python scripts/verdict.py <iteration-dir> --meta <meta.json>`.
- **`scripts/trend.py`** — summarize the rolling ledger over time;
  `python scripts/trend.py [--skill NAME] [--since DATE]`.
- **`scripts/resolve_channels.py`** — locate installed stable+beta copies of a
  skill for `--channels` mode; `python scripts/resolve_channels.py <skill-name>`.
- **`evals/evals.json`** (in each skill) — bundled starter tasks so `--channels`
  runs with zero setup; users can add their own.
- **Ledger** — `~/.claude/skill-compare/ledger.jsonl` (append-only, one line per
  comparison) + `trend.md` (regenerated by trend mode).
- **Optional (skill-creator)** — `agents/grader.md`, `agents/analyzer.md`,
  `eval-viewer/generate_review.py`; used if installed, skipped gracefully if not.
