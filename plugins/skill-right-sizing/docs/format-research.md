# Skill format/size research — findings behind `right-format-skills`

Research pass conducted 2026-07-17 to ground `right-format-skills` in
Anthropic's published Skill-authoring guidance, distinct from
`right-size-skills`' model/effort cost research documented in
`validation-report.md`. Full rubric with inline citations lives at
`skills/right-format-skills/references/rubric.md`; this doc is the narrative
summary of what the sources say and why it turned into a companion skill.

## Sources consulted

- Anthropic, *Skill authoring best practices* —
  `platform.claude.com/docs/en/agents-and-tools/agent-skills/best-practices`
  (fetched directly; primary source for all numeric thresholds below).
- Anthropic, *Equipping agents for the real world with Agent Skills*
  (engineering blog post introducing Agent Skills and progressive disclosure).
- Claude Code docs, *Extend Claude with skills* —
  `code.claude.com/docs/en/skills` (Claude-Code-specific runtime behavior:
  skill content lifecycle, compaction re-attachment budget).
- Secondary coverage citing a Feb 2026 survey of 40,000+ publicly listed
  skills (median body ≈1,414 tokens) — used as a corpus sanity-check on the
  500-line ceiling, not as a primary source for the limit itself.

## What the research says, in short

1. **There is a hard size ceiling, stated explicitly and repeated across both
   the API-facing and Claude-Code-facing docs: keep the SKILL.md body under
   500 lines.** This is listed as a checklist item ("SKILL.md body is under
   500 lines"), not just a suggestion. Both docs recommend the same fix when
   a skill grows past it: split content into separate files.
2. **The mechanism that makes size discipline worthwhile is progressive
   disclosure**, a three-stage load model: only `name`+`description` preload
   for every skill at session start; the full body loads once a skill is
   triggered; referenced files and scripts load only when actually read or
   executed. A skill can bundle arbitrarily large reference material at zero
   standing cost — the 500-line rule targets specifically the part that
   *always* gets paid once triggered.
3. **Frontmatter has real validation, not just convention**: `name` (≤64
   chars, lowercase/digits/hyphens, no reserved words `claude`/`anthropic`)
   and `description` (non-empty, ≤1024 chars, third person). These are
   correctness constraints — violating them risks the skill failing to
   register or failing discovery, not just looking unpolished.
4. **Structural rules exist specifically to make progressive disclosure work
   in practice**, not just to look tidy:
   - Keep reference files **one link-hop deep** from SKILL.md — chained
     references risk a partial read (Claude previewing with `head -100`)
     silently dropping content.
   - Reference files **over 100 lines need a table of contents** so a partial
     read still reveals the full scope.
   - Three named organizing patterns cover most cases: high-level guide +
     references, domain-specific reference splitting, and conditional
     (collapsed) details.
5. **Content-quality rules** (concision, consistent terminology, avoiding
   time-sensitive branching logic, providing a default instead of a menu of
   options, matching "degrees of freedom" to task fragility) are explicit
   authoring guidance but are **not mechanically checkable** — a script can
   flag a paragraph as long, not as unjustified. These stay judgment calls in
   the skill's workflow, not the deterministic script.
6. **Claude-Code-specific stakes beyond the API-generic guidance**: a
   triggered skill's body stays resident across turns (recurring cost per
   turn, not a one-shot charge), and after a context compaction Claude Code
   re-attaches only the **first 5,000 tokens** of each recently-invoked skill
   under a shared 25,000-token cap across all recently-invoked skills. This
   makes the 500-line ceiling a correctness argument in Claude Code
   specifically — an over-budget skill can have its instructions silently
   truncated mid-procedure post-compaction, not merely cost more tokens.

## Why this became a skill rather than a section added to `right-size-skills`

`right-size-skills` audits **which model/effort tier** a skill's *work*
deserves — a judgment-heavy classification task (archetype + stakes dial),
correctly pinned at `sonnet`/`high`. Format/size auditing is a different
question — **is the skill's own file structured the way Anthropic's runtime
expects** — and is mostly mechanical (line counts, regex-checkable frontmatter
rules) with a smaller judgment residual (how to decompose coherently). Folding
it into `right-size-skills` would have diluted that skill's single concern and
forced a coarser model/effort pin across two different-shaped tasks. Keeping
them as siblings in the same plugin — mirroring the `right-size-skills` +
`skill-compare` propose/prove pairing — keeps each skill's frontmatter
correctly tiered for what it actually does, and keeps `right-size-skills`'
own validated rubric un-diluted.

## What `right-format-skills` added beyond a report

The user-facing ask was explicitly for "a means to reformat and decompose as
needed," not just a report. `scripts/decompose.py` is that mechanism: given an
approved plan item (a heading to extract and a target filename), it performs
exactly one section-extraction per invocation — small, individually reviewable
diffs — rather than the skill attempting to rewrite a file wholesale from
inside the model's own edit loop. This mirrors the existing plugin's
philosophy (audit skills never silently edit; changes are a separate,
explicit, user-approved step) while still giving the "decompose" ask a real,
runnable tool rather than only a written suggestion.
