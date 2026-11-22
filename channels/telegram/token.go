package telegram

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gurcuff91/harness/internal/config"
)

// TokenEnv is the environment variable that supplies a bot token.
const TokenEnv = "TELEGRAM_BOT_TOKEN"

// SaveToken stores the bot token in the credentials store (what
// `harness telegram token <tok>` does).
func SaveToken(token string) error {
	return config.GetCredentialsManager().SetTelegramToken(strings.TrimSpace(token))
}

// LoadToken returns the stored bot token ("" when none).
func LoadToken() string { return config.GetCredentialsManager().TelegramToken() }

// DeleteToken removes the stored bot token (no error when none).
func DeleteToken() error { return config.GetCredentialsManager().DeleteTelegramToken() }

// ResolveToken returns the token a process uses: the explicit one (a flag),
// else TELEGRAM_BOT_TOKEN, else the stored one; "" when there is none.
func ResolveToken(explicit string) string {
	if t := strings.TrimSpace(explicit); t != "" {
		return t
	}
	if t := strings.TrimSpace(os.Getenv(TokenEnv)); t != "" {
		return t
	}
	return LoadToken()
}

// VerifyToken checks a token against the Bot API and returns the bot's account.
func VerifyToken(ctx context.Context, token string) (*User, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	u, err := NewBot(strings.TrimSpace(token)).GetMe(ctx)
	if err != nil {
		return nil, fmt.Errorf("telegram bot token rejected or unreachable: %w", err)
	}
	return u, nil
}
