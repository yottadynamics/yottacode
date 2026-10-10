package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestApplyHashlineTool_AnchorRejectsEachByteFieldIndividually(t *testing.T) {
	fields := []struct {
		name  string
		value any
	}{
		{"offset", 1},
		{"length", 1},
		{"hash", "0000000000000000"},
		{"old", "beta\n"},
	}
	for _, field := range fields {
		t.Run(field.name, func(t *testing.T) {
			tmp := t.TempDir()
			body := "alpha\nbeta\n"
			writeFile(t, tmp, "a.txt", body)
			anchor := fmt.Sprintf("2#%s", anchorHashForLine(2, "beta"))
			args := map[string]any{"anchor": anchor, "new": "BETA\n", field.name: field.value}
			out, err := anchorHunkArgsJSONWithMap(t, "a.txt", args)
			if err != nil {
				t.Fatal(err)
			}
			_, err = newApplyHashline(tmp).Execute(context.Background(), out)
			if err == nil || !strings.Contains(err.Error(), "one addressing mode") {
				t.Fatalf("err = %v, want mixed-addressing validation error", err)
			}
			if got := readBack(t, tmp, "a.txt"); got != body {
				t.Fatalf("file changed after rejected input: %q", got)
			}
		})
	}
}

func anchorHunkArgsJSONWithMap(t *testing.T, path string, hunk map[string]any) (string, error) {
	t.Helper()
	return anchorHunkArgsJSON(t, path, hunk), nil
}
