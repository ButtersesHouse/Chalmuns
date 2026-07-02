#!/usr/bin/env bash
# release.sh — publish kara-kara. Mirror dev-copy skills into the plugin, then:
#   stable: bump plugin.json version, commit to main, tag vX.Y.Z, push main + tag.
#   beta:   set a prerelease version, (force-)publish to the `beta` branch (no tag).
# Deterministic; no model needed. Source of truth for releases.
#
# Usage:
#   ./release.sh <major|minor|patch|X.Y.Z> "message" [--dry-run]   # stable
#   ./release.sh --beta "message" [X.Y.Z-beta.N]                    # beta channel
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEV="$HOME/.claude/skills"
PLUG="$ROOT/plugins/skill-right-sizing"
MANIFEST="$PLUG/.claude-plugin/plugin.json"
SKILLS=(right-size-skills skill-compare)
cd "$ROOT"

mirror() {  # dev copies -> plugin tree (idempotent)
  echo "== mirroring dev skills into plugin =="
  for S in "${SKILLS[@]}"; do
    [ -f "$DEV/$S/SKILL.md" ] || { echo "ERROR: dev copy missing: $DEV/$S/SKILL.md"; exit 1; }
    local DEST="$PLUG/skills/$S"; mkdir -p "$DEST"
    cp "$DEV/$S/SKILL.md" "$DEST/SKILL.md"
    for SUB in references scripts assets evals; do
      [ -d "$DEV/$S/$SUB" ] && rsync -a --delete --exclude='__pycache__' --exclude='*.pyc' "$DEV/$S/$SUB/" "$DEST/$SUB/"
    done
    [ -d "$DEST/scripts" ] && chmod +x "$DEST/scripts/"*.py 2>/dev/null || true
    echo "  synced $S"
  done
}
setver() { python3 - "$MANIFEST" "$1" <<'PY'
import json,sys
p,new=sys.argv[1],sys.argv[2]; d=json.load(open(p)); d['version']=new
json.dump(d,open(p,'w'),indent=2); open(p,'a').write('\n')
PY
}
curver() { python3 -c "import json;print(json.load(open('$MANIFEST'))['version'])"; }
GIT_ID=(-c user.name="mryave" -c user.email="mryave@gmail.com")

[ -f "$MANIFEST" ] || { echo "ERROR: manifest not found: $MANIFEST"; exit 1; }

# ---------------- BETA lane ----------------
if [ "${1:-}" = "--beta" ]; then
  MSG="${2:-}"; EXPLICIT="${3:-}"
  [ -z "$MSG" ] && { echo 'usage: ./release.sh --beta "message" [X.Y.Z-beta.N]'; exit 2; }
  [ -n "$(git status --porcelain)" ] && { echo "ERROR: working tree dirty — commit/stash first."; exit 1; }
  CUR="$(curver)"
  BVER="$EXPLICIT"
  if [ -z "$BVER" ]; then
    BVER="$(python3 - "$CUR" <<'PY'
import sys,re
cur=re.sub(r'-.*$','',sys.argv[1]); a,b,c=(int(x) for x in cur.split('.'))
print(f"{a}.{b}.{c+1}-beta.1")
PY
)"
  fi
  echo "== BETA publish: version $BVER -> branch 'beta' (bleeding edge) =="
  git checkout -q -B beta main          # beta = main + this mirror (bleeding edge; history not preserved)
  mirror
  setver "$BVER"
  # rename the plugin so beta installs to its own dir (no clash with stable) and
  # so resolve_channels detects the channel by path.
  python3 - "$MANIFEST" <<'PY'
import json,sys
p=sys.argv[1]; d=json.load(open(p)); d['name']='skill-right-sizing-beta'
json.dump(d,open(p,'w'),indent=2); open(p,'a').write('\n')
PY
  git add -A
  git "${GIT_ID[@]}" commit -q -m "beta: $MSG ($BVER)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
  git push -u origin beta 2>/dev/null || git push --force-with-lease origin beta
  git checkout -q main
  echo "== beta branch published ($BVER). Users on the beta entry get this on next update. =="
  exit 0
fi

# ---------------- STABLE lane ----------------
BUMP="${1:-}"; MSG="${2:-}"; DRY=""
[ "${3:-}" = "--dry-run" ] && DRY=1
if [ -z "$BUMP" ] || [ -z "$MSG" ]; then
  echo 'usage: ./release.sh <major|minor|patch|X.Y.Z> "message" [--dry-run]   (or: --beta "message")'; exit 2
fi

mirror
CUR="$(curver)"
NEW="$(python3 - "$CUR" "$BUMP" <<'PY'
import sys,re
cur,bump=re.sub(r'-.*$','',sys.argv[1]),sys.argv[2]
if re.fullmatch(r'\d+\.\d+\.\d+',bump): print(bump); sys.exit()
a,b,c=(int(x) for x in cur.split('.'))
if bump=='major': a,b,c=a+1,0,0
elif bump=='minor': b,c=b+1,0
elif bump=='patch': c=c+1
else: sys.stderr.write(f"bad bump '{bump}'\n"); sys.exit(3)
print(f"{a}.{b}.{c}")
PY
)"
TAG="v$NEW"
echo "== version: $CUR -> $NEW (tag $TAG) =="
if git rev-parse -q --verify "refs/tags/$TAG" >/dev/null; then
  echo "ERROR: tag $TAG already exists. Bump differently."; exit 1
fi
setver "$NEW"
echo "== changes =="; git add -A; git --no-pager diff --cached --stat

if [ -n "$DRY" ]; then
  echo "== DRY RUN: would commit \"$MSG (v$NEW)\", tag $TAG, push main+tag. Restoring tree. =="
  git restore --staged --worktree "$MANIFEST" "$PLUG/skills" 2>/dev/null || git checkout -- "$MANIFEST" "$PLUG/skills"
  exit 0
fi
git "${GIT_ID[@]}" commit -q -m "$MSG (v$NEW)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
git tag -a "$TAG" -m "$MSG"
git push origin main
git push origin "$TAG"
echo "== released $TAG =="; git --no-pager log --oneline -1
