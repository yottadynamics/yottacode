package documents

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/xuri/excelize/v2"
)

// Cell is one spreadsheet cell. Value holds a string, float64, int, or bool
// literal; Formula, when non-empty, is written as a formula instead and
// Value is ignored.
type Cell struct {
	Value        any
	Formula      string
	Bold         bool
	Italic       bool
	NumberFormat string
}

// Sheet is one spreadsheet tab: a name plus its rows, top to bottom,
// left to right.
type Sheet struct {
	Name string
	Rows [][]Cell
}

// SheetModel is the intermediate representation for xlsx generation — see
// docs/document-generation.md's "Two canonical intermediate
// representations". It is a thin wrapper over excelize's own object model
// rather than a new invention, since excelize already owns xlsx generation
// end to end (including formula recalculation), unlike the docx/pdf path.
type SheetModel struct {
	Sheets []Sheet
}

// GenerateXLSX renders model into xlsx file bytes via excelize. Pure Go, no
// subprocess — this path never needs the command sandbox.
func GenerateXLSX(model SheetModel) ([]byte, error) {
	if len(model.Sheets) == 0 {
		return nil, fmt.Errorf("xlsx: at least one sheet is required")
	}

	f := excelize.NewFile()
	defer f.Close()

	// excelize.NewFile() starts with one default sheet ("Sheet1"); rename
	// it to the first requested sheet instead of creating a redundant
	// extra tab, then create the rest.
	//
	// excelize's own NewSheet/SetSheetName silently reuse the existing
	// sheet's index on a name collision instead of erroring — without the
	// seenNames check below, two sheets sharing a name (explicit
	// duplicates, or a later sheet named e.g. "Sheet1" colliding with an
	// earlier unnamed sheet's auto-generated default) would silently
	// write into the same tab, with the second sheet's data overwriting
	// the first's and no error surfaced.
	styles := map[styleKey]int{}
	seenNames := make(map[string]bool, len(model.Sheets))
	for i, sheet := range model.Sheets {
		name := sheetName(sheet, i)
		if seenNames[name] {
			return nil, fmt.Errorf("xlsx: duplicate sheet name %q (sheet %d)", name, i)
		}
		seenNames[name] = true
		if i == 0 {
			if err := f.SetSheetName("Sheet1", name); err != nil {
				return nil, fmt.Errorf("xlsx: rename default sheet: %w", err)
			}
		} else if _, err := f.NewSheet(name); err != nil {
			return nil, fmt.Errorf("xlsx: create sheet %q: %w", name, err)
		}
		if err := writeSheet(f, name, sheet, styles); err != nil {
			return nil, fmt.Errorf("xlsx: sheet %q: %w", name, err)
		}
	}
	idx, err := f.GetSheetIndex(sheetName(model.Sheets[0], 0))
	if err != nil {
		return nil, fmt.Errorf("xlsx: locate first sheet: %w", err)
	}
	f.SetActiveSheet(idx)

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, fmt.Errorf("xlsx: write: %w", err)
	}
	return buf.Bytes(), nil
}

func sheetName(sheet Sheet, index int) string {
	if sheet.Name != "" {
		return sheet.Name
	}
	return fmt.Sprintf("Sheet%d", index+1)
}

func writeSheet(f *excelize.File, name string, sheet Sheet, styles map[styleKey]int) error {
	for r, row := range sheet.Rows {
		for c, cell := range row {
			ref, err := excelize.CoordinatesToCellName(c+1, r+1)
			if err != nil {
				return fmt.Errorf("cell (%d,%d): %w", r, c, err)
			}
			// excelize expects a formula without the leading "="; strip one
			// if present so a caller that writes "=SUM(...)" out of habit
			// doesn't end up with a literal doubled "=" cell. A bare "="
			// (no expression) trims to empty — excelize.SetCellFormula
			// treats an empty formula as "clear any formula", so that case
			// falls through to the Value branch instead of silently
			// discarding Value and leaving the cell blank.
			formula := strings.TrimPrefix(cell.Formula, "=")
			if formula != "" {
				if err := f.SetCellFormula(name, ref, formula); err != nil {
					return fmt.Errorf("cell %s formula: %w", ref, err)
				}
			} else if cell.Value != nil {
				if err := f.SetCellValue(name, ref, cell.Value); err != nil {
					return fmt.Errorf("cell %s value: %w", ref, err)
				}
			}
			if cell.Bold || cell.Italic || cell.NumberFormat != "" {
				styleID, err := cellStyle(f, styles, cell)
				if err != nil {
					return fmt.Errorf("cell %s style: %w", ref, err)
				}
				if err := f.SetCellStyle(name, ref, ref, styleID); err != nil {
					return fmt.Errorf("cell %s apply style: %w", ref, err)
				}
			}
		}
	}
	return nil
}

// styleKey identifies one bold/italic/number-format combination so
// repeated formatting (e.g. a header row) reuses one style ID instead of
// registering a new style per cell — excelize style IDs are a finite,
// file-wide table.
type styleKey struct {
	Bold, Italic bool
	NumberFormat string
}

func cellStyle(f *excelize.File, cache map[styleKey]int, cell Cell) (int, error) {
	key := styleKey{Bold: cell.Bold, Italic: cell.Italic, NumberFormat: cell.NumberFormat}
	if id, ok := cache[key]; ok {
		return id, nil
	}
	style := &excelize.Style{}
	if cell.Bold || cell.Italic {
		style.Font = &excelize.Font{Bold: cell.Bold, Italic: cell.Italic}
	}
	if cell.NumberFormat != "" {
		style.CustomNumFmt = &cell.NumberFormat
	}
	id, err := f.NewStyle(style)
	if err != nil {
		return 0, err
	}
	cache[key] = id
	return id, nil
}
