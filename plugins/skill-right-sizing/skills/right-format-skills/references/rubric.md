# Format/Size Rubric

## Contents

- Sources
- Hard limits (validated, not stylistic)
- Size budget
- Progressive disclosure patterns
- Reference-file hygiene
- Naming conventions
- Content hygiene
- Claude-Code-specific stakes
- What this rubric deliberately doesn't automate

## Sources

- Anthropic, *Skill authoring best practices* —
  `platform.claude.com/docs/en/agents-and-tools/agent-skills/best-practices`
- Anthropic, *Equipping agents for the real world with Agent Skills* (engineering blog)
- Claude Code docs, *Extend Claude with skills* — `code.claude.com/docs/en/skills`
- Feb 2026 survey of 40,000+ publicly listed skills (cited via best-practices
  guide secondary coverage): median skill body ≈ 1,414 tokens.

Fetched and verified against these sources 2026-07-17. Re-verify before relying
on exact numeric thresholds long after that date — see "Avoid time-sensitive
information" below, which applies to this rubric too.

## Hard limits (validated, not stylistic)

These come directly from documented frontmatter validation, not house style:

- `name`: max 64 characters; lowercase letters, numbers, hyphens only; no XML
  tags; cannot contain the reserved words `anthropic` or `claude`.
- `description`: non-empty; max 1,024 characters; no XML tags.

A skill that violates these isn't just unpolished — it may fail to register or
fail discovery. Flag these as **must-fix**, distinct from the advisory items
below.

## Size budget

**Keep the SKILL.md body under 500 lines.** This is stated as a hard
"checklist" item in the authoring guide and repeated in the Claude Code docs
tip. Two bands:

- **≥500 lines: over budget.** Decompose before treating the skill as done.
- **400–499 lines: approaching budget.** Note it; propose a split if the skill
  is likely to keep growing (e.g. still under active iteration), otherwise
  advisory-only.

The 500-line figure is a **ceiling**, not a target — most real skills sit far
below it. The Feb 2026 corpus study (40,000+ public skills) found a median
body of ~1,414 tokens, roughly 100–200 lines depending on prose density. A
skill in the 300+ line range is already unusually large relative to the
corpus, even if still under the hard ceiling; that's a signal to look for a
natural domain split, not just wait for 500.

**Why this budget exists at all** (not just "shorter is better"): only
`name`+`description` preload for every skill at session start (~30–50 tokens
each). The full body loads once, on trigger — and then **stays resident**.
Every line in it competes with conversation history and every other loaded
skill for the rest of the session. Reference files and scripts cost nothing
until actually read or executed. So the budget isn't about total content —
it's about how much must be paid for on every turn once triggered, versus how
much can wait behind a link.

## Progressive disclosure patterns

Three shapes, pick by content, not by default:

1. **High-level guide + references** — SKILL.md is a quick-start plus a list
   of links (`FORMS.md`, `REFERENCE.md`, `EXAMPLES.md`); each loads only if
   the task needs it. Best default for "one skill, escalating detail."
2. **Domain-specific organization** — split reference files by domain
   (`reference/finance.md`, `reference/sales.md`, ...) so an unrelated-domain
   question never pulls in irrelevant schema/detail. Best when a skill spans
   genuinely separate subject areas.
3. **Conditional details** — inline the common path, link out only the rare
   branch (e.g. "for tracked changes, see REDLINING.md"). Best when 90% of
   uses never touch the advanced case.

**Keep references one level deep from SKILL.md.** Every reference file should
be linked directly from SKILL.md. A file that itself links to a further file
the model needs to read creates a chain — and Claude may only partially
preview files reached that way (e.g. `head -100`), silently losing content
past the preview window. If a natural hierarchy tempts you into
SKILL.md → advanced.md → details.md, flatten it: link both advanced.md and
details.md directly from SKILL.md instead.

**Reference files over 100 lines need a table of contents** near the top, so
a partial read still reveals the full scope of what's available.

## Reference-file hygiene

- **Descriptive filenames.** `form_validation_rules.md`, not `doc2.md`.
  Directory-by-domain (`reference/finance.md`) beats directory-by-number
  (`docs/file1.md`).
- **Forward slashes only**, even for Windows-authored content —
  `scripts/helper.py`, not `scripts\helper.py`. Backslash paths are a
  cross-platform correctness bug, not a style nit.
