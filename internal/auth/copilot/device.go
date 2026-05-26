package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultClientID       = "Iv1.b507a08c87ecfe98"
	DefaultScope          = "copilot"
	DeviceCodeEndpoint    = "https://github.com/login/device/code"
	OAuthTokenEndpoint    = "https://github.com/login/oauth/access_token"
)

// DeviceCode is the response from the device code initiation request.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// RequestDeviceCode initiates the device code flow.
func RequestDeviceCode(ctx context.Context, httpClient *http.Client, clientID string) (DeviceCode, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if clientID == "" {
		clientID = DefaultClientID
	}
	v := url.Values{}
	v.Set("client_id", clientID)
	v.Set("scope", DefaultScope)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, DeviceCodeEndpoint, strings.NewReader(v.Encode()))
	if err != nil {
		return DeviceCode{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return DeviceCode{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return DeviceCode{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return DeviceCode{}, fmt.Errorf("copilot: device code request failed (status %d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var dc DeviceCode
	if err := json.Unmarshal(raw, &dc); err != nil {
		return DeviceCode{}, fmt.Errorf("copilot: decode device code response: %w", err)
	}
	if dc.Interval == 0 {
		dc.Interval = 5
	}
	return dc, nil
}

// PollForToken polls the OAuth token endpoint until the user completes
// the device authorization or the code expires.
func PollForToken(ctx context.Context, httpClient *http.Client, clientID string, dc DeviceCode) (string, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if clientID == "" {
		clientID = DefaultClientID
	}

	deadline := time.After(time.Duration(dc.ExpiresIn) * time.Second)
	interval := time.Duration(dc.Interval) * time.Second

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline:
			return "", fmt.Errorf("copilot: device code expired after %ds", dc.ExpiresIn)
		case <-time.After(interval):
		}

		v := url.Values{}
		v.Set("client_id", clientID)
		v.Set("device_code", dc.DeviceCode)
		v.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, OAuthTokenEndpoint, strings.NewReader(v.Encode()))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")

		resp, err := httpClient.Do(req)
		if err != nil {
			return "", err
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		resp.Body.Close()
		if err != nil {
			return "", err
		}

		var tr struct {
			AccessToken string `json:"access_token"`
			TokenType   string `json:"token_type"`
			Scope       string `json:"scope"`
			Error       string `json:"error"`
			ErrorDesc   string `json:"error_description"`
		}
		if err := json.Unmarshal(raw, &tr); err != nil {
			return "", fmt.Errorf("copilot: decode poll response: %w", err)
		}

		switch tr.Error {
		case "":
			if tr.AccessToken != "" {
				return tr.AccessToken, nil
			}
			return "", fmt.Errorf("copilot: empty access token in poll response")
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		case "expired_token":
			return "", fmt.Errorf("copilot: device code expired")
		case "access_denied":
			return "", fmt.Errorf("copilot: access denied by user")
		default:
			desc := tr.ErrorDesc
			if desc == "" {
				desc = tr.Error
			}
			return "", fmt.Errorf("copilot: %s", desc)
		}
	}
}
