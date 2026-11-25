package tui

import (
	"strings"
	"testing"

	"github.com/gurcuff91/harness/client"
)

// A resumed session bound to a profile that is not registered here works
// without it; the user is told which ones, never silently.
func TestResumeWarnsAboutMissingProfiles(t *testing.T) {
	tu := newTestTUIForEvents()
	tu.noteModelFallback(&client.Session{MissingProfiles: []string{"slack", "chat"}})
	got := screenText(tu)
	if !strings.Contains(got, "profiles not registered") || !strings.Contains(got, "slack, chat") {
		t.Fatalf("no warning:\n%s", got)
	}
	tu = newTestTUIForEvents()
	tu.noteModelFallback(&client.Session{})
	if screenText(tu) != "" {
		t.Fatal("a session with no missing profile must print nothing")
	}
}