- **Execute vs. read-as-reference must be unambiguous.** "Run
  `analyze_form.py` to extract fields" (execute — output only enters context)
  reads very differently from "see `analyze_form.py` for the algorithm" (read
  — full source enters context). A script mentioned without either verb is a
  gap to close, not a style choice.
- **MCP tools use fully-qualified names** (`ServerName:tool_name`) — a bare
  tool name risks "tool not found" once more than one MCP server is active.

## Naming conventions

- **Gerund form preferred**: `processing-pdfs`, `analyzing-spreadsheets`.
  Noun-phrase (`pdf-processing`) or imperative (`process-pdfs`) are acceptable
  alternatives — flag as **advisory**, not a defect, when a skill's name is
  clear but not gerund-form; most real-world skill libraries (including this
  repo's) mix conventions.
- **Avoid vague or overly generic names**: `helper`, `utils`, `tools`,
  `documents`, `data`, `files` communicate nothing at a glance and don't
  survive a 100+-skill library.
- **Reserved words are a hard-fail**, not advisory (see Hard limits above).

## Content hygiene

- **Concise by default.** The context window is shared with everything else
  Claude needs. Challenge each paragraph: does Claude already know this?
  Explaining what a PDF is, or how pip install works, costs tokens for zero
  marginal information. This is a judgment call the script cannot make —
  flag noticeably verbose explanatory prose for a human/model read, don't try
  to regex it.
- **Consistent terminology.** Pick one term ("API endpoint", not a mix of
  "endpoint"/"URL"/"route") and hold it throughout — inconsistency measurably
  hurts Claude's ability to parse and follow instructions. Not automatable;
  spot-check during the judgment pass.
- **Avoid time-sensitive information.** Don't write "before/after DATE, use
  X". If a skill genuinely needs to carry deprecated guidance, wrap it in a
  collapsed `<details><summary>Legacy vX (deprecated YYYY-MM)</summary>`
  block rather than branching logic in the main flow. The scan flags
  date-adjacent phrasing as a **candidate** — confirm it's actually a
  deprecation branch before proposing a fix; plenty of legitimate prose
  mentions dates without being date-conditional instructions.
- **Provide a default, not a menu.** "Use pdfplumber... for scanned PDFs, use
  pdf2image instead" beats "you can use pypdf, or pdfplumber, or PyMuPDF,
  or...". Too many equally-weighted options is a comprehension cost, not
  thoroughness.
- **Match degrees of freedom to task fragility.** High-freedom prose
  ("analyze structure, check for bugs...") for open-ended judgment;
  low-freedom exact scripts ("run exactly this command, do not add flags")
  for fragile, must-not-vary operations. When judging a decomposition, a
  low-freedom fragile sequence is a good candidate to move into
  `scripts/` rather than stay as prose — script text is cheaper to keep
  exactly right than repeated natural-language instructions.

## Claude-Code-specific stakes

Two mechanics specific to running skills inside Claude Code (not the
one-shot API) make the size budget more than a token-cost nicety:

1. **Loaded content stays resident across turns.** Unlike a single API call
   where a large system prompt is paid once, a triggered skill's body stays
   in the live conversation and is repaid on every subsequent turn until the
   conversation ends or compacts.
2. **Post-compaction re-attachment is capped.** When the conversation
   summarizes, Claude Code re-attaches the most recent invocation of each
   skill — but keeps only the **first 5,000 tokens** of each, under a
   **shared 25,000-token budget** across all recently-invoked skills. An
   over-budget skill isn't merely expensive: after compaction its
   instructions can be silently truncated mid-procedure, with no error
   surfaced. This is a concrete correctness argument for the 500-line
   ceiling in Claude Code specifically, beyond the general context-window
   argument that applies everywhere Skills run.

## What this rubric deliberately doesn't automate

Some authoring-guide advice resists a script check entirely and stays a
judgment call every time:

- Whether a paragraph "justifies its token cost" (verbosity).
- Whether a naming choice, while not gerund-form, is still clear.
- Whether decomposition boundaries produce *coherent* reference files (a
  mechanical 500-line split down the middle would satisfy the budget while
  producing an incoherent file — never do that).
- Whether an eval/test suite exists at all (the authoring guide recommends
  building evaluations *before* extensive documentation — this rubric checks
  structure, not whether that process was followed).

Report these as advisory notes for a human/model read, not machine-verified
findings.
