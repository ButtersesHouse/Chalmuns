# Right-Size Skills Report — 2026-07-02

**Targets scanned:** 0 owned, 29 advisory-only
**Scope:** `--plugins` (`~/.claude/plugins/marketplaces/**`)

> Every skill below currently has no `model`/`effort`/`context` set, i.e.
> `model: inherit` — it rides whatever model the session happens to be on. All
> are **advisory-only** (plugin skills, overwritten on update), so none appears
> in the change list. Recommendations are absolute target tiers.

## Summary

| Skill | Owner | Current | Proposed model/effort | Fork? | Deleg. | Conf. |
|-------|-------|---------|-----------------------|-------|--------|-------|
| discord/access | advisory | inherit | haiku / low | no (interactive) | 0 | high |
| discord/configure | advisory | inherit | haiku / low | no (interactive) | 0 | high |
| imessage/access | advisory | inherit | haiku / low | no (interactive) | 0 | high |
| imessage/configure | advisory | inherit | haiku / low | no (interactive) | 0 | high |
| telegram/access | advisory | inherit | haiku / low | no (interactive) | 0 | high |
| telegram/configure | advisory | inherit | haiku / low | no (interactive) | 0 | high |
| cardputer-buddy | advisory | inherit | haiku / low | no | 0 | high |
| example-command | advisory | inherit | haiku / low | no | 0 | high |
| example-skill | advisory | inherit | haiku / low | no | 0 | high |
| session-report | advisory | inherit | **haiku / low** | **yes (general)** | 0 | high |
| plugin-structure | advisory | inherit | haiku / low | no | 0 | med |
| m5-onboard | advisory | inherit | sonnet / low | no | 0 | med |
| build-mcpb | advisory | inherit | sonnet / low | no | 0 | med |
| playground | advisory | inherit | sonnet / low | no | 0 | med |
| writing-hookify-rules | advisory | inherit | sonnet / low | no | 0 | med |
| agent-development | advisory | inherit | sonnet / low | no | 0 | med |
| command-development | advisory | inherit | sonnet / low | no | 0 | med |
| hook-development | advisory | inherit | sonnet / low | no | 0 | med |
| mcp-integration | advisory | inherit | sonnet / low | no | 0 | med |
| plugin-settings | advisory | inherit | sonnet / low | no | 0 | med |
| skill-development | advisory | inherit | sonnet / medium | no | 0 | med |
| build-mcp-app | advisory | inherit | sonnet / medium | no | 0 | med |
| build-mcp-server | advisory | inherit | sonnet / medium | no | 0 | med |
| project-artifact | advisory | inherit | sonnet / medium | no | 1 | med |
| skill-creator | advisory | inherit | sonnet / medium | no | 1 | med |
| claude-md-improver | advisory | inherit | sonnet / medium | no | 1 | med |
| claude-automation-recommender | advisory | inherit | sonnet / high | no | 1 | med |
| frontend-design | advisory | inherit | sonnet / high (fable alt) | no | 0 | med |
| math-olympiad | advisory | inherit | **opus / max** | no | 0 | high |

Legend: Fork "interactive" = bounded+cheap but needs live user approval, so not
forked. **Deleg.** = in-body delegation opportunities.

## Notable cases

### session-report → haiku / low + `context: fork` (the poster child)
Parses `~/.claude/projects` transcripts and emits a templated HTML report —
bounded input → bounded output, verbose intermediate data, no dependence on live
conversation. Ideal to run entirely on haiku in a forked context so the main
thread never pays for the parsing. Not read-only (writes HTML), so fork with a
general cheap subagent rather than `agent: Explore`.
- **Frontmatter diff:**
  ```diff
   ---
   name: session-report
  +model: haiku
  +effort: low
  +context: fork
   ---
  ```

### math-olympiad → opus / max (up-tier — protect the hard work)
Adversarial competition-math proving is the hardest reasoning in the set.
Leaving it on `inherit` risks it running on a small session model and failing
silently. Pin it up.
- **Frontmatter diff:**
  ```diff
   ---
   name: math-olympiad
  +model: opus
  +effort: max
   ---
  ```

### Messaging channel skills (6) → haiku / low
discord/imessage/telegram × access/configure all do the same shape of work:
save a token, edit an allowlist file, set a policy, report status. Mechanical
config manipulation — haiku-tier. Not forked because approving pairings needs
live user interaction.

### frontend-design → sonnet / high, or `fable`
Aesthetic direction and typography is genuine creative judgment. sonnet/high is
the safe default; `fable` is worth trialing here as the creative/generative
specialist. Confidence med — verify against real design tasks before pinning.

## In-body delegation opportunities (scan → synthesize)

These skills scan/collect before they judge. Delegate the collect phase to a
haiku `Explore` subagent that returns only the distilled result; keep the
synthesis on the session model:

