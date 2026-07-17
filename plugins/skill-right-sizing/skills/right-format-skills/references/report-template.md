# Report Template

Fill in this structure. Write it to
`~/.claude/plans/right-format-report-<YYYY-MM-DD>.md` and summarize the
highlights inline in the conversation.

---

# Right-Format Skills Report — <YYYY-MM-DD>

**Targets scanned:** <N owned, M advisory-only>
**Scope:** <~/.claude/skills | single skill | + --plugins>

## Summary

| Skill | Owner | Body lines | Status | Structure issues | Frontmatter issues | Confidence |
|-------|-------|-----------|--------|-------------------|---------------------|------------|
| example-skill | owned | 612 | over budget | 1 nested ref, missing TOC | — | high |
| other-skill | owned | 340 | approaching | — | name not gerund-form | high |
| plugin-skill | advisory | 89 | OK | — | — | high |

Legend: **Owner** = owned (editable) or advisory (plugin, do not edit).
**Status** = OK / approaching (400–499 lines) / over budget (≥500 lines).
"OK" in Structure/Frontmatter columns = no issues found.

## Per-skill detail

### <skill-name> — <owned | advisory-only>

- **Body lines:** <N> (<OK | approaching | over budget>)
- **Frontmatter issues:** <list, or "none">
- **Structure issues:** <nested references / missing TOC / Windows paths /
  time-sensitive phrasing — list each with the specific finding, or "none">
- **Confidence:** <high | med | low> — <note if a finding needs a closer human
  read before acting on it>

**Decomposition plan** (only if over budget or structurally messy):

| Extract section | → target file | Why | Est. lines moved |
|------------------|--------------|-----|-------------------|
| "Advanced features" | ADVANCED.md | self-contained, rarely needed | 120 |
| "API reference" | REFERENCE.md | escalating detail, needs a TOC once moved | 200 |

Post-split estimate: SKILL.md body <N-before> → <N-after> lines.

**Direct fixes** (no decomposition needed):

- <e.g. "frontmatter: shorten `name` to ≤64 chars">
- <e.g. "add forward-slash paths in the 'Setup' section">

<Repeat per skill needing action. For advisory-only skills, include the same
detail but add: "Advisory only — plugin skill, overwritten on update; --apply
will refuse to touch this file.">

## Plan-ready decompose commands (owned skills only)

Ordered, ready to run under `--apply` or by hand:

```sh
python <skill-dir>/scripts/decompose.py --skill ~/.claude/skills/<name>/SKILL.md \
  --heading "Advanced features" --target ADVANCED.md

python <skill-dir>/scripts/decompose.py --skill ~/.claude/skills/<name>/SKILL.md \
  --heading "API reference" --target REFERENCE.md
```

List direct (non-decompose) fixes separately — these are applied with `Edit`,
not `decompose.py`:

1. `~/.claude/skills/<name>/SKILL.md` — <direct fix>.

## Excluded

- **Advisory-only (plugin) skills:** <list> — recommendations given, but
  `--apply` will not touch them; overwritten on plugin update regardless.
- **Low-confidence / needs review:** <list> — flagged findings that need a
  human read before deciding whether they're real (e.g. ambiguous
  date-mention vs. genuine deprecation branch).

---

**Next step:** applying this plan is a separate step — re-invoke with
`--apply ~/.claude/plans/right-format-report-<YYYY-MM-DD>.md`, or run the
listed commands by hand.
