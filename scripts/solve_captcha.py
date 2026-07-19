#!/usr/bin/env python3
"""Optional OCR helper for captcha check-in.

Usage (from repo root):
  newapi-checkin -config config.yaml -only "简直了" ^
    -captcha-cmd "python scripts/solve_captcha.py {image}"

Install (pick one engine):
  pip install ddddocr
  # or: pip install pillow pytesseract  (and install Tesseract OCR)

Prints the recognized answer on the first stdout line.
Exit non-zero on failure so newapi-checkin can surface the error.
"""

from __future__ import annotations

import sys
from pathlib import Path


def solve_ddddocr(image_path: Path) -> str:
    """Recognize a compact captcha with the optional ddddocr engine."""
    import ddddocr  # type: ignore

    ocr = ddddocr.DdddOcr(show_ad=False)
    with image_path.open("rb") as f:
        answer = ocr.classification(f.read())
    return str(answer).strip()


def solve_tesseract(image_path: Path) -> str:
    """Recognize one text line with Tesseract and keep only answer characters."""
    import pytesseract  # type: ignore
    from PIL import Image  # type: ignore

    text = pytesseract.image_to_string(Image.open(image_path), config="--psm 7")
    return "".join(ch for ch in text if ch.isalnum()).strip()


def main() -> int:
    """Try available OCR engines in priority order and print one answer line."""
    if len(sys.argv) < 2:
        print("usage: solve_captcha.py <image-path>", file=sys.stderr)
        return 2

    image_path = Path(sys.argv[1])
    if not image_path.is_file():
        print(f"image not found: {image_path}", file=sys.stderr)
        return 2

    errors: list[str] = []
    for name, fn in (("ddddocr", solve_ddddocr), ("tesseract", solve_tesseract)):
        try:
            answer = fn(image_path)
            if answer:
                print(answer)
                return 0
            errors.append(f"{name}: empty result")
        except Exception as exc:  # noqa: BLE001 - report engine errors to stderr
            errors.append(f"{name}: {exc}")

    print("no OCR engine produced an answer:", file=sys.stderr)
    for err in errors:
        print(f"  - {err}", file=sys.stderr)
    print("install: pip install ddddocr", file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
