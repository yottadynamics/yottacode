package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/yottadynamics/yottacode/internal/worktree"
)

func TestWorktreeWhereSingleSlug(t *testing.T) {
	// HOME inside t.TempDir so we don't touch the developer's real home.
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := "/home/me/work/myproj"
	slug := worktree.RepoSlug(repo)
	slugDir := filepath.Join(home, ".yottacode", "worktrees", slug)
	require.NoError(t, worktree.WriteOrigin(slugDir, repo))

	cmd := newWorktreeWhereCmd()
	cmd.SetContext(context.Background())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	require.NoError(t, cmd.RunE(cmd, []string{slug}))

	out := strings.TrimSpace(buf.String())
	require.Equal(t, repo, out)
}

func TestWorktreeWhereListsAllSlugs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoA := "/home/me/work/proj-a"
	repoB := "/home/me/work/proj-b"
	slugA := worktree.RepoSlug(repoA)
	slugB := worktree.RepoSlug(repoB)
	require.NoError(t, worktree.WriteOrigin(filepath.Join(home, ".yottacode", "worktrees", slugA), repoA))
	require.NoError(t, worktree.WriteOrigin(filepath.Join(home, ".yottacode", "worktrees", slugB), repoB))

	cmd := newWorktreeWhereCmd()
	cmd.SetContext(context.Background())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	require.NoError(t, cmd.RunE(cmd, nil))

	out := buf.String()
	require.Contains(t, out, slugA+"\t"+repoA)
	require.Contains(t, out, slugB+"\t"+repoB)
}

func TestWorktreeWhereSlugMissingOrigin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Make a slug dir without .origin (simulates a pre-feature install).
	staleSlug := "legacy-99999999"
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".yottacode", "worktrees", staleSlug), 0o755))

	cmd := newWorktreeWhereCmd()
	cmd.SetContext(context.Background())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	require.NoError(t, cmd.RunE(cmd, nil))

	require.Contains(t, buf.String(), staleSlug+"\t(no .origin)")
}

func TestWorktreeWhereNoWorktreesAtAll(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cmd := newWorktreeWhereCmd()
	cmd.SetContext(context.Background())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	require.NoError(t, cmd.RunE(cmd, nil))
	require.Contains(t, buf.String(), "(no yottacode worktrees)")
}

func TestWorktreeWhereUnknownSlug(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cmd := newWorktreeWhereCmd()
	cmd.SetContext(context.Background())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	err := cmd.RunE(cmd, []string{"does-not-exist-12345678"})
	require.Error(t, err)
}
