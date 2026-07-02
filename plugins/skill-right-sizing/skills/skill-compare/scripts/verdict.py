#!/usr/bin/env python3
"""
verdict.py — cost-adjusted A/B verdict for two skill versions.

Owns the statistics + decision so skill-compare has no hard dependency on
skill-creator's aggregate_benchmark.py. Reads the run layout:

    <iteration_dir>/eval-*/<versionLabel>/run-*/grading.json   (summary.pass_rate)
                                          run-*/timing.json     (total_tokens, total_duration_seconds)
    <iteration_dir>/eval-*/comparison/*/comparison.json        (winner: A|B|TIE)
    <iteration_dir>/eval-*/comparison/*/slotmap.json           ({"A": "<label>", "B": "<label>"})

The two version labels are discovered as the eval-* subdirs that contain run-*/.
See references/verdict-rules.md for the rule and thresholds.

Usage:
    python verdict.py <iteration_dir> [--min-band 0.05] [--decisive 0.70]
                      [--reps-expected 3] [-o verdict.json]
"""
import argparse, datetime, json, math, sys, uuid
from pathlib import Path

DEFAULT_LEDGER = Path.home() / ".claude" / "skill-compare" / "ledger.jsonl"

# Approximate blended $/token relative to haiku=1 (Anthropic list pricing, order-of-
# magnitude). Cross-model cost MUST be price-weighted: two versions can burn equal
# token COUNTS yet cost very differently. Override via meta.json versions[l].price_weight.
PRICE_WEIGHT = {"haiku": 1.0, "sonnet": 3.3, "opus": 15.0, "fable": 3.3, None: 3.3}


def price_weight(model, override=None):
    if override is not None:
        return float(override)
    if not model:
        return PRICE_WEIGHT[None]
    m = model.lower()
    for k in ("haiku", "sonnet", "opus", "fable"):
        if k in m:
            return PRICE_WEIGHT[k]
    return PRICE_WEIGHT[None]


def load_json(p):
    try:
        return json.loads(Path(p).read_text())
    except Exception:
        return None


def stats(xs):
    xs = [x for x in xs if x is not None]
    if not xs:
        return {"mean": None, "stddev": None, "min": None, "max": None, "n": 0}
    n = len(xs)
    mean = sum(xs) / n
    var = sum((x - mean) ** 2 for x in xs) / (n - 1) if n > 1 else 0.0
    return {"mean": round(mean, 4), "stddev": round(math.sqrt(var), 4),
            "min": min(xs), "max": max(xs), "n": n}


def discover(iteration_dir):
    """Return {version_label: {"pass": [...], "tokens": [...], "seconds": [...], "reps": k}}
    and a list of comparison results [(winner, slotmap_dict)]."""
    versions = {}
    comparisons = []
    evals = 0
    for eval_dir in sorted(Path(iteration_dir).glob("eval-*")):
        if not eval_dir.is_dir():
            continue
        evals += 1
        for cfg in sorted(eval_dir.iterdir()):
            if not cfg.is_dir() or cfg.name == "comparison":
                continue
            runs = sorted(cfg.glob("run-*"))
            if not runs:
                continue
            v = versions.setdefault(cfg.name, {"pass": [], "tokens": [], "seconds": [], "reps": 0})
            for run in runs:
                v["reps"] += 1
                g = load_json(run / "grading.json") or {}
                pr = (g.get("summary") or {}).get("pass_rate")
                v["pass"].append(pr)
                t = load_json(run / "timing.json") or {}
                v["tokens"].append(t.get("total_tokens"))
                secs = t.get("total_duration_seconds")
                if secs is None:
                    secs = (g.get("timing") or {}).get("total_duration_seconds")
                v["seconds"].append(secs)
        comp_dir = eval_dir / "comparison"
        if comp_dir.is_dir():
            for cj in sorted(comp_dir.glob("**/comparison.json")):
                c = load_json(cj) or {}
                sm = load_json(cj.parent / "slotmap.json") or {}
                if c.get("winner"):
                    comparisons.append((c["winner"], sm))
    return versions, comparisons, evals


