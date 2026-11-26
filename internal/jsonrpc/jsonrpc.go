// Package jsonrpc is the JSON-RPC 2.0 layer shared by the stdio frontends
// (acp and rpc): message types, newline-delimited framing with serialized
// writes, and a bidirectional connection — a side can send requests and wait
// for their answers (Call) while it also serves the requests of the other
// side (Serve).
//
// Framing: one complete JSON object per line, in both directions (no
// Content-Length header, unlike LSP). Nothing but protocol may be written to
// the underlying writer.
package jsonrpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// Version is the fixed "jsonrpc" field value on every message.
const Version = "2.0"

// Standard JSON-RPC error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// Message is any JSON-RPC message, incoming or outgoing: a request (Method and
// ID), a notification (Method, no ID) or — incoming only — a response (ID and
// Result or Error, no Method). A notification omits ID entirely.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// IsResponse reports whether the message answers a request (it has an id and
// no method).
func (m *Message) IsResponse() bool { return m.Method == "" && len(m.ID) > 0 }

// IsNotification reports whether the message is a request with no id.
func (m *Message) IsNotification() bool { return m.Method != "" && len(m.ID) == 0 }

// Response is an outgoing reply. The id is always present (null when the
// request's could not be read).
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is the standard JSON-RPC error shape. It is also a Go error, which is
// what Call returns when the other side answers with one.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
	// After, when set on an error a handler returns, runs once the reply has
	// been written (never serialized): how a handler says "answer, then exit".
	After func() `json:"-"`
}

func (e *Error) Error() string { return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message) }

// ErrClosed is returned by Call when the connection is closed (or closes
// while the call waits).
var ErrClosed = errors.New("jsonrpc: connection closed")

// ParseError is what ReadMessage returns for a line that is not a valid
// message. ID is the request id when it could be recovered, else nil.
type ParseError struct {
	ID  json.RawMessage
	Err error
}

func (e *ParseError) Error() string { return "invalid JSON-RPC message: " + e.Err.Error() }
func (e *ParseError) Unwrap() error { return e.Err }

// Conn is a JSON-RPC connection over a reader and a writer.
type Conn struct {
	reader *bufio.Reader
	writer io.Writer
	wmu    sync.Mutex // serializes writes: two partial lines would corrupt the stream

	mu      sync.Mutex
	pending map[string]chan Message
	nextID  int64
	closed  bool
}

// NewConn builds a connection. r may be nil when only writing.
func NewConn(r io.Reader, w io.Writer) *Conn {
	c := &Conn{writer: w, pending: make(map[string]chan Message)}
	if r != nil {
		c.reader = bufio.NewReaderSize(r, 64*1024)
	}
	return c
}

// ReadMessage blocks for the next message. io.EOF means the peer closed its
// side (a normal shutdown signal). A malformed line is a *ParseError, and the
// connection stays usable (the next call reads the next line). Blank lines are
// skipped.
func (c *Conn) ReadMessage() (*Message, error) {
	for {
		line, err := c.reader.ReadBytes('\n')
		if err != nil && len(line) == 0 {
			return nil, err
		}
		if len(bytes.TrimSpace(line)) == 0 {
			if err != nil {
				return nil, err
			}
			continue
		}
		var m Message
		if uerr := json.Unmarshal(line, &m); uerr != nil {
			return nil, &ParseError{ID: recoverID(line), Err: uerr}
		}
		return &m, nil
	}
}

// recoverID digs the id out of a line that did not decode as a Message, when
// the line is at least a JSON object with a scalar id.
func recoverID(line []byte) json.RawMessage {
	var probe struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(line, &probe) != nil || len(probe.ID) == 0 || probe.ID[0] == '{' || probe.ID[0] == '[' {
		return nil
	}
	return probe.ID
}

// WriteLine serializes v as one compact JSON line and writes it atomically
// with respect to other writers.
func (c *Conn) WriteLine(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.writer.Write(b)
	return err
}

