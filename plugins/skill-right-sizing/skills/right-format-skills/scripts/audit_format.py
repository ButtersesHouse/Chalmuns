#!/usr/bin/env python3
"""
audit_format.py — deterministic SKILL.md format/size checks.

Runs the mechanical half of a format audit (line counts, frontmatter
validation, reference-nesting depth, TOC presence, path style, naming
convention, time-sensitive phrasing) so the model only has to judge how
to decompose an over-budget skill, not count lines by eye.

Thresholds and rules are sourced from Anthropic's published Skill
authoring guidance (see ../references/rubric.md for citations).

Usage:
    python audit_format.py <SKILL.md-path> [<SKILL.md-path> ...]
    python audit_format.py --glob "~/.claude/skills/*/SKILL.md"

Prints one JSON object per skill to stdout (JSON Lines).
"""
import argparse, glob, json, os, re, sys
from pathlib import Path

BODY_LINE_LIMIT = 500
BODY_LINE_WARN = 400
REF_FILE_TOC_THRESHOLD = 100
NAME_MAX_CHARS = 64
DESC_MAX_CHARS = 1024
RESERVED_WORDS = ("claude", "anthropic")
VAGUE_NAMES = {"helper", "helpers", "utils", "util", "tools", "tool",
               "documents", "data", "files", "misc", "stuff"}

FRONTMATTER_RE = re.compile(r"^---\s*\n(.*?)\n---\s*\n?", re.DOTALL)
MD_LINK_RE = re.compile(r"\[([^\]]*)\]\(([^)\s]+)\)")
BARE_PATH_RE = re.compile(r"[\w][\w-]*(?:/[\w.-]+)+\.md\b")
HEADING_RE = re.compile(r"^(#{1,6})\s+(.*)$")
TIME_SENSITIVE_RE = re.compile(
    r"\b(before|after|as of|since|starting)\b[^.\n]{0,25}"
    r"\b(20[0-9]{2}|Q[1-4]\s?20[0-9]{2}|January|February|March|April|May|June|"
    r"July|August|September|October|November|December)\b",
    re.IGNORECASE,
)
WINDOWS_PATH_RE = re.compile(r"[A-Za-z0-9_.\-]+\\[A-Za-z0-9_.\-\\]+\.\w+")


def parse_frontmatter(text):
    m = FRONTMATTER_RE.match(text)
    if not m:
        return {}, text, 0
    fm_text = m.group(1)
    body = text[m.end():]
    fields = {}
    for line in fm_text.splitlines():
        mm = re.match(r"^([A-Za-z_-]+):\s*(.*)$", line)
        if mm:
            fields[mm.group(1).strip()] = mm.group(2).strip().strip('"\'')
    fm_lines = len(fm_text.splitlines()) + 2  # + the two --- markers
    return fields, body, fm_lines


def check_frontmatter(fields):
    issues = []
    name = fields.get("name", "")
    desc = fields.get("description", "")
    if not name:
        issues.append("frontmatter missing required 'name'")
    else:
        if len(name) > NAME_MAX_CHARS:
            issues.append(f"name exceeds {NAME_MAX_CHARS} chars ({len(name)})")
        if not re.fullmatch(r"[a-z0-9\-]+", name):
            issues.append("name must be lowercase letters, numbers, hyphens only")
        for w in RESERVED_WORDS:
            if w in name.lower():
                issues.append(f"name contains reserved word '{w}'")
        first_token = name.split("-")[0]
        if not first_token.endswith("ing"):
            issues.append("name is not gerund-form (e.g. 'processing-pdfs') — advisory")
        if name.lower() in VAGUE_NAMES or any(t in VAGUE_NAMES for t in name.split("-")):
            issues.append(f"name is vague ('{name}') — prefer a specific noun/gerund phrase")
    if not desc:
        issues.append("frontmatter missing required 'description'")
    elif len(desc) > DESC_MAX_CHARS:
        issues.append(f"description exceeds {DESC_MAX_CHARS} chars ({len(desc)})")
    if re.search(r"\bI can\b|\byou can use this\b", desc, re.IGNORECASE):
        issues.append("description reads first/second-person — should be third person")
    return issues


