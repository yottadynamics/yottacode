package acp

import (
	"context"
	"testing"

	coderacp "github.com/coder/acp-go-sdk"
)

func TestSessionDetachReleasesController(t *testing.T) {
	conn := &fakeOutbound{id: "controller"}
	sess := &acpSession{controller: conn, controllerScopes: map[string]bool{"approve": true}}
	sess.detach(conn)
	if sess.controller != nil || sess.controllerScopes != nil {
		t.Fatal("controller was not detached")
	}
}

func TestSessionOutboundRequiresApproveScope(t *testing.T) {
	conn := &fakeOutbound{id: "controller"}
	sess := &acpSession{controller: conn, controllerScopes: map[string]bool{"prompt": true}}
	_, err := (sessionOutbound{session: sess}).RequestPermission(context.Background(), coderacp.RequestPermissionRequest{})
	if err == nil {
		t.Fatal("expected approval scope denial")
	}
}
