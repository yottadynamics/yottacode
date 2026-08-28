#!/bin/sh
# Smoke-test the documents image through the same binaries and helper scripts
# production document tools invoke from the documents sandbox profile.
set -eu

pandoc --version
weasyprint --version
pdftotext -v
pdfinfo -v
tesseract --version
python3 --version

echo "# Smoke test" > /tmp/fixture.md
pandoc -f markdown -t docx -o /tmp/fixture.docx /tmp/fixture.md
pandoc -f markdown -t pdf --pdf-engine=weasyprint -o /tmp/fixture.pdf /tmp/fixture.md
pdftotext /tmp/fixture.pdf - | grep "Smoke test"
pdfinfo /tmp/fixture.pdf | grep "Pages"

# Exercise pdfplumber's text-alignment fallback for borderless tables.
printf '%s\n' '# Report' '' '| Name | Qty |' '| --- | --- |' '| Widget | 3 |' > /tmp/table.md
pandoc -f markdown -t pdf --pdf-engine=weasyprint -o /tmp/table.pdf /tmp/table.md
python3 /opt/yottacode/doc-helpers/extract_pdf_tables.py /tmp/table.pdf > /tmp/tables.json
python3 -c 'import json, sys; data=json.load(open(sys.argv[1], encoding="utf-8")); rows=[row for page in data.get("pages", []) for table in page.get("tables", []) for row in table.get("rows", [])]; assert any("Widget" in row and "3" in row for row in rows), data' /tmp/tables.json

# Fill a real docx template, then verify the output through pandoc.
printf '%s\n' 'Dear {{name}}, your total is {{total}}.' > /tmp/template.md
pandoc -f markdown -t docx -o /tmp/template.docx /tmp/template.md
python3 -c 'import json; json.dump({"name":"Smoke Test","total":"42"}, open("/tmp/replacements.json", "w", encoding="utf-8"))'
python3 /opt/yottacode/doc-helpers/fill_docx_template.py /tmp/template.docx /tmp/filled.docx /tmp/replacements.json > /tmp/fill-status.json
python3 -c 'import json, sys; data=json.load(open(sys.argv[1], encoding="utf-8")); assert data.get("replacements_applied") == 1, data' /tmp/fill-status.json
pandoc -f docx -t plain /tmp/filled.docx | grep -q "Smoke Test"

# OCR fallback: convert a text PDF to an image-only PDF, then run pytesseract.
printf '%s\n' '# OCR Smoke Test' '' 'RecognizeThisMarkerText' > /tmp/ocr-source.md
pandoc -f markdown -t pdf --pdf-engine=weasyprint -o /tmp/ocr-source.pdf /tmp/ocr-source.md
pdftoppm -png -r 200 /tmp/ocr-source.pdf /tmp/ocr-page
printf '%s\n' '![](/tmp/ocr-page-1.png)' | pandoc -t pdf --pdf-engine=weasyprint -o /tmp/ocr-image-only.pdf
python3 /opt/yottacode/doc-helpers/extract_pdf_ocr.py /tmp/ocr-image-only.pdf > /tmp/ocr.json
python3 -c 'import json, sys; data=json.load(open(sys.argv[1], encoding="utf-8")); text="".join(p.get("text", "") for p in data.get("pages", [])); assert "RecognizeThisMarkerText" in text, data' /tmp/ocr.json
