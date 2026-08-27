#!/usr/bin/env python3
"""OCR a PDF's pages via pytesseract, printing one JSON object to
stdout: {"pages": [{"page": N, "text": "..."}]}.

A page pytesseract recognizes no text on still appears with an empty
"text" string, so the caller can tell "checked, found nothing" apart
from "never checked" (e.g. because --start/--end excluded it).

Usage: extract_pdf_ocr.py <input.pdf> [--start N] [--end N] [--lang LANG]
--lang is a Tesseract language code (e.g. "eng", "fra"), or several
joined with "+" (e.g. "eng+fra") to recognize mixed-language text in
one pass — passed straight through to pytesseract, which passes it
straight through to the tesseract binary's own -l flag. Omitted means
pytesseract's own default (English). Requesting a language whose
traineddata isn't installed (only "eng" is guaranteed present — see
infra/documents.Containerfile) fails like any other recognition error
below, not a distinct case: the tesseract binary itself is what
reports "Failed loading language ... Error opening data file".

Exit 1 with a message on stderr for: pytesseract/pdf2image not
installed, the tesseract binary not found, the input file
missing/unreadable, an encrypted PDF pdf2image can't open without a
password, or a --lang whose language pack isn't installed. All are
meant to be treated as "this tier is unavailable right now", not a
crash — see the Go-side caller, which falls back to the plain-text
"may be scanned/image-only" warning on any non-zero exit.
"""
import argparse
import json
import sys


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("input")
    parser.add_argument("--start", type=int, default=1, help="1-indexed first page (default 1)")
    parser.add_argument("--end", type=int, default=None, help="1-indexed last page (default: last page)")
    parser.add_argument("--lang", default=None, help='Tesseract language code, e.g. "eng" or "eng+fra" (default: pytesseract\'s own default, English)')
    args = parser.parse_args()

    try:
        import pytesseract
        from pdf2image import convert_from_path
    except ImportError as exc:
        print(f"pytesseract/pdf2image not installed: {exc}", file=sys.stderr)
        return 1

    try:
        images = convert_from_path(args.input, first_page=max(1, args.start), last_page=args.end)
    except Exception as exc:  # noqa: BLE001 - surfacing any open/render failure verbatim is the point
        print(f"could not rasterize {args.input!r}: {exc}", file=sys.stderr)
        return 1

    start = max(1, args.start)
    pages_out = []
    try:
        for i, image in enumerate(images):
            text = pytesseract.image_to_string(image, lang=args.lang)
            pages_out.append({"page": start + i, "text": text})
    except pytesseract.TesseractNotFoundError as exc:
        print(f"tesseract binary not found: {exc}", file=sys.stderr)
        return 1
    except Exception as exc:  # noqa: BLE001 - surfacing any recognition failure verbatim is the point (including a missing --lang language pack, which tesseract itself reports as an "Error opening data file")
        print(f"OCR recognition failed: {exc}", file=sys.stderr)
        return 1

    print(json.dumps({"pages": pages_out}))
    return 0


if __name__ == "__main__":
    sys.exit(main())
