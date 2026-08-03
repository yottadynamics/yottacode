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
	if !strings.Contains(out, "manager\topen=") || !strings.Contains(out, "/2\tbusy=") || !strings.Contains(out, "\tcapacity_hits=") {
		t.Fatalf("status should include manager stats, got %q", out)
	}
	if !strings.Contains(out, "syntax=parser") {
		t.Fatalf("status should include offline syntax capability, got %q", out)
	}
}

func TestLSPStatusReportsCapabilityProbe(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "main.go", "package main")
	tool := &LSPStatusTool{lspToolBase: lspToolBase{
		Cwd: NewCwdRef(tmp),
		NewClient: func(context.Context, lspci.Language, string) (lspClient, error) {
			return &fakeLSPClient{}, nil
		},
	}}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "probe=ok") || !strings.Contains(out, "capabilities=document_symbol,definition,references,rename") {
		t.Fatalf("status should include initialized capability probe, got %q", out)
	}
}
