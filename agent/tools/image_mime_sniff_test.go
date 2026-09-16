package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── sniffImageMime ───────────────────────────────────────────────────────

func TestSniffImageMimeDetectsRealFormatRegardlessOfClaim(t *testing.T) {
	jpegMagic := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0, 0, 0, 0, 0, 0, 0}
	gifMagic := []byte("GIF89a0000000000")
	webpMagic := []byte("RIFF\x00\x00\x00\x00WEBPVP8 0000")

	cases := []struct {
		name string
		data []byte
		want string
		ok   bool
	}{
		{"png", pngMagic, "image/png", true},
		{"jpeg", jpegMagic, "image/jpeg", true},
		{"gif", gifMagic, "image/gif", true},
		{"webp", webpMagic, "image/webp", true},
		{"plain text", []byte("hello, this is not an image at all"), "text/plain", false},
		{"empty", []byte{}, "text/plain", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mime, ok := sniffImageMime(c.data)
			if ok != c.ok {
				t.Errorf("ok = %v, want %v (mime=%q)", ok, c.ok, mime)
			}
			if c.ok && mime != c.want {
				t.Errorf("mime = %q, want %q", mime, c.want)
			}
		})
	}
}

// ── Read: mismatched extension vs. real content ─────────────────────────

// TestReadFile_ExtensionContentMismatchUsesRealDetectedMime is the direct
// regression test for a real, field-reported session corruption: a file
// named "campaign_setup_current.png" whose actual bytes were JPEG got the
// WRONG mime_type persisted (inferred from the ".png" extension alone),
// which Anthropic then rejected on every future turn that replayed the
// session's history — the session was permanently stuck until the .jsonl
// was hand-edited. Read must sniff the real bytes and tag the image with
// the ACTUAL format (image/jpeg here), not the extension's claim — the
// content IS a supported format, just mislabeled, so it must still be
// readable; only content that ISN'T any supported format should error (see
// TestReadFile_RejectsUnsupportedContentEvenWithImageExtension).
func TestReadFile_ExtensionContentMismatchUsesRealDetectedMime(t *testing.T) {
	dir := t.TempDir()
	// A JPEG's real magic bytes, saved under a .png extension — exactly the
	// field-reported shape (a screenshot tool or download mislabeling the
	// file, not a deliberately malicious upload).
	jpegBytes := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 100)...)
	path := filepath.Join(dir, "campaign_setup_current.png")
	if err := os.WriteFile(path, jpegBytes, 0644); err != nil {
		t.Fatal(err)
	}

	r := ReadFile(dir)
	out, images, err := r.ExecuteRich(context.Background(), json.RawMessage(`{"path":"campaign_setup_current.png"}`))

	if err != nil {
		t.Fatalf("unexpected error: %v (out=%q)", err, out)
	}
	if len(images) != 1 {
		t.Fatalf("expected exactly 1 image, got %d", len(images))
	}
	if images[0].MimeType != "image/jpeg" {
		t.Errorf("MimeType = %q, want image/jpeg (the REAL content, not the .png extension's claim) — this exact mismatch is what corrupted the field-reported session", images[0].MimeType)
	}
	if !strings.Contains(out, "image/jpeg") {
		t.Errorf("output = %q, want it to name the ACTUAL detected format (image/jpeg)", out)
	}
}

// TestReadFile_RejectsUnsupportedContentEvenWithImageExtension confirms the
// other half of the fix: content that ISN'T any supported image format at
// all (not just mislabeled, but genuinely not an image) must still be
// rejected outright, even with a perfectly normal image extension.
func TestReadFile_RejectsUnsupportedContentEvenWithImageExtension(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not-really-an-image.png")
	if err := os.WriteFile(path, []byte("just some plain text, not an image at all"), 0644); err != nil {
		t.Fatal(err)
	}

	r := ReadFile(dir)
	out, images, err := r.ExecuteRich(context.Background(), json.RawMessage(`{"path":"not-really-an-image.png"}`))
	if err == nil {
		t.Fatal("expected an error for a .png file whose content isn't any supported image format")
	}
	if images != nil {
		t.Errorf("images = %v, want nil", images)
	}
	if !strings.Contains(out, "not a supported image format") {
		t.Errorf("output = %q, want a clear 'not a supported image format' message", out)
	}
}

// TestReadFile_CorrectExtensionStillWorks confirms the fix doesn't break
// the ordinary case: a .png file that IS actually a PNG must still be read
// and tagged image/png (sniffed, not just trusted — but they happen to
// agree here).
func TestReadFile_CorrectExtensionStillWorks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "real.png")
	data := append(append([]byte{}, pngMagic...), make([]byte, 50)...)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	r := ReadFile(dir)
	out, images, err := r.ExecuteRich(context.Background(), json.RawMessage(`{"path":"real.png"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v (out=%q)", err, out)
	}
	if len(images) != 1 {
		t.Fatalf("expected exactly 1 image, got %d", len(images))
	}
	if images[0].MimeType != "image/png" {
		t.Errorf("MimeType = %q, want image/png", images[0].MimeType)
	}
}

// TestReadFile_JPEGWithJPGExtensionDetectedCorrectly confirms a genuinely
// mismatched-but-still-valid case works too: a .jpg file (not .jpeg) with
// real JPEG bytes must be tagged image/jpeg by content, same as before —
// sniffing must not regress the common, already-correct case.
func TestReadFile_JPEGWithJPGExtensionDetectedCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "photo.jpg")
	data := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 50)...)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	r := ReadFile(dir)
	_, images, err := r.ExecuteRich(context.Background(), json.RawMessage(`{"path":"photo.jpg"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(images) != 1 || images[0].MimeType != "image/jpeg" {
		t.Errorf("expected image/jpeg, got %+v", images)
	}
}

// ── loadColleagueImages (Subagent/ColleagueAsk image attachments) ────────

func TestLoadColleagueImages_ExtensionContentMismatchUsesRealDetectedMime(t *testing.T) {
	dir := t.TempDir()
	jpegBytes := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 50)...)
	path := filepath.Join(dir, "mislabeled.png")
	if err := os.WriteFile(path, jpegBytes, 0644); err != nil {
		t.Fatal(err)
	}

	images, err := loadColleagueImages([]string{path})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(images) != 1 || images[0].MimeType != "image/jpeg" {
		t.Errorf("expected 1 image tagged image/jpeg (the real content), got %+v", images)
	}
}

func TestLoadColleagueImages_RejectsUnsupportedContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not-an-image.png")
	if err := os.WriteFile(path, []byte("plain text, not an image"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := loadColleagueImages([]string{path})
	if err == nil {
		t.Fatal("expected an error for content that isn't any supported image format")
	}
}

func TestLoadColleagueImages_CorrectContentStillWorks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "real.png")
	data := append(append([]byte{}, pngMagic...), make([]byte, 20)...)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	images, err := loadColleagueImages([]string{path})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(images) != 1 || images[0].MimeType != "image/png" {
		t.Errorf("expected 1 image tagged image/png, got %+v", images)
	}
}
