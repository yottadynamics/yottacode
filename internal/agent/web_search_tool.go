package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// WebSearchTool provides web search for models whose provider lacks a native
// web_search built-in (Ollama, NVIDIA NIM, OpenAI-compatible endpoints, etc.).
// Queries DuckDuckGo's HTML endpoint and returns parsed results.
type WebSearchTool struct{}

func (t *WebSearchTool) Name() string { return "web_search" }

func (t *WebSearchTool) Description() string {
	return "Search the web using DuckDuckGo. Returns titles, URLs, and snippets for the top results. Use this for current information, live data, or anything beyond your training cutoff."
}

func (t *WebSearchTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "The search query string.",
			},
			"max_results": map[string]any{
				"type":        "integer",
				"description": "Maximum number of results to return. Default 10, max 20.",
			},
		},
		"required": []string{"query"},
	}
}

func (t *WebSearchTool) RequiresApproval(string) bool { return false }
func (t *WebSearchTool) ParallelSafe(string) bool     { return true }

func (t *WebSearchTool) PreviewCall(argsJSON string) string {
	var a struct {
		Query string `json:"query"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	if strings.TrimSpace(a.Query) == "" {
		return "web_search(?)"
	}
	return fmt.Sprintf("web_search(%s)", a.Query)
}

func (t *WebSearchTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var a struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("web_search: invalid args: %w", err)
	}
	query := strings.TrimSpace(a.Query)
	if query == "" {
		return "", fmt.Errorf("web_search: query is required")
	}
	maxResults := a.MaxResults
	if maxResults <= 0 || maxResults > 20 {
		maxResults = 10
	}

	results, err := ddgSearchFunc(ctx, query, maxResults)
	if err != nil {
		return "", fmt.Errorf("web_search: %w", err)
	}
	if len(results) == 0 {
		return fmt.Sprintf("No results found for: %s", query), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Search results for: %s\n\n", query)
	for i, r := range results {
		fmt.Fprintf(&b, "%d. %s\n   %s\n", i+1, r.Title, r.URL)
		if r.Snippet != "" {
			fmt.Fprintf(&b, "   %s\n", r.Snippet)
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

type ddgResult struct {
	Title   string
	URL     string
	Snippet string
}

var ddgSearchFunc = ddgSearch

func ddgSearch(ctx context.Context, query string, maxResults int) ([]ddgResult, error) {
	return ddgSearchWithBase(ctx, query, maxResults, "https://html.duckduckgo.com/html/")
}

func ddgSearchWithBase(ctx context.Context, query string, maxResults int, baseURL string) ([]ddgResult, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	searchURL := baseURL + "?q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "yottacode/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d from DuckDuckGo", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return nil, err
	}

	return parseDDGHTML(string(body), maxResults), nil
}

// parseDDGHTML extracts search results from DuckDuckGo's HTML response.
// Each result lives in a <div class="result ..."> containing an <a> with
// class "result__a" (title+URL) and an <a> with class "result__snippet"
// (description text).
func parseDDGHTML(rawHTML string, maxResults int) []ddgResult {
	doc, err := html.Parse(strings.NewReader(rawHTML))
	if err != nil {
		return nil
	}

	var results []ddgResult
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(results) >= maxResults {
			return
		}
		if isResultDiv(n) {
			if r, ok := extractResult(n); ok {
				results = append(results, r)
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return results
}

func isResultDiv(n *html.Node) bool {
	if n.Type != html.ElementNode || n.Data != "div" {
		return false
	}
	for _, a := range n.Attr {
		if a.Key == "class" && strings.Contains(a.Val, "result") && strings.Contains(a.Val, "web-result") {
			return true
		}
	}
	return false
}

func extractResult(n *html.Node) (ddgResult, bool) {
	var r ddgResult
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "a" {
			cls := attrVal(node, "class")
			if strings.Contains(cls, "result__a") && r.Title == "" {
				r.Title = cleanText(textContent(node))
				r.URL = resolveDDGURL(attrVal(node, "href"))
			} else if strings.Contains(cls, "result__snippet") && r.Snippet == "" {
				r.Snippet = cleanText(textContent(node))
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(n)
	return r, r.Title != "" && r.URL != ""
}

func attrVal(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func textContent(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(textContent(c))
	}
	return b.String()
}

// resolveDDGURL unwraps DuckDuckGo's redirect URLs. DDG wraps result links
// as //duckduckgo.com/l/?uddg=<encoded-url>&... — extract the real target.
func resolveDDGURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "duckduckgo.com/l/?") {
		if !strings.HasPrefix(raw, "http") {
			raw = "https:" + raw
		}
		if u, err := url.Parse(raw); err == nil {
			if target := u.Query().Get("uddg"); target != "" {
				return target
			}
		}
	}
	if strings.HasPrefix(raw, "//") {
		return "https:" + raw
	}
	return raw
}

func cleanText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(s)
}
