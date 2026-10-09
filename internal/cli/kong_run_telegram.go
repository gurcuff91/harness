// Run() methods for `harness telegram` and its nested subcommands
// (pair/unpair/list/token). Each Run() below is a thin adapter over the
// telegram.* package functions and telegram.Run.
package cli

import (
	"fmt"

	"github.com/gurcuff91/harness/internal/logx"
	"github.com/gurcuff91/harness/transports/telegram"
)

func (c *telegramRunCmd) Run() error {
	// c.Scheduler decides the AGENT's own scheduler engine here — it's an
	// agent.AgentOptions.EnableScheduler concern, not something
	// telegram.Options carries (see its doc comment for why).
	// No telegram.Directive here: the transport adds it to the sessions it
	// creates/resumes (per-session directives), so it never leaks into other
	// sessions of the same agent.
	a := newInteractiveAgent(c.Scheduler)
	ctx, cancel := signalContext()
	defer cancel()

	opts := []telegram.Option{
		telegram.WithToken(c.Token),
		telegram.WithLogger(logx.NewHarnessLogger()),
	}
	if c.Model != "" {
		opts = append(opts, telegram.WithSessionModel(c.Model))
	}
	if c.Thinking != "" {
		opts = append(opts, telegram.WithSessionThinking(c.Thinking))
	}
	if c.AllowUnpair {
		opts = append(opts, telegram.WithAllowUnpair())
	}
	return telegram.Run(ctx, a, opts...)
}

func (c *telegramPairCmd) Run() error {
	added, err := telegram.PairChat(c.ChatID)
	if err != nil {
		return err
	}
	if added {
		fmt.Printf("Paired chat %d.\n", c.ChatID)
	} else {
		fmt.Printf("Chat %d was already paired.\n", c.ChatID)
	}
	return nil
}

func (c *telegramUnpairCmd) Run() error {
	removed, err := telegram.UnpairChat(c.ChatID)
	if err != nil {
		return err
	}
	if removed {
		fmt.Printf("Unpaired chat %d.\n", c.ChatID)
	} else {
		fmt.Printf("Chat %d was not paired.\n", c.ChatID)
	}
	return nil
}

func (c *telegramListCmd) Run() error {
	ids, err := telegram.PairedChats()
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		fmt.Println("No paired chats. Run 'harness telegram pair <chat_id>'.")
		return nil
	}
	fmt.Println("Paired chats:")
	for _, id := range ids {
		fmt.Printf("  %d\n", id)
	}
	return nil
}

func (c *telegramTokenCmd) Run() error {
	if c.Status {
		return telegramTokenStatus()
	}
	if c.Token == "" {
		return fmt.Errorf("a token is required — pass one, or use --status to check the saved one")
	}

	// Verify the token against the real Bot API BEFORE saving it — never
	// persist a token that doesn't actually work. Without this, a typo'd or
	// already-revoked token would sit in the credentials store until the next
	// `harness telegram` launch fails, or until someone happens to run
	// `harness telegram token --status` to notice.
	ctx, cancel := signalContext()
	defer cancel()
	me, err := telegram.NewBot(c.Token).GetMe(ctx)
	if err != nil {
		return fmt.Errorf("token rejected by Telegram's API (%w) — not saved", err)
	}

	if err := telegram.SaveToken(c.Token); err != nil {
		return fmt.Errorf("save token: %w", err)
	}
	fmt.Printf("✓ Token verified — bot: @%s\n", me.Username)
	fmt.Println("Token saved. 'harness telegram' will use it automatically — no need to pass --token every time.")
	return nil
}

// telegramTokenStatus verifies the saved token against the real Bot API
// (GetMe) rather than just reporting its presence — the same
// "check it actually works" approach slackLoginStatus takes for Slack's
// saved credentials, catching a revoked/invalid token immediately instead
// of only at the next `harness telegram` launch.
func telegramTokenStatus() error {
	token, err := telegram.LoadToken()
	if err != nil {
		return fmt.Errorf("read saved token: %w", err)
	}
	if token == "" {
		fmt.Println("No token saved. Run 'harness telegram token <token>' to save one.")
		return nil
	}
	ctx, cancel := signalContext()
	defer cancel()
	bot := telegram.NewBot(token)
	me, err := bot.GetMe(ctx)
	if err != nil {
		fmt.Printf("✗ Saved token is invalid or unreachable: %s\n", err)
		fmt.Println("  Run 'harness telegram token <token>' to save a new one.")
		return nil
	}
	fmt.Printf("✓ Token valid — bot: @%s\n", me.Username)
	return nil
}
