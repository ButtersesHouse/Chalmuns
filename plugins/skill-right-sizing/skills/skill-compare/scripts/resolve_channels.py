#!/usr/bin/env python3
"""resolve_channels.py — locate the installed stable and beta copies of a skill.

For the marketplace "release channels" pattern: a user adds both the stable
marketplace and a beta marketplace, so the same skill exists twice on disk under
different marketplace dirs. This resolves a skill name to those two directories
so skill-compare can A/B them via its --dirs flow — no hand-typed paths.

Channel is inferred from the marketplace directory name (contains
beta/latest/next/rc/dev/edge/nightly -> beta; otherwise stable), overridable.

Usage:
  python resolve_channels.py <skill-name> [--root ~/.claude]
                             [--stable <marketplace>] [--beta <marketplace>]
Prints JSON {skill, stable, beta, candidates[]}; exit 1 if both aren't resolved.
"""
import argparse, json, re, sys
from pathlib import Path

BETA_HINT = re.compile(r"(beta|latest|next|rc|dev|edge|nightly)", re.I)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("skill")
    ap.add_argument("--root", default=str(Path.home() / ".claude"))
    ap.add_argument("--stable", help="marketplace dir name to force as stable")
    ap.add_argument("--beta", help="marketplace dir name to force as beta")
    a = ap.parse_args()

    # Installed plugins live under plugins/cache/<marketplace>/<plugin>/<version>/skills/
    # (verified). plugins/marketplaces/ is only the marketplace clone (source), used
    # as a fallback for local-source plugins that aren't cached separately.
    cache = Path(a.root) / "plugins" / "cache"
    mkt_base = Path(a.root) / "plugins" / "marketplaces"
    hits = []
    for skillmd in cache.glob(f"*/*/*/skills/{a.skill}/SKILL.md"):
        p = skillmd.parent.relative_to(cache).parts  # <mkt>/<plugin>/<version>/skills/<skill>
        hits.append({"marketplace": p[0], "plugin": p[1], "version": p[2],
                     "dir": str(skillmd.parent), "src": "cache"})
    if not hits:
        for skillmd in mkt_base.glob(f"*/plugins/*/skills/{a.skill}/SKILL.md"):
            p = skillmd.parent.relative_to(mkt_base).parts  # <mkt>/plugins/<plugin>/skills/<skill>
            hits.append({"marketplace": p[0], "plugin": p[2], "version": None,
                         "dir": str(skillmd.parent), "src": "marketplace"})
    # keep the highest version per (marketplace, plugin)
    best = {}
    for h in hits:
        k = (h["marketplace"], h["plugin"])
        if k not in best or (h["version"] or "") > (best[k]["version"] or ""):
            best[k] = h
    hits = list(best.values())

    # Channel is "beta" if EITHER the marketplace name OR the plugin dir name hints
    # beta — covers both patterns (two marketplaces, or two entries in one marketplace).
    stable = beta = None
    for h in hits:
        m, p = h["marketplace"], h["plugin"]
        if a.stable and m == a.stable:
            stable = h["dir"]
        elif a.beta and m == a.beta:
            beta = h["dir"]
        elif BETA_HINT.search(m) or BETA_HINT.search(p):
            beta = beta or h["dir"]
        else:
            stable = stable or h["dir"]

    out = {"skill": a.skill, "stable": stable, "beta": beta, "candidates": hits}
    print(json.dumps(out, indent=2))
    if not (stable and beta):
        sys.stderr.write(
            f"\nCould not resolve both channels for '{a.skill}'. "
            f"Found {len(hits)} copy(ies). "
            "Ensure both the stable and beta marketplaces are added, "
            "or pass --stable/--beta explicitly.\n")
        sys.exit(1)


if __name__ == "__main__":
    main()
