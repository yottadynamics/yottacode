package mcp_test

import (
	"testing"

	"github.com/yottadynamics/yottacode/internal/mcp"
)

func names(tools []mcp.ToolDescriptor) []string {
	out := make([]string, len(tools))
	for i, td := range tools {
		out[i] = td.Name
	}
	return out
}

func TestFilterTools_NoFiltersKeepsEverything(t *testing.T) {
	tools := []mcp.ToolDescriptor{{Name: "get_issue"}, {Name: "delete_issue"}}
	kept, hidden := mcp.FilterTools(tools, nil, nil)
	if hidden != 0 || len(kept) != 2 {
		t.Errorf("FilterTools with no filters = %v hidden=%d, want all 2 kept", names(kept), hidden)
	}
}

func TestFilterTools_IncludeKeepsOnlyMatches(t *testing.T) {
	tools := []mcp.ToolDescriptor{{Name: "get_issue"}, {Name: "list_issues"}, {Name: "delete_issue"}}
	kept, hidden := mcp.FilterTools(tools, []string{"get_*", "list_*"}, nil)
	if hidden != 1 {
		t.Errorf("hidden = %d, want 1", hidden)
	}
	got := names(kept)
	if len(got) != 2 || got[0] != "get_issue" || got[1] != "list_issues" {
		t.Errorf("kept = %v, want [get_issue list_issues]", got)
	}
}

func TestFilterTools_ExcludeDropsMatches(t *testing.T) {
	tools := []mcp.ToolDescriptor{{Name: "get_issue"}, {Name: "delete_issue"}, {Name: "delete_repository"}}
	kept, hidden := mcp.FilterTools(tools, nil, []string{"delete_*"})
	if hidden != 2 {
		t.Errorf("hidden = %d, want 2", hidden)
	}
	if got := names(kept); len(got) != 1 || got[0] != "get_issue" {
		t.Errorf("kept = %v, want [get_issue]", got)
	}
}

func TestFilterTools_IncludeThenExclude(t *testing.T) {
	tools := []mcp.ToolDescriptor{
		{Name: "get_issue"}, {Name: "get_repo"}, {Name: "delete_issue"}, {Name: "create_pull_request"},
	}
	kept, hidden := mcp.FilterTools(tools, []string{"get_*", "create_pull_request"}, []string{"get_repo"})
	if hidden != 2 { // delete_issue (not included) + get_repo (excluded)
		t.Errorf("hidden = %d, want 2", hidden)
	}
	got := names(kept)
	if len(got) != 2 || got[0] != "get_issue" || got[1] != "create_pull_request" {
		t.Errorf("kept = %v, want [get_issue create_pull_request]", got)
	}
}

func TestFilterTools_PreservesOrder(t *testing.T) {
	tools := []mcp.ToolDescriptor{{Name: "c"}, {Name: "a"}, {Name: "b"}}
	kept, _ := mcp.FilterTools(tools, []string{"a", "b", "c"}, nil)
	if got := names(kept); got[0] != "c" || got[1] != "a" || got[2] != "b" {
		t.Errorf("FilterTools should preserve input order; got %v", got)
	}
}