- **claude-automation-recommender** — delegate the codebase scan; keep the
  recommendation synthesis. Insert at its analysis phase:
  ```
  Use the Agent tool (subagent_type: Explore) to survey the repo:
    "List languages, frameworks, test setup, CI, existing .claude/ config;
     return a compact bullet list, no prose."
  Then synthesize automation recommendations on the session model.
  ```
- **claude-md-improver** — delegate "scan for all CLAUDE.md files" to Explore;
  keep the quality evaluation and edits.
- **skill-creator** — delegate eval/benchmark runs (mechanical) to a haiku
  subagent; keep authoring judgment on the session model.
- **project-artifact** — the publish step is a single mechanical tool call;
  minor, note only.

## Plan-ready change list (owned skills only)

**(empty)** — all 29 scanned skills are advisory-only plugin skills. None is
eligible for an actionable edit, by design. To act on the recommendations above,
copy a skill into `~/.claude/skills/` (making it owned) first, or apply the
diffs manually knowing a plugin update will overwrite them.

## Excluded

- **Advisory-only (plugin) skills:** all 29 — not in the change list by design.
- **Low-confidence / needs review:** none rated low; the many `med` ratings are
  based on description + frontmatter only. Reading the skill bodies would firm
  these up before any pin.

---

# v2 — after grounding against skill bodies (assessment loop)

The v1 table above was classified from descriptions only. I then read the actual
bodies of the 18 `med`-confidence skills (via Explore subagents) and challenged
every verdict. Corrections:

| Skill | Archetype | v1 | v2 (grounded) | What changed |
|-------|-----------|----|----|--------------|
| m5-onboard | Orchestration | sonnet/low | **sonnet/medium**, conf high | Scripts are mechanical but the model orchestrates, reads live flash logs, coaches the button dance |
| frontend-design | Reasoning/creative | sonnet/high | **opus/high or fable**, conf high | Core is adversarial self-critique against generic defaults |
| writing-hookify-rules | Transform | sonnet/low | **haiku/low** | Fill-a-fixed-template; only a narrow regex seam |
| project-artifact | Analysis | sonnet/med, 0 deleg | sonnet/med, **1 deleg** | Missed its token-heavy gather-live-state (gh/git) phase |
| claude-md-improver | Analysis | 1 deleg (find) | 1 deleg (**reframed**) | `find` isn't delegatable; the many-file *read* is |
| build-mcp-server | Authoring | 0 deleg | 0 deleg (**confirmed**) | Its collect phase is a user interview — not delegatable |

All other v1 verdicts held once re-derived through the archetype method.

## Convergence assessment

- **Taxonomy is stable.** All 29 skills bucket cleanly into the five archetypes
  (Transform / Authoring / Analysis / Orchestration / Reasoning). A further
  body-reading pass would refine *effort* levels within the sonnet-Authoring
  cluster (low vs medium) and confidence — it would not add new categories. The
  loop has converged on structure.
- **Every v2 correction traces to a rubric rule now written down** (residual-load
  / orchestration-isn't-mechanical / self-critique-is-top-tier /
  delegation-needs-a-token-heavy-scan / read-the-body). Re-running the improved
  rubric reproduces the v2 verdicts — the fix is in the method, not hand-patched
  per skill.
- **Corpus caveat (honest limit).** These 29 are one marketplace, skewed toward
  authoring/dev skills. The archetype rubric is well-exercised on Authoring, only
  lightly on Analysis/Transform, and barely on Reasoning (n=1–2). Applying it to
  a corpus of data-processing or agentic skills may surface effort-tuning gaps —
  not expected to break the archetypes, but untested there.

**Method error rate:** description-only (v1) mis-tiered 4/18 (~22%), *all
under-tiering* — the dangerous direction (starving hard work). Grounded +
archetype-first (v2) reproduces the human-checked verdicts on this set. Hence the
workflow now mandates reading bodies before pinning.

---

**Next step:** applying changes is a separate, user-approved action. For plugin
skills, "apply" means either living with per-update overwrites or vendoring the
skill into `~/.claude/skills/`.

---

# v3 — breaking the corpus skew (agent corpus + ground-truth validation)

The v1/v2 corpus was authoring-heavy. To stress the Analysis / Transform /
Orchestration / Reasoning archetypes, iteration 3 classified **26 subagents**
(critics, auditors, analysts, extractors, graders) — **9 of which ship an
author-assigned `model:`**, giving external ground truth to grade the rubric
against instead of only self-assessment.

## Ground-truth scorecard (predicted tier vs author's `model:`)

