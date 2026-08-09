package tui

import (
	"fmt"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
)

// cmdInit handles `/init` — drafts a starter YOTTACODE.md by handing
// the agent a directive prompt and letting the normal turn loop run
// with full tool access. The agent uses list_dir / glob / grep /
// read_file to learn the repo, then calls write_file (approval-gated)
// to land .yottacode/YOTTACODE.md.
//
// The slash command is a thin wrapper: it composes the directive
// (with an "already exists — refresh, don't overwrite blindly" hint
// when the file is present) and submits via the same path a typed
// user message would take. That way the agent loop, approval modal,
// retrieval orchestrator, and session save all behave normally.
func cmdInit(m Model, _ []string) (Model, tea.Cmd) {
	if m.turnActive {
		m.appendLine(styleError.Render("[init] a turn is already running — wait for it to finish or press Esc to cancel"))
		return m, nil
	}
	prompt := buildInitPrompt(m.cwd)
	out, cmd := m.startTurn(prompt)
	return out.(Model), cmd
}

// buildInitPrompt composes the directive sent to the agent. Path-aware:
// when YOTTACODE.md already exists, the prompt asks the agent to refresh
// it rather than overwriting blindly, so a hand-edited file isn't
// flattened on a re-run. The approval modal still gates the write
// either way.
func buildInitPrompt(cwd string) string {
	target := projectMdRelPath()
	exists := false
	if cwd != "" {
		if _, err := os.Stat(filepath.Join(cwd, target)); err == nil {
			exists = true
		}
	}

	mode := "Create"
	stance := `If the file does not exist, draft it from scratch using what you find in the repo.`
	if exists {
		mode = "Refresh"
		stance = `The file already exists — read it first, preserve sections that are still accurate, and update only what has drifted. Do not flatten hand-written content.`
	}

	return fmt.Sprintf(`%s the per-repo context file at %s.

YOTTACODE.md is a hand-curated brief on this repo that auto-injects into
every turn's system prompt. It should help a fresh agent (or a teammate)
ramp up in under a minute.

%s

Scope: every path in this task is relative to the project root, which is the
agent's current working directory. The project root is "." — never call
list_dir with path "/" (that's the filesystem root and will scan everything
on the machine). Start with list_dir on "." (or omit path) to see the
top-level layout, then descend into the directories that look interesting.

Investigate the repo with list_dir, glob, grep, and read_file as needed.
Look at: README.md, language/build files (go.mod, package.json, pyproject.toml,
Cargo.toml, Makefile, etc.), top-level directory layout, any CONTRIBUTING /
ARCHITECTURE / docs/ files, and the test setup. Don't read every file — the
goal is a high-signal summary, not an exhaustive index.

Cover these sections (skip ones that don't apply):

  # <Project name> — one-line tagline

  ## What this project is
  2-3 sentences. The thing the repo builds, the audience, the language/runtime.

  ## Repo layout
  Top-level directories and what each one holds. Bullet list, one line each.

  ## How to build, test, run
  The exact commands a contributor would type. Prefer commands the project
  already documents (Makefile targets, package.json scripts, etc.) over
  ones you infer.

  ## Conventions
  Style or pattern rules visible in the code (formatter, lint, naming,
  test-discipline, error-handling pattern). Only what's actually enforced
  or consistently followed — don't invent.

  ## Key files / entry points
  Where the main loop / CLI / HTTP handlers / package roots live.

  ## Notes for the agent
  Anything an agent should know before touching this repo: hot directories,
  generated files to leave alone, mock-vs-real test patterns, sandbox
  requirements, etc.

Keep it under ~150 lines. YOTTACODE.md auto-injects on every turn — bloat
becomes a prompt-cache and latency tax. Drop sections that would just
restate the README; link to it instead.

When you have the draft, write the whole file in one write_file call. The
user will see the approval modal and either accept or edit before it lands
on disk. Do not stop after creating a todo card just to ask whether to
proceed; todo cards are progress trackers, and write_file provides the
approval gate for the actual file change.`, mode, target, stance)
}

// projectMdRelPath is the canonical relative path the agent should
// write to. Kept as a function so tests can swap it if we ever support
// alternate roots, and so the path appears once instead of being
// duplicated across the prompt body.
func projectMdRelPath() string {
	return filepath.Join(".yottacode", "YOTTACODE.md")
}
