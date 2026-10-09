package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/session"
)

// inspectBenchSession is a long session with realistic message sizes, so the
// benchmarks below exercise the per-keypress and per-frame paths a user would
// hit on a multi-thousand-turn run.
func inspectBenchSession(turns int) *session.Session {
	s := &session.Session{ID: "bench-session"}
	body := strings.Repeat("some assistant prose about the change being made. ", 80)
	for i := range turns {
		id := fmt.Sprintf("c%d", i)
		s.Messages = append(s.Messages,
			adapter.Message{Role: adapter.RoleUser, Content: body},
			adapter.Message{
				Role: adapter.RoleAssistant, Content: body,
				Usage:     &adapter.Usage{InputTokens: int64(20_000 + i*10), OutputTokens: 300},
				ToolCalls: []adapter.ToolCall{{ID: id, Name: fmt.Sprintf("tool_%d", i%25), ArgsJSON: `{"path":"a.go"}`}},
			},
		)
		if i%9 == 0 {
			s.Messages = append(s.Messages, adapter.Message{Role: adapter.RoleTool, ToolCallID: id, Content: "error: something failed in " + body[:40]})
		}
	}
	return s
}

func benchInspectModel(b *testing.B, turns int) Model {
	b.Helper()
	// A bare Model is enough: the inspect panel only reads its size and its own
	// view state (newTestModel needs a *testing.T).
	var m Model
	m.width, m.height = 140, 50
	return m.openInspectSession(inspectBenchSession(turns))
}

// BenchmarkInspectKeyDown is the cost of one ↓ press: re-render the whole
// panel and re-derive the scroll position.
func BenchmarkInspectKeyDown(b *testing.B) {
	for _, turns := range []int{200, 2000} {
		b.Run(fmt.Sprintf("%d turns", turns), func(b *testing.B) {
			m := benchInspectModel(b, turns)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				code := tea.KeyDown
				if i%2 == 1 {
					code = tea.KeyUp
				}
				m, _ = m.updateInspectPanel(tea.KeyPressMsg{Code: code})
			}
		})
	}
}

// BenchmarkInspectSearchKeystroke is one typed search character, which
// re-filters every turn's text.
func BenchmarkInspectSearchKeystroke(b *testing.B) {
	for _, turns := range []int{200, 2000} {
		b.Run(fmt.Sprintf("%d turns", turns), func(b *testing.B) {
			m := benchInspectModel(b, turns)
			m, _ = m.updateInspectPanel(tea.KeyPressMsg{Code: '/', Text: "/"})
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m, _ = m.updateInspectPanel(tea.KeyPressMsg{Code: 'x', Text: "x"})
				m, _ = m.updateInspectPanel(tea.KeyPressMsg{Code: tea.KeyBackspace})
			}
		})
	}
}

// BenchmarkInspectFrame is one View() of the popup: windowing the panel.
func BenchmarkInspectFrame(b *testing.B) {
	for _, turns := range []int{200, 2000} {
		b.Run(fmt.Sprintf("%d turns", turns), func(b *testing.B) {
			m := benchInspectModel(b, turns)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = m.windowedInspectPanel()
			}
		})
	}
}
