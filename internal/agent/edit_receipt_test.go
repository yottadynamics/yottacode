package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yottadynamics/yottacode/internal/edit/hashline"
)

func TestEditFile_UsesReadReceiptWithoutOldString(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "x.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	receipt := editReceipt{Path: "x.txt", Offset: 0, Length: 11, Old: "alpha\nbeta\n", EndsWithNewline: true, Complete: true}
	anchor, err := hashlineReceiptForTest(receipt.Old)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Hash = anchor
	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	tool := &EditFileTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	if _, err := tool.Execute(context.Background(), `{"path":"x.txt","new_string":"ALPHA\nBETA\n","receipt":`+string(data)+`}`); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ALPHA\nBETA\n" {
		t.Fatalf("file = %q", got)
	}
}

func hashlineReceiptForTest(text string) (string, error) {
	anchor, err := hashline.HashSpan([]byte(text), 0, len(text))
	if err != nil {
		return "", err
	}
	return anchor.Hash, nil
}
