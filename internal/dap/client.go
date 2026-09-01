// Package dap provides a small yottacode-owned Debug Adapter Protocol client.
// It delegates protocol structs and message framing to github.com/google/go-dap,
// while this package owns request correlation, event delivery, timeouts, and
// debug-adapter process teardown policy.
package dap

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	godap "github.com/google/go-dap"
)

const defaultRequestTimeout = 5 * time.Second

var (
	// ErrRequestTimeout marks a DAP request that did not receive a response
	// before the configured request deadline.
	ErrRequestTimeout = errors.New("DAP request timed out")

	// ErrProtocolMalformed marks invalid DAP framing or a message that could not
	// be decoded by the go-dap protocol codec.
	ErrProtocolMalformed = errors.New("malformed DAP protocol message")

	// ErrSessionClosed marks attempts to use a client after its transport has
	// closed or after the adapter process has exited.
	ErrSessionClosed = errors.New("DAP session closed")
)

// ClientOptions tunes request behavior for an already-open DAP transport.
type ClientOptions struct {
	RequestTimeout time.Duration
}

// Client speaks DAP over one bidirectional stream. It is safe for concurrent
// requests; asynchronous adapter events are delivered through Events.
type Client struct {
	conn io.ReadWriteCloser
	br   *bufio.Reader

	requestTimeout time.Duration

	mu      sync.Mutex
	seq     int
	pending map[int]chan godap.ResponseMessage
	closed  bool

	writeMu       sync.Mutex
	eventMu       sync.Mutex
	events        chan godap.EventMessage
	eventOverflow bool
	done          chan error
	once          sync.Once
}

// NewClient starts the reader loop for conn and returns a DAP client bound to
// that transport. The caller remains responsible for closing process resources
// unless it uses StartSession.
func NewClient(conn io.ReadWriteCloser, opts ClientOptions) (*Client, error) {
	if conn == nil {
		return nil, fmt.Errorf("create DAP client: nil transport")
	}
	timeout := opts.RequestTimeout
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	c := &Client{
		conn:           conn,
		br:             bufio.NewReader(conn),
		requestTimeout: timeout,
		pending:        make(map[int]chan godap.ResponseMessage),
		events:         make(chan godap.EventMessage, 1024),
		done:           make(chan error, 1),
	}
	go c.readLoop()
	return c, nil
}

// Events returns the asynchronous event stream from the debug adapter. The
// stream closes when the client closes or the adapter transport fails.
func (c *Client) Events() <-chan godap.EventMessage { return c.events }

// EventOverflow reports whether any adapter events were dropped because the
// consumer fell behind. Lifecycle events are preserved preferentially.
func (c *Client) EventOverflow() bool {
	c.eventMu.Lock()
	defer c.eventMu.Unlock()
	return c.eventOverflow
}

// Done reports why the reader loop ended. A nil error means the client was
// closed intentionally.
func (c *Client) Done() <-chan error { return c.done }

// Close closes the underlying transport and releases pending requests. It does
// not send a DAP disconnect request; callers that need protocol-level teardown
// should call Disconnect first.
func (c *Client) Close(context.Context) error {
	c.closeWith(nil)
	return c.conn.Close()
}

// Initialize sends DAP initialize and returns the adapter capabilities.
func (c *Client) Initialize(ctx context.Context, args godap.InitializeRequestArguments) (godap.Capabilities, error) {
	resp, err := c.roundTrip(ctx, &godap.InitializeRequest{Request: newRequest("initialize"), Arguments: args})
	if err != nil {
		return godap.Capabilities{}, err
	}
	typed, ok := resp.(*godap.InitializeResponse)
	if !ok {
		return godap.Capabilities{}, unexpectedResponse("initialize", resp)
	}
	return typed.Body, nil
}

// Launch starts a debuggee through implementation-specific adapter arguments.
func (c *Client) Launch(ctx context.Context, args map[string]any) error {
	raw, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("marshal launch arguments: %w", err)
	}
	resp, err := c.roundTrip(ctx, &godap.LaunchRequest{Request: newRequest("launch"), Arguments: raw})
	if err != nil {
		return err
	}
	_, ok := resp.(*godap.LaunchResponse)
	if !ok {
		return unexpectedResponse("launch", resp)
	}
	return nil
}

