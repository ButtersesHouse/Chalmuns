#!/usr/bin/env python3
"""
prepare.py — deterministic version materialization + meta + run scaffold for skill-compare.

Replaces the model-run bookkeeping in SKILL.md step 1 (and the step-4 run-tree
layout): materialize the two skill versions into <ws>/versionA and <ws>/versionB,
parse each version's model:/effort: frontmatter, write meta.json (the exact schema
verdict.py consumes), and optionally scaffold the run tree from an evals file.

Usage:
  python prepare.py --ws <dir> [--evals <evals.json>] [--reps N] [--change "desc"]
    ( --dirs A B
    | --skill PATH --patch k=v [k=v ...]
    | --git REPO refA refB [--subpath P]
    | --channels SKILL [--root R] )
"""
import argparse, json, os, re, shutil, subprocess, sys, tempfile
from pathlib import Path

FM_RE = re.compile(r"^---\n(.*?)\n---", re.S)


def frontmatter(skill_dir):
    md = Path(skill_dir) / "SKILL.md"
    if not md.exists():
        return {}
    m = FM_RE.match(md.read_text())
    fm = m.group(1) if m else ""
    def get(k):
        mm = re.search(rf"^{k}:\s*(.+?)\s*$", fm, re.M)
        v = mm.group(1).strip() if mm else None
        return None if (v is None or v.lower() == "inherit") else v
    return {"name": get("name"), "model": get("model"), "effort": get("effort")}


def fresh(dst):
    dst = Path(dst)
    if dst.exists():
        shutil.rmtree(dst)
    dst.parent.mkdir(parents=True, exist_ok=True)
    return dst


def copy_dir(src, dst):
    shutil.copytree(src, fresh(dst))


def patch_frontmatter(skill_dir, kvs):
    md = Path(skill_dir) / "SKILL.md"
    text = md.read_text()
    m = FM_RE.match(text)
    if not m:
        raise SystemExit(f"no frontmatter in {md}")
    fm = m.group(1)
    for pair in kvs:
        k, _, v = pair.partition("=")
        if re.search(rf"^{k}:.*$", fm, re.M):
            fm = re.sub(rf"^{k}:.*$", f"{k}: {v}", fm, count=1, flags=re.M)
        else:
            fm = fm + f"\n{k}: {v}"
    md.write_text(f"---\n{fm}\n---" + text[m.end():])


def git_extract(repo, ref, subpath, dst):
    with tempfile.TemporaryDirectory() as tmp:
        tar = Path(tmp) / "a.tar"
        args = ["git", "-C", repo, "archive", "--format=tar", ref]
        if subpath:
            args.append(subpath)
        subprocess.run(args, stdout=tar.open("wb"), check=True)
        subprocess.run(["tar", "-xf", str(tar), "-C", tmp], check=True)
        src = Path(tmp) / subpath if subpath else Path(tmp)
        copy_dir(src, dst)


def resolve_channels(skill, root):
    here = Path(__file__).parent / "resolve_channels.py"
    cmd = [sys.executable, str(here), skill]
    if root:
        cmd += ["--root", root]
    out = subprocess.run(cmd, capture_output=True, text=True)
    data = json.loads(out.stdout or "{}")
    if not (data.get("stable") and data.get("beta")):
        raise SystemExit(f"--channels: could not resolve both channels for '{skill}'.\n{out.stderr}")
    return data["stable"], data["beta"]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--ws", required=True)
    ap.add_argument("--evals")
    ap.add_argument("--reps", type=int, default=3)
    ap.add_argument("--change", default=None)
    ap.add_argument("--dirs", nargs=2, metavar=("A", "B"))
    ap.add_argument("--skill")
    ap.add_argument("--patch", nargs="+", default=None, metavar="k=v")
    ap.add_argument("--git", nargs=3, metavar=("REPO", "refA", "refB"))
    ap.add_argument("--subpath", default=None)
    ap.add_argument("--channels", metavar="SKILL")
    ap.add_argument("--root", default=None)
    a = ap.parse_args()

    ws = Path(a.ws)
    vA, vB = ws / "versionA", ws / "versionB"
    change = a.change

    if a.dirs:
        copy_dir(a.dirs[0], vA); copy_dir(a.dirs[1], vB)
        change = change or f"{a.dirs[0]} vs {a.dirs[1]}"
    elif a.skill and a.patch:
        copy_dir(a.skill, vA); copy_dir(a.skill, vB)
        patch_frontmatter(vB, a.patch)
        change = change or "patch: " + ", ".join(a.patch) + " (B) vs unchanged (A)"
    elif a.git:
        repo, refA, refB = a.git
        git_extract(repo, refA, a.subpath, vA); git_extract(repo, refB, a.subpath, vB)
        change = change or f"git {refA} (A) vs {refB} (B)"
    elif a.channels:
        stable, beta = resolve_channels(a.channels, a.root)
        copy_dir(stable, vA); copy_dir(beta, vB)
        change = change or f"stable (A) vs beta (B) of {a.channels}"
    else:
        raise SystemExit("choose one mode: --dirs | --skill+--patch | --git | --channels")

    fA, fB = frontmatter(vA), frontmatter(vB)
    skill_name = fA.get("name") or a.channels or a.skill or "unknown"
    meta = {"skill": skill_name, "change": change,
            "versions": {"versionA": {"model": fA["model"], "effort": fA["effort"]},
                         "versionB": {"model": fB["model"], "effort": fB["effort"]}}}
    (ws / "meta.json").write_text(json.dumps(meta, indent=2) + "\n")

    scaffolded = 0
    if a.evals:
        evals = json.loads(Path(a.evals).read_text()).get("evals", [])
        it = ws / "iteration-1"
        for i, ev in enumerate(evals):
            ed = it / f"eval-{i}"
            for v in ("versionA", "versionB"):
                for r in range(1, a.reps + 1):
                    (ed / v / f"run-{r}" / "outputs").mkdir(parents=True, exist_ok=True)
            (ed / "comparison").mkdir(parents=True, exist_ok=True)
            (ed / "eval_meta.json").write_text(json.dumps(
                {"eval_id": ev.get("id", i), "prompt": ev.get("prompt"),
                 "expectations": ev.get("expectations", [])}, indent=2) + "\n")
            scaffolded += 1

    print(json.dumps({"ws": str(ws), "meta": meta,
                      "evals_scaffolded": scaffolded, "reps": a.reps}, indent=2))


if __name__ == "__main__":
    main()
