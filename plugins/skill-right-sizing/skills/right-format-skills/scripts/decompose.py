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


def find_section(lines, heading_text):
    """Locate a heading matching heading_text (case-insensitive, exact text)
    and return (start_index, level, end_index_exclusive)."""
    start = None
    level = None
    for i, line in enumerate(lines):
        m = HEADING_RE.match(line)
        if m and m.group(2).strip().lower() == heading_text.strip().lower():
            start, level = i, len(m.group(1))
            break
    if start is None:
        return None
    end = len(lines)
    for j in range(start + 1, len(lines)):
        m = HEADING_RE.match(lines[j])
        if m and len(m.group(1)) <= level:
            end = j
            break
    return start, level, end


def decompose(skill_md, heading_text, target_name, summary=None, dry_run=False):
    skill_md = Path(skill_md).expanduser().resolve()
    if not skill_md.exists():
        raise SystemExit(f"error: {skill_md} not found")

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

    if dry_run:
        print(f"-- would write {target_path} ({len(new_file_lines)} lines) --")
        print(new_file_text)
        print(f"-- would rewrite {skill_md} (section '{heading_title}' -> pointer) --")
        return

    if target_path.exists():
        raise SystemExit(f"error: {target_path} already exists — pick a different --target")
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
