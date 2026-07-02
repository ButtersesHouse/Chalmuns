---
name: right-size-skills
description: This skill should be used when the user asks to "right-size skills", "assign models to skills", "optimize skill model usage", "audit skills for model cost", "decompose a skill", "route skill work to smaller models", or wants to reduce model spend by matching each skill to the cheapest model that can do its work well. Analyzes SKILL.md files and produces a plan-ready report proposing model/effort/decomposition changes. Advisory only — it does not edit skills.
argument-hint: "[path-or-skill-name | --all | --plugins]"
allowed-tools: Read, Grep, Glob, Write
model: sonnet
effort: high
---

# Right-Size Skills

Audit skills and propose the cheapest model + effort each one needs, plus
opportunities to hand mechanical work off to smaller models. The purpose is to
spend expensive models only where judgment is impactful, and push routine
skill-work (search, extraction, formatting, scaffolding, validation) down the
cost curve.

This skill **reads and reports only**. It never edits a skill in the same pass.
It emits a report whose change list drops straight into plan mode for a
separate, user-approved apply step.

This skill pins itself to `model: sonnet`, `effort: high` — classification is
high-judgment Analysis and must never run on a small model (which systematically
under-tiers). Switch its frontmatter to `model: opus` for large or consequential
audits (sweeping many skills, or when acting on the results broadly). The
token-heavy body-reading is delegated to haiku `Explore` subagents (see Workflow),
so only the judgment runs on the strong model.

## Key mechanism (why this works)

Skills natively support model routing in their frontmatter. A `SKILL.md` may set:

- `model:` — `haiku` | `sonnet` | `opus` | `fable` | full model ID | `inherit`
- `effort:` — `low` | `medium` | `high` | `xhigh` | `max`
- `context: fork` (+ `agent:`) — run the whole skill in an isolated subagent;
  use `agent: Explore` for read-only search/extraction (Explore runs on haiku)

So "assign a model" is a frontmatter edit, and "hand off part of the work" is
either `context: fork` (whole skill) or in-body delegation to a cheap subagent
via the Agent tool (part of a skill).

## Workflow

### 1. Resolve targets

- **No argument** or `--all` → enumerate every `SKILL.md` under
  `~/.claude/skills/`. These are **owned** (editable).
- **A path or skill-name** → that single skill (resolve a bare name against
  `~/.claude/skills/<name>/SKILL.md`).
- **`--plugins`** → additionally scan
  `~/.claude/plugins/marketplaces/**/skills/**/SKILL.md`. Mark every plugin
  result **advisory-only** — these are overwritten on plugin update and must
  never appear in the change list.

Use Glob to enumerate and Read to load each `SKILL.md`. If no owned skills are
found, say so and suggest re-running with `--plugins` for an advisory pass.

### 2. Classify each skill

Read `references/rubric.md` and apply it. **Read each skill's body workflow, not
just its description** — description-only classification systematically
mis-tiers (usually under-tiering skills whose hard work isn't in the blurb) and
must be capped at med confidence with no pinned change. For a large audit,
delegate the body-reading to `Explore` subagents (dogfood the pattern below) and
do the classification here.

Per skill, in order:

1. **Identify the archetype** (rubric Step 1): Transform / Authoring / Analysis /
   Orchestration / Reasoning. This sets the baseline tier and decides whether
   decomposition even applies (only Analysis/Orchestration have a real seam).
2. **Tier from the residual + dial** — what is left for the model *after* bundled
   scripts/references/tools absorb the rote work, then apply the archetype's dial
   (rubric Step 1/2). Do not down-tier just because a skill ships `scripts/` or
   sounds mechanical; orchestration and semantic transforms are judgment-tier.
   For Analysis/Reasoning, apply the **cost-of-miss** check and, when it's a
   user call (bug-recall vs economy), surface both options rather than pick one.
   Give a one-line rationale and a **confidence**. Treat a missing/`inherit`
   model as *unsized*, not endorsed.
3. **Whole-skill `context: fork`** — recommend when the skill is bounded
   input→output, needs no live history, and isn't interactive (add
   `agent: Explore` for read-only work).
4. **Decomposition** — only for Analysis/Orchestration with a **token-heavy,
   read-only** scan phase. Never propose it for a one-line command, a user
   interview, or an already-delegated phase.

### 3. Compute the delta

Compare each recommendation to the skill's **current** frontmatter (`model:`,
`effort:`, `context:`). Skip no-ops. Mark already-well-sized skills as **OK**.
Never down-tier on **low** confidence — flag those for human review instead.

### 4. Emit the report

Fill in `references/report-template.md`:

- **Summary table** — one row per skill.
- **Per-skill detail** — rationale, the exact frontmatter diff to apply, and for
  each in-body delegation the target phase plus a sketch of the delegating Agent
  call to insert.
- **Plan-ready change list** — an ordered `file → change` checklist containing
  **owned skills only**, ready to paste into plan mode.

Write the report to `~/.claude/plans/right-size-report-<YYYY-MM-DD>.md` and also
summarize the highlights inline.

### 5. Stop

Do not edit any skill. State that applying the change list is a separate,
user-approved step, and that advisory-only (plugin) skills were excluded from
the change list by design.

## Resources

- **`references/rubric.md`** — model/effort tiers, `context: fork` signals,
  decomposition signals, and confidence calibration.
- **`references/report-template.md`** — the exact output format to fill in.
