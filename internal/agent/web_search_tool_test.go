package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const ddgSampleHTML = `<!DOCTYPE html>
<html>
<body>
<div class="results">
  <div class="result results_links results_links_deep web-result">
    <div class="links_main links_deep result__body">
      <h2 class="result__title">
        <a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpage1&amp;rut=abc">First Result Title</a>
      </h2>
      <a class="result__snippet" href="https://example.com/page1">This is the first result snippet.</a>
    </div>
  </div>
  <div class="result results_links results_links_deep web-result">
    <div class="links_main links_deep result__body">
      <h2 class="result__title">
        <a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpage2&amp;rut=def">Second Result Title</a>
      </h2>
      <a class="result__snippet" href="https://example.com/page2">This is the second result snippet.</a>
    </div>
  </div>
  <div class="result results_links results_links_deep web-result">
    <div class="links_main links_deep result__body">
      <h2 class="result__title">
        <a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpage3&amp;rut=ghi">Third Result Title</a>
      </h2>
      <a class="result__snippet" href="https://example.com/page3">Third snippet here.</a>
    </div>
  </div>
</div>
</body>
</html>`

func TestParseDDGHTML_HappyPath(t *testing.T) {
	results := parseDDGHTML(ddgSampleHTML, 10)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	if results[0].Title != "First Result Title" {
		t.Errorf("result[0].Title = %q", results[0].Title)
	}
	if results[0].URL != "https://example.com/page1" {
		t.Errorf("result[0].URL = %q", results[0].URL)
	}
	if results[0].Snippet != "This is the first result snippet." {
		t.Errorf("result[0].Snippet = %q", results[0].Snippet)
	}

	if results[1].URL != "https://example.com/page2" {
		t.Errorf("result[1].URL = %q", results[1].URL)
	}
}

func TestParseDDGHTML_MaxResults(t *testing.T) {
	results := parseDDGHTML(ddgSampleHTML, 2)
	if len(results) != 2 {
		t.Fatalf("expected 2 results (capped), got %d", len(results))
	}
}

func TestParseDDGHTML_EmptyHTML(t *testing.T) {
	results := parseDDGHTML("<html><body></body></html>", 10)
	if len(results) != 0 {
		t.Fatalf("expected 0 results for empty HTML, got %d", len(results))
	}
}

func TestResolveDDGURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpage&rut=abc", "https://example.com/page"},
		{"https://duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpage&rut=abc", "https://example.com/page"},
		{"https://example.com/direct", "https://example.com/direct"},
		{"//example.com/bare", "https://example.com/bare"},
	}
	for _, tt := range tests {
		got := resolveDDGURL(tt.input)
		if got != tt.want {
			t.Errorf("resolveDDGURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestWebSearchTool_ExecuteWithServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		if q == "" {
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(ddgSampleHTML))
	}))
	t.Cleanup(srv.Close)

	origSearch := ddgSearchFunc
	ddgSearchFunc = func(ctx context.Context, query string, maxResults int) ([]ddgResult, error) {
		return ddgSearchWithBase(ctx, query, maxResults, srv.URL+"/html/")
	}
	t.Cleanup(func() { ddgSearchFunc = origSearch })

	tool := &WebSearchTool{}
	out, err := tool.Execute(context.Background(), `{"query":"test query"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "First Result Title") {
		t.Errorf("output missing first result title:\n%s", out)
	}
	if !strings.Contains(out, "https://example.com/page1") {
		t.Errorf("output missing first result URL:\n%s", out)
	}
	if !strings.Contains(out, "Search results for: test query") {
		t.Errorf("output missing query header:\n%s", out)
	}
}

func TestWebSearchTool_EmptyQuery(t *testing.T) {
	tool := &WebSearchTool{}
	_, err := tool.Execute(context.Background(), `{"query":""}`)
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestWebSearchTool_NoResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body><div class='no-results'>No results</div></body></html>"))
	}))
	t.Cleanup(srv.Close)

	origSearch := ddgSearchFunc
	ddgSearchFunc = func(ctx context.Context, query string, maxResults int) ([]ddgResult, error) {
		return ddgSearchWithBase(ctx, query, maxResults, srv.URL+"/html/")
	}
	t.Cleanup(func() { ddgSearchFunc = origSearch })

	tool := &WebSearchTool{}
	out, err := tool.Execute(context.Background(), `{"query":"xyznonexistent"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "No results found") {
		t.Errorf("expected no-results message, got:\n%s", out)
	}
}

func TestWebSearchTool_SchemaAndMeta(t *testing.T) {
	tool := &WebSearchTool{}
	if tool.Name() != "web_search" {
		t.Errorf("Name() = %q", tool.Name())
	}
	if tool.RequiresApproval("") {
		t.Error("RequiresApproval should be false")
	}
	if !tool.ParallelSafe("") {
		t.Error("ParallelSafe should be true")
	}
	schema := tool.Schema()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema missing properties")
	}
	if _, ok := props["query"]; !ok {
		t.Error("schema missing query property")
	}
}
