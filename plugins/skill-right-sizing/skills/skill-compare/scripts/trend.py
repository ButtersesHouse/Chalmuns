#!/usr/bin/env python3
"""
trend.py — summarize the rolling skill-compare ledger over time.

Reads the append-only JSONL written by verdict.py and reports the pool of
completed A/B comparisons: verdict mix, cumulative token savings, per-skill
history, and tasks that keep coming back inconclusive (need more power).

Usage:
    python trend.py [--ledger PATH] [--skill NAME] [--since YYYY-MM-DD]
                    [-o trend.md]     # also writes a markdown report
"""
import argparse, json
from collections import defaultdict, Counter
from pathlib import Path

DEFAULT_LEDGER = Path.home() / ".claude" / "skill-compare" / "ledger.jsonl"


def read_ledger(path, skill=None, since=None):
    rows = []
    if not Path(path).exists():
        return rows
    for line in Path(path).read_text().splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            r = json.loads(line)
        except Exception:
            continue
        if skill and r.get("skill") != skill:
            continue
        if since and (r.get("ts") or "") < since:
            continue
        rows.append(r)
    return rows


def summarize(rows):
    recs = Counter(r.get("recommendation") for r in rows)
    total_saved = sum(r.get("cost_saved_if_adopted") or 0 for r in rows)
    by_skill = defaultdict(list)
    for r in rows:
        by_skill[r.get("skill") or "(unnamed)"].append(r)
    inconclusive = [r for r in rows if r.get("recommendation") == "INCONCLUSIVE"]
    span = (min((r.get("ts") for r in rows if r.get("ts")), default=None),
            max((r.get("ts") for r in rows if r.get("ts")), default=None))
    return recs, total_saved, by_skill, inconclusive, span


def render(rows):
    if not rows:
        return "# skill-compare trend\n\nNo comparisons logged yet.\n"
    recs, saved, by_skill, inconclusive, span = summarize(rows)
    L = []
    L.append("# skill-compare trend\n")
    L.append(f"**{len(rows)} comparisons** logged from {span[0]} to {span[1]}.\n")
    L.append("## Verdict mix")
    for k in ("ADOPT_CHEAPER", "KEEP_PRICIER", "INCONCLUSIVE"):
        L.append(f"- {k}: {recs.get(k, 0)}")
    L.append(f"\n**Cumulative cost saved (adopted-cheaper runs): "
             f"~{saved:,} haiku-token-equivalents** (price-weighted)\n")

    L.append("## Per-skill history (most recent last)")
    for skill, rs in sorted(by_skill.items()):
        rs = sorted(rs, key=lambda r: r.get("ts") or "")
        L.append(f"\n### {skill}")
        L.append("| ts | change | verdict | conf | cost ratio | adopted |")
        L.append("|---|---|---|---|---|---|")
        for r in rs:
            L.append(f"| {r.get('ts','—')} | {r.get('change') or '—'} | "
                     f"{r.get('recommendation','—')} | {r.get('confidence','—')} | "
                     f"{fmt(r.get('cost_ratio'))} | {r.get('adopted') or '—'} |")

    if inconclusive:
        L.append("\n## Needs more power (recurring INCONCLUSIVE)")
        for r in inconclusive:
            reasons = "; ".join((r.get("power") or {}).get("reasons") or [])
            L.append(f"- {r.get('skill') or '—'} ({r.get('change') or '—'}): {reasons}")
    return "\n".join(L) + "\n"


def fmt(x):
    return "—" if x is None else (f"{x:,}" if isinstance(x, int) else f"{x}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--ledger", default=str(DEFAULT_LEDGER))
    ap.add_argument("--skill", default=None)
    ap.add_argument("--since", default=None, help="ISO date lower bound, e.g. 2026-07-01")
    ap.add_argument("-o", "--output", default=None,
                    help="also write markdown here (default: alongside the ledger as trend.md)")
    args = ap.parse_args()
    rows = read_ledger(args.ledger, args.skill, args.since)
    md = render(rows)
    out = Path(args.output) if args.output else Path(args.ledger).with_name("trend.md")
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(md)
    print(md)


if __name__ == "__main__":
    main()
