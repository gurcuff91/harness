package cli

import (
	"strings"
	"testing"

	telegramch "github.com/gurcuff91/harness/channels/telegram"
)

// The channel is registered when a token exists — the flag, else
// TELEGRAM_BOT_TOKEN, else the saved one — and never otherwise.
func TestTelegramChannelIsRegisteredWithAToken(t *testing.T) {
	isolate(t)
	t.Setenv(telegramch.TokenEnv, "")
	if got := telegramChannels("", false); len(got) != 0 {
		t.Fatalf("no token anywhere: %d channels", len(got))
	}
	if got := telegramChannels("1:flag", false); len(got) != 1 || got[0].Type() != "telegram" {
		t.Fatalf("flag: %v", got)
	}
	t.Setenv(telegramch.TokenEnv, "1:env")
	if got := telegramChannels("", false); len(got) != 1 {
		t.Fatal("env token not used")
	}
	t.Setenv(telegramch.TokenEnv, "")
	if err := telegramch.SaveToken("1:saved"); err != nil {
		t.Fatal(err)
	}
	if got := telegramChannels("", false); len(got) != 1 {
		t.Fatal("saved token not used")
	}
}

func TestTelegramTokenPrecedence(t *testing.T) {
	isolate(t)
	if err := telegramch.SaveToken("1:saved"); err != nil {
		t.Fatal(err)
	}
	t.Setenv(telegramch.TokenEnv, "1:env")
	if got := telegramch.ResolveToken("1:flag"); got != "1:flag" {
		t.Errorf("flag first: %q", got)
	}
	if got := telegramch.ResolveToken(""); got != "1:env" {
		t.Errorf("env second: %q", got)
	}
	t.Setenv(telegramch.TokenEnv, "")
	if got := telegramch.ResolveToken(""); got != "1:saved" {
		t.Errorf("saved last: %q", got)
	}
}

// --telegram-allow → the channel request; its rules fail before anything opens.
func TestTelegramAllowRules(t *testing.T) {
	isolate(t)
	t.Setenv(telegramch.TokenEnv, "")
	t.Setenv(TelegramAllowEnv, "")
	reg := telegramChannels("1:flag", false)

	got, err := telegramInputChannels([]string{"@Gus_Test", "ana_test,@gus_test"}, "1:flag", reg)
	if err != nil || len(got) != 1 || got[0].Type != "telegram" || got[0].Ref != "ana_test,gus_test" {
		t.Fatalf("got %v, %v", got, err)
	}
	if got, err := telegramInputChannels(nil, "", nil); err != nil || got != nil {
		t.Errorf("nothing asked: %v %v", got, err)
	}
	if _, err := telegramInputChannels([]string{"gus_test"}, "", nil); err == nil || !strings.Contains(err.Error(), "no Telegram bot token") {
		t.Errorf("users without a token: %v", err)
	}
	if _, err := telegramInputChannels(nil, "1:flag", reg); err == nil || !strings.Contains(err.Error(), "needs --telegram-allow") {
		t.Errorf("a token flag without users: %v", err)
	}
	if _, err := telegramInputChannels([]string{"x"}, "1:flag", reg); err == nil {
		t.Error("an invalid username must fail")
	}
	// The environment supplies the list when the flag does not.
	t.Setenv(TelegramAllowEnv, "@gus_test, ana_test")
	if got, err := telegramInputChannels(nil, "1:flag", reg); err != nil || len(got) != 1 || got[0].Ref != "ana_test,gus_test" {
		t.Errorf("env list: %v %v", got, err)
	}
	// A stored token alone (no flag, no users) is fine: nothing is bound.
	t.Setenv(TelegramAllowEnv, "")
	if got, err := telegramInputChannels(nil, "", reg); err != nil || got != nil {
		t.Errorf("registered but not asked: %v %v", got, err)
	}
}

// Kong: --telegram-allow repeats AND splits on commas (a username has none);
// --telegram-token is on the TUI and on serve; `telegram` keeps only `token`.
func TestTelegramFlagsParse(t *testing.T) {
	p, err := newParser()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseArgs(p, []string{"--telegram-allow", "@gus_test", "--telegram-allow", "ana_test,bob_test", "--telegram-token", "1:x"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(CLI.TUI.TelegramAllow, "|"); got != "@gus_test|ana_test|bob_test" {
		t.Errorf("TelegramAllow = %q", got)
	}
	if CLI.TUI.TelegramToken != "1:x" {
		t.Errorf("TUI token = %q", CLI.TUI.TelegramToken)
	}
	p, _ = newParser()
	if _, err := parseArgs(p, []string{"serve", ":0", "--telegram-token", "1:s"}); err != nil {
		t.Fatal(err)
	}
	if CLI.Serve.TelegramToken != "1:s" {
		t.Errorf("serve token = %q", CLI.Serve.TelegramToken)
	}
	for _, args := range [][]string{{"telegram", "pair", "1"}, {"telegram", "list"}, {"telegram", "--allow-unpair"}, {"serve", ":0", "--telegram-allow", "x"}} {
		p, _ := newParser()
		if _, err := p.Parse(args); err == nil {
			t.Errorf("harness %v must be an error now", args)
		}
	}
}

// serve registers the channel with its token; the agent it builds has it.
func TestServeRegistersTheTelegramChannel(t *testing.T) {
	isolate(t)
	t.Setenv(telegramch.TokenEnv, "")
	parseFor(t, "serve", ":0", "--telegram-token", "1:s")
	a, err := CLI.Serve.newAgent()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	found := false
	for _, ch := range a.Options().Channels {
		found = found || ch.Type() == "telegram"
	}
	if !found {
		t.Error("serve's agent has no Telegram channel")
	}
}
