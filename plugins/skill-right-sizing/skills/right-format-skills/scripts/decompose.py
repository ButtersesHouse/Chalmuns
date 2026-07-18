#!/usr/bin/env python3
"""
decompose.py — mechanically extract one section of a SKILL.md body into a
separate reference file and leave a pointer behind, per Anthropic's
"progressive disclosure" pattern (high-level guide + references, kept one
link-hop deep from SKILL.md).

This performs exactly one extraction per invocation so each change is a
small, reviewable diff. It never chooses *what* to extract — that judgment
call belongs to the skill body (right-format-skills), driven by a plan the
user has approved.

Usage:
    python decompose.py --skill <path/to/SKILL.md> --heading "<exact heading text>" \
        --target <filename.md> [--summary "<one-line pointer text>"] [--dry-run]

Example:
    python decompose.py --skill ~/.claude/skills/pdf/SKILL.md \
        --heading "API reference" --target REFERENCE.md \
        --summary "**API reference**: See [REFERENCE.md](REFERENCE.md) for all methods."
"""
import argparse, re, sys
from pathlib import Path

HEADING_RE = re.compile(r"^(#{1,6})\s+(.*)$")
FENCE_RE = re.compile(r"^\s*(```+|~~~+)")


def _heading_lines(lines):
    """Yield (index, match) for lines that are real headings — i.e. HEADING_RE
    matches and the line is not inside a fenced code block. A `#`-prefixed
    comment inside a ``` fence must never be mistaken for a markdown heading."""
    in_fence = False
    fence_marker = None
    for i, line in enumerate(lines):
        fm = FENCE_RE.match(line)
        if fm:
            marker = fm.group(1)[0] * 3  # normalize to the fence character run
            if not in_fence:
                in_fence, fence_marker = True, marker
            elif marker[0] == fence_marker[0]:
                in_fence, fence_marker = False, None
            continue
        if in_fence:
            continue
        m = HEADING_RE.match(line)
        if m:
            yield i, m


def find_section(lines, heading_text):
    """Locate the heading matching heading_text (case-insensitive, exact
    text, ignoring anything inside fenced code blocks) and return
    (start_index, level, end_index_exclusive). Raises SystemExit if the
    heading text is ambiguous (matches more than once)."""
    wanted = heading_text.strip().lower()
    matches = [(i, m) for i, m in _heading_lines(lines) if m.group(2).strip().lower() == wanted]
    if not matches:
        return None
    if len(matches) > 1:
        lines_found = ", ".join(str(i + 1) for i, _ in matches)
        raise SystemExit(
            f"error: heading {heading_text!r} matches {len(matches)} times "
            f"(lines {lines_found}) — use a more specific --heading or disambiguate manually"
        )
    start, m = matches[0]
    level = len(m.group(1))

    end = len(lines)
    for j, m2 in _heading_lines(lines):
        if j > start and len(m2.group(1)) <= level:
            end = j
            break
    return start, level, end


def decompose(skill_md, heading_text, target_name, summary=None, dry_run=False):
    skill_md = Path(skill_md).expanduser().resolve()
    if not skill_md.exists():
        raise SystemExit(f"error: {skill_md} not found")

    target_parts = Path(target_name).parts
    if Path(target_name).is_absolute() or len(target_parts) != 1 or target_name in (".", ".."):
        raise SystemExit(
            f"error: --target must be a plain filename in the skill directory, got {target_name!r}"
        )

    text = skill_md.read_text()
    lines = text.splitlines()
    found = find_section(lines, heading_text)
    if found is None:
        raise SystemExit(f"error: no heading matching {heading_text!r} found in {skill_md}")
    start, level, end = found

    section_lines = lines[start:end]
    heading_line = section_lines[0]
    heading_title = HEADING_RE.match(heading_line).group(2).strip()
    section_body = section_lines[1:]
    while section_body and section_body[0].strip() == "":
        section_body.pop(0)
    while section_body and section_body[-1].strip() == "":
        section_body.pop()

    target_path = skill_md.parent / target_name
    new_file_lines = [f"# {heading_title}", ""] + section_body
    new_file_text = "\n".join(new_file_lines) + "\n"

    pointer = summary or f"**{heading_title}**: See [{target_name}]({target_name}) for details."
    replacement = [heading_line, "", pointer, ""]

    new_lines = lines[:start] + replacement + lines[end:]
    new_text = "\n".join(new_lines) + "\n"

    if target_path.exists():
        raise SystemExit(f"error: {target_path} already exists — pick a different --target")

    if dry_run:
        print(f"-- would write {target_path} ({len(new_file_lines)} lines) --")
        print(new_file_text)
        print(f"-- would rewrite {skill_md} (section '{heading_title}' -> pointer) --")
        return

    target_path.write_text(new_file_text)
    skill_md.write_text(new_text)
    print(f"extracted '{heading_title}' -> {target_path}")
    print(f"rewrote {skill_md} ({len(lines)} -> {len(new_lines)} lines)")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--skill", required=True, help="path to SKILL.md")
    ap.add_argument("--heading", required=True, help="exact heading text to extract")
    ap.add_argument("--target", required=True, help="filename for the new reference file, e.g. REFERENCE.md")
    ap.add_argument("--summary", default=None, help="pointer line left in SKILL.md (default: auto-generated)")
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()
    decompose(args.skill, args.heading, args.target, args.summary, args.dry_run)


if __name__ == "__main__":
    main()
