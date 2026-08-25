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

// SandboxManager owns the command-sandbox lifecycle for one session or dispatch
// worker. It lazily creates one container per requested profile and never tracks
// a user-facing "active" profile: tools route each command by intent.
type SandboxManager struct {
	baseConfig  config.SandboxConfig
	sessionID   string
	mountRoot   string
	constructor SandboxConstructor

	mu        sync.Mutex
	sandboxes map[agent.SandboxProfile]agent.Sandbox
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
	}
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
	m.sandboxes = make(map[agent.SandboxProfile]agent.Sandbox)
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
	m.mu.Lock()
	defer m.mu.Unlock()
	if sb := m.sandboxes[profile]; sb != nil {
		return sb, nil
	}
	if m.constructor == nil {
		return nil, errors.New("no sandbox constructor configured")
	}
	cfg := m.profileConfig(profile)
	sb, err := m.constructor(ctx, cfg, m.profileID(profile), m.mountRoot)
	if err != nil {
		return nil, err
	}
	m.sandboxes[profile] = sb
	return sb, nil
}

func (m *SandboxManager) profileConfig(profile agent.SandboxProfile) config.SandboxConfig {
	cfg := m.baseConfig
	if profile == agent.SandboxProfileDocuments {
		if doc := m.baseConfig.DocumentsProfile(); doc.Image != "" {
			cfg = doc
		}
	}
	return cfg
}

func (m *SandboxManager) profileID(profile agent.SandboxProfile) string {
	if profile == "" || profile == agent.SandboxProfileDefault {
		return m.sessionID
	}
	return m.sessionID + "-" + string(profile)
}

// LiveProfiles returns the profiles that have created containers. It is used by
// worktree guards so changing cwd cannot strand live containers with stale
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
	return h.manager.Command(ctx, profile, command, cwd)
}

func (h SandboxHandler) Label() string {
	return h.LabelProfile(agent.SandboxProfileDefault)
}

func (h SandboxHandler) LabelProfile(profile agent.SandboxProfile) string {
	return h.manager.Label(profile)
}

func (h SandboxHandler) Close() error {
	return h.manager.Close()
}

func failingCommand(ctx context.Context, err error) *exec.Cmd {
	msg := fmt.Sprintf("echo %s >&2; exit 125", shellQuoteSingle(err.Error()))
	return exec.CommandContext(ctx, "/bin/sh", "-c", msg)
}

func shellQuoteSingle(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
