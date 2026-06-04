package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/yottadynamics/yottacode/internal/skills"
)

// TestSkillTool_ConcurrentAccess models the real race: the agent
// goroutine reads the skill set every turn (Description/Active/Execute/
// IsEnabled) while the TUI goroutine mutates it from the /skills picker
// and install/uninstall reloads (SetAll/SetEnabled/Enable). Without the
// RWMutex this is a concurrent map read+write — a fatal, uncatchable
// runtime panic that crashes the CLI mid-turn. Run with -race.
// Regression for the release audit's skilltool-enabled-all-data-race.
func TestSkillTool_ConcurrentAccess(t *testing.T) {
	base := []skills.Skill{
		{Name: "alpha", Description: "a", Body: "A"},
		{Name: "beta", Description: "b", Body: "B"},
		{Name: "gamma", Description: "c", Body: "C"},
	}
	tool := &SkillTool{All: base}

	const iters = 200
	var wg sync.WaitGroup

	// Writers (TUI goroutine): reload + enablement churn.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range iters {
			tool.SetAll(base)
			tool.SetEnabled(map[string]bool{"alpha": true})
			tool.Enable("beta")
			tool.SetEnabled(nil)
		}
	}()

	// Readers (agent goroutine): every per-turn access path.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range iters {
			_ = tool.Description()
			_ = tool.Active()
			_ = tool.IsEnabled("alpha")
			_, _ = tool.Execute(context.Background(), `{"skill":"alpha"}`)
		}
	}()

	wg.Wait()
}
