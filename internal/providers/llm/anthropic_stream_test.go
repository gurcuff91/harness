package llm

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

// TestParseAnthropicStreamContextCancelUnblocks mirrors the OpenAI-side test:
// a stalled Anthropic SSE stream (server stops sending real content, keeps
// the connection open) must unblock ParseAnthropicStream promptly when ctx
// is cancelled — this is the exact mechanism behind the field-reported freeze
// (spinner stuck, Esc unresponsive) in a long, thinking:high session.
func TestParseAnthropicStreamContextCancelUnblocks(t *testing.T) {
	r := newBlockingReader()
	defer r.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := ParseAnthropicStream(ctx, r, nil, func(s string) string { return s })
		done <- err
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Unblocked — the exact error doesn't matter, just that it returned.
	case <-time.After(2 * time.Second):
		t.Fatal("ParseAnthropicStream did not unblock within 2s of ctx cancellation")
	}
}

// TestParseAnthropicStreamHappyPath is a light smoke test (no existing
// anthropic-specific test file covered ParseAnthropicStream directly) so the
// cancellation test above isn't the only coverage of this parser.
func TestParseAnthropicStreamHappyPath(t *testing.T) {
	raw := "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	resp, err := ParseAnthropicStream(context.Background(), &staticReader{data: []byte(raw)}, nil, func(s string) string { return s })
	if err != nil {
		t.Fatalf("ParseAnthropicStream: %v", err)
	}
	if resp.Text != "hi" {
		t.Errorf("resp.Text = %q, want %q", resp.Text, "hi")
	}
	if got := resp.Message.TextContent(); got != "hi" {
		t.Errorf("resp.Message.TextContent() = %q, want %q", got, "hi")
	}
}

// TestParseAnthropicStreamDroppedConnectionMidToolCallIsAnError is the
// regression test for the field-reported incident: a real Anthropic
// connection dropped while streaming a tool_use block's arguments — the
// model had already announced the tool (content_block_start), but the
// connection broke (genuine I/O error, NOT a ctx cancellation) before
// content_block_stop/message_stop ever arrived. Before this fix,
// ParseAnthropicStream returned (resp, nil) as if the turn had completed
// normally with no tool calls — the UI was left showing the tool
// announcement (EventToolStart already fired) with nothing ever resolving
// it, no error, no retry, indefinitely stuck until the user typed something
// new. Now it must return a genuine error instead.
func TestParseAnthropicStreamDroppedConnectionMidToolCallIsAnError(t *testing.T) {
	raw := "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_abc","name":"Bash"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\": \"ec"}}` + "\n\n"
		// Connection drops HERE — no content_block_stop, no message_stop.

	sentinel := io.ErrUnexpectedEOF
	r := &erroringReader{data: []byte(raw), err: sentinel}

	resp, err := ParseAnthropicStream(context.Background(), r, nil, func(s string) string { return s })
	if err == nil {
		t.Fatalf("expected an error for a connection dropped mid-tool-call, got a successful response: %+v", resp)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want it to wrap the underlying read error %v", err, sentinel)
	}
}

// TestParseAnthropicStreamCleanEOFBeforeMessageStopIsAnError covers the
// EOF-without-message_stop variant of the same bug — the server closed the
// connection cleanly (no read error at all) but never sent message_stop, e.g.
// a proxy or load balancer terminating the connection early. errFn() itself
// reports nil here (clean EOF), so this exercises the OTHER half of the
// guard: sawMessageStop being false must still produce an error on its own.
func TestParseAnthropicStreamCleanEOFBeforeMessageStopIsAnError(t *testing.T) {
	raw := "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial answ"}}` + "\n\n"
		// Reader hits a clean io.EOF here — no content_block_stop, no message_stop.

	resp, err := ParseAnthropicStream(context.Background(), &staticReader{data: []byte(raw)}, nil, func(s string) string { return s })
	if err == nil {
		t.Fatalf("expected an error for a stream that EOF'd before message_stop, got a successful response: %+v", resp)
	}
}
