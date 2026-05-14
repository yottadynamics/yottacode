package github

import "testing"

func TestParseRemoteURL_SSHForm(t *testing.T) {
	owner, repo, err := ParseRemoteURL("git@github.com:yottadynamics/yottacode.git")
	if err != nil {
		t.Fatalf("ParseRemoteURL: %v", err)
	}
	if owner != "yottadynamics" || repo != "yottacode" {
		t.Errorf("got %s/%s; want yottadynamics/yottacode", owner, repo)
	}
}

func TestParseRemoteURL_SSHFormWithoutGitSuffix(t *testing.T) {
	// Some configurations omit the .git suffix in remote URLs.
	owner, repo, err := ParseRemoteURL("git@github.com:owner/repo")
	if err != nil {
		t.Fatalf("ParseRemoteURL: %v", err)
	}
	if owner != "owner" || repo != "repo" {
		t.Errorf("got %s/%s; want owner/repo", owner, repo)
	}
}

func TestParseRemoteURL_HTTPSForm(t *testing.T) {
	owner, repo, err := ParseRemoteURL("https://github.com/owner/repo.git")
	if err != nil {
		t.Fatalf("ParseRemoteURL: %v", err)
	}
	if owner != "owner" || repo != "repo" {
		t.Errorf("got %s/%s; want owner/repo", owner, repo)
	}
}

func TestParseRemoteURL_HTTPSWithToken(t *testing.T) {
	// `https://<token>@github.com/owner/repo.git` is the
	// credential-in-URL form some setups use (especially CI
	// environments that bake a token into the remote URL).
	// The authority's user portion must be ignored.
	owner, repo, err := ParseRemoteURL("https://ghp_secret@github.com/owner/repo.git")
	if err != nil {
		t.Fatalf("ParseRemoteURL: %v", err)
	}
	if owner != "owner" || repo != "repo" {
		t.Errorf("got %s/%s; want owner/repo", owner, repo)
	}
}

func TestParseRemoteURL_HyphensAndDigits(t *testing.T) {
	// GitHub names allow letters, digits, hyphens. Verify the
	// regex isn't too restrictive on either side.
	owner, repo, err := ParseRemoteURL("git@github.com:some-org42/repo-name-v2.git")
	if err != nil {
		t.Fatalf("ParseRemoteURL: %v", err)
	}
	if owner != "some-org42" || repo != "repo-name-v2" {
		t.Errorf("got %s/%s; want some-org42/repo-name-v2", owner, repo)
	}
}

func TestParseRemoteURL_RejectsNonGitHub(t *testing.T) {
	cases := []string{
		"git@gitlab.com:owner/repo.git",
		"https://gitlab.com/owner/repo.git",
		"https://example.com/owner/repo.git",
		"git@bitbucket.org:owner/repo.git",
	}
	for _, url := range cases {
		t.Run(url, func(t *testing.T) {
			if _, _, err := ParseRemoteURL(url); err == nil {
				t.Errorf("expected error for non-GitHub URL %q", url)
			}
		})
	}
}

func TestParseRemoteURL_RejectsMalformed(t *testing.T) {
	cases := []string{
		"",
		"not a URL at all",
		"https://github.com/onlyone",       // missing repo segment
		"git@github.com:owner",             // missing repo
		"https://github.com/owner/repo/extra", // too many path segments
	}
	for _, url := range cases {
		t.Run(url, func(t *testing.T) {
			if _, _, err := ParseRemoteURL(url); err == nil {
				t.Errorf("expected error for malformed URL %q", url)
			}
		})
	}
}
