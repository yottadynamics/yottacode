package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestDemo_PRFormatting renders the user-reported PR body and pr_create popup
// for manual inspection. Run with:
// go test ./internal/tui -run TestDemo_PRFormatting -v
func TestDemo_PRFormatting(t *testing.T) {
	if testing.Short() {
		t.Skip("visual formatting demo — run without -short")
	}

	body := strings.Join([]string{
		"## Summary",
		"",
		"Adds standard `Edit(...)` permission coverage for `edit_anchored`, alongside `edit_file` and `apply_hashline`. Improves anchored-edit no-op errors by identifying the failing operation and preserving the existing atomic batch behavior.",
		"",
		"## Related issue",
		"",
		"Not linked.",
		"",
		"## How was this tested?",
		"",
		"- `go test ./internal/agent ./internal/permissions ./internal/tui`",
		"- `go vet ./internal/agent ./internal/permissions ./internal/tui`",
		"",
		"Both passed.",
		"",
		"## Checklist",
		"",
		"- [ ] `go test ./...` passes",
		"- [ ] `go vet ./...` is clean",
		"- [ ] `govulncheck ./...` reports no reachable vulnerabilities",
		"- [x] Tests added or updated",
		"- [x] Docs updated",
		"- [x] PR is focused on one logical change",
		"",
		"---",
		"",
		"Drafted with [yottacode](https://yottacode.ai)",
	}, "\n")

	m := newTestModel(t)
	m.width = 80
	m.height = 24

	fmt.Println("─── PR transcript body ───")
	fmt.Println(stripANSI(renderAssistantBlock(body)))
	fmt.Println()

	fmt.Println("─── pr_create approval modal ───")
	m.awaitingApproval = true
	m.approvalTool = "pr_create"
	m.approvalPreview = "pr_create(base=main, title=\"Unify anchored edit permissions and diagnostics\")\n\n" + body
	args := map[string]string{"base": "main", "title": "Unify anchored edit permissions and diagnostics", "body": body}
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	m.approvalArgs = string(encoded)
	fmt.Println(stripANSI(renderApprovalModal(m)))
}
