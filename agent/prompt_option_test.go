package agent

import (
	"testing"

	"github.com/gurcuff91/harness/types"
)

// TestBuildPromptConfigDefaultsToUserOrigin verifies buildPromptConfig's
// zero-value default (no options passed) is OriginUser — the same default
// documented on Session.Prompt.
func TestBuildPromptConfigDefaultsToUserOrigin(t *testing.T) {
	c := buildPromptConfig(nil)
	if c.origin != OriginUser {
		t.Errorf("origin = %q, want %q", c.origin, OriginUser)
	}
	if len(c.images) != 0 {
		t.Errorf("images = %v, want empty", c.images)
	}
}

// TestPromptWithImagesAppendsImages verifies PromptWithImages (renamed from
// WithImages to match the PromptWith* naming convention — distinguishing
// PromptOption constructors from AgentOption ones in harness.go, which use
// AgentWith*) attaches images and that repeated calls accumulate.
func TestPromptWithImagesAppendsImages(t *testing.T) {
	img1 := types.ImageData{Base64: "aaa", MimeType: "image/png"}
	img2 := types.ImageData{Base64: "bbb", MimeType: "image/jpeg"}

	c := buildPromptConfig([]PromptOption{
		PromptWithImages(img1),
		PromptWithImages(img2),
	})
	if len(c.images) != 2 {
		t.Fatalf("images = %v, want 2 entries", c.images)
	}
	if c.images[0] != img1 || c.images[1] != img2 {
		t.Errorf("images = %v, want [%v, %v]", c.images, img1, img2)
	}
}

// TestPromptWithOriginUserAndScheduled verifies both origin-tagging options
// (renamed from WithOriginUser/WithOriginScheduled) set the expected origin
// constant.
func TestPromptWithOriginUserAndScheduled(t *testing.T) {
	if c := buildPromptConfig([]PromptOption{PromptWithOriginUser()}); c.origin != OriginUser {
		t.Errorf("PromptWithOriginUser: origin = %q, want %q", c.origin, OriginUser)
	}
	if c := buildPromptConfig([]PromptOption{PromptWithOriginScheduled()}); c.origin != OriginScheduled {
		t.Errorf("PromptWithOriginScheduled: origin = %q, want %q", c.origin, OriginScheduled)
	}
}

// TestBuildPromptConfigDefaultsToEmptyDisplayText verifies the zero-value
// default (no PromptWithDisplayText) leaves displayText empty — the signal
// promptSync's echo path uses to fall back to the real prompt text.
func TestBuildPromptConfigDefaultsToEmptyDisplayText(t *testing.T) {
	c := buildPromptConfig(nil)
	if c.displayText != "" {
		t.Errorf("displayText = %q, want empty (no override)", c.displayText)
	}
}

// TestPromptWithDisplayTextSetsOverride verifies PromptWithDisplayText sets
// displayText without touching any other config field.
func TestPromptWithDisplayTextSetsOverride(t *testing.T) {
	c := buildPromptConfig([]PromptOption{PromptWithDisplayText("Skill: goal build a widget")})
	if c.displayText != "Skill: goal build a widget" {
		t.Errorf("displayText = %q, want %q", c.displayText, "Skill: goal build a widget")
	}
	if c.origin != OriginUser {
		t.Errorf("origin = %q, want default %q unaffected by PromptWithDisplayText", c.origin, OriginUser)
	}
}

