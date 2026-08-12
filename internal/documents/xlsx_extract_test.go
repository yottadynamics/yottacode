package documents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
