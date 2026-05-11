package agent

import (
	"context"
	"strings"
	"testing"
)

func newTodoWriteTool() *TodoWriteTool {
	return &TodoWriteTool{Store: NewPlanStore()}
}

func TestTodoWriteTool_StaticMetadata(t *testing.T) {
	tool := newTodoWriteTool()
	if tool.Name() != "todo_write" {
		t.Errorf("Name = %q, want todo_write", tool.Name())
	}
	if tool.RequiresApproval("") {
		t.Errorf("RequiresApproval = true, want false (todo_write is a visibility primitive, gating lives on the mutating tools)")
	}
	schema := tool.Schema()
	if schema["type"] != "object" {
		t.Errorf("Schema type = %v, want object", schema["type"])
	}
	if _, ok := schema["properties"].(map[string]any)["todos"]; !ok {
		t.Errorf("Schema missing 'todos' property")
	}
}

func TestTodoWriteTool_ImplementsPlanAware(t *testing.T) {
	var _ planAware = newTodoWriteTool()
}

func TestTodoWriteTool_ReplacesPlan(t *testing.T) {
	tool := newTodoWriteTool()
	args := `{"todos":[
		{"content":"step one","status":"completed"},
		{"content":"step two","status":"in_progress"},
		{"content":"step three","status":"pending"}
	]}`
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "3 items") || !strings.Contains(out, "1 done") {
		t.Errorf("output = %q, want 3 items/1 done", out)
	}
	got := tool.Store.Snapshot()
	if len(got) != 3 {
		t.Fatalf("Snapshot len = %d, want 3", len(got))
	}
	if got[1].Status != TodoInProgress {
		t.Errorf("second item status = %q, want in_progress", got[1].Status)
	}
}

func TestTodoWriteTool_EmptyClearsPlan(t *testing.T) {
	tool := newTodoWriteTool()
	tool.Store.Replace([]Todo{{Content: "existing", Status: TodoPending}})
	out, err := tool.Execute(context.Background(), `{"todos":[]}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "cleared") {
		t.Errorf("output = %q, want 'cleared'", out)
	}
	if got := tool.Store.Snapshot(); got != nil {
		t.Errorf("plan not cleared, got %+v", got)
	}
}

func TestTodoWriteTool_RejectsEmptyContent(t *testing.T) {
	tool := newTodoWriteTool()
	args := `{"todos":[{"content":"","status":"pending"}]}`
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Errorf("expected error on empty content")
	}
}

func TestTodoWriteTool_RejectsUnknownStatus(t *testing.T) {
	tool := newTodoWriteTool()
	args := `{"todos":[{"content":"a","status":"halfway"}]}`
	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatalf("expected error on unknown status")
	}
	if !strings.Contains(err.Error(), "halfway") {
		t.Errorf("error should name the bad status; got: %v", err)
	}
}

func TestTodoWriteTool_RejectsEmptyStatus(t *testing.T) {
	tool := newTodoWriteTool()
	args := `{"todos":[{"content":"a","status":""}]}`
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Errorf("expected error on empty status")
	}
}

func TestTodoWriteTool_RejectsMultipleInProgress(t *testing.T) {
	tool := newTodoWriteTool()
	args := `{"todos":[
		{"content":"a","status":"in_progress"},
		{"content":"b","status":"in_progress"}
	]}`
	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatalf("expected error on multiple in_progress")
	}
	if !strings.Contains(err.Error(), "in_progress") {
		t.Errorf("error should mention in_progress; got: %v", err)
	}
}

func TestTodoWriteTool_BadJSON(t *testing.T) {
	tool := newTodoWriteTool()
	if _, err := tool.Execute(context.Background(), `not json`); err == nil {
		t.Errorf("expected error on bad JSON")
	}
}

func TestTodoWriteTool_TrimsContent(t *testing.T) {
	tool := newTodoWriteTool()
	args := `{"todos":[{"content":"  step  ","status":"pending"}]}`
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := tool.Store.Snapshot()[0].Content; got != "step" {
		t.Errorf("Content = %q, want trimmed 'step'", got)
	}
}

func TestTodoWriteTool_PreviewCall(t *testing.T) {
	tool := newTodoWriteTool()
	args := `{"todos":[
		{"content":"a","status":"completed"},
		{"content":"b","status":"completed"},
		{"content":"c","status":"pending"}
	]}`
	preview := tool.PreviewCall(args)
	if !strings.Contains(preview, "3 items") || !strings.Contains(preview, "2 done") {
		t.Errorf("PreviewCall = %q, want 3 items / 2 done", preview)
	}
}

func TestTodoWriteTool_NoStoreErrors(t *testing.T) {
	tool := &TodoWriteTool{}
	_, err := tool.Execute(context.Background(), `{"todos":[]}`)
	if err == nil {
		t.Errorf("expected error when Store is nil")
	}
}

