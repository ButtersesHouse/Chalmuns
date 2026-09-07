# Step 6: Extract signals via subagents

## Contents

- Preprocessing and the lean view schema
- Extraction subagent prompt

## Preprocessing and the lean view schema

For each batch of up to 20 PR numbers:
1. Read each `.claude/pattern-learner/raw-cache/pr-N.json`.
2. **Preprocess the raw data into a lean view** by running the `extract-lean` subcommand. This deterministically parses `code_before` from each review comment's `diff_hunk`, computes `is_pr_author`, and drops all noise fields:

   ```
   $BIN extract-lean --cache-dir .claude/pattern-learner/raw-cache --prs <comma-separated batch PR numbers>
   ```

   The output is a JSON array of lean PR views ready to insert into the subagent prompt. The lean view schema (for reference — do not reconstruct it by hand):
   ```json
   {
     "pr_number": 42,
     "files_touched": ["internal/api/handlers/users.go"],
     "comments": [
       {
         "id": 12345,
         "user": "bob",
         "is_pr_author": false,
         "created_at": "2024-01-15T...",
         "in_reply_to_id": null,
         "type": "review_comment",
         "path": "internal/api/handlers/users.go",
         "body": "Could we use context.WithTimeout here?",
         "code_before": "ctx := context.Background()"
       }
     ]
   }
   ```

3. Launch a subagent with this exact prompt (fill in the `extract-lean` output at the end):

## Extraction subagent prompt

Paste this verbatim — do not paraphrase it or rebuild the schema from memory.

---
**SUBAGENT PROMPT:**

You are analyzing PR review comments to discover how this team prefers to write code.

Your output becomes the coding rules that AI assistants follow in this repository. Extract the team's stated preferences, naming conventions, architectural choices, and coding standards as expressed in code review.

Below is raw JSON for a batch of merged pull requests including reviews, comments, and reply threads.

---

**Default assumption**: in code review, reviewers rarely state preferences as blunt directives. Most conventions are communicated through polite questions, hedged suggestions, or skeptical critique. **Questions are usually directives.** A reviewer wouldn't ask if they didn't have an opinion. Take indirect feedback at face value as a real preference signal.

---

**PRIORITY 0 — GitHub code suggestions (strongest possible signal)**

