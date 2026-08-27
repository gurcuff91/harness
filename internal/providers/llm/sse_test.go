package llm

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

// blockingReader never returns from Read until closed — it simulates a
// stalled HTTP response body (server stopped sending real content but kept
// the connection open: degraded network, provider hiccup, mid-stream stall).
// Implements io.ReadCloser so ParseSSE's ctx-cancellation path (which closes
// the reader if it's an io.Closer) can unblock it, mirroring how an
// *http.Response.Body behaves in production.
type blockingReader struct {
	closed chan struct{}
}

func newBlockingReader() *blockingReader {
	return &blockingReader{closed: make(chan struct{})}
}

func (b *blockingReader) Read(p []byte) (int, error) {
	<-b.closed
	return 0, io.ErrClosedPipe
}

func (b *blockingReader) Close() error {
	select {
	case <-b.closed:
		// already closed
	default:
		close(b.closed)
	}
	return nil
}

// TestParseSSEContextCancelUnblocks reproduces the field-reported freeze: a
// stream stalls (no more bytes, connection stays open) and the caller cancels
// ctx (Stop()/Esc). Before the fix, the bufio.Scanner inside ParseSSE blocked
// in a read syscall forever, deaf to ctx — this asserts the channel closes
// promptly instead.
func TestParseSSEContextCancelUnblocks(t *testing.T) {
	r := newBlockingReader()
	defer r.Close()

	ctx, cancel := context.WithCancel(context.Background())
	ch, errFn := ParseSSE(ctx, r)

	// Give the scan goroutine a moment to actually start blocking on Read.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected channel to close with no events, got an event")
		}
		// ok == false: channel closed as expected.
	case <-time.After(2 * time.Second):
		t.Fatal("ParseSSE did not unblock within 2s of ctx cancellation")
	}
	// A caller-initiated cancellation must NOT be reported as a stream
	// error — see errFn's doc comment. It has its own signaling (EventStop),
	// and misreporting it as a stream failure would surface a spurious
	// EventError alongside (or instead of) the expected stop behavior.
	if err := errFn(); err != nil {
		t.Errorf("errFn() = %v, want nil for a ctx-cancelled (not a genuine I/O) stop", err)
	}
}

// TestParseSSENilContextStillWorks verifies the nil-ctx path (defensive — all
// real callers pass a real ctx) still parses normally without panicking.
func TestParseSSENilContextStillWorks(t *testing.T) {
	r := &staticReader{data: []byte("data: hello\n\n")}
	ch, errFn := ParseSSE(nil, r) //nolint:staticcheck // intentional nil-ctx defensive test
	var got []SSEEvent
	for e := range ch {
		got = append(got, e)
	}
	if len(got) != 1 || got[0].Data != "hello" {
		t.Errorf("got %+v", got)
	}
	if err := errFn(); err != nil {
		t.Errorf("errFn() = %v, want nil for a clean EOF", err)
	}
}

// erroringReader returns a genuine I/O error mid-stream — never closed by the
// caller, never cancelled via ctx — simulating a connection that was reset or
// dropped by the network/server itself, not by us. errFn() must surface it.
type erroringReader struct {
	data []byte
	pos  int
	err  error
}

func (e *erroringReader) Read(p []byte) (int, error) {
	if e.pos < len(e.data) {
		n := copy(p, e.data[e.pos:])
		e.pos += n
		return n, nil
	}
	return 0, e.err
}

// TestParseSSEGenuineReadErrorIsReported is the regression test for the field
// incident this whole fix addresses: a connection that drops mid-stream
// (NOT a user cancellation) must have errFn() report the real cause, so
// callers (ParseAnthropicStream, DoOpenAIStream) can surface it as a genuine
// error instead of silently treating a truncated response as a complete,
// successful one.
func TestParseSSEGenuineReadErrorIsReported(t *testing.T) {
	sentinel := io.ErrUnexpectedEOF
	r := &erroringReader{data: []byte("data: partial\n\n"), err: sentinel}

	ch, errFn := ParseSSE(context.Background(), r)
	var got []SSEEvent
	for e := range ch {
		got = append(got, e)
	}
	if len(got) != 1 || got[0].Data != "partial" {
		t.Fatalf("expected the one event that arrived before the error, got %+v", got)
	}
	err := errFn()
	if err == nil {
		t.Fatal("errFn() = nil, want the genuine read error to be reported")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("errFn() = %v, want it to wrap %v", err, sentinel)
	}
}

// staticReader serves a fixed byte slice then io.EOF — a normal, well-behaved
// reader for the happy-path test above.
type staticReader struct {
	data []byte
	pos  int
}

func (s *staticReader) Read(p []byte) (int, error) {
	if s.pos >= len(s.data) {
		return 0, io.EOF
	}
	n := copy(p, s.data[s.pos:])
	s.pos += n
	return n, nil
}
