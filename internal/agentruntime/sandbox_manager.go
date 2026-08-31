package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"sync"

	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/sandbox"
)

// SandboxConstructor is the injectable container factory used by
// SandboxManager. Production wiring points it at sandbox.NewPodmanSandbox;
// tests replace it with spy sandboxes so profile routing can be exercised
// without Podman.
type SandboxConstructor func(ctx context.Context, cfg config.SandboxConfig, id, mountRoot string) (agent.Sandbox, error)

var podmanSandboxConstructor SandboxConstructor = func(ctx context.Context, cfg config.SandboxConfig, id, mountRoot string) (agent.Sandbox, error) {
	return sandbox.NewPodmanSandbox(ctx, cfg, id, mountRoot)
}

// SandboxConfigReloader returns the latest config.toml snapshot before a lazy
// profile is created. It lets a session recover from a bad documents_image by
// editing config and retrying, without rebuilding the entire tool registry.
type SandboxConfigReloader func() (config.Config, error)

// SandboxManager owns the command-sandbox lifecycle for one session or dispatch
// worker. It lazily creates one container per requested profile and never tracks
// a user-facing "active" profile: tools route each command by intent.
type SandboxManager struct {
	baseConfig  config.SandboxConfig
	sessionID   string
	mountRoot   string
	constructor SandboxConstructor
	reloader    SandboxConfigReloader

	mu        sync.Mutex
	sandboxes map[agent.SandboxProfile]agent.Sandbox
	creating  map[agent.SandboxProfile]chan sandboxCreateResult
	closed    bool
}

type sandboxCreateResult struct {
	sandbox agent.Sandbox
	err     error
}

// NewSandboxManager returns a manager for an enabled sandbox backend. The first
// container is not started until a tool actually requests its profile, which
// avoids paying for the documents image in sessions that never touch documents.
func NewSandboxManager(cfg config.SandboxConfig, sessionID, mountRoot string, constructor SandboxConstructor) *SandboxManager {
	return &SandboxManager{
		baseConfig:  cfg,
		sessionID:   sessionID,
		mountRoot:   mountRoot,
		constructor: constructor,
		sandboxes:   make(map[agent.SandboxProfile]agent.Sandbox),
		creating:    make(map[agent.SandboxProfile]chan sandboxCreateResult),
	}
}

// SetConfigReloader installs an optional live config reload hook used only for
// profiles that have not yet created a container. Existing live containers keep
// their startup config until the session ends, preserving mount/image stability.
func (m *SandboxManager) SetConfigReloader(reloader SandboxConfigReloader) {
	m.reloader = reloader
}

// Handler returns the stable agent.Sandbox implementation injected into tools.
// The handler is intentionally tiny: it preserves the old Sandbox interface for
// run_bash while exposing ProfiledSandbox for document-tool routing.
func (m *SandboxManager) Handler() agent.Sandbox {
	if m == nil {
		return nil
	}
	return SandboxHandler{manager: m}
}

// Command returns a command routed through the requested profile's container,
// creating that container on first use. A creation failure is encoded as an
// exec.Cmd that fails from Run, matching the Sandbox interface's no-error shape
// without falling back to host execution.
func (m *SandboxManager) Command(ctx context.Context, profile agent.SandboxProfile, command, cwd string) *exec.Cmd {
	sb, err := m.sandbox(ctx, profile)
	if err != nil {
		return failingCommand(ctx, fmt.Errorf("sandbox %s: %w", profile, err))
	}
	return sb.Command(ctx, command, cwd)
}

// Label reports the profile-specific sandbox label used in approval previews.
func (m *SandboxManager) Label(profile agent.SandboxProfile) string {
	if profile == "" {
		profile = agent.SandboxProfileDefault
	}
	return fmt.Sprintf("[podman:%s]", profile)
}

