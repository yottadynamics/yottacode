package acp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/yottadynamics/yottacode/internal/agentruntime"
	"github.com/yottadynamics/yottacode/internal/session"

	coderacp "github.com/coder/acp-go-sdk"
)

func TestConnectionAgentAttachAndTakeover(t *testing.T) {
	a := &fakeOutbound{id: "a"}
	b := &fakeOutbound{id: "b"}
	srv := NewServer(testOptions())
	sess := &acpSession{id: "session-1", srv: srv, controller: a, conn: a}
	srv.mu.Lock()
	srv.sessions[sess.id] = sess
	srv.mu.Unlock()

	agent := &connectionAgent{Server: srv, conn: b, scopes: map[string]bool{"read": true}}
	params, _ := json.Marshal(sessionAttachRequest{SessionID: sess.id, Role: "controller"})
	if _, err := agent.HandleExtensionMethod(context.Background(), extAttach, params); err == nil {
		t.Fatal("expected controller attach without prompt scope to fail")
	}

	agent.scopes["prompt"] = true
	if _, err := agent.HandleExtensionMethod(context.Background(), extAttach, params); err == nil {
		t.Fatal("expected takeover=false to reject existing controller")
	}
	params, _ = json.Marshal(sessionAttachRequest{SessionID: sess.id, Role: "controller", Takeover: true})
	response, err := agent.HandleExtensionMethod(context.Background(), extTakeover, params)
	if err != nil {
		t.Fatalf("takeover: %v", err)
	}
	if got := response.(sessionAttachResponse); got.Role != "controller" {
		t.Fatalf("response = %+v", got)
	}
	if !sess.canControl(b) || sess.canControl(a) {
		t.Fatal("controller ownership was not transferred")
	}
}

func TestConnectionAgentReplayExtension(t *testing.T) {
	srv := NewServer(testOptions())
	conn := &fakeOutbound{id: "replay"}
	sess := &acpSession{id: "session-1", srv: srv, controller: conn, conn: conn, rt: &agentruntime.Runtime{Session: &session.Session{}}}
	srv.mu.Lock()
	srv.sessions[sess.id] = sess
	srv.mu.Unlock()
	agent := &connectionAgent{Server: srv, conn: conn, scopes: map[string]bool{"read": true}}
	params, _ := json.Marshal(sessionReplayRequest{SessionID: sess.id})
	if _, err := agent.HandleExtensionMethod(context.Background(), extReplay, params); err != nil {
		t.Fatalf("replay: %v", err)
	}
}

var _ coderacp.ExtensionMethodHandler = (*connectionAgent)(nil)
