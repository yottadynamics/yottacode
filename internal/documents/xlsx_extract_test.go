package documents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func writeXLSXFixture(t *testing.T, dir string, model SheetModel) string {
	t.Helper()
	data, err := GenerateXLSX(model)
	if err != nil {
		t.Fatalf("GenerateXLSX: %v", err)
	}
	path := filepath.Join(dir, "book.xlsx")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestXLSXExtractorMatch(t *testing.T) {
	e := &XLSXExtractor{}
	if !e.Match("book.xlsx") || !e.Match("BOOK.XLSX") {
		t.Error("expected .xlsx (any case) to match")
	}
	if e.Match("book.xls") {
		t.Error("expected legacy .xls not to match")
	}
}

func TestXLSXExtractorHappyPath(t *testing.T) {
	dir := t.TempDir()
	model := SheetModel{Sheets: []Sheet{{
		Name: "Data",
		Rows: [][]Cell{
			{{Value: "Item"}, {Value: "Qty"}},
			{{Value: "Widget"}, {Value: 3.0}},
		},
	}}}
	path := writeXLSXFixture(t, dir, model)

	e := &XLSXExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if res.Metadata.Kind != "xlsx" {
		t.Errorf("Kind = %q, want xlsx", res.Metadata.Kind)
	}
	if !strings.Contains(res.Metadata.Shape, "1 sheets: Data") {
		t.Errorf("Shape = %q, want to mention 1 sheets: Data", res.Metadata.Shape)
	}
	if res.Metadata.RowCount != 2 {
		t.Errorf("RowCount = %d, want 2", res.Metadata.RowCount)
	}
	if len(res.Sections) != 1 || res.Sections[0].Label != "sheet Data" {
		t.Fatalf("expected one 'sheet Data' section, got %+v", res.Sections)
	}
	if !strings.Contains(res.Sections[0].Text, "Widget | 3") {
		t.Errorf("expected row content, got %q", res.Sections[0].Text)
	}
}

func TestXLSXExtractorMultipleSheetsAndOffsetSpansSheets(t *testing.T) {
	dir := t.TempDir()
	model := SheetModel{Sheets: []Sheet{
		{Name: "First", Rows: [][]Cell{{{Value: "a1"}}, {{Value: "a2"}}}},
		{Name: "Second", Rows: [][]Cell{{{Value: "b1"}}, {{Value: "b2"}}}},
	}}
	path := writeXLSXFixture(t, dir, model)

	e := &XLSXExtractor{}
	// Offset 3 skips both rows of "First" and the first row of "Second".
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path, Offset: 3})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(res.Sections) != 1 || res.Sections[0].Label != "sheet Second" {
		t.Fatalf("expected only the tail of sheet Second, got %+v", res.Sections)
	}
	if res.Sections[0].Text != "b2" {
		t.Errorf("expected only the last row, got %q", res.Sections[0].Text)
	}
	if res.Metadata.RowCount != 4 {
		t.Errorf("RowCount = %d, want 4 (total across both sheets)", res.Metadata.RowCount)
	}
}

func TestXLSXExtractorOffsetPastLastRowIsReported(t *testing.T) {
	dir := t.TempDir()
	model := SheetModel{Sheets: []Sheet{{Rows: [][]Cell{{{Value: "only"}}}}}}
	path := writeXLSXFixture(t, dir, model)

	e := &XLSXExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path, Offset: 5})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(res.Sections) != 0 {
		t.Errorf("expected no sections past the last row, got %+v", res.Sections)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "past the last row") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a 'past the last row' warning, got %v", res.Warnings)
	}
}

func TestXLSXExtractorMaxRowsCap(t *testing.T) {
	dir := t.TempDir()
	rows := make([][]Cell, 5)
	for i := range rows {
		rows[i] = []Cell{{Value: "row"}}
	}
	model := SheetModel{Sheets: []Sheet{{Rows: rows}}}
	path := writeXLSXFixture(t, dir, model)

	e := &XLSXExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path, MaxRows: 2})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if res.Metadata.RowCount != 5 {
		t.Errorf("RowCount = %d, want 5 (true total, not capped count)", res.Metadata.RowCount)
	}
	gotRows := strings.Count(res.Sections[0].Text, "\n") + 1
	if gotRows != 2 {
		t.Errorf("expected 2 rows in the preview under MaxRows=2, got %d: %q", gotRows, res.Sections[0].Text)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "preview cap") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a preview-cap warning, got %v", res.Warnings)
	}
}

