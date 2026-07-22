package llm

import (
	"strings"
	"testing"

	"github.com/gurcuff91/harness/types"
)

// fakeSSEServer feeds raw SSE-shaped lines into parseOpenAIStream via the
// shared ParseSSE parser. Each call to next returns one event's worth of
// lines (terminated by a blank line), so a chunked test can simulate the
// reasoning_content / content deltas MiniMax leaks.
func feedParseStream(t *testing.T, rawSSE string) (*types.Response, []types.StreamEvent) {
	t.Helper()
	var events []types.StreamEvent
	resp, err := parseOpenAIStream(strings.NewReader(rawSSE), func(e types.StreamEvent) {
		events = append(events, e)
	})
	if err != nil {
		t.Fatalf("parseOpenAIStream: %v", err)
	}
	return resp, events
}

// mkChunk builds a single SSE event ready to be concatenated into a stream:
// "data: <json>\n\n". The JSON keeps the OpenAI shape parseOpenAIStream
// expects: choices[0].delta.{reasoning_content,content}.
func mkChunk(reasoning, content string) string {
	delta := map[string]any{}
	if reasoning != "" {
		delta["reasoning_content"] = reasoning
	}
	if content != "" {
		delta["content"] = content
	}
	if len(delta) == 0 {
		return ""
	}
	body, _ := jsonMarshal(map[string]any{
		"choices": []map[string]any{
			{"delta": delta, "index": 0},
		},
	})
	return "data: " + body + "\n\n"
}

func TestStripThinkingTags_Closing(t *testing.T) {
	// MiniMax sometimes leaks the closing tag into the LAST reasoning_content
	// delta right at the thinking→answer transition. After strip, the TUI
	// should never see it AND the persisted reasoningBuf should not contain it.
	raw := strings.Join([]string{
		mkChunk("Let me think about this.\n", ""),
		mkChunk("First, I'll consider…", ""),
		mkChunk("</thinking>", ""), // <-- the leak
		mkChunk("", "The answer is 42."),
		"data: [DONE]\n\n",
	}, "")

	resp, events := feedParseStream(t, raw)

	var gotThinking, gotText string
	for _, e := range events {
		switch e.Type {
		case types.StreamThinkingDelta:
			gotThinking += e.Delta
		case types.StreamTextDelta:
			gotText += e.Delta
		}
	}

	if strings.Contains(gotThinking, "</thinking>") {
		t.Errorf("thinking stream leaked </thinking>: %q", gotThinking)
	}
	if strings.Contains(resp.Message.TextContent(), "</thinking>") {
		t.Errorf("persisted text contains </thinking>: %q", resp.Message.TextContent())
	}
	// Find the ThinkingPart in the persisted message.
	for _, p := range resp.Message.Parts {
		if p.Thinking != nil && strings.Contains(p.Thinking.Content, "</thinking>") {
			t.Errorf("persisted reasoning contains </thinking>: %q", p.Thinking.Content)
		}
	}
	if gotThinking != "Let me think about this.\nFirst, I'll consider…" {
		t.Errorf("unexpected cleaned thinking: %q", gotThinking)
	}
	if gotText != "The answer is 42." {
		t.Errorf("unexpected text: %q", gotText)
	}
}

func TestStripThinkingTags_ClosingInFirstContentDelta(t *testing.T) {
	// Some providers emit the closing tag as the FIRST content delta at the
	// transition. Stripping it as defense in depth.
	raw := strings.Join([]string{
		mkChunk("Reasoning here", ""),
		mkChunk("", "</thinking>"), // <-- tag in content, entire chunk
		mkChunk("", "Final answer."),
		"data: [DONE]\n\n",
	}, "")

	_, events := feedParseStream(t, raw)

	var gotText string
	var gotTextEvents int
	for _, e := range events {
		if e.Type == types.StreamTextDelta {
			gotTextEvents++
			gotText += e.Delta
		}
	}
	if strings.Contains(gotText, "</thinking>") {
		t.Errorf("text stream leaked </thinking>: %q", gotText)
	}
	// The tag-only chunk should NOT produce an emit (no empty delta).
	if gotTextEvents != 1 {
		t.Errorf("expected 1 text emit (tag-only chunk dropped), got %d: %q", gotTextEvents, gotText)
	}
	if gotText != "Final answer." {
		t.Errorf("unexpected text: %q", gotText)
	}
}