// Attach connects to a debuggee through implementation-specific adapter arguments.
func (c *Client) Attach(ctx context.Context, args map[string]any) error {
	raw, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("marshal attach arguments: %w", err)
	}
	resp, err := c.roundTrip(ctx, &godap.AttachRequest{Request: newRequest("attach"), Arguments: raw})
	if err != nil {
		return err
	}
	_, ok := resp.(*godap.AttachResponse)
	if !ok {
		return unexpectedResponse("attach", resp)
	}
	return nil
}

// SetBreakpoints replaces breakpoints for one source file.
func (c *Client) SetBreakpoints(ctx context.Context, args godap.SetBreakpointsArguments) (godap.SetBreakpointsResponseBody, error) {
	resp, err := c.roundTrip(ctx, &godap.SetBreakpointsRequest{Request: newRequest("setBreakpoints"), Arguments: args})
	if err != nil {
		return godap.SetBreakpointsResponseBody{}, err
	}
	typed, ok := resp.(*godap.SetBreakpointsResponse)
	if !ok {
		return godap.SetBreakpointsResponseBody{}, unexpectedResponse("setBreakpoints", resp)
	}
	return typed.Body, nil
}

// ConfigurationDone tells the adapter that breakpoint configuration is complete.
func (c *Client) ConfigurationDone(ctx context.Context) error {
	resp, err := c.roundTrip(ctx, &godap.ConfigurationDoneRequest{Request: newRequest("configurationDone"), Arguments: &godap.ConfigurationDoneArguments{}})
	if err != nil {
		return err
	}
	_, ok := resp.(*godap.ConfigurationDoneResponse)
	if !ok {
		return unexpectedResponse("configurationDone", resp)
	}
	return nil
}

// Continue resumes execution for a thread.
func (c *Client) Continue(ctx context.Context, args godap.ContinueArguments) (godap.ContinueResponseBody, error) {
	resp, err := c.roundTrip(ctx, &godap.ContinueRequest{Request: newRequest("continue"), Arguments: args})
	if err != nil {
		return godap.ContinueResponseBody{}, err
	}
	typed, ok := resp.(*godap.ContinueResponse)
	if !ok {
		return godap.ContinueResponseBody{}, unexpectedResponse("continue", resp)
	}
	return typed.Body, nil
}

// Next steps over the next source statement for a thread.
func (c *Client) Next(ctx context.Context, args godap.NextArguments) error {
	resp, err := c.roundTrip(ctx, &godap.NextRequest{Request: newRequest("next"), Arguments: args})
	if err != nil {
		return err
	}
	_, ok := resp.(*godap.NextResponse)
	if !ok {
		return unexpectedResponse("next", resp)
	}
	return nil
}

// StepIn steps into the next call target for a thread.
func (c *Client) StepIn(ctx context.Context, args godap.StepInArguments) error {
	resp, err := c.roundTrip(ctx, &godap.StepInRequest{Request: newRequest("stepIn"), Arguments: args})
	if err != nil {
		return err
	}
	_, ok := resp.(*godap.StepInResponse)
	if !ok {
		return unexpectedResponse("stepIn", resp)
	}
	return nil
}

// StepOut steps out of the current frame for a thread.
func (c *Client) StepOut(ctx context.Context, args godap.StepOutArguments) error {
	resp, err := c.roundTrip(ctx, &godap.StepOutRequest{Request: newRequest("stepOut"), Arguments: args})
	if err != nil {
		return err
	}
	_, ok := resp.(*godap.StepOutResponse)
	if !ok {
		return unexpectedResponse("stepOut", resp)
	}
	return nil
}

// StackTrace returns frames for a stopped thread.
func (c *Client) StackTrace(ctx context.Context, args godap.StackTraceArguments) (godap.StackTraceResponseBody, error) {
	resp, err := c.roundTrip(ctx, &godap.StackTraceRequest{Request: newRequest("stackTrace"), Arguments: args})
	if err != nil {
		return godap.StackTraceResponseBody{}, err
	}
	typed, ok := resp.(*godap.StackTraceResponse)
	if !ok {
		return godap.StackTraceResponseBody{}, unexpectedResponse("stackTrace", resp)
	}
	return typed.Body, nil
}

// Scopes returns variable scopes for a stack frame.
func (c *Client) Scopes(ctx context.Context, args godap.ScopesArguments) (godap.ScopesResponseBody, error) {
	resp, err := c.roundTrip(ctx, &godap.ScopesRequest{Request: newRequest("scopes"), Arguments: args})
	if err != nil {
		return godap.ScopesResponseBody{}, err
	}
	typed, ok := resp.(*godap.ScopesResponse)
	if !ok {
		return godap.ScopesResponseBody{}, unexpectedResponse("scopes", resp)
	}
	return typed.Body, nil
}

