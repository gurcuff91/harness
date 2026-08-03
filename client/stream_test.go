package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestStreamEventsDecodesEvents verifies the basic SSE decode path: "data:"
// lines become decoded events on the channel, non-"data:" lines are ignored.
func TestStreamEventsDecodesEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		fmt.Fprint(w, "event: message\n")
		fmt.Fprint(w, `data: {"type":"turn_start"}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"turn_end"}`+"\n\n")
		fl.Flush()
	}))
	defer srv.Close()

	c := New(srv.Listener.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	events, err := c.StreamEvents(ctx, "sess-1")
	if err != nil {
		t.Fatalf("StreamEvents: %v", err)
	}

	var got []string
	for e := range events {
		got = append(got, e.Type)
	}
	if len(got) != 2 || got[0] != "turn_start" || got[1] != "turn_end" {
		t.Errorf("got %v, want [turn_start turn_end]", got)
	}
}

// TestStreamEventsDecodesLoopIndexIncludingZero verifies client.Event.Loop
// decodes correctly from a real SSE line for both loop_start/loop_end — and
// specifically that Loop: 0 (the first ReAct iteration) round-trips as 0,
// not as a missing/default value indistinguishable from any other event
// type that never sets Loop at all. Loop has no `omitempty` on either side
// (server or client) precisely so 0 is never ambiguous with "absent".
func TestStreamEventsDecodesLoopIndexIncludingZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		fmt.Fprint(w, `data: {"type":"loop_start","loop":0}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"loop_end","loop":0}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"loop_start","loop":49}`+"\n\n")
		fl.Flush()
	}))
	defer srv.Close()

	c := New(srv.Listener.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	events, err := c.StreamEvents(ctx, "sess-1")
	if err != nil {
		t.Fatalf("StreamEvents: %v", err)
	}

	var got []int
	for e := range events {
		got = append(got, e.Loop)
	}
	if len(got) != 3 || got[0] != 0 || got[1] != 0 || got[2] != 49 {
		t.Errorf("got Loop values %v, want [0 0 49]", got)
	}
}

// TestStreamEventsHandlesLargeLine is the regression test for the bug this
// package's unification fixed: the CLI's pre-unification `-p` client used
// bufio.Scanner with NO explicit Buffer() call, defaulting to a 64KB max line
// — a single large SSE data line (e.g. a big tool_result or long response)
// over that would silently truncate the scan (bufio.ErrTooLong) and the rest
// of the turn's events would never arrive. This asserts a line well over 64KB
// still decodes correctly.
func TestStreamEventsHandlesLargeLine(t *testing.T) {
	bigOutput := strings.Repeat("x", 200*1024) // 200KB — well past the old 64KB default
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		payload, _ := json.Marshal(map[string]string{"type": "tool_result", "output": bigOutput})
		fmt.Fprintf(w, "data: %s\n\n", payload)
		fl.Flush()
	}))
	defer srv.Close()

	c := New(srv.Listener.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	events, err := c.StreamEvents(ctx, "sess-1")
	if err != nil {
		t.Fatalf("StreamEvents: %v", err)
	}

	select {
	case e, ok := <-events:
		if !ok {
			t.Fatal("channel closed with no event — the large line was likely dropped (the bug this test guards against)")
		}
		if len(e.Output) != len(bigOutput) {
			t.Errorf("output length = %d, want %d (truncated?)", len(e.Output), len(bigOutput))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the large event")
	}
}

// TestStreamEventsRespectsContextCancellation verifies the channel closes
// promptly when ctx is cancelled, even with the server still "sending"
// (simulated by just not writing anything further).
func TestStreamEventsRespectsContextCancellation(t *testing.T) {
	blockCh := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-blockCh // hang until the test is done, simulating a stalled/idle stream
	}))
	defer srv.Close()
	defer close(blockCh)

	c := New(srv.Listener.Addr().String())
	ctx, cancel := context.WithCancel(context.Background())

	events, err := c.StreamEvents(ctx, "sess-1")
	if err != nil {
		t.Fatalf("StreamEvents: %v", err)
	}

	cancel()
	select {
	case _, ok := <-events:
		if ok {
			t.Error("expected channel to close (no more events), got one instead")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel did not close within 2s of ctx cancellation")
	}
}
