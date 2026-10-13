package llm

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"
)

// sseIdleTimeout is StreamIdleTimeout, overridable by tests.
var sseIdleTimeout = StreamIdleTimeout

// activityReader records the time of every read that returned data.
type activityReader struct {
	r    io.Reader
	last *atomic.Int64
}

func (a *activityReader) Read(p []byte) (int, error) {
	n, err := a.r.Read(p)
	if n > 0 {
		a.last.Store(time.Now().UnixNano())
	}
	return n, err
}

// SSEEvent represents a single Server-Sent Event.
type SSEEvent struct {
	Event string
	Data  string
}

// ParseSSE reads an SSE stream and yields events on a channel, closing the
// channel when the reader is exhausted, an error occurs, or ctx is cancelled.
// It also returns errFn, which reports the underlying cause once the channel
// has been fully drained (range ch has returned) — call it only after that
// point; earlier, its result is not yet meaningful.
//
// errFn returns nil for a clean end of stream (EOF), and nil for a
// caller-initiated cancellation (ctx.Done() — e.g. the user hit Stop(), or a
// deadline was reached): those are expected, already-handled outcomes with
// their own signaling (EventStop, a timeout error from the caller's own ctx),
// not a stream failure to report as one. It returns a non-nil error ONLY for
// a genuine, unexpected I/O failure — the connection was reset, the server
// closed it, a read timed out on its own — with ctx still live. This is the
// distinction callers need to tell "the model finished" or "the user
// cancelled" apart from "the network dropped mid-response, and here is
// however much content arrived before that happened" (see
// ParseAnthropicStream and DoOpenAIStream's stream loops, which surface this
// as a real error instead of silently returning a truncated response as if
// it were a complete, successful one).
//
// bufio.Scanner has no context awareness: a Scan() waiting on a stalled or
// slow-drip HTTP body (the model stops sending real content but the
// connection stays open — degraded network, a mid-stream provider hiccup,
// etc.) blocks in a read syscall indefinitely. Cancelling ctx alone does not
// unblock it. To make Stop()/Esc actually interrupt a stuck stream, the scan
// runs in its own goroutine and a second goroutine races ctx.Done() against
// it: on cancellation, if r is an io.Closer (true for HTTP response bodies,
// the only real-world caller), it is closed — which turns the blocked Scan()
// into an I/O error and lets the scan goroutine exit. The output channel is
// closed exactly once, by whichever goroutine finishes the scan.
//
// Idle timeout: if r delivers NO bytes for sseIdleTimeout (StreamIdleTimeout),
// the connection is presumed dead (a peer that vanished mid-stream keeps TCP
// "open" until the kernel gives up, ~15 min). The same watchdog closes r and
// errFn reports a wrapped ErrStreamIdle — a transient error, safe to retry.
// Any byte resets the timer, including SSE comments / keep-alive pings.
func ParseSSE(ctx context.Context, r io.Reader) (out <-chan SSEEvent, errFn func() error) {
	ch := make(chan SSEEvent, 32)
	scanDone := make(chan struct{})

	idleTimeout := sseIdleTimeout // snapshot: the watchdog reads it on every tick

	// Wrap r so every successful read refreshes lastRead (unix nanos).
	var lastRead atomic.Int64
	lastRead.Store(time.Now().UnixNano())
	var idled atomic.Bool
	src := r
	r = &activityReader{r: src, last: &lastRead}

	// cancelled is set by the watchdog goroutine BEFORE it closes r, so the
	// scan goroutine's error classification (below) can tell "I was closed on
	// purpose because ctx was cancelled" apart from "the connection genuinely
	// broke while ctx was still live". atomic.Bool (not a plain bool) because
	// the write (watchdog goroutine) and the read (scan goroutine, after its
	// own Scan() loop returns) aren't otherwise ordered by any Go memory-model
	// happens-before relationship — closing r only guarantees OS-level Scan()
	// unblocking, not a Go-visible synchronization point for a plain variable.
	var cancelled atomic.Bool

	var streamErr error
	go func() {
		defer close(ch)
		defer close(scanDone)

		scanner := bufio.NewScanner(r)
		// Increase buffer for large SSE payloads
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

		var currentEvent string
		var dataLines []string

		for scanner.Scan() {
			line := scanner.Text()

			// Empty line = end of event, dispatch it
			if line == "" {
				if currentEvent != "" || len(dataLines) > 0 {
					event := currentEvent
					if event == "" {
						event = "message"
					}
					data := strings.Join(dataLines, "\n")
					currentEvent = ""
					dataLines = dataLines[:0]

					if event != "ping" {
						ch <- SSEEvent{Event: event, Data: data}
					}
				}
				continue
			}

			// Parse field
			if strings.HasPrefix(line, "event: ") {
				currentEvent = line[7:]
			} else if strings.HasPrefix(line, "data: ") {
				dataLines = append(dataLines, line[6:])
			} else if strings.HasPrefix(line, ":") {
				// Comment — ignore
			}
		}

		// Flush any remaining event
		if currentEvent != "" || len(dataLines) > 0 {
			event := currentEvent
			if event == "" {
				event = "message"
			}
			data := strings.Join(dataLines, "\n")
			if event != "ping" {
				ch <- SSEEvent{Event: event, Data: data}
			}
		}

		// Classify why the scan ended — see errFn's doc comment for the full
		// reasoning. scanner.Err() is nil for a clean EOF; non-nil means Scan()
		// stopped because of a read error, which is either the watchdog
		// deliberately closing r (cancelled == true — not a failure to
		// report) or a genuine, unprompted I/O break (report it).
		switch {
		case idled.Load():
			streamErr = fmt.Errorf("%w for %s (connection presumed dead)", ErrStreamIdle, idleTimeout)
		case scanner.Err() != nil && !cancelled.Load():
			streamErr = scanner.Err()
		}
	}()

	// Watchdog: if ctx is cancelled before the scan finishes on its own, or
	// the stream goes idle, close the reader to force the blocked Scan() to
	// unblock with an I/O error.
	var ctxDone <-chan struct{}
	if ctx != nil {
		ctxDone = ctx.Done()
	}
	go func() {
		closeSrc := func() {
			if closer, ok := src.(io.Closer); ok {
				closer.Close()
			}
		}
		tick := time.NewTicker(max(idleTimeout/20, 5*time.Millisecond))
		defer tick.Stop()
		for {
			select {
			case <-scanDone:
				return // scan finished on its own — nothing to do
			case <-ctxDone:
				cancelled.Store(true) // set BEFORE closing r — see its doc comment
				closeSrc()
				<-scanDone // wait for the scan goroutine to actually exit
				return
			case <-tick.C:
				if time.Since(time.Unix(0, lastRead.Load())) >= idleTimeout {
					idled.Store(true) // set BEFORE closing r, same ordering as cancelled
					closeSrc()
					<-scanDone
					return
				}
			}
		}
	}()

	return ch, func() error { return streamErr }
}
