# Report Template

Fill in this structure. Write it to
`~/.claude/plans/right-size-report-<YYYY-MM-DD>.md` and summarize the highlights
inline in the conversation.

---

# Right-Size Skills Report — <YYYY-MM-DD>

**Targets scanned:** <N owned, M advisory-only>
**Scope:** <~/.claude/skills | single skill | + --plugins>

## Summary

| Skill | Owner | Archetype | Current model/effort | Proposed model/effort | Fork? | Delegations | Confidence |
|-------|-------|-----------|----------------------|-----------------------|-------|-------------|------------|
| example-skill | owned | Transform | inherit / — | haiku / low | yes (Explore) | 1 | high |
| other-skill | owned | Authoring | sonnet / high | sonnet / medium | no | 0 | med |
| plugin-skill | advisory | Reasoning | inherit / — | opus / high | no | 0 | low |

Legend: **Owner** = owned (editable) or advisory (plugin, do not edit).
"OK" in the proposed column = already well-sized, no change.

## Per-skill detail

### <skill-name> — <owned | advisory-only>
- **Recommendation:** `model: <tier>`, `effort: <level>`<, `context: fork` (+ `agent: Explore`)>
- **Rationale:** <one or two lines — the hardest step and why the tier fits>
- **Confidence:** <high | med | low><; note the judgment seam if med>
- **Frontmatter diff:**
  ```diff
   ---
   name: <skill-name>
  +model: <tier>
  +effort: <level>
   ---
  ```
- **In-body delegation** (if any):
  - Phase: <which phase / heading in the body>
  - Insert:
    ```
    Use the Agent tool (subagent_type: Explore) to <gather X>:
      "<focused instruction; return only the distilled result>"
    Then <the judgment step stays on the session model>.
    ```

<Repeat per skill. For advisory-only skills, include the recommendation but add:
"Advisory only — plugin skill, overwritten on update; not in the change list.">

## Plan-ready change list (owned skills only)

Ordered, concrete edits. Paste into plan mode to execute.

1. `~/.claude/skills/<name>/SKILL.md` — add `model: <tier>`, `effort: <level>` to frontmatter.
2. `~/.claude/skills/<name>/SKILL.md` — add `context: fork` + `agent: Explore` to frontmatter.
3. `~/.claude/skills/<name>/SKILL.md` — insert Explore delegation at "<phase>" (see detail above).

## Excluded

- **Advisory-only (plugin) skills:** <list> — not in the change list by design.
- **Low-confidence / needs review:** <list> — no down-tier proposed.

---

**Next step:** applying this change list is a separate, user-approved action.
