package transport

import (
	"bytes"
	"io"
	"testing"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

type memoryConn struct {
	bytes.Buffer
}

func (m *memoryConn) Close() error { return nil }

func TestWebSocketStreamReadTextMessage(t *testing.T) {
	var wire bytes.Buffer
	if err := wsutil.WriteClientText(&wire, []byte(`{"jsonrpc":"2.0","id":1}`)); err != nil {
		t.Fatalf("write client frame: %v", err)
	}
	stream := NewWebSocketStream(&memoryConn{Buffer: wire})
	buf := make([]byte, 128)
	n, err := stream.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got, want := string(buf[:n]), "{\"jsonrpc\":\"2.0\",\"id\":1}\n"; got != want {
		t.Fatalf("read = %q, want %q", got, want)
	}
}

func TestWebSocketStreamRejectsBinaryMessage(t *testing.T) {
	var wire bytes.Buffer
	if err := wsutil.WriteClientMessage(&wire, ws.OpBinary, []byte("binary")); err != nil {
		t.Fatalf("write client frame: %v", err)
	}
	stream := NewWebSocketStream(&memoryConn{Buffer: wire})
	_, err := stream.Read(make([]byte, 32))
	if err != ErrBinaryMessage {
		t.Fatalf("read error = %v, want %v", err, ErrBinaryMessage)
	}
}

func TestWebSocketStreamWriteTextMessage(t *testing.T) {
	conn := &memoryConn{}
	stream := NewWebSocketStream(conn)
	payload := []byte("{\"jsonrpc\":\"2.0\"}\n")
	n, err := stream.Write(payload)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != len(payload) {
		t.Fatalf("write count = %d, want %d", n, len(payload))
	}
	got, opcode, err := wsutil.ReadServerData(conn)
	if err != nil {
		t.Fatalf("read server frame: %v", err)
	}
	if opcode != ws.OpText {
		t.Fatalf("opcode = %v, want text", opcode)
	}
	if string(got) != string(payload[:len(payload)-1]) {
		t.Fatalf("frame = %q, want %q", got, payload[:len(payload)-1])
	}
}

func TestWebSocketStreamWriteRequiresNewline(t *testing.T) {
	stream := NewWebSocketStream(&memoryConn{})
	if _, err := stream.Write([]byte("partial")); err == nil {
		t.Fatal("expected newline-delimited write error")
	}
}

func TestWebSocketStreamReadHandlesPartialBuffer(t *testing.T) {
	var wire bytes.Buffer
	if err := wsutil.WriteClientText(&wire, []byte("hello")); err != nil {
		t.Fatalf("write client frame: %v", err)
	}
	stream := NewWebSocketStream(&memoryConn{Buffer: wire})
	var got bytes.Buffer
	buf := make([]byte, 2)
	for {
		n, err := stream.Read(buf)
		got.Write(buf[:n])
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("read: %v", err)
		}
		if got.String() == "hello\n" {
			break
		}
	}
	if got.String() != "hello\n" {
		t.Fatalf("read = %q, want %q", got.String(), "hello\n")
	}
}
