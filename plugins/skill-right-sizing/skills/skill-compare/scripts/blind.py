#!/usr/bin/env python3
"""
blind.py — deterministic A/B slot randomization + slotmap + neutral copies.

Replaces the model-run "randomize which version is slot A vs B and write the
mapping" in SKILL.md step 5 — the one place blinding hygiene can leak. For each
eval (or rep), it randomly (seeded → reproducible) assigns versionA/versionB to
neutral slots A/B, copies their run outputs to comparison/run-N/{slotA,slotB},
and writes comparison/run-N/slotmap.json. verdict.py:win_rates de-blinds via those.

Usage:
  python blind.py <iteration_dir> [--seed S] [--per eval|rep]
"""
import argparse, json, random, shutil
from pathlib import Path


def outputs_dir(eval_dir, version, run):
    return eval_dir / version / f"run-{run}" / "outputs"


def stage(eval_dir, run, rng):
    """Assign slots for one comparison at run N; copy outputs; write slotmap."""
    va = outputs_dir(eval_dir, "versionA", run)
    vb = outputs_dir(eval_dir, "versionB", run)
    if not va.exists() or not vb.exists():
        return None
    versions = ["versionA", "versionB"]
    rng.shuffle(versions)                       # seeded → reproducible
    slotmap = {"A": versions[0], "B": versions[1]}
    cdir = eval_dir / "comparison" / f"run-{run}"
    cdir.mkdir(parents=True, exist_ok=True)
    for slot, ver in (("A", versions[0]), ("B", versions[1])):
        dst = cdir / f"slot{slot}"
        if dst.exists():
            shutil.rmtree(dst)
        shutil.copytree(outputs_dir(eval_dir, ver, run), dst)
    (cdir / "slotmap.json").write_text(json.dumps(slotmap) + "\n")
    return slotmap


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("iteration_dir")
    ap.add_argument("--seed", type=int, default=0)
    ap.add_argument("--per", choices=["eval", "rep"], default="eval")
    a = ap.parse_args()

    it = Path(a.iteration_dir)
    assignments = []
    for eval_dir in sorted(it.glob("eval-*")):
        if not eval_dir.is_dir():
            continue
        # per-eval seed keeps assignments stable & independent across evals
        idx = int(eval_dir.name.split("-")[-1])
        rng = random.Random(a.seed * 1000 + idx)
        if a.per == "eval":
            sm = stage(eval_dir, 1, rng)         # compare run-1 outputs
            if sm:
                assignments.append({"eval": eval_dir.name, "run": 1, "slotmap": sm})
        else:
            runs = sorted({int(p.name.split("-")[1])
                           for p in (eval_dir / "versionA").glob("run-*")})
            for r in runs:
                sm = stage(eval_dir, r, rng)
                if sm:
                    assignments.append({"eval": eval_dir.name, "run": r, "slotmap": sm})

    print(json.dumps({"iteration": str(it), "per": a.per, "seed": a.seed,
                      "comparisons_staged": len(assignments),
                      "assignments": assignments}, indent=2))


if __name__ == "__main__":
    main()