// SendResponse writes a successful reply to the request with the given id.
func (c *Conn) SendResponse(id json.RawMessage, result any) error {
	b, err := json.Marshal(result)
	if err != nil {
		return c.SendError(id, CodeInternalError, err.Error(), nil)
	}
	return c.WriteLine(Response{JSONRPC: Version, ID: nullID(id), Result: b})
}

// SendError writes an error reply to the request with the given id.
func (c *Conn) SendError(id json.RawMessage, code int, message string, data any) error {
	return c.WriteLine(Response{JSONRPC: Version, ID: nullID(id), Error: &Error{Code: code, Message: message, Data: data}})
}

func nullID(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}

// Notify writes a notification (no id, no reply expected).
func (c *Conn) Notify(method string, params any) error {
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.WriteLine(Message{JSONRPC: Version, Method: method, Params: b})
}

// SendNotification is Notify under the name acp has always used.
func (c *Conn) SendNotification(method string, params any) error { return c.Notify(method, params) }

// Call sends a request with a fresh id and waits for the matching response.
// A cancelled ctx abandons the wait (a late response is dropped silently); a
// closed connection fails the call with ErrClosed; an error response is
// returned as a *Error.
func (c *Conn) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	c.nextID++
	id := fmt.Sprintf("%d", c.nextID)
	ch := make(chan Message, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	forget := func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}
	if err := ctx.Err(); err != nil {
		forget()
		return nil, err
	}
	if err := c.WriteLine(Message{JSONRPC: Version, ID: json.RawMessage(id), Method: method, Params: raw}); err != nil {
		forget()
		return nil, err
	}
	select {
	case <-ctx.Done():
		forget()
		return nil, ctx.Err()
	case m, ok := <-ch:
		if !ok {
			return nil, ErrClosed
		}
		if m.Error != nil {
			return nil, m.Error
		}
		return m.Result, nil
	}
}

// complete hands a response to the Call waiting for it; one nobody waits for
// (abandoned by a cancel, or unknown) is dropped.
func (c *Conn) complete(m *Message) {
	c.mu.Lock()
	ch, ok := c.pending[string(bytes.TrimSpace(m.ID))]
	if ok {
		delete(c.pending, string(bytes.TrimSpace(m.ID)))
	}
	c.mu.Unlock()
	if ok {
		ch <- *m // buffered 1, sent once
	}
}

// Close marks the connection closed: every pending Call fails with ErrClosed
// and later ones fail at once. It does not close the underlying streams.
// Idempotent.
func (c *Conn) Close() {
	c.mu.Lock()
	c.closed = true
	pend := c.pending
	c.pending = make(map[string]chan Message)
	c.mu.Unlock()
	for _, ch := range pend {
		close(ch)
	}
}

// AfterSender may be implemented by a handler's RESULT: AfterSend runs once
// the reply carrying it has been written (see also Error.After).
type AfterSender interface{ AfterSend() }

// Receiver is an optional interface of a Handler's owner: Serve calls
// Received for every request and notification ON THE READING GOROUTINE, in the
// order the messages arrived and before any handler goroutine starts. It lets a
// protocol record "this message was seen" in a way later messages can rely on
// (e.g. `initialize` has begun) even though requests are handled concurrently.
// It must be quick and must not block.
type Receiver interface{ Received(method string) }

// Handler serves one incoming request or notification. For a request the
// result or the error is sent back (a *Error keeps its code, any other error
// is -32603); for a notification both are ignored.
type Handler func(ctx context.Context, method string, params json.RawMessage, isNotification bool) (any, error)

// handlerGrace bounds how long Serve waits, once it is stopping, for handlers
// that are still running (they are told to stop through their ctx first).
const handlerGrace = 5 * time.Second

type readResult struct {
	msg *Message
	err error
}