func TestXLSXExtractorFormulaSectionRendered(t *testing.T) {
	dir := t.TempDir()
	model := SheetModel{Sheets: []Sheet{{
		Name: "Data",
		Rows: [][]Cell{
			{{Value: "Item"}, {Value: "Qty"}},
			{{Value: "Widget"}, {Value: 2.0}},
			{{Value: "Total"}, {Formula: "SUM(B2:B2)"}},
		},
	}}}
	path := writeXLSXFixture(t, dir, model)

	e := &XLSXExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	var formulaSec *DocumentSection
	for i := range res.Sections {
		if res.Sections[i].Label == "sheet Data formulas" {
			formulaSec = &res.Sections[i]
		}
	}
	if formulaSec == nil {
		t.Fatalf("expected a %q section, got: %+v", "sheet Data formulas", res.Sections)
	}
	if formulaSec.Text != "B3: =SUM(B2:B2)" {
		t.Errorf("formula section = %q, want %q", formulaSec.Text, "B3: =SUM(B2:B2)")
	}
	// excelize never recalculates on write, so a freshly-generated
	// formula cell has no cached value and GetRows returns "" for it —
	// exactly the gap the formulas section exists to fill: the plain
	// "sheet Data" text alone would show "Total |" with no indication
	// anything computes it.
	if !strings.Contains(res.Sections[0].Text, "Total |") {
		t.Errorf("expected the plain section to still list the row, got %q", res.Sections[0].Text)
	}
}

func TestXLSXExtractorNoFormulaProducesNoFormulaSection(t *testing.T) {
	dir := t.TempDir()
	model := SheetModel{Sheets: []Sheet{{Rows: [][]Cell{{{Value: "plain"}}}}}}
	path := writeXLSXFixture(t, dir, model)

	e := &XLSXExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, sec := range res.Sections {
		if strings.Contains(sec.Label, "formulas") {
			t.Errorf("expected no formulas section for a sheet with no formulas, got %+v", sec)
		}
	}
}

// TestXLSXExtractorFormulaRespectsRowWindow confirms formula reporting
// is scoped to the same rows the row/char-capped preview actually
// included, not the whole sheet.
func TestXLSXExtractorFormulaRespectsRowWindow(t *testing.T) {
	dir := t.TempDir()
	model := SheetModel{Sheets: []Sheet{{
		Rows: [][]Cell{
			{{Formula: "1+1"}},
			{{Formula: "2+2"}},
		},
	}}}
	path := writeXLSXFixture(t, dir, model)

	e := &XLSXExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path, MaxRows: 1})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	var formulaSec *DocumentSection
	for i := range res.Sections {
		if strings.Contains(res.Sections[i].Label, "formulas") {
			formulaSec = &res.Sections[i]
		}
	}
	if formulaSec == nil {
		t.Fatalf("expected a formulas section, got: %+v", res.Sections)
	}
	if strings.Contains(formulaSec.Text, "2+2") {
		t.Errorf("expected formula reporting to respect MaxRows, got %q", formulaSec.Text)
	}
}

// writeXLSXFileFixture builds a workbook via excelize directly (not
// GenerateXLSX) for fixtures needing structure GenerateXLSX/SheetModel
// don't expose — merged cells, embedded pictures.
func writeXLSXFileFixture(t *testing.T, dir string, build func(f *excelize.File)) string {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	build(f)
	path := filepath.Join(dir, "book.xlsx")
	if err := f.SaveAs(path); err != nil {
		t.Fatalf("SaveAs: %v", err)
	}
	return path
}

func TestXLSXExtractorMergedCellSectionRendered(t *testing.T) {
	dir := t.TempDir()
	path := writeXLSXFileFixture(t, dir, func(f *excelize.File) {
		_ = f.SetCellValue("Sheet1", "A1", "Quarterly Report")
		_ = f.MergeCell("Sheet1", "A1", "C1")
		_ = f.SetCellValue("Sheet1", "A2", "data")
	})

	e := &XLSXExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	var mergeSec *DocumentSection
	for i := range res.Sections {
		if res.Sections[i].Label == "sheet Sheet1 merged cells" {
			mergeSec = &res.Sections[i]
		}
	}
	if mergeSec == nil {
		t.Fatalf("expected a %q section, got: %+v", "sheet Sheet1 merged cells", res.Sections)
	}
	if !strings.Contains(mergeSec.Text, "A1:C1") || !strings.Contains(mergeSec.Text, "Quarterly Report") {
		t.Errorf("merged cell section = %q, want it to mention A1:C1 and the merged value", mergeSec.Text)
	}
}

func TestXLSXExtractorNoMergeProducesNoMergedCellSection(t *testing.T) {
	dir := t.TempDir()
	model := SheetModel{Sheets: []Sheet{{Rows: [][]Cell{{{Value: "plain"}}}}}}
	path := writeXLSXFixture(t, dir, model)

	e := &XLSXExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, sec := range res.Sections {
		if strings.Contains(sec.Label, "merged cells") {
			t.Errorf("expected no merged-cells section for a sheet with no merges, got %+v", sec)
		}
	}
}

