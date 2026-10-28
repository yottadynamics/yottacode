package transport

import (
	"bytes"
	"errors"
	"io"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"

	"github.com/yottadynamics/yottacode/internal/syncutil"
)

// ClientWebSocketStream adapts a client-side WebSocket to the ACP SDK stream.
type ClientWebSocketStream struct {
	conn    io.ReadWriteCloser
	readMu  syncutil.Mutex
	writeMu syncutil.Mutex
	current []byte
}

// NewClientWebSocketStream wraps an established client-side WebSocket.
func NewClientWebSocketStream(conn io.ReadWriteCloser) *ClientWebSocketStream {
	return &ClientWebSocketStream{conn: conn}
}

func (s *ClientWebSocketStream) Read(p []byte) (int, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	for len(s.current) == 0 {
		payload, opcode, err := wsutil.ReadServerData(s.conn)
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

func (s *ClientWebSocketStream) Write(p []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	written := len(p)
	for len(p) > 0 {
		line, rest, found := bytes.Cut(p, []byte{'\n'})
		if !found || len(line) == 0 || len(line) > maxMessageSize {
			return 0, errors.New("acp transport: invalid newline-delimited WebSocket message")
		}
		if err := wsutil.WriteClientText(s.conn, line); err != nil {
			return 0, err
		}
		p = rest
	}
	return written, nil
}

func (s *ClientWebSocketStream) Close() error { return s.conn.Close() }
