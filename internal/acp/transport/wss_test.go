package transport

import (
	"context"
	"crypto/tls"
	"net/http/httptest"
	"testing"
	"time"

	coderacp "github.com/coder/acp-go-sdk"
	"github.com/gobwas/ws"

	"github.com/yottadynamics/yottacode/internal/acp"
	acpauth "github.com/yottadynamics/yottacode/internal/acp/auth"
	"github.com/yottadynamics/yottacode/internal/cli"
)

func TestWSSHandlerRejectsInvalidToken(t *testing.T) {
	store := acpauth.NewStoreAt(t.TempDir() + "/tokens.json")
	server := httptest.NewTLSServer(&WSSHandler{Server: acp.NewServer(cli.ChatOptions{}), TokenStore: store})
	defer server.Close()
	dialer := ws.Dialer{TLSConfig: &tls.Config{InsecureSkipVerify: true}}
	if _, _, _, err := dialer.Dial(context.Background(), "wss://"+server.Listener.Addr().String()); err == nil {
		t.Fatal("expected invalid token handshake failure")
	}
}

func TestWSSHandlerAuthenticatesPersistedToken(t *testing.T) {
	store := acpauth.NewStoreAt(t.TempDir() + "/tokens.json")
	_, token, err := store.Create("test", []acpauth.Scope{acpauth.ScopeRead}, nil, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	server := httptest.NewTLSServer(&WSSHandler{Server: acp.NewServer(cli.ChatOptions{}), TokenStore: store})
	defer server.Close()
	dialer := ws.Dialer{
		TLSConfig: &tls.Config{InsecureSkipVerify: true},
		Protocols: []string{ACPSubprotocol},
		Header:    ws.HandshakeHeaderString("Authorization: Bearer " + token + "\r\n"),
	}
	conn, _, handshake, err := dialer.Dial(context.Background(), "wss://"+server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if handshake.Protocol != ACPSubprotocol {
		t.Fatalf("protocol = %q, want %q", handshake.Protocol, ACPSubprotocol)
	}
}

var _ coderacp.Client
