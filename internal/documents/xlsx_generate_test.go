package documents

import (
	"bytes"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestGenerateXLSXRequiresSheets(t *testing.T) {
	if _, err := GenerateXLSX(SheetModel{}); err == nil {
		t.Fatal("expected an error for an empty SheetModel")
	}
}

func TestGenerateXLSXValuesAndFormula(t *testing.T) {
	model := SheetModel{Sheets: []Sheet{{
		Name: "Report",
		Rows: [][]Cell{
			{{Value: "Item"}, {Value: "Qty"}},
			{{Value: "Widget"}, {Value: 3.0}},
			{{Value: "Gadget"}, {Value: 5.0}},
			{{Value: "Total"}, {Formula: "SUM(B2:B3)"}},
		},
	}}}

	data, err := GenerateXLSX(model)
	if err != nil {
		t.Fatalf("GenerateXLSX: %v", err)
	}

	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer f.Close()

	got, err := f.GetCellValue("Report", "A1")
	if err != nil || got != "Item" {
		t.Errorf("A1 = %q, %v; want %q", got, err, "Item")
	}
	qty, err := f.GetCellValue("Report", "B2")
	if err != nil || qty != "3" {
		t.Errorf("B2 = %q, %v; want %q", qty, err, "3")
	}
	formula, err := f.GetCellFormula("Report", "B4")
	if err != nil || formula != "SUM(B2:B3)" {
		t.Errorf("B4 formula = %q, %v; want %q", formula, err, "SUM(B2:B3)")
	}
}

func TestGenerateXLSXFormulaStripsLeadingEquals(t *testing.T) {
	model := SheetModel{Sheets: []Sheet{{
		Rows: [][]Cell{{{Formula: "=SUM(A1:A2)"}}},
	}}}
	data, err := GenerateXLSX(model)
	if err != nil {
		t.Fatalf("GenerateXLSX: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer f.Close()
	formula, err := f.GetCellFormula("Sheet1", "A1")
	if err != nil || formula != "SUM(A1:A2)" {
		t.Errorf("A1 formula = %q, %v; want %q (no leading '=')", formula, err, "SUM(A1:A2)")
	}
}

func TestGenerateXLSXBareEqualsFormulaFallsBackToValue(t *testing.T) {
	model := SheetModel{Sheets: []Sheet{{
		Rows: [][]Cell{{{Value: "fallback text", Formula: "="}}},
	}}}
	data, err := GenerateXLSX(model)
	if err != nil {
		t.Fatalf("GenerateXLSX: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer f.Close()
	got, err := f.GetCellValue("Sheet1", "A1")
	if err != nil || got != "fallback text" {
		t.Errorf("A1 value = %q, %v; want %q (bare '=' formula should fall back to Value)", got, err, "fallback text")
	}
	formula, err := f.GetCellFormula("Sheet1", "A1")
	if err != nil || formula != "" {
		t.Errorf("A1 formula = %q, %v; want empty", formula, err)
	}
}

func TestGenerateXLSXDuplicateSheetNameErrors(t *testing.T) {
	model := SheetModel{Sheets: []Sheet{
		{Name: "Data", Rows: [][]Cell{{{Value: "first"}}}},
		{Name: "Data", Rows: [][]Cell{{{Value: "second"}}}},
	}}
	if _, err := GenerateXLSX(model); err == nil {
		t.Fatal("expected an error for duplicate sheet names")
	}
}

func TestGenerateXLSXExplicitNameCollidesWithDefault(t *testing.T) {
	// Sheet 0 is unnamed (defaults to "Sheet1"); sheet 1 explicitly claims
	// that same default name — must still be caught as a collision.
	model := SheetModel{Sheets: []Sheet{
		{Rows: [][]Cell{{{Value: "first"}}}},
		{Name: "Sheet1", Rows: [][]Cell{{{Value: "second"}}}},
	}}
	if _, err := GenerateXLSX(model); err == nil {
		t.Fatal("expected an error when an explicit name collides with an earlier sheet's default name")
	}
}

func TestGenerateXLSXBoldItalicNumberFormat(t *testing.T) {
	model := SheetModel{Sheets: []Sheet{{
		Rows: [][]Cell{
			{{Value: "Header", Bold: true}},
			{{Value: 0.5, NumberFormat: "0.00%"}},
			{{Value: "Note", Italic: true}},
		},
	}}}

	data, err := GenerateXLSX(model)
	if err != nil {
		t.Fatalf("GenerateXLSX: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer f.Close()

	sheet := "Sheet1"
	headerStyleID, err := f.GetCellStyle(sheet, "A1")
	if err != nil {
		t.Fatalf("GetCellStyle A1: %v", err)
	}
	headerStyle, err := f.GetStyle(headerStyleID)
	if err != nil {
		t.Fatalf("GetStyle: %v", err)
	}
	if headerStyle.Font == nil || !headerStyle.Font.Bold {
		t.Errorf("expected A1 to be bold, got style %+v", headerStyle)
	}

	pctStyleID, err := f.GetCellStyle(sheet, "A2")
	if err != nil {
		t.Fatalf("GetCellStyle A2: %v", err)
	}
	pctStyle, err := f.GetStyle(pctStyleID)
	if err != nil {
		t.Fatalf("GetStyle: %v", err)
	}
	if pctStyle.CustomNumFmt == nil || *pctStyle.CustomNumFmt != "0.00%" {
		t.Errorf("expected A2 number format 0.00%%, got %+v", pctStyle)
	}

	noteStyleID, err := f.GetCellStyle(sheet, "A3")
	if err != nil {
		t.Fatalf("GetCellStyle A3: %v", err)
	}
	noteStyle, err := f.GetStyle(noteStyleID)
	if err != nil {
		t.Fatalf("GetStyle: %v", err)
	}
	if noteStyle.Font == nil || !noteStyle.Font.Italic {
		t.Errorf("expected A3 to be italic, got style %+v", noteStyle)
	}
}

func TestGenerateXLSXMultipleSheetsAndActiveSheet(t *testing.T) {
	model := SheetModel{Sheets: []Sheet{
		{Name: "First", Rows: [][]Cell{{{Value: "a"}}}},
		{Name: "Second", Rows: [][]Cell{{{Value: "b"}}}},
	}}

	data, err := GenerateXLSX(model)
	if err != nil {
		t.Fatalf("GenerateXLSX: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer f.Close()

	names := f.GetSheetList()
	if len(names) != 2 || names[0] != "First" || names[1] != "Second" {
		t.Errorf("sheet list = %v, want [First Second]", names)
	}

	activeIdx := f.GetActiveSheetIndex()
	activeName := f.GetSheetName(activeIdx)
	if activeName != "First" {
		t.Errorf("active sheet = %q, want %q", activeName, "First")
	}
}

func TestGenerateXLSXDefaultSheetNaming(t *testing.T) {
	model := SheetModel{Sheets: []Sheet{{Rows: [][]Cell{{{Value: "x"}}}}}}
	data, err := GenerateXLSX(model)
	if err != nil {
		t.Fatalf("GenerateXLSX: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer f.Close()
	names := f.GetSheetList()
	if len(names) != 1 || names[0] != "Sheet1" {
		t.Errorf("sheet list = %v, want [Sheet1]", names)
	}
}

func TestGenerateXLSXStyleCacheReusesStyleID(t *testing.T) {
	model := SheetModel{Sheets: []Sheet{{
		Rows: [][]Cell{
			{{Value: "a", Bold: true}},
			{{Value: "b", Bold: true}},
		},
	}}}
	data, err := GenerateXLSX(model)
	if err != nil {
		t.Fatalf("GenerateXLSX: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer f.Close()
	s1, err := f.GetCellStyle("Sheet1", "A1")
	if err != nil {
		t.Fatalf("GetCellStyle A1: %v", err)
	}
	s2, err := f.GetCellStyle("Sheet1", "A2")
	if err != nil {
		t.Fatalf("GetCellStyle A2: %v", err)
	}
	if s1 != s2 {
		t.Errorf("expected identical bold cells to share a style ID, got %d and %d", s1, s2)
	}
}
