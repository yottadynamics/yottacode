package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// doJSON executes req and decodes a 2xx body into out. Non-2xx
// surfaces the body in the error so the operator can see the
// provider's exact complaint (auth, rate limit, schema mismatch, …).
func doJSON(req *http.Request, out any) error {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
}

func boolPtr(b bool) *bool { return &b }
