package main

import "time"

// reconnectBackoff returns bounded delays for attach reconnect attempts.
func reconnectBackoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	d := time.Second << min(attempt, 5)
	if d > 30*time.Second {
		return 30 * time.Second
	}
	return d
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
