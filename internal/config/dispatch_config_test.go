package config

import "testing"

func TestDispatchMaxWorkers(t *testing.T) {
	var c Config
	if got := c.DispatchMaxWorkers(); got != 0 {
		t.Fatalf("unset = %d, want 0", got)
	}
	c.Dispatch.MaxWorkers = 3
	if got := c.DispatchMaxWorkers(); got != 3 {
		t.Fatalf("set = %d, want 3", got)
	}
	c.Dispatch.MaxWorkers = -2
	if got := c.DispatchMaxWorkers(); got != 0 {
		t.Fatalf("negative = %d, want 0", got)
	}
	c.Dispatch.MaxWorkers = 1
	if err := Validate(c); err == nil {
		t.Fatal("max_workers = 1 should be rejected")
	}
}