def find_headings(body):
    return [(i, len(m.group(1)), m.group(2).strip())
            for i, line in enumerate(body.splitlines())
            for m in [HEADING_RE.match(line)] if m]


def find_local_md_links(body):
    """Markdown link targets plus bare relative-path mentions (this repo's
    skills often reference files as `references/rubric.md` in prose/bold
    rather than as [text](path) links)."""
    links = set()
    for m in MD_LINK_RE.finditer(body):
        target = m.group(2)
        if target.startswith(("http://", "https://", "#")):
            continue
        target = target.split("#", 1)[0].split("?", 1)[0]  # strip anchor/query
        if target.lower().endswith(".md"):
            links.add(target)
    # Exclude URLs before the bare-path scan so a substring inside
    # https://example.com/docs/spec.md isn't misread as a local file.
    body_no_urls = re.sub(r"https?://\S+", "", body)
    for m in BARE_PATH_RE.finditer(body_no_urls):
        links.add(m.group(0).strip("`*"))
    return sorted(links)


def check_reference_file(path):
    """Returns (line_count, has_toc, is_windows_path_used)."""
    try:
        text = Path(path).read_text()
    except OSError:
        return None, None, None
    lines = text.splitlines()
    head = "\n".join(lines[:15]).lower()
    # Require an actual "Contents" *heading*, not just the word "content"
    # appearing in ordinary prose (e.g. "documents the content of...").
    has_toc = bool(re.search(r"^#+\s*(table of )?contents\b", head, re.MULTILINE))
    return len(lines), has_toc, bool(WINDOWS_PATH_RE.search(text))


def audit_skill(skill_md_path):
    p = Path(skill_md_path).expanduser().resolve()
    result = {"path": str(p), "skill_dir": str(p.parent)}
    if not p.exists():
        result["error"] = "file not found"
        return result

    text = p.read_text()
    fields, body, fm_lines = parse_frontmatter(text)
    body_lines = len(body.splitlines())

    result["name"] = fields.get("name", p.parent.name)
    result["frontmatter_lines"] = fm_lines
    result["body_lines"] = body_lines
    result["over_budget"] = body_lines > BODY_LINE_LIMIT
    result["approaching_budget"] = BODY_LINE_WARN <= body_lines <= BODY_LINE_LIMIT
    result["frontmatter_issues"] = check_frontmatter(fields)

    headings = find_headings(body)
    result["sections"] = [
        {"level": lvl, "title": title, "line": i}
        for i, lvl, title in headings
    ]

    links = find_local_md_links(body)
    nested = []
    missing_toc = []
    windows_paths = set(WINDOWS_PATH_RE.findall(body))
    for link in links:
        ref_path = (p.parent / link).resolve()
        line_count, has_toc, uses_windows = check_reference_file(ref_path)
        if line_count is None:
            continue
        if line_count > REF_FILE_TOC_THRESHOLD and not has_toc:
            missing_toc.append({"file": link, "lines": line_count})
        if uses_windows:
            windows_paths.add(link)
        # one-hop nesting check: does the referenced file itself link to
        # further local .md files?
        try:
            ref_text = Path(ref_path).read_text()
        except OSError:
            continue
        deeper = find_local_md_links(ref_text)
        if deeper:
            nested.append({"file": link, "links_to": deeper})

    result["referenced_md_files"] = links
    result["nested_references"] = nested
    result["reference_files_missing_toc"] = missing_toc
    result["windows_style_paths"] = sorted(windows_paths)
    result["time_sensitive_phrases"] = [
        m.group(0) for m in TIME_SENSITIVE_RE.finditer(body)
    ]
    return result


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("paths", nargs="*", help="SKILL.md file paths")
    ap.add_argument("--glob", dest="glob_pat", default=None,
                    help="glob pattern for SKILL.md files, e.g. '~/.claude/skills/*/SKILL.md'")
    args = ap.parse_args()

    paths = list(args.paths)
    if args.glob_pat:
        paths.extend(sorted(glob.glob(os.path.expanduser(args.glob_pat))))
    if not paths:
        ap.error("no paths given (pass SKILL.md paths or --glob)")

    for path in paths:
        print(json.dumps(audit_skill(path)))


if __name__ == "__main__":
    main()
