package acp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCapturePreEditStateExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(path, []byte("old content"), 0o644); err != nil {
		t.Fatal(err)
	}

	args, _ := json.Marshal(map[string]string{"path": path})
	pre, ok := capturePreEditState(string(args))
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !pre.hadFile || pre.oldText != "old content" {
		t.Errorf("pre = %+v", pre)
	}
}

func TestCapturePreEditStateNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "brand-new.txt")

	args, _ := json.Marshal(map[string]string{"path": path})
	pre, ok := capturePreEditState(string(args))
	if !ok {
		t.Fatal("expected ok=true even for a not-yet-existing file")
	}
	if pre.hadFile {
		t.Error("hadFile should be false for a new file")
	}
}

func TestCapturePreEditStateNoPath(t *testing.T) {
	_, ok := capturePreEditState(`{"not_path": "x"}`)
	if ok {
		t.Error("expected ok=false when toolArgs has no path field")
	}
}

func TestCapturePreEditStateInvalidJSON(t *testing.T) {
	_, ok := capturePreEditState(`not json`)
	if ok {
		t.Error("expected ok=false for invalid JSON")
	}
}

func TestBuildDiffContentModifiedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "modified.txt")
	if err := os.WriteFile(path, []byte("new content"), 0o644); err != nil {
		t.Fatal(err)
	}

	pre := pendingEdit{path: path, oldText: "old content", hadFile: true}
	content, ok := buildDiffContent(pre)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if content.Type != "diff" || content.Path != path {
		t.Errorf("content = %+v", content)
	}
	if content.OldText == nil || *content.OldText != "old content" {
		t.Errorf("OldText = %v, want \"old content\"", content.OldText)
	}
	if content.NewText != "new content" {
		t.Errorf("NewText = %q", content.NewText)
	}
}

func TestBuildDiffContentNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "created.txt")
	if err := os.WriteFile(path, []byte("fresh content"), 0o644); err != nil {
		t.Fatal(err)
	}

	pre := pendingEdit{path: path, hadFile: false}
	content, ok := buildDiffContent(pre)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if content.OldText != nil {
		t.Errorf("OldText should be nil (ACP's null = new file) for a brand-new file, got %v", *content.OldText)
	}
	if content.NewText != "fresh content" {
		t.Errorf("NewText = %q", content.NewText)
	}
}

func TestBuildDiffContentReadFailure(t *testing.T) {
	pre := pendingEdit{path: "/nonexistent/path/that/does/not/exist.txt"}
	_, ok := buildDiffContent(pre)
	if ok {
		t.Error("expected ok=false when the post-edit read fails")
	}
}

func TestToolCallContentDiffMarshalsNullOldTextForNewFile(t *testing.T) {
	content := toolCallContent{Type: "diff", Path: "/a/b.txt", OldText: nil, NewText: "hi"}
	b, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if string(m["oldText"]) != "null" {
		t.Errorf("oldText = %s, want null (ACP semantics: null = new file)", m["oldText"])
	}
}