Reviewer comments containing ` ```suggestion ` blocks. The reviewer literally wrote the code they want — this IS the convention. The diff between the original line(s) and the suggested replacement shows the pattern. Extract as `"strength": "explicit"`.

**PRIORITY 1 — Stated preferences (explicit OR indirect; capture from a single occurrence)**

Treat all of the following as explicit preference signals (`"strength": "explicit"`):

- **Direct preferences**: "we prefer / we like / we always / we never / our convention is / always use / never use / going forward / in this codebase / our pattern is"
- **Polite directives framed as questions**: "could we use X?", "what about X?", "have you considered X?", "why not X?", "should this be X?", "any reason not to X?"
- **Skeptical or passive-aggressive critique**: "is there a reason for this approach?", "I usually see this done with Y", "I would have done X", "hmm, this is unusual", "interesting choice", "this works but...", "this is fine I guess but..."
- **Hedged preferences**: "maybe consider X", "I think we usually X", "we tend to X here", "it might be cleaner to X", "wouldn't it be better to X?"

**Author acknowledgment** strengthens a signal. Walk the comment threads (`in_reply_to_id` linkage). When the PR author responds to a reviewer's comment with confirmation language — "good catch", "you're right", "ok fixed", "updated", "thanks", "sgtm", "done", "addressed" — the reviewer's feedback was a real ask. Treat such confirmed comments as the highest-quality explicit signals.

**PRIORITY 2 — Recurring corrections without explicit framing** (`"strength": "implicit"`)

Reviewers fixing the same thing across multiple comments without stated preference.

---

**DO NOT extract:**

- **Bug fixes for specific code**: "this returns null when X is empty", "missing nil check on line 42", "wrong assertion in this test", "this throws when input has trailing spaces". The comment points to a concrete defect in *this* PR, not a general principle.
- **Product or feature correctness**: "this doesn't handle X user scenario", "the API returns the wrong response for Y", "this breaks in production when Z". These describe what the code should *do*, not how it should be *written*.
- **Genuine clarification questions** (information-seeking): "what does this variable represent?", "is this still used anywhere?", "what's the use case for this case?", "is this intentional?". Distinguish from **rhetorical questions** (preference signals): "why are we doing it this way?", "why not just X?", "do we really need this?".
- **Author asking reviewer**: when the PR author is asking the reviewer a question, not the other way around. Look at who is the author of each comment.
- **Mechanical style enforced by tooling**: whitespace, semicolons, brace placement when a linter or formatter already handles it.
- **Bot comments**: skip any comment where `user.login` ends in `[bot]`, or matches one of: `dependabot`, `renovate`, `coderabbitai`, `copilot`, `github-actions`, `sonarqube`, `codecov`.

---

**Output schema** — a JSON array, each element:

```json
{
  "title": "Short rule title (5-8 words)",
  "rule": "The convention as a clear imperative instruction",
  "strength": "explicit",
  "do_examples": [
    {"code": "...", "language": "go", "context": "optional: surrounding function (±5 lines from diff)"}
  ],
  "dont_examples": [
    {"code": "...", "language": "go", "context": "optional: surrounding function (±5 lines from diff)"}
  ],
  "suggested_target": {
    "location": "CLAUDE.md (the universal-rule sentinel) or domain-name (e.g. api, auth, models)",
    "file_glob": ["src/api/**/*.go"]
  },
  "raw_signal": {
    "pr_number": 42,
    "comment_id": 12345,
    "reviewer": "username",
    "date": "2024-01-15",
    "snippet": "EXACT VERBATIM QUOTE"
  }
}
```

`strength`: `"explicit"` for any Priority 0 or Priority 1 signal (direct, indirect, polite, skeptical, hedged, or code-suggestion); `"implicit"` only for Priority 2 recurring corrections without stated framing.

`snippet`: the reviewer's exact verbatim words. Do not paraphrase. If you cannot quote the reviewer directly supporting the rule, omit the signal entirely. For code-suggestion signals, the snippet may be the suggested code block itself.

**Rule text provenance (required)**: `title` and `rule` MUST be synthesized from the reviewer's actual words, not from a pre-authored category or taxonomy. If the reviewer wrote "we prefer returning errors as values here, not panicking", the rule should say "Return errors as values; do not panic for expected error cases" — not a generic error-handling rule invented by the subagent. A reviewer must be able to read the rule and recognize their own feedback. Rules that are disconnected from reviewer language are fabricated — discard them and omit the signal.

`do_examples` / `dont_examples`: arrays — include all examples available. Use this priority order for sourcing them:

1. **Priority 0 suggestion blocks**: `dont_examples[0].code` = original lines replaced; `do_examples[0].code` = suggested replacement. Populate `context` with ±5 lines of surrounding diff context.
2. **`code_before` field** (present on `review_comment` type): use as `dont_examples[0].code` when no suggestion block is present. This is verbatim code from the diff the reviewer was looking at — the highest-fidelity dont example available. Use the comment's body, inline code, or your inference to construct the corresponding `do_example`.
3. **Inline code in comment body**: backtick-quoted code in the comment text.
4. **Inference from reviewer wording**: only when none of the above are available.

A single comment may produce multiple examples; return them all. Omit rather than invent when no example source is available. Use the file paths the PR touched to infer the language.

`suggested_target`: use the file paths the PR touches as a hint. Pick the **most specific** domain the PR's file paths suggest — e.g. `"migrations"` not `"database"`, `"mutations"` not `"backend"`, `"workers"` not `"jobs"`. Never use coarse buckets like `"backend"`, `"frontend"`, or `"general"` as a location — always go one level more specific (e.g. `"api"`, `"auth"`, `"models"`, `"migrations"`, `"workers"`, `"components"`, `"hooks"`). Reserve `"CLAUDE.md"` **only** for rules that apply universally regardless of which file is being edited: naming conventions, commit message format, anti-patterns true everywhere in the codebase. When in doubt, prefer a specific domain over `"CLAUDE.md"`. Note that `"CLAUDE.md"` is a **sentinel value, not a file path** — nothing is ever written to a file by that name. Rules carrying it are generated into `.claude/skills/conventions/SKILL.md`, an ungated skill that auto-loads on every file.

Output only the JSON array, no other text.

PR data (lean preprocessed view — comments + metadata + file paths; review comments include `code_before` extracted from diff_hunk):
[INSERT LEAN PR DATA HERE]

---

Collect all signals returned by all subagent runs.

---
