package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultBrowserbaseURL is Browserbase's public API.
const defaultBrowserbaseURL = "https://api.browserbase.com"

// BrowserbaseOptions configures the Browserbase provider
// (https://browserbase.com): a cloud browser reached over CDP that supplies
// stealth, residential proxies and CAPTCHA solving as server-side features —
// none of that logic lives here; a session is just a REST call that flips
// those flags and returns a CDP URL.
type BrowserbaseOptions struct {
	// APIKey and ProjectID authenticate the session request. The caller
	// resolves them (the key from the environment, never from a config file);
	// this package never logs or echoes the key.
	APIKey, ProjectID string
	// APIKeyEnv is the environment variable APIKey came from, used only to
	// make a "missing key" error actionable.
	APIKeyEnv string
	// BaseURL overrides the API endpoint (self-hosted gateways, tests).
	BaseURL string

	// NoProxies turns off residential proxies (on by default — they are what
	// makes datacenter-IP blocks go away, and they are billed). NoKeepAlive
	// turns off session reconnection after a dropped connection.
	NoProxies, NoKeepAlive bool
	// AdvancedStealth requests Browserbase's custom Chromium build; needs
	// their Scale plan.
	AdvancedStealth bool
	// SessionTimeoutSeconds caps the session's lifetime on Browserbase's
	// side (their maximum is 21600, six hours). 0 keeps the project default.
	SessionTimeoutSeconds int

	// HTTPClient overrides the client used for the REST calls (tests).
	HTTPClient *http.Client
}

// browserbaseClient makes the two REST calls a session needs.
type browserbaseClient struct {
	opts BrowserbaseOptions
	http *http.Client
}

func newBrowserbaseClient(opts BrowserbaseOptions) *browserbaseClient {
	if opts.BaseURL == "" {
		opts.BaseURL = defaultBrowserbaseURL
	}
	opts.BaseURL = strings.TrimRight(opts.BaseURL, "/")
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &browserbaseClient{opts: opts, http: hc}
}

// bbSession is a created Browserbase session.
type bbSession struct {
	ID         string
	ConnectURL string
	// Degraded lists paid features that were requested but dropped because
	// the account's plan doesn't include them (see create).
	Degraded []string
}

// checkConfigured reports a missing credential as ErrProviderNotConfigured.
func (c *browserbaseClient) checkConfigured() error {
	if strings.TrimSpace(c.opts.APIKey) == "" {
		hint := "an API key"
		if c.opts.APIKeyEnv != "" {
			hint = "the API key in $" + c.opts.APIKeyEnv
		}
		return fmt.Errorf("%w: Browserbase needs %s", ErrProviderNotConfigured, hint)
	}
	if strings.TrimSpace(c.opts.ProjectID) == "" {
		return fmt.Errorf("%w: Browserbase needs a project id ([browser.browserbase] project_id, or $BROWSERBASE_PROJECT_ID)", ErrProviderNotConfigured)
	}
	return nil
}

// create starts a session. A 402 means the account's plan lacks a paid
// feature that was asked for: keep-alive is dropped first, then proxies, and
// the create is retried — browsing on a free plan should degrade, not fail.
// What was dropped is reported in bbSession.Degraded.
func (c *browserbaseClient) create(ctx context.Context) (bbSession, error) {
	if err := c.checkConfigured(); err != nil {
		return bbSession{}, err
	}
	keepAlive, proxies := !c.opts.NoKeepAlive, !c.opts.NoProxies
	var degraded []string
	for {
		body := map[string]any{"projectId": c.opts.ProjectID}
		if keepAlive {
			body["keepAlive"] = true
		}
		if proxies {
			body["proxies"] = true
		}
		if c.opts.AdvancedStealth {
			body["browserSettings"] = map[string]any{"advancedStealth": true}
		}
		if c.opts.SessionTimeoutSeconds > 0 {
			body["timeout"] = c.opts.SessionTimeoutSeconds
		}
		code, respBody, err := c.post(ctx, "/v1/sessions", body)
		if err != nil {
			return bbSession{}, fmt.Errorf("%w: Browserbase request failed: %v", ErrLaunchFailed, err)
		}
		if code == http.StatusPaymentRequired {
			switch {
			case keepAlive:
				keepAlive = false
				degraded = append(degraded, "keep-alive")
				continue
			case proxies:
				proxies = false
				degraded = append(degraded, "proxies")
				continue
			}
		}
		if code < 200 || code > 299 {
			return bbSession{}, fmt.Errorf("%w: Browserbase returned HTTP %d: %s", ErrLaunchFailed, code, excerpt(respBody))
		}
		var out struct {
			ID         string `json:"id"`
			ConnectURL string `json:"connectUrl"`
		}
		if err := json.Unmarshal(respBody, &out); err != nil || out.ID == "" || out.ConnectURL == "" {
			return bbSession{}, fmt.Errorf("%w: Browserbase response had no session id/connectUrl", ErrLaunchFailed)
		}
		return bbSession{ID: out.ID, ConnectURL: out.ConnectURL, Degraded: degraded}, nil
	}
}

// release asks Browserbase to end a session. Sessions bill until released,
// so every path that drops a session calls this.
func (c *browserbaseClient) release(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	code, respBody, err := c.post(ctx, "/v1/sessions/"+url.PathEscape(id), map[string]any{
		"projectId": c.opts.ProjectID,
		"status":    "REQUEST_RELEASE",
	})
	if err != nil {
		return err
	}
	if code < 200 || code > 299 {
		return fmt.Errorf("Browserbase release returned HTTP %d: %s", code, excerpt(respBody))
	}
	return nil
}

// post sends a JSON body with the API key header and returns the status and
// (bounded) response body. The key travels only in the header.
func (c *browserbaseClient) post(ctx context.Context, path string, body any) (int, []byte, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.opts.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-BB-API-Key", c.opts.APIKey)
	resp, err := c.http.Do(req)
	if err != nil {
		// net/http errors quote the URL, never the headers.
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, err
}

// excerpt shortens a response body for an error message.
func excerpt(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
