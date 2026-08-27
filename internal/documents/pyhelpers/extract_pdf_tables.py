#!/usr/bin/env python3
"""Extract tables from a PDF via pdfplumber, printing one JSON object to
stdout: {"pages": [{"page": N, "tables": [{"rows": [["a","b"], ...]}]}]}.

A page with no detected tables still appears with an empty "tables"
list, so the caller can tell "checked, found nothing" apart from
"never checked" (e.g. because --start/--end excluded it).

Usage: extract_pdf_tables.py <input.pdf> [--start N] [--end N]
Exit 1 with a message on stderr for: pdfplumber not installed, the
input file missing/unreadable, or an encrypted PDF pdfplumber can't
open without a password. All are meant to be treated as "this tier is
unavailable right now", not a crash — see the Go-side caller, which
falls back to the plain pdftotext result on any non-zero exit.
"""
import argparse
import json
import sys

# Verified against real pandoc+weasyprint-rendered tables (no visible
# cell borders): pdfplumber's default "lines" detection strategy finds
# NOTHING there, since it looks for drawn ruling lines. "text" strategy
# (word-alignment based) finds it, but injects a spurious all-empty row
# between every real row — an artifact of the whitespace gap between
# table rows, not real data. Try "lines" first (more precise when a
# table genuinely has drawn borders, e.g. a scanned or native business
# document), fall back to "text" when that finds nothing on a page, and
# always drop fully-blank rows regardless of which strategy produced
# them — a table row with zero non-empty cells is never real content.
#
# min_words_vertical/min_words_horizontal lowered from pdfplumber's own
# default (3) to 2: a small table (e.g. 2 columns x 2 rows) has too few
# aligned words per column/row to clear the default threshold at all —
# verified against a real 2-column table that the default silently
# missed entirely. Lowering to 2 catches it with zero regression
# against a real 3-column, 4-row table (byte-identical output at 2 vs.
# 3). Going lower still (1) was also tested and rejected: it starts
# treating body paragraphs as spurious "table" rows, splitting
# individual words into their own cells.
_TEXT_STRATEGY = {
    "vertical_strategy": "text",
    "horizontal_strategy": "text",
    "min_words_vertical": 2,
    "min_words_horizontal": 2,
}


def extract_page_tables(page):
    tables = page.extract_tables()
    if not tables:
        tables = page.extract_tables(table_settings=_TEXT_STRATEGY)
    out = []
    for table in tables:
        rows = [["" if cell is None else str(cell) for cell in row] for row in table]
        rows = [row for row in rows if any(cell.strip() for cell in row)]
        if rows:
            out.append({"rows": rows})
    return out


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("input")
    parser.add_argument("--start", type=int, default=1, help="1-indexed first page (default 1)")
    parser.add_argument("--end", type=int, default=None, help="1-indexed last page (default: last page)")
    args = parser.parse_args()

    try:
        import pdfplumber
    except ImportError as exc:
        print(f"pdfplumber not installed: {exc}", file=sys.stderr)
        return 1

    try:
        pdf = pdfplumber.open(args.input)
    except Exception as exc:  # noqa: BLE001 - surfacing any open failure verbatim is the point
        print(f"could not open {args.input!r}: {exc}", file=sys.stderr)
        return 1

    try:
        total = len(pdf.pages)
        start = max(1, args.start)
        end = min(args.end, total) if args.end else total
        if start > end:
            print(f"start page {start} is after end page {end} (PDF has {total} pages)", file=sys.stderr)
            return 1

        pages_out = []
        for i in range(start - 1, end):
            page = pdf.pages[i]
            pages_out.append({"page": i + 1, "tables": extract_page_tables(page)})

        print(json.dumps({"pages": pages_out}))
        return 0
    finally:
        pdf.close()


if __name__ == "__main__":
    sys.exit(main())
