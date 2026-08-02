package telegram

import (
	"context"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/agent"
	agentstore "github.com/gurcuff91/harness/agent/store"
)

func newTestAgent(t *testing.T) *agent.Agent {
	t.Helper()
	a := agent.New(agent.AgentOptions{Store: agentstore.NewInMemoryStore()})
	t.Cleanup(func() { _ = a.Close() })
	return a
}

// TestSaveTokenAndLoadTokenRoundTrip verifies the persistence this feature
// adds: SaveToken writes the bot token to ~/.harness/telegram.json, and
// LoadToken reads it back — the same fallback mechanism Run uses so
// `harness telegram` doesn't require --token/TELEGRAM_BOT_TOKEN on every
// invocation once a token has been saved via `harness telegram token
// <token>`.
func TestSaveTokenAndLoadTokenRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate from the real ~/.harness

	if err := SaveToken("test-token-123"); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	got, err := LoadToken()
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if got != "test-token-123" {
		t.Errorf("LoadToken() = %q, want %q", got, "test-token-123")
	}
}

// TestLoadTokenEmptyWhenNeverSaved verifies LoadToken returns "" (no error)
// when telegram.json doesn't exist yet — the common first-run case.
func TestLoadTokenEmptyWhenNeverSaved(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	got, err := LoadToken()
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if got != "" {
		t.Errorf("LoadToken() = %q, want empty string", got)
	}
}

// TestSaveTokenPreservesAllowlistAndSessions verifies SaveToken does a
// read-modify-write (like slack.SaveCredentials) — it must not clobber an
// existing allowlist/session mappings already on disk.
func TestSaveTokenPreservesAllowlistAndSessions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	st, err := openStore("")
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	if _, err := st.pair(12345); err != nil {
		t.Fatalf("pair: %v", err)
	}
	if err := st.bind(12345, "sess-abc"); err != nil {
		t.Fatalf("bind: %v", err)
	}

	if err := SaveToken("new-token"); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	reopened, err := openStore("")
	if err != nil {
		t.Fatalf("re-openStore: %v", err)
	}
	if reopened.data.Token != "new-token" {
		t.Errorf("token = %q, want %q", reopened.data.Token, "new-token")
	}
	if !reopened.allowed(12345) {
		t.Error("SaveToken clobbered the existing allowlist entry")
	}
	if id, ok := reopened.sessionFor(12345); !ok || id != "sess-abc" {
		t.Errorf("SaveToken clobbered the existing session mapping: got (%q, %v)", id, ok)
	}
}

// TestSaveTokenOverwritesPreviousToken verifies a second SaveToken call
// replaces the first, rather than erroring or appending.
func TestSaveTokenOverwritesPreviousToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := SaveToken("first-token"); err != nil {
		t.Fatalf("SaveToken (first): %v", err)
	}
	if err := SaveToken("second-token"); err != nil {
		t.Fatalf("SaveToken (second): %v", err)
	}
	got, err := LoadToken()
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if got != "second-token" {
		t.Errorf("LoadToken() = %q, want %q (overwritten)", got, "second-token")
	}
}

// TestRunFallsBackToSavedToken is the regression test for the actual
// feature: Run must use the token saved via SaveToken when neither
// WithToken nor TELEGRAM_BOT_TOKEN provides one — same fallback precedence
// as Slack's Run (flags/env > saved config). Since this test can't reach
// the real Telegram Bot API, it distinguishes "fallback happened" from "no
// token at all" by the ERROR MESSAGE: with no fallback, Run fails fast with
// "a bot token is required" before ever calling the network; with the
// fallback applied, it gets far enough to attempt GetMe against the (fake,
// unreachable-as-a-real-bot) saved token and fails with "invalid token or
// unreachable API" instead — proving opts.Token was populated from disk.
func TestRunFallsBackToSavedToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := SaveToken("saved-fallback-token"); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := Run(ctx, newTestAgent(t)) // no WithToken — must fall back to the saved one
	if err == nil {
		t.Fatal("Run succeeded unexpectedly (no real bot token was ever valid in this test)")
	}
	if strings.Contains(err.Error(), "a bot token is required") {
		t.Fatalf("Run did not fall back to the saved token — got the no-token error: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid token or unreachable API") {
		t.Errorf("Run error = %q, want it to have reached the GetMe call (proving the fallback applied)", err.Error())
	}
}

// TestRunFailsWithoutTokenOrSavedFallback verifies the OTHER side: with
// nothing saved and no WithToken/env var, Run fails fast with the
// no-token error, never reaching the network at all.
func TestRunFailsWithoutTokenOrSavedFallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // empty — nothing saved

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := Run(ctx, newTestAgent(t))
	if err == nil {
		t.Fatal("Run succeeded unexpectedly with no token anywhere")
	}
	if !strings.Contains(err.Error(), "a bot token is required") {
		t.Errorf("Run error = %q, want the no-token error", err.Error())
	}
}
