package dap

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	godap "github.com/google/go-dap"
)

func TestClientLifecycleBreakpointVariablesDisconnect(t *testing.T) {
	t.Parallel()

	server := newFakeServer(t, func(t *testing.T, c net.Conn) {
		msg := readDAPMessage(t, c)
		initReq, ok := msg.(*godap.InitializeRequest)
		if !ok {
			t.Fatalf("first message = %T, want InitializeRequest", msg)
		}
		writeDAPMessage(t, c, &godap.InitializeResponse{
			Response: godap.Response{ProtocolMessage: godap.ProtocolMessage{Seq: 1, Type: "response"}, RequestSeq: initReq.Seq, Success: true, Command: initReq.Command},
			Body:     godap.Capabilities{SupportsConfigurationDoneRequest: true},
		})
		writeDAPMessage(t, c, &godap.InitializedEvent{Event: godap.Event{ProtocolMessage: godap.ProtocolMessage{Seq: 2, Type: "event"}, Event: "initialized"}})

		msg = readDAPMessage(t, c)
		setReq, ok := msg.(*godap.SetBreakpointsRequest)
		if !ok {
			t.Fatalf("second message = %T, want SetBreakpointsRequest", msg)
		}
		writeDAPMessage(t, c, &godap.SetBreakpointsResponse{
			Response: godap.Response{ProtocolMessage: godap.ProtocolMessage{Seq: 3, Type: "response"}, RequestSeq: setReq.Seq, Success: true, Command: setReq.Command},
			Body:     godap.SetBreakpointsResponseBody{Breakpoints: []godap.Breakpoint{{Verified: true, Line: 12}}},
		})

		msg = readDAPMessage(t, c)
		configReq, ok := msg.(*godap.ConfigurationDoneRequest)
		if !ok {
			t.Fatalf("third message = %T, want ConfigurationDoneRequest", msg)
		}
		writeDAPMessage(t, c, &godap.ConfigurationDoneResponse{Response: godap.Response{ProtocolMessage: godap.ProtocolMessage{Seq: 4, Type: "response"}, RequestSeq: configReq.Seq, Success: true, Command: configReq.Command}})

		msg = readDAPMessage(t, c)
		continueReq, ok := msg.(*godap.ContinueRequest)
		if !ok {
			t.Fatalf("fourth message = %T, want ContinueRequest", msg)
		}
		writeDAPMessage(t, c, &godap.ContinueResponse{
			Response: godap.Response{ProtocolMessage: godap.ProtocolMessage{Seq: 5, Type: "response"}, RequestSeq: continueReq.Seq, Success: true, Command: continueReq.Command},
			Body:     godap.ContinueResponseBody{AllThreadsContinued: true},
		})
		writeDAPMessage(t, c, &godap.StoppedEvent{
			Event: godap.Event{ProtocolMessage: godap.ProtocolMessage{Seq: 6, Type: "event"}, Event: "stopped"},
			Body:  godap.StoppedEventBody{Reason: "breakpoint", ThreadId: 7},
		})

		msg = readDAPMessage(t, c)
		traceReq, ok := msg.(*godap.StackTraceRequest)
		if !ok {
			t.Fatalf("fifth message = %T, want StackTraceRequest", msg)
		}
		writeDAPMessage(t, c, &godap.StackTraceResponse{
			Response: godap.Response{ProtocolMessage: godap.ProtocolMessage{Seq: 7, Type: "response"}, RequestSeq: traceReq.Seq, Success: true, Command: traceReq.Command},
			Body:     godap.StackTraceResponseBody{StackFrames: []godap.StackFrame{{Id: 99, Name: "TestThing", Line: 12, Column: 1}}, TotalFrames: 1},
		})

		msg = readDAPMessage(t, c)
		scopesReq, ok := msg.(*godap.ScopesRequest)
		if !ok {
			t.Fatalf("sixth message = %T, want ScopesRequest", msg)
		}
		writeDAPMessage(t, c, &godap.ScopesResponse{
			Response: godap.Response{ProtocolMessage: godap.ProtocolMessage{Seq: 8, Type: "response"}, RequestSeq: scopesReq.Seq, Success: true, Command: scopesReq.Command},
			Body:     godap.ScopesResponseBody{Scopes: []godap.Scope{{Name: "Locals", VariablesReference: 123}}},
		})

		msg = readDAPMessage(t, c)
		varsReq, ok := msg.(*godap.VariablesRequest)
		if !ok {
			t.Fatalf("seventh message = %T, want VariablesRequest", msg)
		}
		writeDAPMessage(t, c, &godap.VariablesResponse{
			Response: godap.Response{ProtocolMessage: godap.ProtocolMessage{Seq: 9, Type: "response"}, RequestSeq: varsReq.Seq, Success: true, Command: varsReq.Command},
			Body:     godap.VariablesResponseBody{Variables: []godap.Variable{{Name: "got", Value: "nil", Type: "error"}}},
		})

		msg = readDAPMessage(t, c)
		discReq, ok := msg.(*godap.DisconnectRequest)
		if !ok {
			t.Fatalf("last message = %T, want DisconnectRequest", msg)
		}
		writeDAPMessage(t, c, &godap.DisconnectResponse{Response: godap.Response{ProtocolMessage: godap.ProtocolMessage{Seq: 10, Type: "response"}, RequestSeq: discReq.Seq, Success: true, Command: discReq.Command}})
	})
	defer server.Close()

	client, err := NewClient(server, ClientOptions{RequestTimeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close(context.Background())

	ctx := context.Background()
	if _, err := client.Initialize(ctx, godap.InitializeRequestArguments{AdapterID: "go", LinesStartAt1: true, ColumnsStartAt1: true}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if ev := mustEvent(t, client.Events(), "initialized"); ev.GetEvent().Event != "initialized" {
		t.Fatalf("event = %q, want initialized", ev.GetEvent().Event)
	}
	bps, err := client.SetBreakpoints(ctx, godap.SetBreakpointsArguments{Source: godap.Source{Path: "thing_test.go"}, Breakpoints: []godap.SourceBreakpoint{{Line: 12}}})
	if err != nil {
		t.Fatalf("SetBreakpoints: %v", err)
	}
	if len(bps.Breakpoints) != 1 || !bps.Breakpoints[0].Verified {
		t.Fatalf("breakpoints = %#v, want one verified breakpoint", bps.Breakpoints)
	}
	if err := client.ConfigurationDone(ctx); err != nil {
		t.Fatalf("ConfigurationDone: %v", err)
	}
	if _, err := client.Continue(ctx, godap.ContinueArguments{ThreadId: 7}); err != nil {
		t.Fatalf("Continue: %v", err)
	}
	stopped := mustEvent(t, client.Events(), "stopped")
	stoppedEvent, ok := stopped.(*godap.StoppedEvent)
	if !ok || stoppedEvent.Body.Reason != "breakpoint" {
		t.Fatalf("stopped event = %#v, want breakpoint", stopped)
	}
	trace, err := client.StackTrace(ctx, godap.StackTraceArguments{ThreadId: 7})
	if err != nil {
		t.Fatalf("StackTrace: %v", err)
	}
	if len(trace.StackFrames) != 1 || trace.StackFrames[0].Id != 99 {
		t.Fatalf("stack trace = %#v, want frame 99", trace.StackFrames)
	}
	scopes, err := client.Scopes(ctx, godap.ScopesArguments{FrameId: 99})
	if err != nil {
		t.Fatalf("Scopes: %v", err)
	}
	if len(scopes.Scopes) != 1 || scopes.Scopes[0].VariablesReference != 123 {
		t.Fatalf("scopes = %#v, want variables reference 123", scopes.Scopes)
	}
	vars, err := client.Variables(ctx, godap.VariablesArguments{VariablesReference: 123})
	if err != nil {
		t.Fatalf("Variables: %v", err)
	}
	if len(vars.Variables) != 1 || vars.Variables[0].Name != "got" || vars.Variables[0].Value != "nil" {
		t.Fatalf("variables = %#v, want got=nil", vars.Variables)
	}
	if err := client.Disconnect(ctx, godap.DisconnectArguments{}); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
}

func TestClientSupportsLaunchAttachSteppingAndEvaluate(t *testing.T) {
	t.Parallel()

	server := newFakeServer(t, func(t *testing.T, c net.Conn) {
		for _, want := range []string{"launch", "attach", "next", "stepIn", "stepOut", "evaluate"} {
			msg := readDAPMessage(t, c)
			req, ok := msg.(godap.RequestMessage)
			if !ok {
				t.Fatalf("message = %T, want request", msg)
			}
			if req.GetRequest().Command != want {
				t.Fatalf("command = %q, want %q", req.GetRequest().Command, want)
			}
			switch want {
			case "evaluate":
				writeDAPMessage(t, c, &godap.EvaluateResponse{
					Response: godap.Response{ProtocolMessage: godap.ProtocolMessage{Seq: 1, Type: "response"}, RequestSeq: req.GetRequest().Seq, Success: true, Command: want},
					Body:     godap.EvaluateResponseBody{Result: "42"},
				})
			case "next":
				writeDAPMessage(t, c, &godap.NextResponse{Response: godap.Response{ProtocolMessage: godap.ProtocolMessage{Seq: 1, Type: "response"}, RequestSeq: req.GetRequest().Seq, Success: true, Command: want}})
			case "stepIn":
				writeDAPMessage(t, c, &godap.StepInResponse{Response: godap.Response{ProtocolMessage: godap.ProtocolMessage{Seq: 1, Type: "response"}, RequestSeq: req.GetRequest().Seq, Success: true, Command: want}})
			case "stepOut":
				writeDAPMessage(t, c, &godap.StepOutResponse{Response: godap.Response{ProtocolMessage: godap.ProtocolMessage{Seq: 1, Type: "response"}, RequestSeq: req.GetRequest().Seq, Success: true, Command: want}})
			case "launch":
				writeDAPMessage(t, c, &godap.LaunchResponse{Response: godap.Response{ProtocolMessage: godap.ProtocolMessage{Seq: 1, Type: "response"}, RequestSeq: req.GetRequest().Seq, Success: true, Command: want}})
			case "attach":
				writeDAPMessage(t, c, &godap.AttachResponse{Response: godap.Response{ProtocolMessage: godap.ProtocolMessage{Seq: 1, Type: "response"}, RequestSeq: req.GetRequest().Seq, Success: true, Command: want}})
			}
		}
	})
	defer server.Close()

	client, err := NewClient(server, ClientOptions{RequestTimeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close(context.Background())

	ctx := context.Background()
	if err := client.Launch(ctx, map[string]any{"program": "."}); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if err := client.Attach(ctx, map[string]any{"processId": 1234}); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := client.Next(ctx, godap.NextArguments{ThreadId: 1}); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if err := client.StepIn(ctx, godap.StepInArguments{ThreadId: 1}); err != nil {
		t.Fatalf("StepIn: %v", err)
	}
	if err := client.StepOut(ctx, godap.StepOutArguments{ThreadId: 1}); err != nil {
		t.Fatalf("StepOut: %v", err)
	}
	eval, err := client.Evaluate(ctx, godap.EvaluateArguments{Expression: "answer"})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if eval.Result != "42" {
		t.Fatalf("Evaluate result = %q, want 42", eval.Result)
	}
}

func TestClientRequestTimeout(t *testing.T) {
	t.Parallel()

	server := newFakeServer(t, func(t *testing.T, c net.Conn) {
		_ = readDAPMessage(t, c)
		<-time.After(250 * time.Millisecond)
	})
	defer server.Close()

	client, err := NewClient(server, ClientOptions{RequestTimeout: 25 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close(context.Background())

	_, err = client.Initialize(context.Background(), godap.InitializeRequestArguments{AdapterID: "go", LinesStartAt1: true, ColumnsStartAt1: true})
	if !errors.Is(err, ErrRequestTimeout) {
		t.Fatalf("Initialize error = %v, want ErrRequestTimeout", err)
	}
}

func TestClientPreservesLifecycleEventWhenEventBufferFull(t *testing.T) {
	client := &Client{events: make(chan godap.EventMessage, 1)}
	client.deliverEvent(&godap.OutputEvent{Event: godap.Event{Event: "output"}})
	client.deliverEvent(&godap.StoppedEvent{Event: godap.Event{Event: "stopped"}, Body: godap.StoppedEventBody{Reason: "breakpoint", ThreadId: 7}})

	select {
	case ev := <-client.Events():
		stopped, ok := ev.(*godap.StoppedEvent)
		if !ok || stopped.Body.ThreadId != 7 {
			t.Fatalf("event = %#v, want stopped thread 7", ev)
		}
	default:
		t.Fatal("no lifecycle event delivered")
	}
	if !client.EventOverflow() {
		t.Fatal("EventOverflow = false, want dropped-output signal")
	}
}

func TestClientReportsMalformedFrame(t *testing.T) {
	t.Parallel()

	server := newFakeServer(t, func(t *testing.T, c net.Conn) {
		_, _ = c.Write([]byte("Content-Length: nope\r\n\r\n{}"))
	})
	defer server.Close()

	client, err := NewClient(server, ClientOptions{RequestTimeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close(context.Background())

	select {
	case err := <-client.Done():
		if !errors.Is(err, ErrProtocolMalformed) {
			t.Fatalf("Done error = %v, want ErrProtocolMalformed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for malformed frame error")
	}
}

func TestSafeStringAllowsConcurrentWriteAndRead(t *testing.T) {
	var buf safeString
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			_, _ = buf.Write([]byte("stderr\n"))
		}
	}()
	for i := 0; i < 1000; i++ {
		_ = buf.String()
	}
	<-done
	if got := buf.String(); !strings.Contains(got, "stderr") {
		t.Fatalf("safeString content = %q, want stderr", got)
	}
}

func TestSessionCloseDisconnectsAndKillsProcess(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	sess, err := StartSession(context.Background(), SessionOptions{
		Command:        "sh",
		Args:           []string{"-c", "cat >/dev/null"},
		StartupTimeout: time.Second,
		RequestTimeout: 25 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	if err := sess.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if sess.Cmd.ProcessState == nil {
		t.Fatal("process state is nil, want process reaped")
	}
}

type fakeServer struct {
	conn net.Conn
	done chan struct{}
	once sync.Once
}

func newFakeServer(t *testing.T, serve func(*testing.T, net.Conn)) *fakeServer {
	t.Helper()
	server, client := net.Pipe()
	fs := &fakeServer{conn: client, done: make(chan struct{})}
	go func() {
		defer close(fs.done)
		defer server.Close()
		serve(t, server)
	}()
	return fs
}

func (s *fakeServer) Read(p []byte) (int, error)  { return s.conn.Read(p) }
func (s *fakeServer) Write(p []byte) (int, error) { return s.conn.Write(p) }
func (s *fakeServer) Close() error {
	var err error
	s.once.Do(func() {
		err = s.conn.Close()
		select {
		case <-s.done:
		case <-time.After(time.Second):
		}
	})
	return err
}

func readDAPMessage(t *testing.T, r io.Reader) godap.Message {
	t.Helper()
	msg, err := godap.ReadProtocolMessage(bufio.NewReader(r))
	if err != nil {
		t.Fatalf("read DAP message: %v", err)
	}
	return msg
}

func writeDAPMessage(t *testing.T, w io.Writer, msg godap.Message) {
	t.Helper()
	if err := godap.WriteProtocolMessage(w, msg); err != nil {
		t.Fatalf("write DAP message: %v", err)
	}
}

func mustEvent(t *testing.T, events <-chan godap.EventMessage, name string) godap.EventMessage {
	t.Helper()
	select {
	case ev := <-events:
		if ev.GetEvent().Event != name {
			t.Fatalf("event = %q, want %q", ev.GetEvent().Event, name)
		}
		return ev
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s event", name)
	}
	return nil
}
