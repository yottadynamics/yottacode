package github

import (
	"slices"
	"strings"
	"testing"
)

func TestBuildCreateArgs_MinimalRequest(t *testing.T) {
	args := buildCreateArgs(CreatePRRequest{
		Base:  "main",
		Title: "fix the thing",
		Body:  "details",
	})
	// Order matters — gh accepts named flags in any order, but pinning
	// it here lets the test catch accidental rearrangements that might
	// change observed shell-invocation cost or break test fixtures.
	want := []string{"pr", "create", "--base", "main", "--title", "fix the thing", "--body-file", "-"}
	if !slices.Equal(args, want) {
		t.Errorf("args = %v; want %v", args, want)
	}
}

func TestBuildCreateArgs_DraftFlag(t *testing.T) {
	args := buildCreateArgs(CreatePRRequest{
		Base: "main", Title: "t", Body: "b", Draft: true,
	})
	if !slices.Contains(args, "--draft") {
		t.Errorf("draft flag missing: %v", args)
	}
}

func TestBuildCreateArgs_HeadFlag(t *testing.T) {
	args := buildCreateArgs(CreatePRRequest{
		Base: "main", Title: "t", Body: "b", Head: "feature/x",
	})
	if !slices.Contains(args, "--head") || !slices.Contains(args, "feature/x") {
		t.Errorf("head flag missing: %v", args)
	}
}

func TestBuildCreateArgs_OwnerRepoOnlyWhenBoth(t *testing.T) {
	// Only one side populated: skip --repo so gh's cwd-inference path
	// stays in play. Setting --repo with a half-empty value would
	// produce a useless "yotta/" arg.
	args := buildCreateArgs(CreatePRRequest{
		Base: "main", Title: "t", Body: "b", Owner: "yotta",
	})
	if slices.Contains(args, "--repo") {
		t.Errorf("expected no --repo with only Owner set: %v", args)
	}
	args = buildCreateArgs(CreatePRRequest{
		Base: "main", Title: "t", Body: "b", Owner: "yotta", Repo: "code",
	})
	if !slices.Contains(args, "--repo") || !slices.Contains(args, "yotta/code") {
		t.Errorf("expected --repo yotta/code: %v", args)
	}
}

func TestValidateCreateRequest_Required(t *testing.T) {
	cases := []struct {
		name string
		req  CreatePRRequest
		want string
	}{
		{"missing base", CreatePRRequest{Title: "t", Body: "b"}, "Base"},
		{"missing title", CreatePRRequest{Base: "main", Body: "b"}, "Title"},
		{"missing body", CreatePRRequest{Base: "main", Title: "t"}, "Body"},
		{"whitespace title rejected", CreatePRRequest{Base: "main", Title: "   ", Body: "b"}, "Title"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCreateRequest(tc.req)
			if err == nil {
				t.Fatalf("expected validation error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("expected error mentioning %q, got %v", tc.want, err)
			}
		})
	}
}

func TestValidateCreateRequest_Happy(t *testing.T) {
	err := validateCreateRequest(CreatePRRequest{
		Base: "main", Title: "fix the thing", Body: "details",
	})
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestParsePRURL_Standalone(t *testing.T) {
	url := parsePRURL("https://github.com/owner/repo/pull/42")
	if url != "https://github.com/owner/repo/pull/42" {
		t.Errorf("got %q", url)
	}
}

func TestParsePRURL_WithSurroundingChrome(t *testing.T) {
	// gh sometimes wraps the URL with a creating-status line or a
	// tip; the regex must still pick it out cleanly.
	stdout := "Creating pull request for feature/x into main in owner/repo\n\nhttps://github.com/owner/repo/pull/7\n"
	url := parsePRURL(stdout)
	if url != "https://github.com/owner/repo/pull/7" {
		t.Errorf("got %q", url)
	}
}

func TestParsePRURL_AbsentReturnsEmpty(t *testing.T) {
	if url := parsePRURL("no pr was created"); url != "" {
		t.Errorf("expected empty, got %q", url)
	}
}

func TestParsePRNumber(t *testing.T) {
	cases := []struct {
		url  string
		want int
	}{
		{"https://github.com/owner/repo/pull/42", 42},
		{"https://github.com/owner/repo/pull/9999", 9999},
		{"", 0},
		{"https://github.com/owner/repo/pull/", 0},
		{"https://github.com/owner/repo/pull/abc", 0},
	}
	for _, tc := range cases {
		if got := parsePRNumber(tc.url); got != tc.want {
			t.Errorf("parsePRNumber(%q) = %d, want %d", tc.url, got, tc.want)
		}
	}
}

