package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/skills"
)

func TestSkillTool_NameAndSchema(t *testing.T) {
	tool := &SkillTool{}
	if tool.Name() != "Skill" {
		t.Errorf("name = %q, want Skill", tool.Name())
	}
	s := tool.Schema()
	if s["type"] != "object" {
		t.Errorf("schema type = %v", s["type"])
	}
	req, _ := s["required"].([]string)
	if len(req) != 1 || req[0] != "skill" {
		t.Errorf("required = %v, want [skill]", req)
	}
}

func TestSkillTool_DescriptionListsSkills(t *testing.T) {
	tool := &SkillTool{All: []skills.Skill{
		{Name: "alpha", Description: "first."},
		{Name: "beta", Description: "second."},
	}}
	d := tool.Description()
	if !strings.Contains(d, "- alpha: first.") {
		t.Errorf("description missing alpha row: %q", d)
	}
	if !strings.Contains(d, "- beta: second.") {
		t.Errorf("description missing beta row: %q", d)
	}
}

func TestSkillTool_DescriptionSortedByName(t *testing.T) {
	tool := &SkillTool{All: []skills.Skill{
		{Name: "zulu", Description: "last."},
		{Name: "alpha", Description: "first."},
	}}
	d := tool.Description()
	if strings.Index(d, "alpha") > strings.Index(d, "zulu") {
		t.Errorf("description not sorted: alpha after zulu in %q", d)
	}
}

func TestSkillTool_Execute_ReturnsBody(t *testing.T) {
	tool := &SkillTool{All: []skills.Skill{
		{Name: "foo", Description: "x", Body: "## the body"},
	}}
	got, err := tool.Execute(context.Background(), `{"skill":"foo"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got != "## the body" {
		t.Errorf("body = %q", got)
	}
}

func TestSkillTool_Execute_UnknownReturnsErrorString(t *testing.T) {
	tool := &SkillTool{All: []skills.Skill{
		{Name: "foo", Description: "x", Body: "b"},
	}}
	got, err := tool.Execute(context.Background(), `{"skill":"missing"}`)
	if err != nil {
		t.Fatalf("expected nil error (recoverable result), got %v", err)
	}
	if !strings.Contains(got, "unknown skill") {
		t.Errorf("expected unknown-skill error, got %q", got)
	}
	if !strings.Contains(got, "foo") {
		t.Errorf("expected available list to mention foo, got %q", got)
	}
}

func TestSkillTool_Execute_MissingSkillArg(t *testing.T) {
	tool := &SkillTool{}
	if _, err := tool.Execute(context.Background(), `{}`); err == nil {
		t.Error("expected error when skill arg is missing")
	}
}

func TestSkillTool_PreviewCall(t *testing.T) {
	tool := &SkillTool{}
	if got := tool.PreviewCall(`{"skill":"remote-ops"}`); got != "Skill[remote-ops]" {
		t.Errorf("preview = %q", got)
	}
}

func TestSkillTool_ParallelSafe(t *testing.T) {
	tool := &SkillTool{}
	if !tool.ParallelSafe("") {
		t.Error("Skill should be parallel-safe")
	}
}

func TestSkillTool_NeverRequiresApproval(t *testing.T) {
	tool := &SkillTool{}
	if tool.RequiresApproval("") {
		t.Error("Skill should not require approval — text injection only")
	}
}

func TestSkillTool_EnabledFilter(t *testing.T) {
	tool := &SkillTool{All: []skills.Skill{
		{Name: "alpha", Description: "first.", Body: "a"},
		{Name: "beta", Description: "second.", Body: "b"},
		{Name: "gamma", Description: "third.", Body: "g"},
	}}
	if !tool.IsEnabled("alpha") {
		t.Error("default is all-enabled; alpha should be enabled")
	}
	if got := tool.Active(); len(got) != 3 {
		t.Errorf("default active count = %d, want 3", len(got))
	}

	tool.SetEnabled(map[string]bool{"alpha": true, "gamma": true})
	if tool.IsEnabled("beta") {
		t.Error("beta should be disabled after SetEnabled excludes it")
	}
	active := tool.Active()
	if len(active) != 2 || active[0].Name != "alpha" || active[1].Name != "gamma" {
		t.Errorf("active after partial enable = %v", active)
	}

	desc := tool.Description()
	if strings.Contains(desc, "- beta:") {
		t.Errorf("description should hide disabled beta: %q", desc)
	}
	if !strings.Contains(desc, "- alpha:") {
		t.Errorf("description should still list alpha: %q", desc)
	}

	got, err := tool.Execute(context.Background(), `{"skill":"beta"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "unknown skill") {
		t.Errorf("disabled skill should be unreachable via Execute, got %q", got)
	}

	tool.SetEnabled(nil)
	if !tool.IsEnabled("beta") {
		t.Error("SetEnabled(nil) should reset to all-enabled")
	}
}
