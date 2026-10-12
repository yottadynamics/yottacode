package agent

import (
	"context"
	"testing"
)

func TestEditFilePrevalidateRejectsMissingPath(t *testing.T) {
	tool := &EditFileTool{}
	if err := tool.PrevalidateArgs(`{"new_string":"replacement","old_string":"old"}`); err == nil || err.Error() != "edit_file: path is required" {
		t.Fatalf("PrevalidateArgs error = %v", err)
	}
}

func TestEditFileMissingPathDoesNotReachExecute(t *testing.T) {
	tool := &EditFileTool{}
	if _, err := tool.Execute(context.Background(), `{"new_string":"replacement","old_string":"old"}`); err == nil || err.Error() != "edit_file: path is required" {
		t.Fatalf("Execute error = %v", err)
	}
}