def win_rates(comparisons, labels):
    """Translate blind A/B winners to per-version win-rate (TIE splits 0.5)."""
    wins = {l: 0.0 for l in labels}
    total = 0
    for winner, slotmap in comparisons:
        a, b = slotmap.get("A"), slotmap.get("B")
        if a not in wins or b not in wins:
            continue  # can't de-blind without a valid slotmap
        total += 1
        if winner == "A":
            wins[a] += 1
        elif winner == "B":
            wins[b] += 1
        else:  # TIE
            wins[a] += 0.5
            wins[b] += 0.5
    return ({l: round(wins[l] / total, 4) for l in labels} if total else
            {l: None for l in labels}), total


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("iteration_dir")
    ap.add_argument("--min-band", type=float, default=0.05)
    ap.add_argument("--decisive", type=float, default=0.70)
    ap.add_argument("--reps-expected", type=int, default=3)
    ap.add_argument("-o", "--output", default=None)
    ap.add_argument("--ledger", default=str(DEFAULT_LEDGER),
                    help="append-only JSONL log of every comparison (default: ~/.claude/skill-compare/ledger.jsonl)")
    ap.add_argument("--no-ledger", action="store_true", help="do not append to the rolling ledger")
    ap.add_argument("--meta", default=None,
                    help="path to meta.json {skill, change, versions{label:{model,effort}}}; "
                         "defaults to <iteration_dir>/meta.json then its parent")
    args = ap.parse_args()

    versions, comparisons, evals = discover(args.iteration_dir)
    labels = sorted(versions.keys())
    if len(labels) != 2:
        print(f"ERROR: expected exactly 2 version dirs with run-*/, found {labels}", file=sys.stderr)
        sys.exit(2)

    q = {l: {"pass": stats(versions[l]["pass"]),
             "tokens": stats(versions[l]["tokens"]),
             "seconds": stats(versions[l]["seconds"]),
             "reps": versions[l]["reps"]} for l in labels}
    wr, n_comp = win_rates(comparisons, labels)

    # Price-weighted cost: equal token COUNTS across models are NOT equal cost.
    meta = load_meta(args)
    mv = meta.get("versions") or {}
    models = {l: (mv.get(l) or {}).get("model") for l in labels}
    weights = {l: price_weight(models[l], (mv.get(l) or {}).get("price_weight")) for l in labels}

    def cost_index(l):
        tk = q[l]["tokens"]["mean"]
        base = tk if tk is not None else (q[l]["seconds"]["mean"] or 0)
        return base * weights[l]
    cheaper, pricier = sorted(labels, key=cost_index)[0], sorted(labels, key=cost_index)[-1]
    if cheaper == pricier:  # identical / no cost data
        pricier = [l for l in labels if l != cheaper][0]

    pc, pp = q[cheaper]["pass"], q[pricier]["pass"]
    d_pass = (pc["mean"] - pp["mean"]) if (pc["mean"] is not None and pp["mean"] is not None) else None
    combined_sd = math.sqrt((pc["stddev"] or 0) ** 2 + (pp["stddev"] or 0) ** 2) if pc["mean"] is not None else None
    noise_band = max(combined_sd, args.min_band) if combined_sd is not None else args.min_band

    # power guard
    reasons = []
    min_reps = min(q[l]["reps"] for l in labels)
    if min_reps < args.reps_expected:
        reasons.append(f"only {min_reps} reps/version (<{args.reps_expected})")
    if evals < 2:
        reasons.append(f"only {evals} eval(s)")
    if d_pass is not None and combined_sd is not None and combined_sd >= abs(d_pass) and abs(d_pass) > 0:
        reasons.append(f"pass_rate stddev ({combined_sd:.3f}) >= |delta| ({abs(d_pass):.3f})")
    if d_pass is None:
        reasons.append("no pass_rate data (no oracle/assertions graded)")
    underpowered = bool(reasons)

    wr_pricier = wr.get(pricier)
    pricier_decisive = (wr_pricier is not None and wr_pricier >= args.decisive)

    if underpowered:
        rec, conf = "INCONCLUSIVE", "low"
    else:
        parity = (abs(d_pass) <= noise_band) and not pricier_decisive
        if parity:
            rec = "ADOPT_CHEAPER"
        elif d_pass >= 0:            # cheaper is also at least as good on pass_rate
            rec = "ADOPT_CHEAPER"
        else:
            rec = "KEEP_PRICIER"
        # confidence: high when clear power + clear signal or clear parity
        margin = abs(abs(d_pass) - noise_band)
        conf = "high" if (min_reps >= args.reps_expected and evals >= 2 and margin >= 0.05) else "medium"

    tk_c, tk_p = q[cheaper]["tokens"]["mean"], q[pricier]["tokens"]["mean"]
    ci_c, ci_p = cost_index(cheaper), cost_index(pricier)
    verdict = {
        "cheaper": cheaper, "pricier": pricier,
        "recommendation": rec, "confidence": conf,
        "quality": {l: {"pass_rate_mean": q[l]["pass"]["mean"],
                        "pass_rate_std": q[l]["pass"]["stddev"],
                        "win_rate": wr[l]} for l in labels},
        "cost": {l: {"tokens_mean": q[l]["tokens"]["mean"], "model": models[l],
                     "price_weight": weights[l], "cost_index": round(cost_index(l))} for l in labels}
                | {"cost_ratio": round(ci_c / ci_p, 4) if ci_p else None,
                   "cost_index_delta": round(ci_c - ci_p) if (tk_c is not None and tk_p is not None) else None,
                   "tokens_delta": round(tk_c - tk_p) if (tk_c is not None and tk_p is not None) else None},
        "latency": {l: {"seconds_mean": q[l]["seconds"]["mean"]} for l in labels}
                   | {"seconds_delta": (q[cheaper]["seconds"]["mean"] - q[pricier]["seconds"]["mean"])
                      if (q[cheaper]["seconds"]["mean"] is not None and q[pricier]["seconds"]["mean"] is not None) else None},
        "power": {"reps_per_version": min_reps, "evals": evals,
                  "comparisons": n_comp, "underpowered": underpowered, "reasons": reasons},
    }
    verdict["rationale"] = build_rationale(verdict, d_pass, noise_band, pricier_decisive)

    out = Path(args.output) if args.output else Path(args.iteration_dir) / "verdict.json"
    out.write_text(json.dumps(verdict, indent=2))
    (out.with_suffix(".md")).write_text(render_md(verdict))

    if not args.no_ledger:
        rec = build_ledger_record(verdict, args)
        append_ledger(rec, Path(args.ledger))
        verdict["ledger_appended"] = str(args.ledger)

    print(json.dumps(verdict, indent=2))


