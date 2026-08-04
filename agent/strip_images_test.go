package agent

import (
	"strings"
	"testing"

	"github.com/gurcuff91/harness/types"
)

// imageHistory builds a history shaped like the one that triggered the field
// failure: several turns carrying large inline images, both as a top-level
// user image part (pasted screenshot) and inside a tool result (the Read tool
// on an image file).
func imageHistory() []types.Message {
	bigBase64 := strings.Repeat("A", 4096) // stands in for a >2000px payload
	return []types.Message{
		{Role: types.RoleUser, Parts: []types.ContentPart{
			{Text: "look at this"},
			{Image: &types.ImageData{MimeType: "image/png", Base64: bigBase64}},
		}},
		{Role: types.RoleAssistant, Parts: []types.ContentPart{
			{Text: "reading the file"},
		}},
		{Role: types.RoleUser, Parts: []types.ContentPart{
			{ToolResult: &types.ToolResult{
				ID:     "toolu_1",
				Output: "Image loaded: shot.png",
				Images: []types.ImageData{
					{MimeType: "image/png", Base64: bigBase64},
					{MimeType: "image/jpeg", Base64: bigBase64},
				},
			}},
		}},
	}
}

// TestStripImagesRemovesEveryBase64Payload is the regression test for the
// reported HTTP 400 on the max-iterations summary call:
//
//	"At least one of the image dimensions exceed max allowed size for
//	 many-image requests: 2000 pixels"
//
// requestProgressUpdate used to send s.store.Messages() verbatim — every
// image at full size — which is the most exposed path of all, because the
// ReAct loop's own stripOldTurnImages deliberately PRESERVES the current
// turn's images, and after ~120 iterations that turn can hold many. This
// verifies stripImages leaves NO base64 payload anywhere in the wire history.
func TestStripImagesRemovesEveryBase64Payload(t *testing.T) {
	stripped := stripImages(imageHistory())

	for i, m := range stripped {
		for j, p := range m.Parts {
			if p.Image != nil {
				t.Errorf("messages[%d].parts[%d]: top-level image survived stripping", i, j)
			}
			if p.ToolResult != nil && len(p.ToolResult.Images) > 0 {
				t.Errorf("messages[%d].parts[%d]: tool-result carries %d image(s) after stripping",
					i, j, len(p.ToolResult.Images))
			}
		}
	}
}

// TestStripImagesKeepsStructureAndSignalsImagePresence verifies the summary
// still knows an image WAS there (a placeholder naming the mime type), and
// that surrounding text/tool output is preserved — the model needs the
// conversational structure intact to write a useful summary.
func TestStripImagesKeepsStructureAndSignalsImagePresence(t *testing.T) {
	original := imageHistory()
	stripped := stripImages(original)

	if len(stripped) != len(original) {
		t.Fatalf("message count changed: got %d, want %d", len(stripped), len(original))
	}

	// Turn 1: the text part survives, the image becomes a placeholder.
	var texts []string
	for _, p := range stripped[0].Parts {
		texts = append(texts, p.Text)
	}
	joined := strings.Join(texts, " | ")
	if !strings.Contains(joined, "look at this") {
		t.Errorf("original user text lost: %q", joined)
	}
	if !strings.Contains(joined, "[image: image/png]") {
		t.Errorf("expected an image placeholder naming the mime type, got: %q", joined)
	}

	// Turn 3: the tool result keeps its output and gains one placeholder per image.
	tr := stripped[2].Parts[0].ToolResult
	if tr == nil {
		t.Fatal("tool result part disappeared")
	}
	if !strings.Contains(tr.Output, "Image loaded: shot.png") {
		t.Errorf("tool output lost: %q", tr.Output)
	}
	if got := strings.Count(tr.Output, "[image:"); got != 2 {
		t.Errorf("expected 2 image placeholders in tool output, got %d: %q", got, tr.Output)
	}
	if tr.ID != "toolu_1" {
		t.Errorf("tool result ID changed: %q — tool_use/tool_result correlation would break", tr.ID)
	}
}

// TestStripImagesDoesNotMutateTheOriginal verifies the on-disk history (which
// the caller passes straight from the store) is left untouched — stripping is
// a wire-payload concern only. If this regressed, compacting or summarizing
// would permanently destroy the session's images on disk.
func TestStripImagesDoesNotMutateTheOriginal(t *testing.T) {
	original := imageHistory()
	_ = stripImages(original)

	if original[0].Parts[1].Image == nil {
		t.Error("stripImages mutated the caller's history: top-level image was cleared")
	}
	if got := len(original[2].Parts[0].ToolResult.Images); got != 2 {
		t.Errorf("stripImages mutated the caller's history: tool result images = %d, want 2", got)
	}
	if original[2].Parts[0].ToolResult.Output != "Image loaded: shot.png" {
		t.Errorf("stripImages mutated the caller's tool output: %q",
			original[2].Parts[0].ToolResult.Output)
	}
}

// TestStripImagesPreservesSystemGeneratedMeta verifies Message.Meta survives —
// requestProgressUpdate injects its prompt with IsSystemGenerated:true so
// transports render it as a notice, not as a user message the human typed.
// Since stripImages now runs over a history that includes that message, losing
// Meta here would make the TUI replay it as a fake user prompt on resume.
func TestStripImagesPreservesSystemGeneratedMeta(t *testing.T) {
	msgs := []types.Message{{
		Role:  types.RoleUser,
		Parts: []types.ContentPart{{Text: maxIterationsPrompt}},
		Meta:  &types.MessageMeta{IsSystemGenerated: true},
	}}
	stripped := stripImages(msgs)
	if stripped[0].Meta == nil || !stripped[0].Meta.IsSystemGenerated {
		t.Error("IsSystemGenerated meta lost — the TUI would replay this as a real user prompt")
	}
}
