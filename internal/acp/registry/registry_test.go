package registry

import (
	"context"
	"testing"

	coderacp "github.com/coder/acp-go-sdk"
)

type target struct {
	updates chan coderacp.SessionNotification
}

func (t *target) SessionUpdate(_ context.Context, n coderacp.SessionNotification) error {
	t.updates <- n
	return nil
}
func (t *target) RequestPermission(context.Context, coderacp.RequestPermissionRequest) (coderacp.RequestPermissionResponse, error) {
	return coderacp.RequestPermissionResponse{}, nil
}

func TestRegistryRejectsFullAndQueuesUpdates(t *testing.T) {
	r := New(1)
	tgt := &target{updates: make(chan coderacp.SessionNotification, 1)}
	c, err := r.Add("one", "token", nil, tgt)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := r.Add("two", "token", nil, tgt); err != ErrConnectionLimit {
		t.Fatalf("err=%v", err)
	}
	if err := c.SessionUpdate(context.Background(), coderacp.SessionNotification{}); err != nil {
		t.Fatal(err)
	}
}
