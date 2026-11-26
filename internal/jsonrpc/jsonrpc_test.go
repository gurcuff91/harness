package jsonrpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// peer is the test's other end of a connection: raw lines in, raw lines out.
type peer struct {
	toConn   *io.PipeWriter // what the Conn reads
	fromConn *bufio.Reader
	conn     *Conn
}

func newPeer(t *testing.T) *peer {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &peer{toConn: inW, fromConn: bufio.NewReader(outR), conn: NewConn(inR, outW)}
	t.Cleanup(func() { inW.Close(); outW.Close(); inR.Close(); outR.Close() })
	return p
}

func (p *peer) send(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(p.toConn, line+"\n"); err != nil {
		t.Fatalf("send: %v", err)
	}
}

func (p *peer) recv(t *testing.T) map[string]any {
	t.Helper()
	type r struct {
		m   map[string]any
		err error
	}
	ch := make(chan r, 1)
	go func() {
		line, err := p.fromConn.ReadBytes('\n')
		var m map[string]any
		if err == nil {
			err = json.Unmarshal(line, &m)
		}
		ch <- r{m, err}
	}()
	select {
	case x := <-ch:
		if x.err != nil {
			t.Fatalf("recv: %v", x.err)
		}
		return x.m
	case <-time.After(5 * time.Second):
		t.Fatal("recv: timeout")
		return nil
	}
}

func TestFramingReadMessage(t *testing.T) {
	c := NewConn(strings.NewReader("\n"+`{"jsonrpc":"2.0","id":1,"method":"x","params":{"a":1}}`+"\n"), io.Discard)
	m, err := c.ReadMessage()
	if err != nil || m.Method != "x" || string(m.ID) != "1" || string(m.Params) != `{"a":1}` {
		t.Fatalf("got %+v, %v", m, err)
	}
	if _, err := c.ReadMessage(); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestFramingWriteIsOneCompactLine(t *testing.T) {
	var buf bytes.Buffer
	c := NewConn(nil, &buf)
	if err := c.Notify("n", map[string]any{"a": 1, "b": "two"}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("not exactly one line: %q", out)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["id"]; ok {
		t.Fatal("a notification must not carry an id")
	}
}

func TestSendResponseAndErrorShapes(t *testing.T) {
	var buf bytes.Buffer
	c := NewConn(nil, &buf)
	_ = c.SendResponse(json.RawMessage("7"), map[string]string{"ok": "yes"})
	_ = c.SendError(json.RawMessage(`"s"`), CodeMethodNotFound, "nope", map[string]string{"kind": "k"})
	_ = c.SendError(nil, CodeParseError, "bad", nil)
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines: %v", lines)
	}
	var r1, r2, r3 Response
	_ = json.Unmarshal([]byte(lines[0]), &r1)
	_ = json.Unmarshal([]byte(lines[1]), &r2)
	_ = json.Unmarshal([]byte(lines[2]), &r3)
	if string(r1.ID) != "7" || r1.Error != nil || r1.JSONRPC != Version {
		t.Errorf("r1 %+v", r1)
	}
	if string(r2.ID) != `"s"` || r2.Error == nil || r2.Error.Code != CodeMethodNotFound {
		t.Errorf("r2 %+v", r2)
	}
	if string(r3.ID) != "null" {
		t.Errorf("r3 id = %s, want null", r3.ID)
	}
}

func TestConcurrentWritesDoNotInterleave(t *testing.T) {
	var buf bytes.Buffer
	c := NewConn(nil, &slowWriter{w: &buf})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = c.Notify("n", map[string]any{"i": i, "pad": strings.Repeat("x", 500)})
		}(i)
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 50 {
		t.Fatalf("got %d lines", len(lines))
	}
	for _, l := range lines {
		var m Message
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("corrupt line %q: %v", l[:40], err)
		}
	}
}

// slowWriter yields between the two halves a naive writer would produce.
type slowWriter struct{ w *bytes.Buffer }

func (s *slowWriter) Write(b []byte) (int, error) {
	half := len(b) / 2
	s.w.Write(b[:half])
	time.Sleep(50 * time.Microsecond)
	s.w.Write(b[half:])
	return len(b), nil
}

// bounded is a ctx for a Call a test expects to be failed by the connection,
// so a broken Close shows as a failure, not a hang.
func bounded(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func serveAsync(p *peer, ctx context.Context, h Handler) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- p.conn.Serve(ctx, h) }()
	return ch
}

func echoHandler(_ context.Context, method string, params json.RawMessage, _ bool) (any, error) {
	return map[string]any{"method": method, "params": params}, nil
}

