package github

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	gogithub "github.com/google/go-github/v66/github"
)

func TestLastLines(t *testing.T) {
	got, lines, truncated := lastLines("one\ntwo\nthree\nfour\n", 2)
	if got != "three\nfour" || lines != 2 || !truncated {
		t.Fatalf("lastLines = (%q,%d,%v), want tail two lines truncated", got, lines, truncated)
	}
	got, lines, truncated = lastLines("one\ntwo", 5)
	if got != "one\ntwo" || lines != 2 || truncated {
		t.Fatalf("lastLines short = (%q,%d,%v), want full untruncated", got, lines, truncated)
	}
}

func TestMapWorkflowJobLogTail(t *testing.T) {
	job := &gogithub.WorkflowJob{
		ID:           gogithub.Int64(202),
		RunID:        gogithub.Int64(101),
		Name:         gogithub.String("test"),
		WorkflowName: gogithub.String("CI"),
		Conclusion:   gogithub.String("failure"),
		HTMLURL:      gogithub.String("https://github.com/o/r/actions/runs/101/job/202"),
	}
	got := mapWorkflowJobLogTail(job, "tail", 1, true)
	if got.RunID != 101 || got.JobID != 202 || got.Name != "test" || got.Workflow != "CI" {
		t.Fatalf("mapped identity fields incorrectly: %+v", got)
	}
	if got.Conclusion != "FAILURE" || got.Tail != "tail" || got.Lines != 1 || !got.Truncated {
		t.Fatalf("mapped tail fields incorrectly: %+v", got)
	}
}

func TestFetchLogTailRejectsNon2xx(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Status:     "403 Forbidden",
			Body:       io.NopCloser(strings.NewReader("nope")),
			Request:    r,
		}, nil
	})}
	_, _, _, err := fetchLogTail(context.Background(), client, "https://example.test/log", 10)
	if err == nil || !strings.Contains(err.Error(), "403 Forbidden") {
		t.Fatalf("expected status error, got %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