// Variables returns children for a DAP variables reference.
func (c *Client) Variables(ctx context.Context, args godap.VariablesArguments) (godap.VariablesResponseBody, error) {
	resp, err := c.roundTrip(ctx, &godap.VariablesRequest{Request: newRequest("variables"), Arguments: args})
	if err != nil {
		return godap.VariablesResponseBody{}, err
	}
	typed, ok := resp.(*godap.VariablesResponse)
	if !ok {
		return godap.VariablesResponseBody{}, unexpectedResponse("variables", resp)
	}
	return typed.Body, nil
}

// Evaluate evaluates an expression in the adapter-selected context.
func (c *Client) Evaluate(ctx context.Context, args godap.EvaluateArguments) (godap.EvaluateResponseBody, error) {
	resp, err := c.roundTrip(ctx, &godap.EvaluateRequest{Request: newRequest("evaluate"), Arguments: args})
	if err != nil {
		return godap.EvaluateResponseBody{}, err
	}
	typed, ok := resp.(*godap.EvaluateResponse)
	if !ok {
		return godap.EvaluateResponseBody{}, unexpectedResponse("evaluate", resp)
	}
	return typed.Body, nil
}

// Disconnect requests protocol-level teardown from the debug adapter.
func (c *Client) Disconnect(ctx context.Context, args godap.DisconnectArguments) error {
	resp, err := c.roundTrip(ctx, &godap.DisconnectRequest{Request: newRequest("disconnect"), Arguments: &args})
	if err != nil {
		return err
	}
	_, ok := resp.(*godap.DisconnectResponse)
	if !ok {
		return unexpectedResponse("disconnect", resp)
	}
	return nil
}

func (c *Client) roundTrip(ctx context.Context, req godap.RequestMessage) (godap.ResponseMessage, error) {
	seq, ch, err := c.register(req)
	if err != nil {
		return nil, err
	}
	defer c.unregister(seq)

	c.writeMu.Lock()
	err = godap.WriteProtocolMessage(c.conn, req)
	c.writeMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("send DAP %s request: %w", req.GetRequest().Command, err)
	}

	waitCtx := ctx
	cancel := func() {}
	if _, ok := ctx.Deadline(); !ok && c.requestTimeout > 0 {
		waitCtx, cancel = context.WithTimeout(ctx, c.requestTimeout)
	}
	defer cancel()

	select {
	case resp := <-ch:
		if errResp, ok := resp.(*godap.ErrorResponse); ok {
			return nil, dapError(errResp)
		}
		return resp, nil
	case <-waitCtx.Done():
		if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
			return nil, ErrRequestTimeout
		}
		return nil, waitCtx.Err()
	case err := <-c.done:
		if err == nil {
			return nil, ErrSessionClosed
		}
		return nil, err
	}
}

func (c *Client) register(req godap.RequestMessage) (int, chan godap.ResponseMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, nil, ErrSessionClosed
	}
	c.seq++
	seq := c.seq
	req.GetRequest().Seq = seq
	req.GetRequest().Type = "request"
	ch := make(chan godap.ResponseMessage, 1)
	c.pending[seq] = ch
	return seq, ch, nil
}

func (c *Client) unregister(seq int) {
	c.mu.Lock()
	delete(c.pending, seq)
	c.mu.Unlock()
}

func (c *Client) readLoop() {
	for {
		msg, err := godap.ReadProtocolMessage(c.br)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || strings.Contains(err.Error(), "closed") {
				c.closeWith(nil)
				return
			}
			c.closeWith(fmt.Errorf("read DAP message: %w", errors.Join(ErrProtocolMalformed, err)))
			return
		}
		switch typed := msg.(type) {
		case godap.ResponseMessage:
			c.deliverResponse(typed)
		case godap.EventMessage:
			c.deliverEvent(typed)
		default:
			c.closeWith(fmt.Errorf("read DAP message: %w", ErrProtocolMalformed))
			return
		}
	}
}

func (c *Client) deliverResponse(resp godap.ResponseMessage) {
	seq := resp.GetResponse().RequestSeq
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	ch := c.pending[seq]
	if ch == nil {
		return
	}
	select {
	case ch <- resp:
	default:
	}
}

