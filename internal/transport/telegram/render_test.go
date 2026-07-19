package telegram

import (
	"strings"
	"testing"
)

func TestToMarkdownV2EscapesSpecials(t *testing.T) {
	// Plain sentence with a period and hyphen — both must be escaped.
	got := toMarkdownV2("Done. Fixed 3 bugs - all green!")
	for _, s := range []string{"\\.", "\\-", "\\!"} {
		if !strings.Contains(got, s) {
			t.Errorf("expected %q to be escaped in %q", s, got)
		}
	}
}

// Regression: multi-byte UTF-8 (accents, ñ, emoji) must pass through intact,
// not be mangled byte-by-byte into mojibake (Déjame → DÃ©jame).
func TestToMarkdownV2PreservesUTF8(t *testing.T) {
	in := "Déjame probar el fútbol de España ⚽ ¡perfecto!"
	got := toMarkdownV2(in)
	for _, want := range []string{"Déjame", "fútbol", "España", "⚽"} {
		if !strings.Contains(got, want) {
			t.Errorf("UTF-8 %q got mangled in %q", want, got)
		}
	}
	if strings.Contains(got, "Ã") {
		t.Errorf("mojibake detected (Ã) in %q", got)
	}
}

func TestToMarkdownV2Bold(t *testing.T) {
	got := toMarkdownV2("This is **bold** text")
	if !strings.Contains(got, "*bold*") {
		t.Errorf("**bold** should become *bold*, got %q", got)
	}
}

// Regression: single-asterisk italic (CommonMark) was being escaped to a literal
// \* instead of mapped to MarkdownV2's _italic_.
func TestToMarkdownV2Italic(t *testing.T) {
	if got := toMarkdownV2("a *italic* b"); !strings.Contains(got, "_italic_") {
		t.Errorf("*italic* should become _italic_, got %q", got)
	}
	if got := toMarkdownV2("a _italic_ b"); !strings.Contains(got, "_italic_") {
		t.Errorf("_italic_ should stay _italic_, got %q", got)
	}
	// A lone / arithmetic asterisk must NOT italicize — it's escaped.
	if got := toMarkdownV2("2 * 3 = 6"); !strings.Contains(got, "\\*") {
		t.Errorf("stray * should be escaped, got %q", got)
	}
	// Bold still wins over italic when doubled.
	if got := toMarkdownV2("x **bold** y"); !strings.Contains(got, "*bold*") || strings.Contains(got, "_bold_") {
		t.Errorf("**bold** should stay bold, got %q", got)
	}
}

func TestToMarkdownV2Heading(t *testing.T) {
	got := toMarkdownV2("# Title")
	if !strings.HasPrefix(got, "*Title*") {
		t.Errorf("heading should become bold line, got %q", got)
	}
}

func TestToMarkdownV2InlineCode(t *testing.T) {
	got := toMarkdownV2("run `go build .` now")
	if !strings.Contains(got, "`go build .`") {
		t.Errorf("inline code should be preserved (period not escaped inside), got %q", got)
	}
}

func TestToMarkdownV2FencePreserved(t *testing.T) {
	in := "```go\nfmt.Println(\"hi\")\n```"
	got := toMarkdownV2(in)
	if !strings.HasPrefix(got, "```go") {
		t.Errorf("fence should be preserved, got %q", got)
	}
	// Period inside the fence must NOT be escaped.
	if strings.Contains(got, "fmt\\.Println") {
		t.Errorf("code inside fence should not be escaped, got %q", got)
	}
}

func TestSplitMessageShort(t *testing.T) {
	if got := splitMessage("hello"); len(got) != 1 || got[0] != "hello" {
		t.Errorf("short text should be one chunk, got %v", got)
	}
}

func TestSplitMessageLong(t *testing.T) {
	// Build a >4096 char text with paragraph breaks.
	para := strings.Repeat("x", 1000) + "\n\n"
	long := strings.Repeat(para, 6) // ~6000 chars
	chunks := splitMessage(long)
	if len(chunks) < 2 {
		t.Fatalf("long text should split into multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > telegramMaxLen {
			t.Errorf("chunk %d exceeds max: %d", i, len(c))
		}
	}
}

func TestStoreSessionRoundTrip(t *testing.T) {
	path := t.TempDir() + "/telegram.json"
	c, _ := openStore(path)
	if _, ok := c.sessionFor(111); ok {
		t.Error("unknown chat should not resolve")
	}
	c.bind(111, "sess-a")
	if id, ok := c.sessionFor(111); !ok || id != "sess-a" {
		t.Errorf("bind failed: %q %v", id, ok)
	}
	// Reload from disk → mapping persists.
	c2, _ := openStore(path)
	if id, ok := c2.sessionFor(111); !ok || id != "sess-a" {
		t.Errorf("mapping should persist across reload: %q %v", id, ok)
	}
	c2.unbind(111)
	if _, ok := c2.sessionFor(111); ok {
		t.Error("unbind should remove the mapping")
	}
}

func TestStoreAllowlist(t *testing.T) {
	path := t.TempDir() + "/telegram.json"
	c, _ := openStore(path)
	if c.allowed(111) {
		t.Error("empty allowlist should allow nobody")
	}
	// pair is idempotent.
	if added, _ := c.pair(111); !added {
		t.Error("first pair should add")
	}
	if added, _ := c.pair(111); added {
		t.Error("second pair should be a no-op")
	}
	if !c.allowed(111) {
		t.Error("paired chat should be allowed")
	}
	// Persists across reload.
	c2, _ := openStore(path)
	if !c2.allowed(111) {
		t.Error("allowlist should persist across reload")
	}
	// unpair removes from allowlist AND drops the session.
	c2.bind(111, "sess-x")
	if removed, _ := c2.unpair(111); !removed {
		t.Error("unpair should report removal")
	}
	if c2.allowed(111) {
		t.Error("unpaired chat should not be allowed")
	}
	if _, ok := c2.sessionFor(111); ok {
		t.Error("unpair should drop the session mapping too")
	}
}

// The two collections coexist in one file.
func TestStoreAllowlistAndSessionsCoexist(t *testing.T) {
	path := t.TempDir() + "/telegram.json"
	c, _ := openStore(path)
	c.pair(111)
	c.bind(111, "sess-1")
	c.pair(222)
	c2, _ := openStore(path)
	if !c2.allowed(111) || !c2.allowed(222) {
		t.Error("both paired chats should persist")
	}
	if id, ok := c2.sessionFor(111); !ok || id != "sess-1" {
		t.Errorf("session should persist alongside allowlist: %q %v", id, ok)
	}
}