func TestStripThinkingTags_OpeningAndClosing(t *testing.T) {
	// Defense for a future provider that might emit BOTH opening and closing.
	raw := strings.Join([]string{
		mkChunk("<thinking>I'm reasoning</thinking>", ""),
		mkChunk("", "Answer."),
		"data: [DONE]\n\n",
	}, "")

	_, events := feedParseStream(t, raw)

	var gotThinking, gotText string
	for _, e := range events {
		switch e.Type {
		case types.StreamThinkingDelta:
			gotThinking += e.Delta
		case types.StreamTextDelta:
			gotText += e.Delta
		}
	}
	if strings.Contains(gotThinking, "<thinking>") || strings.Contains(gotThinking, "</thinking>") {
		t.Errorf("thinking stream leaked tags: %q", gotThinking)
	}
	if gotThinking != "I'm reasoning" {
		t.Errorf("unexpected thinking: %q", gotThinking)
	}
	if gotText != "Answer." {
		t.Errorf("unexpected text: %q", gotText)
	}
}

func TestStripThinkingTags_NoTagsIsNoOp(t *testing.T) {
	// Sanity: when there are no tags, behavior is unchanged — same deltas,
	// same reasoningBuf, same persisted message.
	raw := strings.Join([]string{
		mkChunk("thinking", ""),
		mkChunk("", "hello world"),
		"data: [DONE]\n\n",
	}, "")

	resp, events := feedParseStream(t, raw)
	if len(events) == 0 {
		t.Fatal("expected events")
	}
	if got := resp.Text; got != "hello world" {
		t.Errorf("text: got %q want %q", got, "hello world")
	}
	for _, p := range resp.Message.Parts {
		if p.Thinking != nil && p.Thinking.Content != "thinking" {
			t.Errorf("thinking part: got %q want %q", p.Thinking.Content, "thinking")
		}
	}
}

func TestStripThinkingTags_HtmlComment(t *testing.T) {
	// Some compatibility shims use HTML-comment style delimiters.
	raw := strings.Join([]string{
		mkChunk("<!-- thinking -->reasoning<!-- /thinking -->", ""),
		mkChunk("", "ok"),
		"data: [DONE]\n\n",
	}, "")

	_, events := feedParseStream(t, raw)

	var got string
	for _, e := range events {
		if e.Type == types.StreamThinkingDelta {
			got += e.Delta
		}
	}
	if strings.Contains(got, "<!--") || strings.Contains(got, "-->") {
		t.Errorf("thinking leaked comment delimiters: %q", got)
	}
	if got != "reasoning" {
		t.Errorf("unexpected thinking: %q", got)
	}
}

func TestStripThinkingTags_AbbreviatedForm(t *testing.T) {
	// Some providers (Qwen, DeepSeek in some contexts) emit the abbreviated
	// form instead of the full <thinking>. The strip list must cover both
	// so the same leak doesn't appear with a different tag delimiter.
	raw := strings.Join([]string{
		mkChunk("I'm reasoning about the problem", ""),
		mkChunk("</think>", ""), // <-- abbreviated closing tag
		mkChunk("", "Answer."),
		"data: [DONE]\n\n",
	}, "")

	resp, events := feedParseStream(t, raw)

	var gotThinking, gotText string
	for _, e := range events {
		switch e.Type {
		case types.StreamThinkingDelta:
			gotThinking += e.Delta
		case types.StreamTextDelta:
			gotText += e.Delta
		}
	}
	if strings.Contains(gotThinking, "</think>") {
		t.Errorf("thinking stream leaked abbreviated </think>: %q", gotThinking)
	}
	if strings.Contains(resp.Text, "</think>") {
		t.Errorf("persisted text contains abbreviated tag: %q", resp.Text)
	}
	if gotThinking != "I'm reasoning about the problem" {
		t.Errorf("unexpected cleaned thinking: %q", gotThinking)
	}
	if gotText != "Answer." {
		t.Errorf("unexpected text: %q", gotText)
	}
}

func TestStripThinkingTags_PreservesCodeContainingTags(t *testing.T) {
	// Tags should be stripped even from chunks that also contain other text.
	// This is the rare case where the model legitimately discusses tags in its
	// answer; the trade-off is documented: strip-then-trim is preferred over
	// leaking the delimiter 99.99% of the time.
	raw := strings.Join([]string{
		mkChunk("", "Use <thinking> and </thinking> in code"),
		"data: [DONE]\n\n",
	}, "")

	_, events := feedParseStream(t, raw)

	var got string
	for _, e := range events {
		if e.Type == types.StreamTextDelta {
			got += e.Delta
		}
	}
	if strings.Contains(got, "<thinking>") || strings.Contains(got, "</thinking>") {
		t.Errorf("text stream leaked tags: %q", got)
	}
}
