package transport

import (
	"bufio"
	"crypto/subtle"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"

	coderacp "github.com/coder/acp-go-sdk"
	"github.com/yottadynamics/yottacode/internal/acp"
	acpauth "github.com/yottadynamics/yottacode/internal/acp/auth"
)

const ACPSubprotocol = "yottacode.acp.v1"

// WSSHandler accepts authenticated WebSocket ACP connections and binds each
// connection to a fresh SDK AgentSideConnection backed by the shared server.
type WSSHandler struct {
	Server      *acp.Server
	BearerToken string
	TokenStore  *acpauth.Store
}

// ServeHTTP upgrades one authenticated request and runs ACP until the peer
// disconnects. TLS is intentionally owned by net/http's caller.
func (h *WSSHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	record, scopes := h.identity(r)
	if len(scopes) == 0 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, rw, _, err := (ws.HTTPUpgrader{Protocol: func(protocol string) bool { return protocol == ACPSubprotocol }}).Upgrade(r, w)
	if err != nil {
		return
	}
	upgraded := &upgradedStream{conn: conn, rw: rw}
	stream := NewWebSocketStream(upgraded)
	ref := acp.NewConnectionRef()
	_ = record
	agentConn := coderacp.NewAgentSideConnection(h.Server.AgentWithScopes(ref, scopes, record.AllowsProject), stream, stream)
	ref.Set(agentConn)
	<-agentConn.Done()
	_ = upgraded.Close()
}

func (h *WSSHandler) identity(r *http.Request) (acpauth.TokenRecord, []string) {
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if h.TokenStore != nil {
		record, err := h.TokenStore.Authenticate(got, time.Now())
		if err == nil {
			scopes := make([]string, 0, len(record.Scopes))
			for _, scope := range record.Scopes {
				scopes = append(scopes, string(scope))
			}
			return record, scopes
		}
		return acpauth.TokenRecord{}, nil
	}
	want := strings.TrimSpace(h.BearerToken)
	if want != "" && len(got) == len(want) && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1 {
		return acpauth.TokenRecord{Scopes: []acpauth.Scope{acpauth.ScopeAdmin}}, []string{"admin"}
	}
	return acpauth.TokenRecord{}, nil
}

// upgradedStream preserves bytes buffered by the HTTP upgrade while exposing
// the underlying connection as the io.ReadWriteCloser required by the SDK.
type upgradedStream struct {
	conn net.Conn
	rw   *bufio.ReadWriter
}

func (s *upgradedStream) Read(p []byte) (int, error)  { return s.rw.Read(p) }
func (s *upgradedStream) Write(p []byte) (int, error) { return s.rw.Write(p) }
func (s *upgradedStream) Close() error {
	if s.rw == nil || s.conn == nil {
		return errors.New("acp transport: stream already closed")
	}
	_ = wsutil.WriteServerMessage(s.conn, ws.OpClose, nil)
	return s.conn.Close()
}

var _ io.ReadWriteCloser = (*upgradedStream)(nil)
