# Verdict Rules — the cost-adjusted decision

`scripts/verdict.py` implements these; this file is the human-readable spec and
the rationale for the thresholds. The goal is a **right-sizing** decision: adopt
the cheaper version when quality holds, keep the pricier one only when it buys
real quality.

## Inputs the verdict uses

Per version, aggregated across all `eval × run`:
- **quality**: pass_rate (from oracle/assertions) — mean, stddev(n−1); and the
  **comparator win-rate** (fraction of evals where the blind comparator picked
  that version; TIEs split 0.5/0.5).
- **cost**: **price-weighted** `cost_index` = mean_tokens × price_weight(model),
  where weights are ≈ blended $/token relative to haiku=1 (haiku 1, sonnet 3.3,
  opus 15; override via `meta.json` `versions[l].price_weight`). This matters:
  two versions on different models can burn *equal token counts* yet cost very
  differently — comparing raw tokens across models is wrong. (Validated live:
  a haiku-vs-sonnet run had raw tokens_delta ≈ +412 yet haiku was 3.3× cheaper.)
- **latency**: duration_seconds — mean.

Designate the **cheaper** version = lower `cost_index` (tie-break on latency).

## The decision rule

1. **Quality parity test.** The two versions are "at parity" when BOTH hold:
   - `|Δpass_rate_mean| ≤ noise_band`, where
     `noise_band = max(combined_stddev, MIN_BAND)`,
     `combined_stddev = sqrt(sdA² + sdB²)`, `MIN_BAND = 0.05` (5 points); and
   - the comparator win-rate for the pricier version is **not decisive**
     (`win_rate_pricier < DECISIVE`, `DECISIVE = 0.70`).
2. **Verdict:**
   - Parity → **ADOPT CHEAPER** (right-size). Report the cost/latency savings.
   - Not parity, pricier wins quality → **KEEP PRICIER** (the quality is real).
     Report the price of that quality (Δtokens, Δseconds per unit of pass_rate).
   - Not parity, cheaper *also* wins quality → **ADOPT CHEAPER** (dominates).
3. **Power guard (checked first).** If any metric's `stddev ≥ |delta|` OR
   `runs_per_version < 3` OR total evals `< 2`, emit
   **INCONCLUSIVE — underpowered**: state the observed direction but recommend
   raising `--reps` / adding evals rather than declaring a winner. Never call a
   close verdict on noisy data.

## Why these thresholds

- **MIN_BAND = 5 points**: below this, pass_rate differences on a handful of
  evals are almost always sampling noise, not signal.
- **DECISIVE = 0.70 win-rate**: the comparator is a subjective judge; a bare
  majority (e.g. 3/5) is within its own noise. Requiring a clear supermajority
  before it can *block* a down-tier keeps cost savings from being vetoed by a
  coin-flip.
- **Power guard**: with 3 reps, stddev is barely estimable; the guard makes the
  tool honest about that instead of over-claiming (a lesson from the manual
  session-report run, where the session's growing token totals showed how easily
  a single-run "measurement" misleads).

## Output shape (`verdict.json`)

```json
{
  "cheaper": "versionB",
  "pricier": "versionA",
  "recommendation": "ADOPT_CHEAPER | KEEP_PRICIER | INCONCLUSIVE",
  "confidence": "high | medium | low",
  "quality": {"versionA": {"pass_rate_mean": 0.9, "pass_rate_std": 0.0, "win_rate": 0.4},
              "versionB": {"pass_rate_mean": 0.9, "pass_rate_std": 0.0, "win_rate": 0.6}},
  "cost":    {"versionA": {"tokens_mean": 120000}, "versionB": {"tokens_mean": 12000},
              "tokens_delta": -108000, "tokens_ratio": 0.1},
  "latency": {"versionA": {"seconds_mean": 170}, "versionB": {"seconds_mean": 116},
              "seconds_delta": -54},
  "power": {"reps_per_version": 3, "evals": 2, "underpowered": false,
            "reasons": []},
  "rationale": "one-paragraph plain-English explanation"
}
```