def load_meta(args):
    if args.meta:
        return load_json(args.meta) or {}
    it = Path(args.iteration_dir)
    for cand in (it / "meta.json", it.parent / "meta.json"):
        m = load_json(cand)
        if m:
            return m
    return {}


def build_ledger_record(v, args):
    """One compact, self-contained line per comparison for trend analysis."""
    meta = load_meta(args)
    c, p = v["cheaper"], v["pricier"]
    adopted = c if v["recommendation"] == "ADOPT_CHEAPER" else (
        p if v["recommendation"] == "KEEP_PRICIER" else None)
    # price-weighted cost saved if the recommendation is followed (0 unless adopting cheaper)
    cid = v["cost"].get("cost_index_delta")
    cost_saved = (-cid) if (v["recommendation"] == "ADOPT_CHEAPER" and cid is not None) else 0
    return {
        "ts": datetime.datetime.now().astimezone().isoformat(timespec="seconds"),
        "run_id": uuid.uuid4().hex[:12],
        "skill": meta.get("skill"),
        "change": meta.get("change"),
        "versions": meta.get("versions"),
        "recommendation": v["recommendation"],
        "confidence": v["confidence"],
        "cheaper": c, "pricier": p, "adopted": adopted,
        "quality": v["quality"],
        "tokens_delta": v["cost"].get("tokens_delta"),
        "cost_ratio": v["cost"].get("cost_ratio"),
        "cost_index_delta": cid,
        "cost_saved_if_adopted": cost_saved,
        "seconds_delta": v["latency"].get("seconds_delta"),
        "power": v["power"],
        "workspace": str(Path(args.iteration_dir).resolve()),
        "rationale": v["rationale"],
    }


def append_ledger(record, path):
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("a") as f:
        f.write(json.dumps(record) + "\n")


def build_rationale(v, d_pass, band, pricier_decisive):
    c, p = v["cheaper"], v["pricier"]
    if v["recommendation"] == "INCONCLUSIVE":
        return ("Underpowered: " + "; ".join(v["power"]["reasons"]) +
                ". Raise --reps or add evals before trusting a verdict.")
    ratio = v["cost"].get("cost_ratio")
    save = f"~{1/ratio:.1f}x cheaper (price-weighted)" if ratio else "cheaper"
    if v["recommendation"] == "ADOPT_CHEAPER":
        return (f"{c} holds quality within the noise band ({band:.3f}) of {p} "
                f"(Δpass_rate={d_pass:+.3f}) and the blind comparator did not decisively "
                f"favor {p}; {c} is {save}. Right-size to {c}.")
    return (f"{p} wins on quality (Δpass_rate={d_pass:+.3f} beyond noise band {band:.3f}"
            + (", comparator decisively favors it" if pricier_decisive else "") +
            f"); the extra cost of {p} buys real quality. Keep {p}.")


def render_md(v):
    c, p = v["cheaper"], v["pricier"]
    rows = []
    for l in (p, c):
        q = v["quality"][l]; cst = v["cost"][l]
        rows.append(f"| {l} | {fmt(cst.get('model'))} | {fmt(q['pass_rate_mean'])} ± {fmt(q['pass_rate_std'])} | "
                    f"{fmt(q['win_rate'])} | {fmt(cst.get('tokens_mean'))} | "
                    f"{fmt(cst.get('cost_index'))} | {fmt(v['latency'][l]['seconds_mean'])} |")
    return (f"# skill-compare verdict\n\n"
            f"**Recommendation: {v['recommendation']}** (confidence: {v['confidence']})\n\n"
            f"{v['rationale']}\n\n"
            f"| version | model | pass_rate | comparator win | tokens | cost-index | seconds |\n"
            f"|---|---|---|---|---|---|---|\n" + "\n".join(rows) + "\n\n"
            f"- price-weighted cost ratio (cheaper/pricier): {fmt(v['cost'].get('cost_ratio'))} "
            f"(raw tokens Δ: {fmt(v['cost'].get('tokens_delta'))} — near-zero across models is expected)\n"
            f"- power: {v['power']['reps_per_version']} reps × {v['power']['evals']} evals, "
            f"{v['power']['comparisons']} comparisons"
            + (f" — ⚠️ underpowered: {'; '.join(v['power']['reasons'])}" if v['power']['underpowered'] else "")
            + "\n")


def fmt(x):
    return "—" if x is None else (f"{x:.3f}" if isinstance(x, float) else str(x))


if __name__ == "__main__":
    main()
