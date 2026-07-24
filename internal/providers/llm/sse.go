package llm

import (
	"bufio"
	"context"
	"io"
	"strings"
)

// SSEEvent represents a single Server-Sent Event.
type SSEEvent struct {
	Event string
	Data  string
}

// ParseSSE reads an SSE stream and yields events on a channel, closing the
// channel when the reader is exhausted, an error occurs, or ctx is cancelled.
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
func ParseSSE(ctx context.Context, r io.Reader) <-chan SSEEvent {
	ch := make(chan SSEEvent, 32)
	scanDone := make(chan struct{})

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
	}()

	// Watchdog: if ctx is cancelled before the scan finishes on its own, close
	// the reader to force the blocked Scan() to unblock with an I/O error.
	if ctx != nil {
		go func() {
			select {
			case <-scanDone:
				// Scan finished on its own — nothing to do.
			case <-ctx.Done():
				if closer, ok := r.(io.Closer); ok {
					closer.Close()
				}
				<-scanDone // wait for the scan goroutine to actually exit
			}
		}()
	}

	return ch
}
