package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFileOfSize creates a file of exactly n bytes at path, filled with
// arbitrary content — no actual PNG structure needed since ReadFile's size
// guard runs BEFORE any decoding, purely on the raw byte count via os.Stat.
func writeFileOfSize(t *testing.T, path string, n int) {
	t.Helper()
	data := make([]byte, n)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

// TestReadFile_ImageOverSizeLimitRejectedBeforeReading is the regression test
// for a real incident: an 8.5MB PNG (image/png raw bytes) was read, base64
// encoded to ~11.35MB, exceeded Anthropic's 10MB tool_result image cap, and
// the resulting 400 error left the oversized base64 payload permanently
// embedded in the session's persisted .jsonl history — corrupting that
// session (every future resume replays the same oversized tool_result and
// the same 400). The guard must reject a too-large image BEFORE ever
// reading/encoding it, so no oversized payload can reach the session store.
func TestReadFile_ImageOverSizeLimitRejectedBeforeReading(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.png")
	// One byte over the raw-file threshold (7.5MB = 7,864,320 bytes).
	writeFileOfSize(t, path, maxImageFileBytes+1)

	r := ReadFile(dir)
	out, images, err := r.ExecuteRich(context.Background(), json.RawMessage(`{"path":"big.png"}`))

	if err == nil {
		t.Fatal("expected an error for an over-limit image, got nil")
	}
	if images != nil {
		t.Errorf("images = %v, want nil — the oversized file must never be read/encoded", images)
	}
	if !strings.Contains(out, "image too large") {
		t.Errorf("output = %q, want an explicit 'image too large' message", out)
	}
	if strings.Contains(out, "iVBOR") || len(out) > 500 {
		// A base64 PNG payload would be much longer than any reasonable error
		// message and would contain recognizable base64 image-header bytes.
		t.Errorf("output looks like it may contain base64 image data (len=%d) — the guard must never encode an oversized file", len(out))
	}
}

// A file exactly AT the threshold, and comfortably under it, must still be
// read and encoded normally — the guard must not be off-by-one or overly
// conservative on the common case.
func TestReadFile_ImageAtOrUnderSizeLimitStillWorks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ok.png")
	writeFileOfSize(t, path, maxImageFileBytes) // exactly at the limit — must pass

	r := ReadFile(dir)
	out, images, err := r.ExecuteRich(context.Background(), json.RawMessage(`{"path":"ok.png"}`))
	if err != nil {
		t.Fatalf("unexpected error at exactly the limit: %v (out=%q)", err, out)
	}
	if len(images) != 1 {
		t.Fatalf("expected exactly 1 image, got %d", len(images))
	}
	if !strings.Contains(out, "Image loaded") {
		t.Errorf("out = %q, want the normal success message", out)
	}
}

// Non-image files are completely unaffected by this guard, regardless of size
// — the limit is specific to the base64-image path.
func TestReadFile_LargeTextFileUnaffectedByImageGuard(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	writeFileOfSize(t, path, maxImageFileBytes+1_000_000) // well over the image threshold

	r := ReadFile(dir)
	_, images, err := r.ExecuteRich(context.Background(), json.RawMessage(`{"path":"big.txt"}`))
	if err != nil {
		t.Fatalf("a large non-image file must not be rejected by the image size guard: %v", err)
	}
	if images != nil {
		t.Errorf("images = %v, want nil for a text file", images)
	}
}
