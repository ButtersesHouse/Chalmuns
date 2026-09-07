# Step 12.5: Format check

`write-outputs` already keeps generated skills inside the documented body-line
budget itself (inline rules while the body fits under the 400-line warn
threshold, otherwise the chunked `rules/` index described in Step 12). So this step is **not** a
size-management step — it verifies the one thing the generator does not check:
that the **frontmatter it emitted is valid**. The domain name is written to
`name:` verbatim and the description comes from your `domain_descriptions`
entry; neither is validated or sanitized by the generator. `audit-format`
parses the block as real YAML (the way Claude Code loads it) and then checks
the documented field rules, so a file that would fail to load is reported as
`frontmatter is not valid YAML` rather than passing silently.

If this run wrote **zero** skill files (e.g. a `--review` run that approved nothing), skip this step entirely — do not invoke `audit-format` with no paths. `audit-format` requires at least one path and its usage error is not a sign the subcommand is broken; there is simply nothing to check this run.

Otherwise, run it against every skill file this run wrote or touched. That includes `<skills-dir>/conventions/SKILL.md` when any universal (`CLAUDE.md`-sentinel) rule was approved — it is a real skill with frontmatter like every other domain:

```
$BIN audit-format <skills-dir>/<domain1>/SKILL.md <skills-dir>/<domain2>/SKILL.md ...
```

The thresholds it applies are the ones documented in Anthropic's
Skill-authoring guidance; the `skill-right-sizing` plugin's
`right-format-skills` skill checks hand-authored skills against the same
rubric (`references/rubric.md` there carries the citations).

For each result:

- **`frontmatter_issues` non-empty — act on this.** The domain name violates
  a documented frontmatter rule (e.g. Step 8B/A3 canonicalized a domain to
  `Legacy_API`, which breaks the lowercase/digits/hyphens charset rule). A
  skill whose `name` breaks the charset rule may fail to register, so this
  is worth fixing. A `not valid YAML` finding, or any finding about the
  *description*, on a file `write-outputs` just wrote is a generator bug (it
  quotes every value it emits, and it truncates every description — your
  `domain_descriptions` override included — well under the length limit, so
  a length finding cannot come from state) — STOP and report it, as with
  `over_budget` below.

  Fix it at the **state** level, never by hand-editing the generated file —
  `write-outputs` rewrites that file wholesale from state on every run, so a
  hand-edit is silently discarded next run. Propose to the user a corrected
  domain name (valid charset). On approval, re-target the affected rules'
  `target.location`, then re-run **Step 11** (state-write) and **Step 12**
  (write-outputs), and re-run this check to confirm it comes back clean. On
  decline, note it in the Step 13 summary and continue.

- **`over_budget` / `approaching_budget` — this is a generator bug, not
  something to fix by hand.** Step 12's chunking is supposed to make this
  unreachable: a domain large enough to exceed the budget should already
  have been chunked into a `rules/` index. If a generated skill still
  reports over budget, `write-outputs`' chunking did not do its job.
  Per the Tooling policy, **STOP and report it** — do not re-split the
  domain by hand and do not edit the emitted file. Include the domain name
  and the reported `body_lines` so the generator can be fixed.

- **A non-zero exit — a path was not audited.** `audit-format` fails when it
  cannot read one of the paths you gave it, and names them. That is a
  mistake in the path list, not a finding about the skill: the file was
  never opened, so its empty `frontmatter_issues` means nothing. Re-check
  the paths against what Step 12 reported it wrote and run it again. Do not
  report the run's skills as format-clean until every path audited.

This step never edits a file or state on its own.

---
