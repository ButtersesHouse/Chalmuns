---
name: right-format-skills
description: This skill should be used when the user asks to "check skill size", "is my skill too long", "reformat a skill", "decompose an oversized skill", "split a skill into references", "audit skill format", "fix SKILL.md structure", or wants a SKILL.md brought in line with Anthropic's published Skill-authoring format/size guidance. Audits SKILL.md files for body length, frontmatter validity, progressive-disclosure structure, and reference-file hygiene, then proposes — and can mechanically execute — a decomposition plan.
argument-hint: "[path-or-skill-name | --all | --plugins | --apply <report-path>]"
allowed-tools: Read, Grep, Glob, Bash, Write
model: sonnet
effort: medium
---

# Right-Format Skills

Audit skills against Anthropic's published Skill-authoring format/size guidance
— body-length budget, frontmatter validity, progressive-disclosure structure,
reference-file hygiene — and propose (or execute) the decomposition that brings
an oversized or malformed skill back in spec.

This is the format/size counterpart to `right-size-skills` (which right-sizes
the *model*). Same pipeline shape: audit → plan-ready report → a separate,
explicit `--apply` step. It never rewrites a skill during the audit pass.

## Why this is a `sonnet`/`medium` skill, not `haiku`

Most of the checks here are mechanical (line counts, regex validation against
documented limits) and are delegated to `scripts/audit_format.py` — a
deterministic script absorbs that residual, per `right-size-skills`'
own rubric. What's left for the model is genuine but bounded judgment: *how*
to decompose an over-budget skill (which sections form a coherent reference
file, what to name it, whether a flagged pattern is a real problem or a false
positive). That's Analysis-archetype, low cost-of-miss (advisory, reversible)
— sonnet/medium, not opus, and not haiku (a fixed-template skill would risk
proposing awkward, incoherent splits).

## Key facts (from Anthropic's Skill-authoring research — see `references/rubric.md` for citations)

- Keep the **SKILL.md body under 500 lines**; split into reference files once
  approaching that. A Feb 2026 survey of 40,000+ public skills found the
  median body is ~1,414 tokens — most skills fit comfortably.
- **Progressive disclosure**: only `name`+`description` preload at startup;
  the body loads on trigger; reference files and scripts load only when read
  or run. This is the entire reason size discipline pays off — it's not
  wasted at rest, only when read.
- **Keep references one level deep from SKILL.md.** A file SKILL.md links to
  should not itself link to a further file the model is expected to read —
  Claude may only partially preview (`head -100`) files reached through a
  chain, and miss content.
- **Reference files over 100 lines need a table of contents** near the top.
- **Frontmatter is validated, not just conventional**: `name` ≤64 chars,
  lowercase/digits/hyphens only, no reserved words (`claude`, `anthropic`);
  `description` non-empty, ≤1024 chars, third person, states both what the
  skill does and when to use it.
- **Claude-Code-specific stakes**: once a skill loads, its body stays in the
  live context across turns (a recurring cost, unlike a one-shot API call),
  and post-compaction re-attachment keeps only the **first 5,000 tokens** of
  each recently-invoked skill under a shared 25,000-token budget. An
  over-budget skill isn't just wasteful — it can get silently truncated after
  compaction, with instructions past the cut simply gone.

## Workflow

### 1. Resolve targets

Same convention as `right-size-skills`:

- **No argument** or `--all` → enumerate every `SKILL.md` under
  `~/.claude/skills/`. **Owned** (editable).
- **A path or skill-name** → that single skill.
- **`--plugins`** → additionally scan
  `~/.claude/plugins/marketplaces/**/skills/**/SKILL.md`. Mark results
  **advisory-only** — never appear in the apply-ready change list.
- **`--apply <report-path>`** → skip straight to step 5: read a previously
  emitted report and execute its approved decompose commands.

### 2. Run the deterministic scan

```sh
python <skill-dir>/scripts/audit_format.py --glob "~/.claude/skills/*/SKILL.md"
# or, for a single skill:
python <skill-dir>/scripts/audit_format.py <path>/SKILL.md
```

This emits one JSON object per skill: `body_lines`, `over_budget`,
`approaching_budget`, `frontmatter_issues`, `sections` (heading/line map),
`nested_references`, `reference_files_missing_toc`, `windows_style_paths`,
`time_sensitive_phrases`. For a large audit, delegate this step plus the
raw-finding triage to an `Explore` subagent and keep only the judgment calls
below on the session model.

**Treat every script finding as a candidate, not a verdict** — confirm each
before it goes in the report. The nesting/path/time-sensitive regexes are
heuristics (e.g. a documentation file that merely *mentions* another file's
name isn't necessarily an unwanted read-chain); read the actual line before
flagging it.

### 3. Judge decomposition for over-budget or structurally messy skills

Only for skills where `over_budget` is true, or structure findings are real:

1. Read the skill body and its `sections` map.
2. Group sections into a coherent split using whichever pattern from
   `references/rubric.md` fits:
   - **High-level guide + references** — pull the largest self-contained
     "advanced"/"reference"/"examples" sections out, keep a quick-start plus
     navigation pointers in SKILL.md.
   - **Domain-specific organization** — if sections cover distinct domains
     (e.g. per-integration, per-dataset), split one reference file per
     domain instead of one large reference file.
   - **Conditional details** — collapse rarely-needed detail behind a link
     from the relevant SKILL.md subsection rather than inlining it.
3. Pick target filenames that are descriptive (`FORMS.md`, `reference.md`),
   not `doc1.md`. Keep the split **one level deep** — a newly created
   reference file must not itself point to yet another file the model needs
   to read for the same task.
4. Confirm the post-split SKILL.md body actually lands under 500 lines; if
   one extraction isn't enough, propose more than one.

For frontmatter/naming/path/TOC issues, the fix is usually direct (no
decomposition needed) — state it as a one-line change.

### 4. Emit the report

Fill in `references/report-template.md`. Write it to
`~/.claude/plans/right-format-report-<YYYY-MM-DD>.md` and summarize highlights
inline. Include, per skill needing action, the **exact commands** to run
against `scripts/decompose.py` — these are what step 5 executes.

Do not edit any skill in this pass. State that applying the plan is a
separate, `--apply`-invoked step, and that advisory-only (plugin) skills are
excluded from the actionable command list by design.

### 5. Apply (only on explicit `--apply <report-path>`)

Read the report's "Plan-ready decompose commands" section and run each
`scripts/decompose.py` command in order:

```sh
python <skill-dir>/scripts/decompose.py --skill <path>/SKILL.md \
  --heading "<exact heading text>" --target <FILENAME.md> \
  [--summary "<pointer text>"]
```

Each invocation extracts exactly one section into a new file and leaves a
one-line pointer behind — small, individually reviewable diffs. After
applying, re-run the audit script on the touched skill(s) to confirm they're
now within budget, and report the before/after body-line counts. Frontmatter
fixes and other one-line changes from the report are applied with `Edit`
directly (not through `decompose.py`, which only handles section extraction).

Never run `--apply` against advisory-only (plugin) skills — those are
overwritten on update; report the recommendation but do not touch the file.

## Resources

- **`references/rubric.md`** — the full format/size rubric with citations to
  Anthropic's Skill-authoring documentation.
- **`references/report-template.md`** — the exact report format to fill in.
- **`scripts/audit_format.py`** — deterministic scan (line counts,
  frontmatter validation, reference nesting/TOC/path checks).
- **`scripts/decompose.py`** — mechanically extracts one SKILL.md section
  into a named reference file and leaves a pointer; the actual "reformat and
  decompose" mechanism, run once per approved plan item.
