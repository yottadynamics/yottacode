package main

import (
	"testing"
	"time"
)

func TestReconnectBackoffIsBounded(t *testing.T) {
	if reconnectBackoff(0) != time.Second {
		t.Fatal("first retry should wait one second")
	}
	if reconnectBackoff(5) != 30*time.Second {
		t.Fatal("expected exponential fifth retry")
	}
	if reconnectBackoff(10) != 30*time.Second {
		t.Fatal("backoff must be capped")
	}
}