func TestCallCorrelatesOutOfOrderResponses(t *testing.T) {
	p := newPeer(t)
	_ = serveAsync(p, context.Background(), echoHandler)

	type out struct {
		n   int
		res json.RawMessage
		err error
	}
	results := make(chan out, 2)
	for i := 1; i <= 2; i++ {
		go func(i int) {
			r, err := p.conn.Call(context.Background(), "m", map[string]int{"n": i})
			results <- out{i, r, err}
		}(i)
	}
	reqs := map[int]string{} // n → id
	for i := 0; i < 2; i++ {
		m := p.recv(t)
		params := m["params"].(map[string]any)
		reqs[int(params["n"].(float64))] = fmt.Sprint(m["id"])
	}
	// answer the SECOND request first
	p.send(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"for":2}}`, reqs[2]))
	p.send(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"for":1}}`, reqs[1]))
	for i := 0; i < 2; i++ {
		o := <-results
		if o.err != nil || string(o.res) != fmt.Sprintf(`{"for":%d}`, o.n) {
			t.Fatalf("call %d got %s, %v", o.n, o.res, o.err)
		}
	}
}

func TestCallErrorResponseIsAnError(t *testing.T) {
	p := newPeer(t)
	_ = serveAsync(p, context.Background(), echoHandler)
	done := make(chan error, 1)
	go func() { _, err := p.conn.Call(context.Background(), "m", nil); done <- err }()
	m := p.recv(t)
	p.send(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%v,"error":{"code":-32000,"message":"boom"}}`, m["id"]))
	err := <-done
	var je *Error
	if !errors.As(err, &je) || je.Code != -32000 || je.Message != "boom" {
		t.Fatalf("err = %v", err)
	}
}

func TestCallContextCancelAbandonsAndLateResponseIsDropped(t *testing.T) {
	p := newPeer(t)
	_ = serveAsync(p, context.Background(), echoHandler)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := p.conn.Call(ctx, "m", nil); done <- err }()
	m := p.recv(t)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	// the late response must be ignored silently, and the connection must still work
	p.send(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%v,"result":1}`, m["id"]))
	p.send(t, `{"jsonrpc":"2.0","id":99,"method":"ping"}`)
	r := p.recv(t)
	if fmt.Sprint(r["id"]) != "99" || r["error"] != nil {
		t.Fatalf("after late response: %v", r)
	}
}

func TestCloseFailsPendingCalls(t *testing.T) {
	p := newPeer(t)
	_ = serveAsync(p, context.Background(), echoHandler)
	done := make(chan error, 1)
	go func() { _, err := p.conn.Call(bounded(t), "m", nil); done <- err }()
	p.recv(t)
	p.conn.Close()
	select {
	case err := <-done:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pending call not failed by Close")
	}
	if _, err := p.conn.Call(bounded(t), "m", nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("call after close: %v", err)
	}
}

func TestServeEOFFailsPendingAndReturnsNil(t *testing.T) {
	p := newPeer(t)
	served := serveAsync(p, context.Background(), echoHandler)
	done := make(chan error, 1)
	go func() { _, err := p.conn.Call(bounded(t), "m", nil); done <- err }()
	p.recv(t)
	p.toConn.Close() // the peer closed its stdin: EOF
	if err := <-served; err != nil {
		t.Fatalf("Serve = %v, want nil on EOF", err)
	}
	if err := <-done; !errors.Is(err, ErrClosed) {
		t.Fatalf("pending call: %v", err)
	}
}

func TestServeCtxCancelReturnsCtxErr(t *testing.T) {
	p := newPeer(t)
	ctx, cancel := context.WithCancel(context.Background())
	served := serveAsync(p, ctx, echoHandler)
	cancel()
	if err := <-served; !errors.Is(err, context.Canceled) {
		t.Fatalf("Serve = %v", err)
	}
}

func TestServeClassifiesRequestAndNotification(t *testing.T) {
	p := newPeer(t)
	got := make(chan string, 2)
	_ = serveAsync(p, context.Background(), func(_ context.Context, method string, _ json.RawMessage, notif bool) (any, error) {
		got <- fmt.Sprintf("%s:%v", method, notif)
		if method == "fail" {
			return nil, &Error{Code: -32000, Message: "nope", Data: map[string]string{"kind": "not_found"}}
		}
		if method == "plain" {
			return nil, errors.New("plain failure")
		}
		return "ok", nil
	})
	p.send(t, `{"jsonrpc":"2.0","method":"note"}`)
	if v := <-got; v != "note:true" {
		t.Fatal(v)
	}
	p.send(t, `{"jsonrpc":"2.0","id":5,"method":"fail"}`)
	<-got
	r := p.recv(t) // only the request is answered; the notification wasn't
	e := r["error"].(map[string]any)
	if fmt.Sprint(r["id"]) != "5" || e["code"].(float64) != -32000 || e["data"].(map[string]any)["kind"] != "not_found" {
		t.Fatalf("%v", r)
	}
	p.send(t, `{"jsonrpc":"2.0","id":6,"method":"plain"}`)
	<-got
	r = p.recv(t)
	if r["error"].(map[string]any)["code"].(float64) != CodeInternalError {
		t.Fatalf("%v", r)
	}
	p.send(t, `{"jsonrpc":"2.0","id":7,"method":"ok"}`)
	<-got
	if r = p.recv(t); r["result"] != "ok" {
		t.Fatalf("%v", r)
	}
}

