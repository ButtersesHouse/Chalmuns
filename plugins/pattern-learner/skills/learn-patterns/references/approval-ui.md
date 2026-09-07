# Step 10: Approval UI

**If `IS_AUTO` is true**, skip the interactive loop entirely and use the `triage` subcommand instead:

1. Extract all `status: "proposed"` rules from the state into `proposed.json`.
2. Run:
   ```
   cat proposed.json | $BIN triage --mode auto [--auto-threshold]
   ```
   The tool applies the ordered predicate and returns the same rules with `status` patched to `"approved"` or left as `"proposed"` (deferred). Defer conditions (first match wins): `supersedes` non-empty → defer; `conflicted: true` → defer; `signal_count == 1` AND all sources implicit AND NOT `--auto-threshold` → defer. Otherwise → approve.
3. Replace the proposed rules in the state with the triage output.

Skip the confirmation prompt. Proceed automatically to Step 11. Print a single summary line:
```
Auto-approved: <N> rules  |  Deferred for review: <N> (run /learn-patterns --review to decide)
```

Then continue directly to Step 11.

---

**At scale (50+ proposed rules)**: rules are already grouped by domain — lean into that structure. It is acceptable to approve by confidence tier within a domain ("approve all `established` rules in `api`") rather than reviewing every rule individually. Use `--auto` for large initial runs with no supersessions; reserve the interactive loop for supersession rules and `[CONFLICT]` tags. For first-time runs on large repos, `--auto` + `--refresh` is the recommended path.

**If `IS_AUTO` is false**, present each rule for user decision. Show new candidates first (status: "proposed"), then any existing proposed rules from prior runs.

Group by target: `CLAUDE.md` rules first, then alphabetically by domain.

**Emerging rule unchanged filter** (run before the display loop):

```
cat proposed.json | $BIN triage --mode review-filter [--all]
```

The tool compares each emerging rule's current `signal_count` and sorted source PR numbers against its `reviewed_snapshot` (set by a previous `s` action). Rules where both match are "unchanged" and suppressed — the user already saw them and nothing new has arrived. Pass `--all` when `IS_SHOW_ALL` is true.

Output: `{"show": [...rules to display...], "suppressed": N, "suppressed_ids": [...]}`.

If `suppressed > 0`, print before the loop:
```
Skipping <N> unchanged emerging rule(s) (previously reviewed, no new signals).
Run /learn-patterns --review --all to force-show all.
```

Display only the rules in `show`.

For each rule, display:

```
────────────────────────────────────────────────────
Rule: <title>
Target: <universal (conventions skill) | domain>
Confidence: <stated (explicit preference) | established | emerging> (<N> signals across <M> PRs)
[Supersedes: "<superseded rule title>"                              ← only when supersedes is non-empty
   ↳ This convention: PRs #<min_new>–<max_new> (<N_new> signals)
   ↳ Replaces:        PRs #<min_old>–<max_old> (<N_old> signals)]
[Prior convention (same run, older PRs #<min>–<max>): "<title>"   ← only when prior_convention note from Step 8A-cross is present]
[CONFLICT: contradicts another candidate from overlapping PR range — review both] ← only when rule.conflicted == true

Convention:
  <rule text>

✓ Do:
  <language>
  <do_example.code>

✗ Don't:
  <language>
  <dont_example.code>

Evidence:
  PR #<N> (@<reviewer>) [explicit]: "<snippet>"
  PR #<N> (@<reviewer>): "<snippet>"
  [... up to 3 examples shown; tag [explicit] when signal strength is "explicit"]

[a]pprove  [r]eject  [e]dit  [s]kip (defer)
────────────────────────────────────────────────────
```

For the supersession block: `min_new`/`max_new` = min/max PR numbers from the current rule's sources; `min_old`/`max_old` = min/max PR numbers from the superseded rule's sources (look it up in state). This makes the temporal relationship visible at decision time — the reviewer can see at a glance which convention is newer and by how many PRs.

Wait for user input per rule:
- `a` → set `status: "approved"`; clear `reviewed_snapshot`. If the rule has non-empty `supersedes`, also set the superseded rule's `status: "superseded"` and `superseded_by: "<this_rule_id>"` (the binary will fill `<this_rule_id>` at write time if not yet assigned — pass the rule's index for now).
- `r` → set `status: "rejected"`; clear `reviewed_snapshot`; this rule will move to `rejected_signals` in state
- `e` → prompt user to edit title, rule text, or examples inline; re-display updated rule for confirmation
- `s` → leave as `status: "proposed"` and save a review snapshot: `reviewed_snapshot = {signal_count: <current signal_count>, source_pr_numbers: <sorted list of PR numbers from sources>}`. On the next run the rule will be suppressed unless new signals arrive (i.e. signal_count increases or new PR numbers are added to sources).

After all rules are reviewed, display a summary of decisions:
```
You approved <N>, rejected <N>, edited <N>, skipped <N>. Proceed to write state? [y/n]
```
Wait for confirmation before continuing to Step 11. If `n`, exit without modifying state.

---
