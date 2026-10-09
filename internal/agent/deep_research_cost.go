package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/yottadynamics/yottacode/internal/syncutil"
)

// researchStats counts the sub-agent runs of ONE deep_research run (retries
// included) so the result can say what the run cost. It rides the context so
// concurrent runs on the same tool instance never share counts.
type researchStats struct {
	mu      syncutil.Mutex
	calls   map[string]int
	start   time.Time
	running int // sub-agents in flight right now
	peak    int // most that were ever in flight at once
}

type researchStatsKey struct{}

func withResearchStats(ctx context.Context) (context.Context, *researchStats) {
	s := &researchStats{calls: map[string]int{}, start: time.Now()}
	return context.WithValue(ctx, researchStatsKey{}, s), s
}

func researchStatsFrom(ctx context.Context) *researchStats {
	s, _ := ctx.Value(researchStatsKey{}).(*researchStats)
	return s
}

// begin records the start of one sub-agent run; pair it with end. Runs are
// cumulative across phases, but only some overlap, so the peak is tracked
// separately to keep "6 agent runs" from reading as "6 at once".
func (s *researchStats) begin(agentType string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.calls[agentType]++
	s.running++
	s.peak = max(s.peak, s.running)
	s.mu.Unlock()
}

func (s *researchStats) end() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.running--
	s.mu.Unlock()
}

// researchAgentLabels maps each workflow agent type to its label in the cost
// line, in display order.
var researchAgentLabels = []struct{ agentType, label string }{
	{"research-planner", "planner"},
	{"researcher", "researcher"},
	{"research-verifier", "verifier"},
	{"research-synthesizer", "synthesizer"},
}

// costLine renders the run's cost, e.g.
//
//	Cost: 8 agent runs (planner 1, researcher 4, verifier 2, synthesizer 1), at most 4 at once · ~1.6M tokens · 3m30s
//
// The runs are cumulative over the whole workflow (plan, research, verify,
// report); the "at most N at once" part is the peak concurrency, which is what
// the dock shows at any moment.
//
// Tokens are the exact provider-reported usage the subagent registry records
// for this run's children (those started since the run began). Only one run is
// active per session (see launchBackground), so that window holds no one
// else's agents. With no registry (oneshot, tests) the token part is omitted
// rather than guessed.
func (t *DeepResearchTool) costLine(s *researchStats) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	total := 0
	var parts []string
	for _, l := range researchAgentLabels {
		if n := s.calls[l.agentType]; n > 0 {
			total += n
			parts = append(parts, fmt.Sprintf("%s %d", l.label, n))
		}
	}
	start, peak := s.start, s.peak
	s.mu.Unlock()

	out := fmt.Sprintf("Cost: %d agent runs (%s)", total, strings.Join(parts, ", "))
	if peak > 0 {
		out += fmt.Sprintf(", at most %d at once", peak)
	}
	byType := t.runTokensByType(start)
	sum := 0
	var split []string
	for _, l := range researchAgentLabels {
		if n := byType[l.agentType]; n > 0 {
			sum += n
			split = append(split, l.label+" "+compactCount(n))
		}
	}
	if sum > 0 {
		out += " · ~" + compactCount(sum) + " tokens (" + strings.Join(split, ", ") + ")"
	}
	return out + " · " + time.Since(start).Round(time.Second).String()
}

// runTokensByType sums usage per workflow agent type for agents started at or
// after start. Exact provider usage when reported, else the registry's
// estimate. Returns nil with no registry.
func (t *DeepResearchTool) runTokensByType(start time.Time) map[string]int {
	if t.Agent == nil || t.Agent.Tasks == nil {
		return nil
	}
	types := map[string]bool{}
	for _, l := range researchAgentLabels {
		types[l.agentType] = true
	}
	out := map[string]int{}
	for _, task := range t.Agent.Tasks.List() {
		if !types[task.AgentType] || task.Started.Before(start) {
			continue
		}
		if n := task.UsageTokens(); n > 0 {
			out[task.AgentType] += n
		} else {
			out[task.AgentType] += task.TokensUsed
		}
	}
	return out
}

// compactCount renders 492000 as "492K" and 1610000 as "1.6M".
func compactCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%dK", (n+500)/1_000)
	default:
		return fmt.Sprint(n)
	}
}
