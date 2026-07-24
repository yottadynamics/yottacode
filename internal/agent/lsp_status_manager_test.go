package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	lspci "github.com/yottadynamics/yottacode/internal/lsp"
)

func TestLSPStatusReportsManagerStats(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "main.go", "package main")
	mgr := lspci.NewManager(2, time.Hour)
	defer mgr.CloseAll()
	tool := &LSPStatusTool{lspToolBase: lspToolBase{Cwd: NewCwdRef(tmp), Manager: mgr}}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "manager\topen=0/2") {
		t.Fatalf("status should include manager stats, got %q", out)
	}
}
