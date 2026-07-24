package llm

import (
	"context"
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
