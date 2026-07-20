package logx

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// capture redirects the standard logger's output (and strips its timestamp
// prefix) so tests can assert on the rendered line.
func capture(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	oldW, oldF := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0) // no timestamp during the test
	defer func() {
		log.SetOutput(oldW)
		log.SetFlags(oldF)
	}()
	fn()
	return strings.TrimRight(buf.String(), "\n")
}

func TestInfoFormat(t *testing.T) {
	got := capture(t, func() {
		Info("telegram", "prompt", "chat", 5353, "session", "dde9")
	})
	want := "INFO  [telegram] prompt chat=5353 session=dde9"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestQuotingSpaces(t *testing.T) {
	got := capture(t, func() {
		Info("telegram", "reply", "text", "hola mundo")
	})
	if !strings.Contains(got, `text="hola mundo"`) {
		t.Errorf("value with spaces should be quoted: %q", got)
	}
}

func TestLevels(t *testing.T) {
	for _, tc := range []struct {
		fn    func(string, string, ...any)
		level string
	}{
		{Info, "INFO "},
		{Warn, "WARN "},
		{Error, "ERROR"},
	} {
		got := capture(t, func() { tc.fn("server", "event") })
		if !strings.HasPrefix(got, tc.level+" [server] event") {
			t.Errorf("level %q: got %q", tc.level, got)
		}
	}
}

func TestOddKVSkipped(t *testing.T) {
	// A trailing key with no value is ignored, not rendered as key=.
	got := capture(t, func() {
		Info("x", "e", "a", 1, "dangling")
	})
	if strings.Contains(got, "dangling") {
		t.Errorf("dangling key should be skipped: %q", got)
	}
	if !strings.HasSuffix(got, "a=1") {
		t.Errorf("got %q", got)
	}
}
