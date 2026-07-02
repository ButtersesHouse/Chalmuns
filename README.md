# Chalmuns

A Claude Code **marketplace** of ButtersesHouse plugins.

## Add it

```
/plugin marketplace add ButtersesHouse/Chalmuns
```
Then browse with `/plugin`, or install directly:
```
/plugin install pattern-learner@chalmuns
/plugin install skill-right-sizing@chalmuns
/plugin install skill-right-sizing-beta@chalmuns   # bleeding edge, installed disabled
```

## Plugins

### `pattern-learner`
Extracts recurring coding conventions from PR review history and writes approved
rules to CLAUDE.md and skill files. Go-based (builds a `pattern-learner` binary
into the target repo; requires Go 1.21+). Source in `plugins/pattern-learner/`.

### `skill-right-sizing`
- **`right-size-skills`** — proposes the cheapest `model:`/`effort:` each skill
  needs (plan-ready report; advisory).
- **`skill-compare`** — A/B tests a skill change on your tasks, runs each version
  on its own model, grades blind, returns a **cost-adjusted verdict**; logs every
  run to a rolling ledger for trends over time. `--channels` A/B-tests the beta
  entry against stable.

A `skill-right-sizing-beta` channel (git-subdir @ the `beta` branch,
`defaultEnabled: false`) lets you run the newest build at your own risk.

## Layout

```
.claude-plugin/marketplace.json     ← marketplace manifest
plugins/
├── pattern-learner/                ← Go plugin (cmd/ internal/ hooks/ skills/ go.mod)
└── skill-right-sizing/             ← skills plugin (right-size-skills + skill-compare)
release.sh                          ← publishes skill-right-sizing (stable + --beta lanes)
```

## Releasing skill-right-sizing

`./release.sh <major|minor|patch|X.Y.Z> "msg" [--dry-run]` (stable) or
`./release.sh --beta "msg"` (beta branch). It mirrors the dev-copy skills from
`~/.claude/skills/` into `plugins/skill-right-sizing/`, bumps the version, and
pushes. `pattern-learner` is maintained separately (it's a Go project).
