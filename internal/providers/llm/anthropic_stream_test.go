package llm

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/types"
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

// anthropicRefusalStream builds an SSE body whose message_delta carries delta.
func anthropicRefusalStream(delta string) string {
	return "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":17168,"output_tokens":0}}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":` + delta + `,"usage":{"input_tokens":17168,"cache_read_input_tokens":6189,"output_tokens":0}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
}

// The stream Anthropic really returned for the field incident (captured live):
// a clean, complete stream with NO content blocks and stop_reason "refusal".
// It must surface as a RefusalError carrying Anthropic's category and wording —
// not as an empty response.
func TestParseAnthropicStreamRefusalIsATypedError(t *testing.T) {
	raw := anthropicRefusalStream(`{"stop_reason":"refusal","stop_sequence":null,"stop_details":{"type":"refusal","category":"cyber","explanation":"Cyber safeguards flagged this message. If this is legitimate security work, you can apply for the Cyber Verification Program: https://support.claude.com/en/articles/14604842-cyber-verification-program.","cyber_verification":{"type":"inactive"}},"container":null}`)

	var events []types.StreamEvent
	resp, err := ParseAnthropicStream(context.Background(), &staticReader{data: []byte(raw)},
		func(e types.StreamEvent) { events = append(events, e) }, func(s string) string { return s })
	if resp != nil {
		t.Errorf("resp = %+v, want nil: a refusal produces no message to store", resp)
	}
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v, want ErrRefused", err)
	}
	var re *RefusalError
	if !errors.As(err, &re) || re.Category != "cyber" || !strings.Contains(re.Explanation, "Cyber Verification Program") {
		t.Fatalf("refusal = %+v", re)
	}
	got := err.Error()
	if !strings.HasPrefix(got, "model refused the request (cyber): Cyber safeguards flagged") || !strings.HasSuffix(got, RefusalHint) {
		t.Errorf("message = %q", got)
	}
	// the billed input tokens are still reported before the error is returned
	usage := false
	for _, e := range events {
		if e.Type == types.StreamUsage && e.InputTokens == 17168 {
			usage = true
		}
	}
	if !usage {
		t.Errorf("the usage of a refused request was not reported: %+v", events)
	}
}

// A refusal with no stop_details still refuses (message without category or
// explanation, hint intact); every other stop_reason is untouched.
func TestParseAnthropicStreamRefusalWithoutDetails(t *testing.T) {
	_, err := ParseAnthropicStream(context.Background(), &staticReader{data: []byte(anthropicRefusalStream(`{"stop_reason":"refusal"}`))}, nil, func(s string) string { return s })
	var re *RefusalError
	if !errors.As(err, &re) || re.Category != "" || re.Explanation != "" {
		t.Fatalf("err = %v", err)
	}
	if want := "model refused the request\n" + RefusalHint; err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
}

func TestParseAnthropicStreamOtherStopReasonsAreNotRefusals(t *testing.T) {
	for _, reason := range []string{"end_turn", "max_tokens", "tool_use", "stop_sequence", "pause_turn"} {
		raw := anthropicRefusalStream(`{"stop_reason":"` + reason + `","stop_details":null}`)
		if _, err := ParseAnthropicStream(context.Background(), &staticReader{data: []byte(raw)}, nil, func(s string) string { return s }); errors.Is(err, ErrRefused) {
			t.Errorf("stop_reason %q was treated as a refusal", reason)
		}
	}
}

// A refusal that arrives after the model already produced content (a mid-answer
// classifier trip) is still a refusal: the partial message is not returned.
func TestParseAnthropicStreamRefusalAfterPartialContent(t *testing.T) {
	raw := "event: message_start\n" + `data: {"type":"message_start","message":{"usage":{"input_tokens":5}}}` + "\n\n" +
		"event: content_block_start\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}` + "\n\n" +
		"event: content_block_delta\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me"}}` + "\n\n" +
		"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" + `data: {"type":"message_delta","delta":{"stop_reason":"refusal","stop_details":{"category":"cyber"}},"usage":{"output_tokens":3}}` + "\n\n" +
		"event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"
	resp, err := ParseAnthropicStream(context.Background(), &staticReader{data: []byte(raw)}, nil, func(s string) string { return s })
	if resp != nil || !errors.Is(err, ErrRefused) {
		t.Fatalf("resp=%+v err=%v, want a refusal and no message", resp, err)
	}
}

// A refusal whose stream is cut off before message_stop is a dropped connection,
// not a refusal: it keeps its own (retryable) error.
func TestParseAnthropicStreamTruncatedRefusalIsAConnectionError(t *testing.T) {
	raw := "event: message_start\n" + `data: {"type":"message_start","message":{"usage":{"input_tokens":5}}}` + "\n\n" +
		"event: message_delta\n" + `data: {"type":"message_delta","delta":{"stop_reason":"refusal"},"usage":{"output_tokens":0}}` + "\n\n"
	_, err := ParseAnthropicStream(context.Background(), &staticReader{data: []byte(raw)}, nil, func(s string) string { return s })
	if err == nil || errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v, want a stream-ended-early error", err)
	}
}
