# skill-right-sizing

Three skills that work together to spend expensive models only where they earn
it, and keep the skill files themselves within Anthropic's Skill-authoring
format/size guidance:

1. **`right-size-skills`** — audits your skills and **proposes** the cheapest
   `model:`/`effort:` each one needs (plus decomposition/handoff opportunities),
   as a plan-ready report. Advisory; never edits skills.
2. **`skill-compare`** — **A/B tests** a change (especially a `model:`/`effort:`
   edit) on tasks you provide, runs each version on its own declared model,
   grades blind, and returns a **cost-adjusted verdict** (adopt the cheaper
   version if quality holds within noise). Appends every run to a rolling ledger
   so savings trend over time.
3. **`right-format-skills`** — audits your skills' `SKILL.md` files against
   Anthropic's published format/size guidance (500-line body budget,
   frontmatter validity, progressive-disclosure structure, reference-file
   hygiene), proposes a decomposition for anything oversized, and — on an
   explicit `--apply` — mechanically extracts sections into reference files.

The model-cost pipeline: **propose → prove → apply** (`right-size-skills` →
`skill-compare` → your edit). The format pipeline is `right-format-skills`
alone: **propose → apply**, since format fixes are mechanical/reversible and
don't need an A/B quality check.

## Usage

```
/right-size-skills                         # audit your skills, get a report
/right-size-skills --plugins               # advisory pass over installed plugin skills

/skill-compare --skill <path> --patch model=haiku   # A/B the proposed change
/skill-compare --trend                     # trend over accumulated comparisons

/right-format-skills                                    # audit skill format/size, get a report
/right-format-skills --plugins                          # advisory pass over installed plugin skills
/right-format-skills --apply ~/.claude/plans/right-format-report-<date>.md   # execute the plan
```

## Design notes

- `right-size-skills`'s classification rubric was developed empirically (55
  skills/agents, ground-truth validation against author-pinned models, and a
  live apply-and-measure run). See `docs/validation-report.md`.
- `skill-compare` compares **price-weighted** cost, not raw tokens — two versions
  on different models can burn equal token counts yet cost very differently
  (validated: a live haiku-vs-sonnet run had raw tokens Δ ≈ +412 yet haiku was
  3.3× cheaper). Its blind comparator is **vendored**, so the core has no runtime
  dependency; it *optionally* uses the `skill-creator` plugin's grader/analyzer/
  viewer if installed, and degrades gracefully if not.
- The rolling ledger lives at `~/.claude/skill-compare/ledger.jsonl` (user-home,
  so it survives reinstalls).
- `right-format-skills`'s rubric is grounded directly in Anthropic's published
  Skill-authoring docs (500-line body budget, frontmatter validation rules,
  progressive-disclosure patterns, one-level-deep reference rule) plus
  Claude-Code-specific runtime behavior (skill content stays resident across
  turns; post-compaction re-attachment keeps only the first 5,000 tokens per
  skill under a shared 25,000-token budget). See `docs/format-research.md`.
  Mechanical checks (line counts, frontmatter validity, reference nesting/TOC)
  run via a deterministic script; decomposition judgment (how to split
  coherently) stays on the model. `scripts/decompose.py` is the actual
  reformat/decompose mechanism — one section-extraction per invocation, so
  changes stay small and reviewable.

## Contents

```
skills/
├── right-size-skills/    SKILL.md + references/{rubric,report-template}
├── skill-compare/        SKILL.md + references/{comparator,verdict-rules} + scripts/{verdict.py,trend.py}
└── right-format-skills/  SKILL.md + references/{rubric,report-template} + scripts/{audit_format.py,decompose.py}
docs/validation-report.md
docs/format-research.md
```
