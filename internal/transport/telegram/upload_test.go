package telegram

import (
	"strings"
	"testing"
)

func TestExtractUploadsSingle(t *testing.T) {
	in := "Here's the logo. <tel:uploadFile>/tmp/logo.png</tel:uploadFile>"
	paths, cleaned := extractUploads(in)
	if len(paths) != 1 || paths[0] != "/tmp/logo.png" {
		t.Fatalf("paths=%v", paths)
	}
	if strings.Contains(cleaned, "tel:uploadFile") || strings.Contains(cleaned, "/tmp/logo.png") {
		t.Errorf("tag not stripped: %q", cleaned)
	}
	if cleaned != "Here's the logo." {
		t.Errorf("cleaned text = %q", cleaned)
	}
}

func TestExtractUploadsMultiple(t *testing.T) {
	in := "Files: <tel:uploadFile>/a.png</tel:uploadFile> and <tel:uploadFile>/b.pdf</tel:uploadFile>"
	paths, cleaned := extractUploads(in)
	if len(paths) != 2 || paths[0] != "/a.png" || paths[1] != "/b.pdf" {
		t.Fatalf("paths=%v", paths)
	}
	if strings.Contains(cleaned, "tel:") {
		t.Errorf("tags not stripped: %q", cleaned)
	}
}

func TestExtractUploadsNone(t *testing.T) {
	in := "Just a normal reply, no tags."
	paths, cleaned := extractUploads(in)
	if len(paths) != 0 {
		t.Errorf("expected no paths, got %v", paths)
	}
	if cleaned != in {
		t.Errorf("text should be unchanged: %q", cleaned)
	}
}

// A malformed/empty tag is still stripped (never leaks), but yields no path.
func TestExtractUploadsEmptyTagStripped(t *testing.T) {
	in := "oops <tel:uploadFile></tel:uploadFile> done"
	paths, cleaned := extractUploads(in)
	if len(paths) != 0 {
		t.Errorf("empty tag should yield no path, got %v", paths)
	}
	if strings.Contains(cleaned, "tel:uploadFile") {
		t.Errorf("empty tag must still be stripped: %q", cleaned)
	}
}

// Tags spanning newlines (path on its own line) still parse and strip cleanly.
func TestExtractUploadsMultiline(t *testing.T) {
	in := "Sending now.\n<tel:uploadFile>/tmp/report.pdf</tel:uploadFile>\nDone."
	paths, cleaned := extractUploads(in)
	if len(paths) != 1 || paths[0] != "/tmp/report.pdf" {
		t.Fatalf("paths=%v", paths)
	}
	if strings.Contains(cleaned, "tel:") {
		t.Errorf("tag not stripped: %q", cleaned)
	}
	// No orphaned triple-blank lines.
	if strings.Contains(cleaned, "\n\n\n") {
		t.Errorf("blank lines not collapsed: %q", cleaned)
	}
}