func TestXLSXExtractorImageSectionRendered(t *testing.T) {
	dir := t.TempDir()
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9c, 0x62, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
		0x42, 0x60, 0x82,
	}
	path := writeXLSXFileFixture(t, dir, func(f *excelize.File) {
		_ = f.SetCellValue("Sheet1", "A1", "data")
		_ = f.AddPictureFromBytes("Sheet1", "B2", &excelize.Picture{
			Extension: ".png",
			File:      png,
			Format:    &excelize.GraphicOptions{AltText: "A logo"},
		})
	})

	e := &XLSXExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	var imgSec *DocumentSection
	for i := range res.Sections {
		if strings.Contains(res.Sections[i].Label, "image") {
			imgSec = &res.Sections[i]
		}
	}
	if imgSec == nil {
		t.Fatalf("expected an image section, got: %+v", res.Sections)
	}
	if imgSec.Label != "sheet Sheet1 image B2-1" {
		t.Errorf("image section label = %q, want %q", imgSec.Label, "sheet Sheet1 image B2-1")
	}
	for _, want := range []string{"type: png", "size: 1x1 px", "alt: A logo"} {
		if !strings.Contains(imgSec.Text, want) {
			t.Errorf("image section text %q missing %q", imgSec.Text, want)
		}
	}
}

func TestXLSXExtractorNoImageProducesNoImageSection(t *testing.T) {
	dir := t.TempDir()
	model := SheetModel{Sheets: []Sheet{{Rows: [][]Cell{{{Value: "plain"}}}}}}
	path := writeXLSXFixture(t, dir, model)

	e := &XLSXExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, sec := range res.Sections {
		if strings.Contains(sec.Label, "image") {
			t.Errorf("expected no image section for a sheet with no pictures, got %+v", sec)
		}
	}
}

// TestXLSXExtractorImageOnlySheetStillReportsImageMetadata confirms picture
// metadata is not gated on GetRows returning visible row text: image-only
// sheets are valid workbooks and still have extractable metadata.
func TestXLSXExtractorImageOnlySheetStillReportsImageMetadata(t *testing.T) {
	dir := t.TempDir()
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9c, 0x62, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
		0x42, 0x60, 0x82,
	}
	path := writeXLSXFileFixture(t, dir, func(f *excelize.File) {
		_ = f.AddPictureFromBytes("Sheet1", "B2", &excelize.Picture{Extension: ".png", File: png})
	})

	e := &XLSXExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !hasSectionLabel(res.Sections, "sheet Sheet1 image B2-1") {
		t.Fatalf("expected image metadata for an image-only sheet, got sections: %+v", res.Sections)
	}
}

// TestXLSXExtractorMergedCellsSurviveRowOffset confirms sheet-structure
// metadata remains visible even when the row preview window starts beyond the
// rows containing the merged cells.
func TestXLSXExtractorMergedCellsSurviveRowOffset(t *testing.T) {
	dir := t.TempDir()
	path := writeXLSXFileFixture(t, dir, func(f *excelize.File) {
		_ = f.SetCellValue("Sheet1", "A1", "Quarterly Report")
		_ = f.MergeCell("Sheet1", "A1", "C1")
	})

	e := &XLSXExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path, Offset: 10})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !hasSectionLabel(res.Sections, "sheet Sheet1 merged cells") {
		t.Fatalf("expected merged-cell metadata despite row offset, got sections: %+v", res.Sections)
	}
}

func TestXLSXExtractorMetadataSectionsRespectMaxChars(t *testing.T) {
	dir := t.TempDir()
	path := writeXLSXFileFixture(t, dir, func(f *excelize.File) {
		_ = f.SetCellValue("Sheet1", "A1", "x")
		_ = f.SetCellFormula("Sheet1", "B1", strings.Repeat("1+", 20)+"1")
		_ = f.MergeCell("Sheet1", "A2", "C2")
	})

	e := &XLSXExtractor{}
	res, err := e.Extract(context.Background(), ExtractRequest{Path: path, MaxChars: 8})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	total := 0
	for _, sec := range res.Sections {
		total += len(sec.Text)
	}
	if total > 8 {
		t.Fatalf("sections used %d chars, want <= 8: %+v", total, res.Sections)
	}
	if !containsWarning(res.Warnings, "preview cap") {
		t.Fatalf("expected a preview-cap warning, got %v", res.Warnings)
	}
	if len(res.Sections) == 0 {
		t.Fatalf("expected at least one bounded section")
	}
}

func TestXLSXExtractorRejectsOversizedFile(t *testing.T) {
	dir := t.TempDir()
	model := SheetModel{Sheets: []Sheet{{Rows: [][]Cell{{{Value: strings.Repeat("x", 1000)}}}}}}
	path := writeXLSXFixture(t, dir, model)

	e := &XLSXExtractor{}
	_, err := e.Extract(context.Background(), ExtractRequest{Path: path, MaxBytes: 10})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected a size-cap error, got %v", err)
	}
}
