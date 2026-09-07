# Discover Mode

## Contents

- Discover Step D1: Determine target domains
- Discover Step D2: Run cursor-agent discovery per domain
- Discover Step D3: Parse and normalize candidates
- Discover Step D4: Deduplicate against existing state
- Discover Step D5: Apply threshold

Invoked when `--discover` is set. Runs after Step 4 (state read) and replaces Steps 5–9 with codebase analysis via cursor-agent. Steps 10–13 (approval, state write, output generation, summary) run as normal.

---

## Discover Step D1: Determine target domains

If domain names were passed after `--discover` (e.g. `--discover api auth`), use those.

Otherwise, collect target domains from the current state:
- All domains that have at least one approved rule (from Step 4 state)
- If the state is empty (no approved rules yet), ask the user: "No domains found in state. Which domains would you like to discover? (e.g. `api auth models`)"

For each domain, gather its file globs from the approved rules in state. If a domain has no rules yet (user-specified domain not in state), ask: "What file patterns should I search for the `{domain}` domain? (e.g. `internal/api/**/*.go`)"

---

## Discover Step D2: Run cursor-agent discovery per domain

For each target domain, run the following cursor-agent query. Use the domain's file globs to focus the search.

```
cursor-agent -p --mode=ask "<DISCOVERY PROMPT>"
```

**Discovery prompt** (fill in `{domain}` and `{globs}`):

---
Analyze this codebase and identify coding conventions the team consistently follows in files matching: {globs}

This is for the `{domain}` domain. Your job is to find conventions a new developer or AI coding assistant would need to know — the things that make code in this domain "fit in" with existing code.

Focus on:
- Naming conventions (functions, types, variables, files) specific to this domain
- Error handling and propagation patterns
- Structural/architectural patterns (how things are organized, what calls what)
- Data flow conventions (how data is passed, transformed, returned)
- What NOT to do — patterns that would look wrong to experienced contributors

Do NOT extract:
- General language best practices (these are already known)
- Conventions enforced by linters or formatters
- Project-wide rules that apply everywhere (not domain-specific)

For each convention you find, return a JSON object with these exact fields:
- `title`: short rule title, 5-8 words
- `rule`: imperative instruction, one sentence
- `do_examples`: array of real code from the codebase — take actual snippets verbatim, with file path and line number. At least one example required; up to 3.
  Each: `{"code": "...", "language": "...", "file_ref": "path/file.go:L42", "context": "optional surrounding function"}`
- `dont_examples`: array of what NOT to do (can be constructed from the "before" side of common mistakes you see, or clearly wrong alternatives). Each: `{"code": "...", "language": "..."}`
- `suggested_target`: `{"location": "{domain}", "file_glob": ["{globs}"]}`
- `confidence`: `"high"` (pattern in 5+ places), `"medium"` (2-4 places), `"low"` (1 place but clearly intentional)

Return a JSON array of 5–15 conventions. Real examples only — copy actual code from the files verbatim. Output only the JSON array, no other text.
---

Capture cursor-agent's output. Extract the JSON array from the response (cursor-agent may wrap it in prose — find the `[` ... `]` block).

---

## Discover Step D3: Parse and normalize candidates

Read each extracted candidate and construct a candidate rule:
- `title`: from cursor-agent output
- `rule`: from cursor-agent output
- `do_examples`: from cursor-agent output (already include FileRef and Context)
- `dont_examples`: from cursor-agent output
- `target`: `{location: domain, file_glob: globs}`
- `confidence`: map cursor-agent confidence → rule confidence: `"high"` → `"established"`, `"medium"` → `"emerging"`, `"low"` → `"emerging"` (a single observed instance, not a stated preference — treat with the same human-review level as implicit emerging rules)
- `sources`: one synthetic Signal — `{"reviewer": "cursor-agent", "date": "<today>",
  "snippet": "<the rule statement verbatim>", "strength": "explicit", "pr_number": 0}`.
  The rule schema has no top-level strength field — explicitness lives on the source
  signal (same pattern as Add mode). An empty `sources` list would make any later
  re-classification treat the rule as implicit and `--auto` triage defer it as a
  singleton, losing the "authoritative" status this mode intends.
- `signal_count`: 1
- `status`: `"proposed"`

---

## Discover Step D4: Deduplicate against existing state

Run the same dedup logic as Step 8 (C and D):
- **Equivalent to existing approved rule**: merge examples (cap at 4 per array), note the discovery confirmed the rule. Do not create a new rule.
- **Contradicts existing rule**: create supersession candidate with `supersedes: ["<id>"]`.
- **Semantically distinct and not in rejected_signals**: add as new proposed candidate.
- **Equivalent to rejected_signals entry**: discard silently.

Skip Step 8A (intra-batch dedup) — run it across all domains' candidates combined before the above.

---

## Discover Step D5: Apply threshold

All discover candidates carry an explicit source signal (D3) so they are kept unconditionally regardless of count. Confidence was already set in D3 from cursor-agent's rating.

Then continue with **Step 10** (approval UI), **Step 11** (state write), **Step 12** (generate outputs), **Step 13** (summary).

In Step 13 summary, replace PR-related counters with:
```
Domains analyzed:           <N>
Candidates found:           <N>
  high confidence:          <N>
  medium confidence:        <N>
  low confidence:           <N>
Candidates merged into existing rules: <N>
New candidates proposed:    <N>
```

---
