// Run() for `harness telegram token`: save, check or remove the bot token the
// Telegram input channel uses (the bot itself is a channel since v0.116.0, see
// channels/telegram).
package cli

import (
	"errors"
	"fmt"

	telegramch "github.com/gurcuff91/harness/channels/telegram"
)

func (c *telegramTokenCmd) Run() error {
	switch {
	case c.Delete:
		if err := telegramch.DeleteToken(); err != nil {
			return err
		}
		fmt.Println("Telegram bot token removed.")
		return nil
	case c.Status:
		return telegramTokenStatus()
	case c.Token == "":
		return errors.New("a token is required: pass one, or use --status / --delete")
	}
	ctx, cancel := signalContext()
	defer cancel()
	// Verified BEFORE saving: a token that does not work is never stored.
	me, err := telegramch.VerifyToken(ctx, c.Token)
	if err != nil {
		return fmt.Errorf("%w (not saved)", err)
	}
	if err := telegramch.SaveToken(c.Token); err != nil {
		return fmt.Errorf("save token: %w", err)
	}
	fmt.Printf("✓ Token verified and saved: bot @%s\n", me.Username)
	fmt.Println("  Listen to it from a session: harness --telegram-allow @your_username")
	return nil
}

// telegramTokenStatus verifies the saved token against the Bot API instead of
// only reporting that one exists.
func telegramTokenStatus() error {
	token := telegramch.LoadToken()
	if token == "" {
		fmt.Println("No token saved. Run 'harness telegram token <token>' to save one.")
		return nil
	}
	ctx, cancel := signalContext()
	defer cancel()
	me, err := telegramch.VerifyToken(ctx, token)
	if err != nil {
		fmt.Printf("✗ Saved token: %s\n", err)
		fmt.Println("  Run 'harness telegram token <token>' to save a new one.")
		return nil
	}
	fmt.Printf("✓ Token valid: bot @%s\n", me.Username)
	return nil
}