func (c *Client) deliverEvent(ev godap.EventMessage) {
	c.eventMu.Lock()
	defer c.eventMu.Unlock()
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return
	}
	select {
	case c.events <- ev:
		return
	default:
	}
	if isLifecycleEvent(ev) {
		c.eventOverflow = true
		select {
		case <-c.events:
		default:
		}
		select {
		case c.events <- ev:
		default:
			c.eventOverflow = true
		}
		return
	}
	c.eventOverflow = true
}

func isLifecycleEvent(ev godap.EventMessage) bool {
	switch ev.(type) {
	case *godap.StoppedEvent, *godap.TerminatedEvent, *godap.ExitedEvent:
		return true
	default:
		return false
	}
}

func (c *Client) closeWith(err error) {
	c.once.Do(func() {
		c.eventMu.Lock()
		c.mu.Lock()
		c.closed = true
		c.pending = make(map[int]chan godap.ResponseMessage)
		c.mu.Unlock()
		close(c.events)
		c.eventMu.Unlock()
		c.done <- err
		close(c.done)
	})
}

func newRequest(command string) godap.Request {
	return godap.Request{ProtocolMessage: godap.ProtocolMessage{Type: "request"}, Command: command}
}

func unexpectedResponse(command string, resp godap.ResponseMessage) error {
	return fmt.Errorf("DAP %s request returned unexpected response %T", command, resp)
}

func dapError(resp *godap.ErrorResponse) error {
	if resp.Body.Error != nil && resp.Body.Error.Format != "" {
		return fmt.Errorf("DAP %s request failed: %s", resp.Command, resp.Body.Error.Format)
	}
	if resp.Message != "" {
		return fmt.Errorf("DAP %s request failed: %s", resp.Command, resp.Message)
	}
	return fmt.Errorf("DAP %s request failed", resp.Command)
}

// SessionOptions configures a supervised debug adapter process.
type SessionOptions struct {
	Command string
	Args    []string
	Dir     string
	Env     []string

	// StartupTimeout is reserved for future adapter readiness probing. The first
	// initialize request currently proves protocol readiness and is bounded by
	// RequestTimeout.
	StartupTimeout time.Duration
	RequestTimeout time.Duration
}

// Session owns a debug adapter process and its DAP client. Closing the session
// first attempts protocol-level disconnect, then kills the process if needed.
type Session struct {
	*Client
	Cmd *exec.Cmd

	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr *safeString
}

// StartSession starts a debug adapter process and wires its stdio to a Client.
func StartSession(ctx context.Context, opts SessionOptions) (*Session, error) {
	if opts.Command == "" {
		return nil, fmt.Errorf("start DAP session: missing command")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("start DAP session: %w", err)
	}

	cmd := exec.CommandContext(ctx, opts.Command, opts.Args...)
	cmd.Dir = opts.Dir
	cmd.Env = opts.Env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open DAP stdout: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open DAP stdin: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("open DAP stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start DAP adapter: %w", err)
	}

	sess := &Session{Cmd: cmd, stdin: stdin, stdout: stdout, stderr: &safeString{}}
	go func() {
		_, _ = io.Copy(sess.stderr, stderr)
	}()
	client, err := NewClient(readWriteCloser{Reader: stdout, Writer: stdin, close: func() error {
		_ = stdin.Close()
		return stdout.Close()
	}}, ClientOptions{RequestTimeout: opts.RequestTimeout})
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}
	sess.Client = client
	return sess, nil
}

// Close disconnects from the adapter and guarantees process teardown. A failed
// graceful disconnect does not prevent forceful cleanup.
func (s *Session) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	_ = s.Disconnect(ctx, godap.DisconnectArguments{})
	_ = s.Client.Close(ctx)
	if s.Cmd.ProcessState != nil && s.Cmd.ProcessState.Exited() {
		return nil
	}
	if s.Cmd.Process != nil {
		_ = s.Cmd.Process.Kill()
	}
	if err := s.Cmd.Wait(); err != nil && s.stderr.String() != "" {
		return fmt.Errorf("DAP adapter exited: %w: %s", err, strings.TrimSpace(s.stderr.String()))
	}
	return nil
}

type safeString struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (s *safeString) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeString) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

type readWriteCloser struct {
	io.Reader
	io.Writer
	close func() error
}

func (r readWriteCloser) Close() error {
	if r.close == nil {
		return nil
	}
	return r.close()
}