func TestHandlersRunConcurrentlyAndMayCallBack(t *testing.T) {
	p := newPeer(t)
	release := make(chan struct{})
	_ = serveAsync(p, context.Background(), func(ctx context.Context, method string, _ json.RawMessage, _ bool) (any, error) {
		if method == "slow" {
			<-release
			return "slow", nil
		}
		// a handler may itself call the peer while another handler is blocked
		res, err := p.conn.Call(ctx, "reverse", nil)
		return res, err
	})
	p.send(t, `{"jsonrpc":"2.0","id":1,"method":"slow"}`)
	p.send(t, `{"jsonrpc":"2.0","id":2,"method":"fast"}`)
	m := p.recv(t) // the reverse call
	if m["method"] != "reverse" {
		t.Fatalf("%v", m)
	}
	p.send(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%v,"result":"back"}`, m["id"]))
	r := p.recv(t)
	if fmt.Sprint(r["id"]) != "2" || r["result"] != "back" {
		t.Fatalf("%v", r)
	}
	close(release)
	if r = p.recv(t); fmt.Sprint(r["id"]) != "1" {
		t.Fatalf("%v", r)
	}
}

func TestMalformedLines(t *testing.T) {
	p := newPeer(t)
	_ = serveAsync(p, context.Background(), echoHandler)

	// recoverable id → parse error carrying it
	p.send(t, `{"jsonrpc":"2.0","id":42,"method":123}`)
	r := p.recv(t)
	if fmt.Sprint(r["id"]) != "42" || r["error"].(map[string]any)["code"].(float64) != CodeParseError {
		t.Fatalf("%v", r)
	}
	// garbage → parse error with null id
	p.send(t, `not json at all`)
	r = p.recv(t)
	if r["id"] != nil || r["error"].(map[string]any)["code"].(float64) != CodeParseError {
		t.Fatalf("%v", r)
	}
	// truncated object
	p.send(t, `{"jsonrpc":"2.0","id":`)
	if r = p.recv(t); r["error"].(map[string]any)["code"].(float64) != CodeParseError {
		t.Fatalf("%v", r)
	}
	// a message with neither method nor id is an invalid request
	p.send(t, `{"jsonrpc":"2.0"}`)
	if r = p.recv(t); r["error"].(map[string]any)["code"].(float64) != CodeInvalidRequest {
		t.Fatalf("%v", r)
	}
	// and the loop is still alive
	p.send(t, `{"jsonrpc":"2.0","id":1,"method":"ok"}`)
	if r = p.recv(t); fmt.Sprint(r["id"]) != "1" || r["error"] != nil {
		t.Fatalf("%v", r)
	}
}

func TestHandlerPanicIsAnInternalError(t *testing.T) {
	p := newPeer(t)
	_ = serveAsync(p, context.Background(), func(context.Context, string, json.RawMessage, bool) (any, error) {
		panic("kaboom")
	})
	p.send(t, `{"jsonrpc":"2.0","id":1,"method":"x"}`)
	r := p.recv(t)
	if r["error"].(map[string]any)["code"].(float64) != CodeInternalError {
		t.Fatalf("%v", r)
	}
}

// Notifications are handled in the order they arrive: a stream of events must
// not be reordered (a goroutine per notification used to swap neighbours).
func TestNotificationsAreHandledInOrder(t *testing.T) {
	p := newPeer(t)
	const n = 3000
	got := make(chan int, n)
	_ = serveAsync(p, context.Background(), func(_ context.Context, method string, params json.RawMessage, notif bool) (any, error) {
		if notif && method == "event" {
			var v int
			json.Unmarshal(params, &v)
			got <- v
		}
		return nil, nil
	})
	go func() {
		for i := 0; i < n; i++ {
			io.WriteString(p.toConn, fmt.Sprintf(`{"jsonrpc":"2.0","method":"event","params":%d}`+"\n", i))
		}
	}()
	for i := 0; i < n; i++ {
		select {
		case v := <-got:
			if v != i {
				t.Fatalf("notification %d arrived as %d: order not kept", i, v)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out at %d", i)
		}
	}
}
