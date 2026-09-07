# Step 12.6: Promote to a top-level file (only when explicitly asked)

**Do not run this step as part of a normal run.** The pipeline's outputs are
the per-domain skills; Claude Code auto-loads `.claude/skills/` on its own, so
nothing needs to be copied to the repo root for this repo's own use.

Run it **only** when the user explicitly asks for a top-level `AGENTS.md` or
`CLAUDE.md` — e.g. "publish these to AGENTS.md", "I want Codex to see these",
"promote the conventions". The usual reason is agents other than Claude Code
(Codex, Cursor, Gemini CLI) which never read `.claude/skills/`.

```
$BIN promote --state .claude/pattern-learner/state.json \
  --skills-dir .claude/skills \
  [--agents-md AGENTS.md] [--claude-md CLAUDE.md] [--create]
```

Defaults to `AGENTS.md` when neither target flag is given. Pass both to write
both. The command prints a JSON array of `{path, outcome, reason}`.

What it writes is confined to a marked block:

```
<!-- pattern-learner:begin -->  ...generated...  <!-- pattern-learner:end -->
```

Everything outside those markers is preserved byte-for-byte, so a
hand-written file keeps its content and a re-run only refreshes our section.
Outcomes: `created` (with `--create`), `updated` (block replaced), `appended`
(no block yet — added at the end, nothing overwritten), `unchanged`
(identical, no write), `skipped` (nothing to promote, or the file is absent
and `--create` was not passed).

Two rules for this step:

- **Never pass `--create` unless the user asked for the file to be created.**
  Without it, a missing target is skipped rather than created, which is what
  keeps `promote` from introducing a repo-root file nobody asked for. If the
  result comes back `skipped` for that reason, report it and ask before
  re-running with `--create`.
- **If it errors about malformed markers**, do not try to repair the file by
  hand — report it. A half-present marker pair means someone edited the
  delimiters, and guessing the block bounds risks destroying their content.

---
