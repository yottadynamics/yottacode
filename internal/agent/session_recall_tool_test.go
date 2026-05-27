package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type fakeRecallSearcher struct {
	hits []RecallHit
	err  error
}

func (f *fakeRecallSearcher) Search(query string, limit int) ([]RecallHit, error) {
	if f.err != nil {
		return nil, f.err
	}
	if limit > len(f.hits) {
		limit = len(f.hits)
	}
	return f.hits[:limit], nil
}

func TestSessionRecallTool_ReturnsHits(t *testing.T) {
	searcher := &fakeRecallSearcher{
		hits: []RecallHit{
			{
				SessionID:   "abc12345-6789",
				SessionName: "auth-refactor",
				Model:       "gpt-4o",
				Created:     time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC),
				Role:        "assistant",
				Snippet:     "we discussed the [authentication] middleware refactor",
			},
		},
	}

	tool := &SessionRecallTool{Searcher: searcher}
	args, _ := json.Marshal(sessionRecallArgs{Query: "authentication"})
	result, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "auth-refactor") {
		t.Errorf("expected session name in results; got: %s", result)
	}
	if !strings.Contains(result, "authentication") {
		t.Errorf("expected snippet content in results; got: %s", result)
	}
	if !strings.Contains(result, "2026-05-20") {
		t.Errorf("expected date in results; got: %s", result)
	}
}

func TestSessionRecallTool_NoHits(t *testing.T) {
	searcher := &fakeRecallSearcher{hits: nil}
	tool := &SessionRecallTool{Searcher: searcher}
	args, _ := json.Marshal(sessionRecallArgs{Query: "something never discussed"})
	result, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatal(err)
	}
	if result != "no matching sessions found" {
		t.Errorf("expected 'no matching sessions found'; got: %s", result)
	}
}

func TestSessionRecallTool_NilSearcher(t *testing.T) {
	tool := &SessionRecallTool{Searcher: nil}
	args, _ := json.Marshal(sessionRecallArgs{Query: "anything"})
	_, err := tool.Execute(context.Background(), string(args))
	if err == nil {
		t.Error("expected error when searcher is nil")
	}
}

func TestSessionRecallTool_EmptyQuery(t *testing.T) {
	searcher := &fakeRecallSearcher{}
	tool := &SessionRecallTool{Searcher: searcher}
	args, _ := json.Marshal(sessionRecallArgs{Query: ""})
	_, err := tool.Execute(context.Background(), string(args))
	if err == nil {
		t.Error("expected error for empty query")
	}
}

func TestSessionRecallTool_DefaultLimit(t *testing.T) {
	hits := make([]RecallHit, 15)
	for i := range hits {
		hits[i] = RecallHit{
			SessionID: "sess-" + string(rune('a'+i)),
			Role:      "user",
			Created:   time.Now(),
			Snippet:   "match",
		}
	}
	searcher := &fakeRecallSearcher{hits: hits}
	tool := &SessionRecallTool{Searcher: searcher}
	args, _ := json.Marshal(sessionRecallArgs{Query: "match"})
	result, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "found 10 results") {
		t.Errorf("default limit should be 10; got: %s", result)
	}
}