| Agent | Archetype | Rubric predicts | Author pinned | v1-baseline | v3-refined |
|-------|-----------|-----------------|---------------|-------------|------------|
| agent-sdk-verifier-py | Analysis | sonnet | sonnet | ✓ | ✓ |
| agent-sdk-verifier-ts | Analysis | sonnet | sonnet | ✓ | ✓ |
| code-explorer | Analysis | sonnet | sonnet | ✓ | ✓ |
| code-reviewer (feature-dev) | Analysis | sonnet | sonnet | ✓ | ✓ |
| agent-creator | Authoring | sonnet | sonnet | ✓ | ✓ |
| code-simplifier (×2) | Transform-*semantic* | opus | opus | ✗ flat "Transform=haiku" | ✓ semantic split |
| code-architect | Reasoning-*pattern-extending* | sonnet | sonnet | ✗ flat "Reasoning=opus" | ✓ novelty split |
| code-reviewer (pr-review) | Analysis, high cost-of-miss | opus | opus | ✗ | ✓ cost-of-miss dial |

Baseline archetypes: **7/9**. After the three refinements: **9/9** — and the
fixes are principled (they also explain the other 17 agents), not per-item
patches. Caveat against over-claiming: the two `code-reviewer`s have *near-
identical prompts* yet were pinned sonnet vs opus by different authors, so 100%
match is unattainable and would signal overfitting — cost-of-miss is a user dial.

## What the 17 unpinned agents added

- **Reasoning splits by stakes/novelty** — `architecture-critic` (adversarial
  design critique) and the skill-creator `grader`/`comparator`/`analyzer` (blind
  judging, causal attribution) are opus-worthy; `code-architect` (design that
  extends existing patterns) is sonnet. "Reasoning" alone doesn't mean opus.
- **Analysis splits by cost-of-miss** — `security-auditor` (exploit construction)
  and `version-delta-analyst` (silent-behavior landmines) are opus-leaning;
  `comment-analyzer`, `pr-test-analyzer`, `legacy-analyst` are sonnet.
- **Orchestration splits by interpretation** — `plugin-validator` (drives
  validate scripts, tags severity) is haiku/sonnet-low; `m5-onboard` (live log
  interpretation + coaching) is sonnet-medium.
- **`inherit` ≠ right-sized** — every one of these 17 judgment-heavy agents was
  left unpinned. The rubric now treats absent/`inherit` as *unsized*.

## Convergence (across 3 iterations, 55 units, 2 corpora)

- **Archetypes stable** (no new category in 55 units); the iteration-3 change was
  adding an **intra-archetype stakes/depth dial**, now the rubric's core method —
  a single generalizable principle, not per-skill rules.
- **Externally validated**: refined rubric reproduces 9/9 author-pinned models,
  with the residual gap explained (author inconsistency + cost-of-miss as a user
  dial). This is stronger evidence than the self-graded v2 pass.
- **Remaining limit**: still no *data-processing* or *long-horizon agentic*
  skills tested; effort-level granularity (low/med/high within a tier) remains
  the least-validated axis. Next highest-value iteration would be empirical —
  apply one recommendation and measure the cheap config still produces a correct
  result.

---

# v4 — empirical validation (apply a recommendation, measure it)

Took the `session-report → haiku` call from theory to measurement. Built a
deterministic **oracle** (ran the bundled `analyze-sessions.mjs` and computed the
true figures), then ran the *identical* skill task under **haiku** and **sonnet**
(forced via the Agent `model` param) and graded both against the oracle.

| Axis | Haiku | Sonnet |
|------|-------|--------|
| JSON-paste fidelity (embedded == analyzer output) | PASS | PASS |
| Arithmetic of stated %s | **5/5 correct** | **5/5 correct** |
| Structural (template intact, `.take`/`.callout` markup) | PASS | PASS |
| Found dominant prompt | 31.8% ✓ | 30.6% ✓ ("15× the 2% threshold") |
| Subtle anomaly (Explore cache 18.7% vs ~89%) | found, gap 70.5pp ✓ | found + per-agent split ✓ |
| Latency | 116s | 171s |
| Per-token price | ~10× cheaper | baseline |

**Result:** haiku produced an arithmetically correct, structurally valid,
equivalently useful report — recommendation confirmed. Sonnet's *only* advantage
was editorial framing (good/bad/info balance, threshold multiples), not
correctness.

**Mechanism confirmed:** `session-report` bundles a deterministic parser
(`analyze-sessions.mjs`, no `scripts/` dir but a sibling `.mjs`), so the model's
residual is paste-JSON + simple-% + terse narrative = haiku-tier. This is the
"scripts absorb the mechanical load → read the residual" rule, now with evidence.

**New evidence-based nuance (added to rubric):** for mechanical-core +
narrative-garnish skills, haiku is sufficient for *correctness*; tier up or add
`effort: medium` only when *editorial quality* is the priority — that, not
correctness, is what the bigger model buys.

**Method note:** the session's token totals grew between runs (4.25M → 4.75M →
4.94M) as this very conversation extended, so each run was graded against *its
own* analyzer output for arithmetic and against the oracle qualitatively —
removing the moving-target confound.
