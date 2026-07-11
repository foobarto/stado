#!/usr/bin/env python3
"""Update or validate the release markers in docs/index.html."""

from __future__ import annotations

import argparse
import datetime as dt
import pathlib
import re
import sys


TAG_RE = re.compile(r"v[0-9]+\.[0-9]+\.[0-9]+")
MIDDLE_DOT = "\u00b7"
VERSION_RE = re.compile(
    r'(<span class="ver">)v[0-9]+\.[0-9]+\.[0-9]+'
    r'(?:-[A-Za-z0-9.-]+)?(</span>)'
)
RECEIPT_RE = re.compile(
    r'(<span class="v">)v[0-9]+\.[0-9]+\.[0-9]+'
    r'(?:-[A-Za-z0-9.-]+)? '
    + re.escape(MIDDLE_DOT)
    + r' [0-9]{4}-[0-9]{2}-[0-9]{2}(</span>)'
)


def parse_args() -> argparse.Namespace:
    repo_root = pathlib.Path(__file__).resolve().parent.parent
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("tag", help="stable release tag, for example v0.77.0")
    parser.add_argument("date", help="release date in YYYY-MM-DD form")
    parser.add_argument(
        "--check",
        action="store_true",
        help="fail when the file does not already contain the requested values",
    )
    parser.add_argument(
        "--file",
        type=pathlib.Path,
        default=repo_root / "docs" / "index.html",
        help=argparse.SUPPRESS,
    )
    return parser.parse_args()


def validate_inputs(tag: str, date: str) -> None:
    if TAG_RE.fullmatch(tag) is None:
        raise ValueError(f"invalid stable release tag: {tag!r}")
    try:
        parsed = dt.date.fromisoformat(date)
    except ValueError as exc:
        raise ValueError(f"invalid release date: {date!r}") from exc
    if parsed.isoformat() != date:
        raise ValueError(f"invalid release date: {date!r}")


def render(source: str, tag: str, date: str) -> str:
    updated, version_count = VERSION_RE.subn(rf"\g<1>{tag}\g<2>", source)
    updated, receipt_count = RECEIPT_RE.subn(
        rf"\g<1>{tag} {MIDDLE_DOT} {date}\g<2>", updated
    )
    if version_count != 2 or receipt_count != 1:
        raise ValueError(
            "homepage marker mismatch: expected 2 version markers and 1 receipt "
            f"marker, found {version_count} and {receipt_count}"
        )
    return updated


def main() -> int:
    args = parse_args()
    try:
        validate_inputs(args.tag, args.date)
        source = args.file.read_text()
        updated = render(source, args.tag, args.date)
    except (OSError, ValueError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1

    if args.check:
        if updated != source:
            print(
                f"error: {args.file} does not match {args.tag} {args.date}; "
                "run this command without --check before tagging",
                file=sys.stderr,
            )
            return 1
        print(f"homepage matches {args.tag} {args.date}")
        return 0

    if updated == source:
        print(f"homepage already matches {args.tag} {args.date}")
        return 0
    args.file.write_text(updated)
    print(f"updated {args.file} to {args.tag} {args.date}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