// Serve reads messages until the peer closes its side (nil), ctx is cancelled
// (ctx.Err()) or the stream fails (the error). Responses complete pending
// Calls; every REQUEST runs in its OWN goroutine, so a handler may itself Call
// the peer, while NOTIFICATIONS run in order of arrival, on the reading
// goroutine (their order is meaningful, e.g. a session's events), so a
// notification handler must be quick. A malformed line is answered with a parse
// error (-32700, id null when unrecoverable) and reading continues. On return
// the connection is closed (pending Calls fail) and the handlers' ctx is
// cancelled.
//
// The stdin read happens in its own goroutine: a plain io.Reader (os.Stdin)
// cannot have its blocking Read interrupted by ctx. After a cancel that
// goroutine stays blocked in Read; harmless, the process exits right after.
func (c *Conn) Serve(ctx context.Context, h Handler) error {
	return c.serve(ctx, h, nil)
}

// ServeWith is Serve with a Receiver notified of every request and
// notification, in arrival order, on the reading goroutine.
func (c *Conn) ServeWith(ctx context.Context, h Handler, r Receiver) error {
	return c.serve(ctx, h, r)
}

func (c *Conn) serve(ctx context.Context, h Handler, recv Receiver) error {
	hctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	done := make(chan struct{})
	reads := make(chan readResult)
	go func() {
		for {
			m, err := c.ReadMessage()
			select {
			case reads <- readResult{m, err}:
			case <-done:
				return
			}
			var pe *ParseError
			if err != nil && !errors.As(err, &pe) {
				return
			}
		}
	}()
	defer func() {
		close(done)
		cancel()
		c.Close()
		finished := make(chan struct{})
		go func() { wg.Wait(); close(finished) }()
		select {
		case <-finished:
		case <-time.After(handlerGrace):
		}
	}()

	for {
		var res readResult
		select {
		case <-ctx.Done():
			return ctx.Err()
		case res = <-reads:
		}
		if res.err != nil {
			var pe *ParseError
			switch {
			case errors.As(res.err, &pe):
				_ = c.SendError(pe.ID, CodeParseError, pe.Error(), nil)
				continue
			case errors.Is(res.err, io.EOF):
				return nil
			}
			return fmt.Errorf("jsonrpc: read message: %w", res.err)
		}
		m := res.msg
		if recv != nil && m.Method != "" {
			recv.Received(m.Method)
		}
		switch {
		case m.IsResponse():
			c.complete(m)
		case m.Method == "":
			// no method and no id: nothing to answer.
			_ = c.SendError(nil, CodeInvalidRequest, "invalid request: no method", nil)
		case m.IsNotification():
			// Notifications run IN ORDER, on this goroutine: their order is
			// meaningful (a session's events: turn_start before text), and a
			// goroutine each would let two of them swap. A handler for a
			// notification must therefore be quick (queue and return).
			c.dispatch(hctx, h, m)
		default:
			// A request may take minutes (session/ask) and may itself Call the
			// peer, so each runs in its OWN goroutine.
			wg.Add(1)
			go func() {
				defer wg.Done()
				c.dispatch(hctx, h, m)
			}()
		}
	}
}

func (c *Conn) dispatch(ctx context.Context, h Handler, m *Message) {
	notif := m.IsNotification()
	result, err := safeHandle(ctx, h, m, notif)
	if notif {
		return
	}
	if err != nil {
		var je *Error
		if errors.As(err, &je) {
			_ = c.SendError(m.ID, je.Code, je.Message, je.Data)
			if je.After != nil {
				je.After()
			}
		} else {
			_ = c.SendError(m.ID, CodeInternalError, err.Error(), nil)
		}
		return
	}
	_ = c.SendResponse(m.ID, result)
	if a, ok := result.(AfterSender); ok {
		a.AfterSend()
	}
}

func safeHandle(ctx context.Context, h Handler, m *Message, notif bool) (res any, err error) {
	defer func() {
		if r := recover(); r != nil {
			res, err = nil, &Error{Code: CodeInternalError, Message: fmt.Sprintf("internal error: %v", r)}
		}
	}()
	return h(ctx, m.Method, m.Params, notif)
}
