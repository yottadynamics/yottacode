package cli

import (
	"sync"
	"testing"

	"github.com/yottadynamics/yottacode/internal/config"
)

// TestRouterAdapters_ResolveConcurrent exercises ra.Resolve from many
// goroutines at once — the shape produced when the agent dispatches
// several subagents in one turn, each declaring an explicit model:.
// Resolve reads and writes a shared memo map; without a mutex this trips
// the race detector and can fatally panic ("concurrent map read and map
// write"), which is uncatchable and kills the session. Run with -race.
// Regression for the release audit's router-adapters-built-map-race.
func TestRouterAdapters_ResolveConcurrent(t *testing.T) {
	cfg := routingTestConfig()
	// A third configured model that is NOT the fast/smart pair, so
	// Resolve()ing it concurrently hits the build-and-store (write) path
	// rather than only the pre-built memo entries.
	cfg.Providers[0].Models = append(cfg.Providers[0].Models, config.Model{Name: "claude-sonnet-4-6", Tier: "mid"})
	cfg.Router.Mode = config.RouterModeAuto
	cfg.Router.FastModel = "anthropic:claude-haiku-4-5"
	cfg.Router.SmartModel = "anthropic:claude-opus-4-6"
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")

	ra, err := BuildRouterAdapters(cfg, ChatOptions{})
	if err != nil {
		t.Fatalf("BuildRouterAdapters: %v", err)
	}

	models := []string{"claude-haiku-4-5", "claude-opus-4-6", "claude-sonnet-4-6", "ghost-model"}
	const goroutines = 32
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = ra.Resolve(models[i%len(models)])
		}(i)
	}
	wg.Wait()

	if a := ra.Resolve("claude-sonnet-4-6"); a == nil {
		t.Error("Resolve(sonnet) returned nil for a configured model after concurrent access")
	}
}
