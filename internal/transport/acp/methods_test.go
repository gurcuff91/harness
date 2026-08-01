package acp

import (
	"strings"
	"testing"
)

func TestAcpSessionNameHasTransportPrefix(t *testing.T) {
	name := acpSessionName()
	if !strings.HasPrefix(name, "Acp ") {
		t.Errorf("acpSessionName() = %q, want prefix %q", name, "Acp ")
	}
}

func TestFlattenPromptTextOnly(t *testing.T) {
	text, images := flattenPrompt([]contentBlock{textBlock("hello world")})
	if text != "hello world" {
		t.Errorf("text = %q", text)
	}
	if len(images) != 0 {
		t.Errorf("expected no images, got %d", len(images))
	}
}

func TestFlattenPromptMultipleTextBlocksJoinedWithBlankLine(t *testing.T) {
	text, _ := flattenPrompt([]contentBlock{textBlock("first"), textBlock("second")})
	if text != "first\n\nsecond" {
		t.Errorf("text = %q", text)
	}
}

func TestFlattenPromptEmbeddedResource(t *testing.T) {
	blocks := []contentBlock{
		textBlock("please review:"),
		{Type: "resource", Resource: &embeddedResource{URI: "file:///a.py", Text: "def f(): pass"}},
	}
	text, _ := flattenPrompt(blocks)
	want := "please review:\n\ndef f(): pass"
	if text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
}

func TestFlattenPromptResourceWithoutText(t *testing.T) {
	// A resource block with no embedded text (e.g. a blob) contributes nothing —
	// this transport doesn't resolve blob/binary resources.
	blocks := []contentBlock{
		textBlock("look at this:"),
		{Type: "resource", Resource: &embeddedResource{URI: "file:///a.png", Blob: "base64=="}},
	}
	text, _ := flattenPrompt(blocks)
	if text != "look at this:" {
		t.Errorf("text = %q", text)
	}
}

func TestFlattenPromptImage(t *testing.T) {
	blocks := []contentBlock{
		textBlock("what is this?"),
		{Type: "image", Data: "base64data", MimeType: "image/png"},
	}
	text, images := flattenPrompt(blocks)
	if text != "what is this?" {
		t.Errorf("text = %q", text)
	}
	if len(images) != 1 || images[0].Base64 != "base64data" || images[0].MimeType != "image/png" {
		t.Errorf("images = %+v", images)
	}
}

func TestFlattenPromptResourceLinkIgnored(t *testing.T) {
	// resource_link (URI-only, no embedded content) is out of scope for the
	// first cut — must not panic and must not contribute text.
	blocks := []contentBlock{
		{Type: "resource_link", URI: "file:///doc.pdf", Name: "doc.pdf"},
	}
	text, images := flattenPrompt(blocks)
	if text != "" || len(images) != 0 {
		t.Errorf("text = %q, images = %+v, want both empty", text, images)
	}
}

func TestFlattenPromptEmpty(t *testing.T) {
	text, images := flattenPrompt(nil)
	if text != "" || images != nil {
		t.Errorf("text = %q, images = %v", text, images)
	}
}