// Close tears down every profile container created by this manager. It is
// best-effort and returns a joined error for callers that choose to log it.
func (m *SandboxManager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	sandboxes := make([]agent.Sandbox, 0, len(m.sandboxes))
	for _, sb := range m.sandboxes {
		sandboxes = append(sandboxes, sb)
	}
	m.closed = true
	m.sandboxes = make(map[agent.SandboxProfile]agent.Sandbox)
	m.creating = make(map[agent.SandboxProfile]chan sandboxCreateResult)
	m.mu.Unlock()

	var errs []error
	for _, sb := range sandboxes {
		if err := sb.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *SandboxManager) sandbox(ctx context.Context, profile agent.SandboxProfile) (agent.Sandbox, error) {
	if profile == "" {
		profile = agent.SandboxProfileDefault
	}
	if m == nil {
		return agent.HostSandbox{}, nil
	}
	m.mu.Lock()
	if sb := m.sandboxes[profile]; sb != nil {
		m.mu.Unlock()
		return sb, nil
	}
	if ch := m.creating[profile]; ch != nil {
		m.mu.Unlock()
		select {
		case res := <-ch:
			return res.sandbox, res.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("sandbox manager is closed")
	}
	if m.constructor == nil {
		m.mu.Unlock()
		return nil, errors.New("no sandbox constructor configured")
	}
	id := m.profileID(profile)
	mountRoot := m.mountRoot
	constructor := m.constructor
	ch := make(chan sandboxCreateResult, 1)
	m.creating[profile] = ch
	m.mu.Unlock()

	res := sandboxCreateResult{}
	cfg, err := m.profileConfig(profile)
	if err != nil {
		res.err = err
	} else {
		// Container creation may pull/start Podman and can be slow. Do not hold the
		// manager lock across that boundary: Close must still be able to tear down
		// already-live profile containers while another profile is being created.
		res.sandbox, res.err = constructor(ctx, cfg, id, mountRoot)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.creating, profile)
	if m.closed {
		// Close won the race while the profile was being created. Tear down any
		// just-created sandbox so shutdown does not leak a container.
		if res.sandbox != nil {
			_ = res.sandbox.Close()
		}
		res = sandboxCreateResult{err: errors.New("sandbox manager is closed")}
	}
	if res.err == nil && res.sandbox != nil {
		m.sandboxes[profile] = res.sandbox
	}
	ch <- res
	close(ch)
	return res.sandbox, res.err
}

func (m *SandboxManager) profileConfig(profile agent.SandboxProfile) (config.SandboxConfig, error) {
	cfg, err := m.latestSandboxConfig()
	if profile == agent.SandboxProfileDocuments {
		if doc := cfg.DocumentsProfile(); doc.Image != "" {
			cfg = doc
		}
	}
	return cfg, err
}

func (m *SandboxManager) latestSandboxConfig() (config.SandboxConfig, error) {
	if m.reloader == nil {
		return m.baseConfig, nil
	}
	latest, err := m.reloader()
	if err != nil {
		return config.SandboxConfig{}, fmt.Errorf("reload config: %w", err)
	}
	if latest.Sandbox.Backend != "podman" {
		return config.SandboxConfig{}, fmt.Errorf("sandbox backend is now %q; restart yottacode to change sandbox backends", latest.Sandbox.Backend)
	}
	m.mu.Lock()
	m.baseConfig = latest.Sandbox
	m.mu.Unlock()
	return latest.Sandbox, nil
}

func (m *SandboxManager) profileID(profile agent.SandboxProfile) string {
	if profile == "" || profile == agent.SandboxProfileDefault {
		return m.sessionID
	}
	return m.sessionID + "-" + string(profile)
}

// LiveProfiles returns the profiles that have created containers. It is used by
// worktree guards so changing cwd cannot strand live containers with unseen
// mounts.
func (m *SandboxManager) LiveProfiles() []agent.SandboxProfile {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]agent.SandboxProfile, 0, len(m.sandboxes))
	for profile := range m.sandboxes {
		out = append(out, profile)
	}
	slices.Sort(out)
	return out
}

// VisiblePath reports whether a sandboxed command can run with path as its cwd.
// The Podman sandbox mounts both the session root and this repo's managed
// worktree storage, so an in-session enter_worktree is safe for yottacode-
// managed worktrees even after a profile container is live.
func (m *SandboxManager) VisiblePath(path string) bool {
	if m == nil {
		return false
	}
	return sandbox.PathVisibleFromMountRoot(path, m.mountRoot)
}

// SandboxHandler is the stable sandbox value registered with tools. It routes
// ordinary Sandbox.Command calls to the default profile and profile-aware calls
// to the requested profile.
type SandboxHandler struct {
	manager *SandboxManager
}

func (h SandboxHandler) Command(ctx context.Context, command, cwd string) *exec.Cmd {
	return h.CommandProfile(ctx, agent.SandboxProfileDefault, command, cwd)
}

func (h SandboxHandler) CommandProfile(ctx context.Context, profile agent.SandboxProfile, command, cwd string) *exec.Cmd {
	if h.manager == nil {
		return agent.HostSandbox{}.Command(ctx, command, cwd)
	}
	return h.manager.Command(ctx, profile, command, cwd)
}

func (h SandboxHandler) Label() string {
	return h.LabelProfile(agent.SandboxProfileDefault)
}

func (h SandboxHandler) LabelProfile(profile agent.SandboxProfile) string {
	if h.manager == nil {
		return agent.HostSandbox{}.Label()
	}
	return h.manager.Label(profile)
}

func (h SandboxHandler) Close() error {
	if h.manager == nil {
		return nil
	}
	return h.manager.Close()
}

// LiveProfiles exposes the manager's mounted profiles to worktree guards. An
// empty result means the lazy handler exists but no container has mounted the
// original cwd yet, so an in-session worktree swap is still safe.
func (h SandboxHandler) LiveProfiles() []agent.SandboxProfile {
	if h.manager == nil {
		return nil
	}
	return h.manager.LiveProfiles()
}

func (h SandboxHandler) VisiblePath(path string) bool {
	if h.manager == nil {
		return false
	}
	return h.manager.VisiblePath(path)
}

func failingCommand(ctx context.Context, err error) *exec.Cmd {
	msg := fmt.Sprintf("echo %s >&2; exit 125", shellQuoteSingle(err.Error()))
	return exec.CommandContext(ctx, "/bin/sh", "-c", msg)
}

func shellQuoteSingle(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
