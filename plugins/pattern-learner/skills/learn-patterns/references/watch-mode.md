# Watch Mode

Invoked when `--watch` is set. This records **which code-review skills and tools
pattern-learner keeps eyes on**. It runs after Step 4 (state read) and replaces
every later step: designating a reviewer writes no rules and generates no
skills, so Steps 5–13 do not apply. Release the run-lock when done.

A designation is what makes capture happen at all. The plugin ships a
PostToolUse hook that runs `capture-review --hook` after every Bash, Skill,
SlashCommand and ReportFindings call; with no watchers designated it reads the
payload, matches nothing, and exits. Once a reviewer is designated, that
reviewer's output is written into `.claude/pattern-learner/review-cache/` as it
happens, and `--learn-reviews` mines it. Nothing is captured from a tool nobody
asked for, and nothing is ever captured from a tool that is not designated.

ReportFindings is in that list because Claude Code's own `/code-review` skill
does not return its findings as the skill call's result — it reports them by
calling ReportFindings, whose arguments carry the findings array. Its Skill
call is filtered out so one review is not recorded twice.

A reviewer that runs as a **subagent** (a Task call) is not captured by the
hook: a subagent's transcript is not a tool response the hook can read. Capture
that one by hand — pipe its write-up into `capture-review --source <name>` — or
have it write findings to a file the parent reads.

The hook also records nothing when a designated tool's report parses cleanly
and holds no findings: a linter on a green tree has said nothing, and the hook
fires on every run. `capture-review --file` still records whatever it is
handed. A prose review is always recorded — its content is its text, so having
no parsed findings says nothing about whether it has anything to say.

---

## Watch Step W1: Determine the action

Parse what follows `--watch`:

- `add <name>` (or just a bare name) → designate a reviewer.
- `list` (or nothing at all) → show the current designations.
- `remove <id>` → undesignate.

If the user's intent is a designation but no name is recoverable ("watch my
code review tool"), ask which skill or tool they mean, offering `code-review`
as the likely answer when the session has that skill.

---

## Watch Step W2: Run the subcommand

```
$BIN watch --state .claude/pattern-learner/state.json --list
$BIN watch --state .claude/pattern-learner/state.json --add <name> [--kind skill|tool|any] [--format auto|findings|sarif|eslint|semgrep|markdown]
$BIN watch --state .claude/pattern-learner/state.json --remove <id>
```

Every form prints the resulting designations as JSON, each with a live
`captures` tally read from the review cache.

Choosing the flags:

- `--kind` — `skill` for a Claude Code skill (`/code-review`, `/security-review`),
  `tool` for a command (`semgrep`, `eslint`, `golangci-lint`), `any` when the
  user is not distinguishing. **Default `any`, and prefer it unless the user
  has a reason to narrow**: a reviewer can be reached as a skill call, a slash
  command, or a wrapper script, and `any` matches all three. `skill` and `tool`
  exist for the case where a tool and a skill share a name and capturing the
  wrong one would misattribute the review.
  A `tool` watcher matches only when the tool is the command actually being
  run: `semgrep --json .` matches, `grep semgrep notes.txt` and
  `git commit -m 'fix; semgrep noise'` do not — a name inside an argument or a
  quoted string is data, not an invocation.
- `--format` — leave at the default `auto` unless the user knows the tool emits
  a shape they want forced. `auto` sniffs each artifact, so a tool that emits
  JSON on one run and prose on the next is handled per capture.

`--add` refuses a name already designated rather than replacing it, so changing
a watcher's kind or format is `--remove` then `--add`. Removing a designation
never deletes captured artifacts, and never touches rules already mined from
them — their provenance stands.

---

## Watch Step W3: Report

```
── Watched Reviewers ────────────────────────────────
<name>  (<kind>, format <format>)  — <N> captured, last <timestamp | "none yet">
[...]
─────────────────────────────────────────────────────
Captured reviews are mined by: /learn-patterns --learn-reviews
```

When a designation was just added and nothing has been captured yet, say so
plainly and tell the user what triggers the first capture: running the
designated reviewer. If they have a review output on hand already — a saved
report, a file of findings — mention they can feed it directly:

```
$BIN capture-review --cache-dir .claude/pattern-learner/review-cache \
  --source <name> [--format F] [--label "what was reviewed"] --file <path>
```

Then release the run-lock and stop.

---
