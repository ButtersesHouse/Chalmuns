# skill-right-sizing

Two skills that work together to spend expensive models only where they earn it:

1. **`right-size-skills`** — audits your skills and **proposes** the cheapest
   `model:`/`effort:` each one needs (plus decomposition/handoff opportunities),
   as a plan-ready report. Advisory; never edits skills.
2. **`skill-compare`** — **A/B tests** a change (especially a `model:`/`effort:`
   edit) on tasks you provide, runs each version on its own declared model,
   grades blind, and returns a **cost-adjusted verdict** (adopt the cheaper
   version if quality holds within noise). Appends every run to a rolling ledger
   so savings trend over time.

The pipeline: **propose → prove → apply.**

## Usage

```
/right-size-skills                         # audit your skills, get a report
/right-size-skills --plugins               # advisory pass over installed plugin skills

/skill-compare --skill <path> --patch model=haiku   # A/B the proposed change
/skill-compare --trend                     # trend over accumulated comparisons
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

## Contents

```
skills/
├── right-size-skills/   SKILL.md + references/{rubric,report-template}
└── skill-compare/       SKILL.md + references/{comparator,verdict-rules} + scripts/{verdict.py,trend.py}
docs/validation-report.md
```
