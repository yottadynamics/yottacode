package transport

import (
	"bytes"
	"errors"
	"io"
	"sync"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

const maxMessageSize = 8 << 20

// ErrBinaryMessage reports a binary WebSocket frame where ACP requires text.
var ErrBinaryMessage = errors.New("acp transport: binary WebSocket messages are not supported")

// WebSocketStream adapts one WebSocket connection to the newline-delimited
// stream expected by the ACP SDK. Each text message is exposed as one line;
// each newline-delimited write becomes one text message.
type WebSocketStream struct {
	conn    io.ReadWriteCloser
	readMu  sync.Mutex
	writeMu sync.Mutex
	current []byte
}

// NewWebSocketStream wraps an upgraded server-side WebSocket connection.
func NewWebSocketStream(conn io.ReadWriteCloser) *WebSocketStream {
	return &WebSocketStream{conn: conn}
}

// Read returns one complete ACP JSON-RPC message including a trailing newline.
func (s *WebSocketStream) Read(p []byte) (int, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	for len(s.current) == 0 {
		payload, opcode, err := wsutil.ReadClientData(s.conn)
		if err != nil {
			return 0, err
		}
		if opcode != ws.OpText {
			return 0, ErrBinaryMessage
		}
		if len(payload) == 0 || len(payload) > maxMessageSize {
			return 0, errors.New("acp transport: WebSocket message exceeds size limit or is empty")
		}
		s.current = append(payload, '\n')
	}
	n := copy(p, s.current)
	s.current = s.current[n:]
	return n, nil
}

// Write emits each newline-delimited ACP message as one WebSocket text frame.
func (s *WebSocketStream) Write(p []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	written := len(p)
	for len(p) > 0 {
		line, rest, found := bytes.Cut(p, []byte{'\n'})
		if !found {
			return 0, errors.New("acp transport: write is not newline-delimited")
		}
		if len(line) == 0 || len(line) > maxMessageSize {
			return 0, errors.New("acp transport: WebSocket message exceeds size limit or is empty")
		}
		if err := wsutil.WriteServerText(s.conn, line); err != nil {
			return 0, err
		}
		p = rest
	}
	return written, nil
}

// Close closes the underlying WebSocket.
func (s *WebSocketStream) Close() error { return s.conn.Close() }
