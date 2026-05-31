package memory

import "testing"

func TestParseFrontmatter_Standard(t *testing.T) {
	data := []byte("---\nname: x\ntype: user\ndescription: d\n---\nbody text\n")
	fm, body, ok := ParseFrontmatter(data)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if fm.Name != "x" || fm.Type != "user" || fm.Description != "d" {
		t.Errorf("frontmatter = %+v", fm)
	}
	if body != "body text\n" {
		t.Errorf("body = %q", body)
	}
}

func TestParseFrontmatter_CRLF(t *testing.T) {
	// A file hand-edited on Windows / under WSL with CRLF line endings.
	data := []byte("---\r\nname: y\r\ntype: reference\r\ndescription: z\r\n---\r\nbody\r\n")
	fm, body, ok := ParseFrontmatter(data)
	if !ok {
		t.Fatal("CRLF frontmatter should parse, not fall back to headerless")
	}
	if fm.Name != "y" || fm.Type != "reference" {
		t.Errorf("frontmatter = %+v", fm)
	}
	if body != "body\n" {
		t.Errorf("body = %q, want %q", body, "body\n")
	}
}

func TestParseFrontmatter_ClosingFenceAtEOF(t *testing.T) {
	// Closing --- is the final bytes with no trailing newline; empty body.
	data := []byte("---\nname: z\ntype: project\ndescription: d\n---")
	fm, body, ok := ParseFrontmatter(data)
	if !ok {
		t.Fatal("closing fence at EOF should parse")
	}
	if fm.Name != "z" || fm.Type != "project" {
		t.Errorf("frontmatter = %+v", fm)
	}
	if body != "" {
		t.Errorf("body = %q, want empty", body)
	}
}

func TestParseFrontmatter_Headerless(t *testing.T) {
	data := []byte("just some markdown, no frontmatter\n")
	_, body, ok := ParseFrontmatter(data)
	if ok {
		t.Error("headerless file should report ok=false")
	}
	if body != "just some markdown, no frontmatter\n" {
		t.Errorf("body = %q", body)
	}
}
