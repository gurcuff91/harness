package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runEdit(t *testing.T, path string, in editInput) (string, error) {
	t.Helper()
	in.Path = path
	b, _ := json.Marshal(in)
	return Edit().Execute(context.Background(), b)
}

func writeTmp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f.txt")
	os.WriteFile(p, []byte(content), 0644)
	return p
}

func TestEditSingle(t *testing.T) {
	p := writeTmp(t, "hello world")
	out, err := runEdit(t, p, editInput{Edits: []editEntry{{"world", "khan"}}})
	if err != nil {
		t.Fatal(err, out)
	}
	if got, _ := os.ReadFile(p); string(got) != "hello khan" {
		t.Errorf("got %q", got)
	}
}

func TestEditMulti(t *testing.T) {
	p := writeTmp(t, "aaa\nbbb\nccc")
	out, err := runEdit(t, p, editInput{Edits: []editEntry{{"aaa", "x"}, {"ccc", "z"}}})
	if err != nil {
		t.Fatal(err, out)
	}
	if got, _ := os.ReadFile(p); string(got) != "x\nbbb\nz" {
		t.Errorf("multi-edit got %q", got)
	}
	if !strings.Contains(out, "2 blocks") {
		t.Errorf("expected '2 blocks', got %q", out)
	}
}


func TestEditNotFound(t *testing.T) {
	p := writeTmp(t, "hello")
	_, err := runEdit(t, p, editInput{Edits: []editEntry{{"xyz", "q"}}})
	if err == nil || !strings.Contains(err.Error(), "could not find") {
		t.Errorf("expected not-found, got %v", err)
	}
}

func TestEditNotUnique(t *testing.T) {
	p := writeTmp(t, "dup dup")
	_, err := runEdit(t, p, editInput{Edits: []editEntry{{"dup", "x"}}})
	if err == nil || !strings.Contains(err.Error(), "occurrences") {
		t.Errorf("expected uniqueness error, got %v", err)
	}
}

func TestEditOverlap(t *testing.T) {
	p := writeTmp(t, "abcdef")
	_, err := runEdit(t, p, editInput{Edits: []editEntry{{"abcd", "X"}, {"cdef", "Y"}}})
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Errorf("expected overlap error, got %v", err)
	}
}

func TestEditCRLFPreserved(t *testing.T) {
	// File is CRLF; model sends LF old_text → must still match, ending preserved.
	p := writeTmp(t, "line1\r\nline2\r\nline3")
	out, err := runEdit(t, p, editInput{Edits: []editEntry{{"line2", "LINE2"}}})
	if err != nil {
		t.Fatal(err, out)
	}
	got, _ := os.ReadFile(p)
	if !strings.Contains(string(got), "\r\n") {
		t.Errorf("CRLF endings should be preserved, got %q", got)
	}
	if !strings.Contains(string(got), "LINE2") {
		t.Errorf("edit not applied, got %q", got)
	}
}

func TestEditFuzzySmartQuotes(t *testing.T) {
	// File has a smart quote; model sends ASCII quote → fuzzy match succeeds.
	p := writeTmp(t, "say \u201chello\u201d now")
	out, err := runEdit(t, p, editInput{Edits: []editEntry{{`"hello"`, `"bye"`}}})
	if err != nil {
		t.Fatalf("fuzzy should match smart quotes: %v (%s)", err, out)
	}
	got, _ := os.ReadFile(p)
	if !strings.Contains(string(got), "bye") {
		t.Errorf("fuzzy edit not applied, got %q", got)
	}
}

func TestEditBOMPreserved(t *testing.T) {
	p := writeTmp(t, "\uFEFFhello world")
	_, err := runEdit(t, p, editInput{Edits: []editEntry{{"world", "khan"}}})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(got), "\uFEFF") {
		t.Errorf("BOM should be preserved, got %q", got)
	}
}

func TestEditNoChange(t *testing.T) {
	p := writeTmp(t, "same")
	_, err := runEdit(t, p, editInput{Edits: []editEntry{{"same", "same"}}})
	if err == nil || !strings.Contains(err.Error(), "no changes") {
		t.Errorf("expected no-change error, got %v", err)
	}
}

func TestEditFlatSingle(t *testing.T) {
	// Flat old_text/new_text form (single edit).
	p := writeTmp(t, "foo bar")
	out, err := runEdit(t, p, editInput{OldText: "bar", NewText: "baz"})
	if err != nil {
		t.Fatal(err, out)
	}
	if got, _ := os.ReadFile(p); string(got) != "foo baz" {
		t.Errorf("flat got %q", got)
	}
}

func TestEditRejectsBothForms(t *testing.T) {
	p := writeTmp(t, "foo bar")
	_, err := runEdit(t, p, editInput{OldText: "foo", NewText: "x", Edits: []editEntry{{"bar", "y"}}})
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("expected 'not both' error, got %v", err)
	}
}

func TestEditRejectsNeitherForm(t *testing.T) {
	p := writeTmp(t, "foo bar")
	_, err := runEdit(t, p, editInput{})
	if err == nil || !strings.Contains(err.Error(), "single edit") {
		t.Errorf("expected 'provide...' error, got %v", err)
	}
}
