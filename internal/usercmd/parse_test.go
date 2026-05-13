package usercmd

import (
	"strings"
	"testing"
)

func TestParse_NoFrontmatter(t *testing.T) {
	got, err := parse([]byte("hello world\n"))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Frontmatter.Description != "" {
		t.Errorf("description = %q, want empty", got.Frontmatter.Description)
	}
	if got.Body != "hello world\n" {
		t.Errorf("body = %q, want %q", got.Body, "hello world\n")
	}
}

func TestParse_WithFrontmatter(t *testing.T) {
	src := "---\ndescription: Review a PR\nargument-hint: <pr-num>\n---\nbody line one\nbody line two\n"
	got, err := parse([]byte(src))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Frontmatter.Description != "Review a PR" {
		t.Errorf("description = %q", got.Frontmatter.Description)
	}
	if got.Frontmatter.ArgumentHint != "<pr-num>" {
		t.Errorf("argument-hint = %q", got.Frontmatter.ArgumentHint)
	}
	if got.Body != "body line one\nbody line two\n" {
		t.Errorf("body = %q", got.Body)
	}
}

func TestParse_OnlyFrontmatter(t *testing.T) {
	got, err := parse([]byte("---\ndescription: only meta\n---\n"))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Frontmatter.Description != "only meta" {
		t.Errorf("description = %q", got.Frontmatter.Description)
	}
	if got.Body != "" {
		t.Errorf("body = %q, want empty", got.Body)
	}
}

func TestParse_UnknownKeysIgnored(t *testing.T) {
	src := "---\ndescription: x\nmodel: gpt-9\nallowed-tools: [a, b]\n---\nbody\n"
	got, err := parse([]byte(src))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Frontmatter.Description != "x" {
		t.Errorf("description = %q", got.Frontmatter.Description)
	}
	if got.Body != "body\n" {
		t.Errorf("body = %q", got.Body)
	}
}

func TestParse_MissingClosingDelimErrors(t *testing.T) {
	src := "---\ndescription: oops\nbody with no closer\n"
	_, err := parse([]byte(src))
	if err == nil {
		t.Fatal("expected an error for missing closing delimiter")
	}
	if !strings.Contains(err.Error(), "closing") {
		t.Errorf("error message = %q, want it to mention 'closing'", err.Error())
	}
}

func TestParse_MalformedYAMLErrors(t *testing.T) {
	// `description: : x` is YAML "unexpected key" garbage.
	src := "---\ndescription: : x\n---\nbody\n"
	_, err := parse([]byte(src))
	if err == nil {
		t.Fatal("expected a YAML parse error")
	}
}
