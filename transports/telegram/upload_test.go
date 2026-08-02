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

// A tag inside a fenced code block is an example (the agent explaining how tags
// work) — it must NOT be uploaded and must pass through verbatim.
func TestExtractUploadsInFenceIgnored(t *testing.T) {
	in := "Include this:\n```\n<tel:uploadFile>/ruta/al/archivo</tel:uploadFile>\n```\ndone"
	paths, cleaned := extractUploads(in)
	if len(paths) != 0 {
		t.Errorf("tag in a code fence must not upload, got %v", paths)
	}
	if !strings.Contains(cleaned, "<tel:uploadFile>/ruta/al/archivo</tel:uploadFile>") {
		t.Errorf("example tag should pass through verbatim:\n%s", cleaned)
	}
}

// A tag inside an inline code span is likewise an example, left untouched.
func TestExtractUploadsInInlineCodeIgnored(t *testing.T) {
	in := "use `<tel:uploadFile>/x</tel:uploadFile>` like so"
	paths, cleaned := extractUploads(in)
	if len(paths) != 0 {
		t.Errorf("tag in inline code must not upload, got %v", paths)
	}
	if !strings.Contains(cleaned, "`<tel:uploadFile>/x</tel:uploadFile>`") {
		t.Errorf("inline example should pass through:\n%s", cleaned)
	}
}

// A real tag (normal text) still works even when the message ALSO contains an
// example in a code block.
func TestExtractUploadsMixedRealAndExample(t *testing.T) {
	in := "Example: ```\n<tel:uploadFile>/example</tel:uploadFile>\n```\nHere it is: <tel:uploadFile>/real/file.png</tel:uploadFile>"
	paths, _ := extractUploads(in)
	if len(paths) != 1 || paths[0] != "/real/file.png" {
		t.Errorf("only the real tag should upload, got %v", paths)
	}
}

// Tags immediately wrapped in quotes or parentheses are examples (the directive
// forbids that for real tags) and must not upload.
func TestExtractUploadsWrappedIgnored(t *testing.T) {
	for _, in := range []string{
		`shown as "<tel:uploadFile>/x</tel:uploadFile>"`,
		`shown as '<tel:uploadFile>/x</tel:uploadFile>'`,
		`shown as (<tel:uploadFile>/x</tel:uploadFile>)`,
	} {
		if paths, _ := extractUploads(in); len(paths) != 0 {
			t.Errorf("wrapped tag must not upload: %q -> %v", in, paths)
		}
	}
}

// A parenthesis elsewhere in the sentence must not block a real (unwrapped) tag.
func TestExtractUploadsParenNotAdjacent(t *testing.T) {
	in := "see it (above) here: <tel:uploadFile>/real.png</tel:uploadFile>"
	paths, _ := extractUploads(in)
	if len(paths) != 1 || paths[0] != "/real.png" {
		t.Errorf("a distant paren shouldn't block a real tag, got %v", paths)
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
