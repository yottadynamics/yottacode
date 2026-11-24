package agent

import (
	"context"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// A steering message queued mid-turn is run through PrepareUserMessage at
// delivery time: the history gets the prepared content and images, and the
// delivered receipt carries the user's own words plus the attach notices.
func TestLoop_QueuedUserMessageIsPreparedAtDelivery(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "read_file", ArgsJSON: `{}`})},
		{sseToken("done"), sseDone("done")},
	}}
	reg := NewRegistry()
	reg.Register(parallelReadTool{})

	ch := make(chan UserMessage, 1)
	ch <- UserMessage{Content: "also check @a.go", Timestamp: time.Now()}

	var seen string
	cfg := LoopConfig{
		Adapter:       streamer,
		Registry:      reg,
		MaxIterations: 5,
		UserMessages:  ch,
		PrepareUserMessage: func(msg UserMessage) PreparedUser {
			seen = msg.Content
			return PreparedUser{
				Content: "also check a.go\n\n## attachment",
				Images:  []adapter.ImageBlock{{Data: []byte{1}, MediaType: "image/png"}},
				Notices: []AttachNotice{{Text: "attached @a.go (1 B)"}},
			}
		},
	}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "start"}}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, err := runTurnSync(t, ctx, cfg, &hist, nil)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}

	if seen != "also check @a.go" {
		t.Fatalf("hook saw %q, want the raw queued text", seen)
	}
	var delivered *adapter.Message
	for i := range hist {
		if hist[i].Role == adapter.RoleUser && hist[i].Content == "also check a.go\n\n## attachment" {
			delivered = &hist[i]
		}
	}
	if delivered == nil || len(delivered.Images) != 1 {
		t.Fatalf("prepared message missing from history: %+v", hist)
	}
	var receipt *UserMessageAppended
	for _, ev := range events {
		if e, ok := ev.(UserMessageAppended); ok {
			receipt = &e
		}
	}
	if receipt == nil || receipt.Content != "also check @a.go" || len(receipt.Notices) != 1 {
		t.Fatalf("receipt = %+v", receipt)
	}
}
