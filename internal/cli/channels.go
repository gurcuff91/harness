package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/gurcuff91/harness/agent"
	colleaguech "github.com/gurcuff91/harness/channels/colleague"
	slackch "github.com/gurcuff91/harness/channels/slack"
	telegramch "github.com/gurcuff91/harness/channels/telegram"
	"github.com/gurcuff91/harness/internal/logx"
	"github.com/gurcuff91/harness/types"
)

// errNoSlackLogin is what asking for a Slack channel says when there is no
// stored Slack login to listen with.
const errNoSlackLogin = "slack channel not available: run harness slack login"

// storedChannels are the input-channel implementations the TUI and `serve`
// register: Slack when a login is stored (`harness slack login`). Registering
// opens nothing — a channel only listens for the sessions bound to it.
// loadSlack is a parameter so a test never reads the real credentials.
func storedChannels(loadSlack func() (*slackch.Credentials, error), withLogger bool) []agent.Channel {
	creds, err := loadSlack()
	if err != nil || creds == nil || creds.Workspace == "" || creds.XoxC == "" {
		return nil
	}
	var opts []slackch.Option
	if withLogger {
		opts = append(opts, slackch.WithLogger(logx.NewHarnessLogger()))
	}
	return []agent.Channel{slackch.New(creds.Workspace, creds.XoxC, creds.XoxD, opts...)}
}

// slackInputChannels turns the --slack-channel values into channel requests,
// failing when there is no Slack login to listen with.
func slackInputChannels(refs []string, registered []agent.Channel) ([]types.InputChannel, error) {
	var out []types.InputChannel
	for _, r := range refs {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, types.InputChannel{Type: slackch.Type, Ref: r})
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	for _, ch := range registered {
		if ch.Type() == slackch.Type {
			return out, nil
		}
	}
	return nil, fmt.Errorf("%s", errNoSlackLogin)
}

// TelegramAllowEnv supplies --telegram-allow from the environment
// (comma-separated): a flag only turns the channel on, like every switch.
const TelegramAllowEnv = "TELEGRAM_ALLOW"

// telegramChannels is the Telegram channel when a bot token exists: the flag,
// else TELEGRAM_BOT_TOKEN, else the saved one (`harness telegram token`).
// Registering opens nothing; a session binds the bot to listen.
func telegramChannels(flagToken string, withLogger bool) []agent.Channel {
	token := telegramch.ResolveToken(flagToken)
	if token == "" {
		return nil
	}
	var opts []telegramch.Option
	if withLogger {
		opts = append(opts, telegramch.WithLogger(logx.NewHarnessLogger()))
	}
	return []agent.Channel{telegramch.New(token, opts...)}
}

// telegramInputChannels turns --telegram-allow (and TELEGRAM_ALLOW) into the
// Telegram channel request of the startup session, whose ref is the trusted
// usernames. The rules, checked before anything opens: users without any bot
// token, or an explicit --telegram-token without users, are errors.
func telegramInputChannels(allow []string, flagToken string, registered []agent.Channel) ([]types.InputChannel, error) {
	var users []string
	for _, a := range allow {
		users = append(users, a)
	}
	if len(users) == 0 {
		if env := strings.TrimSpace(os.Getenv(TelegramAllowEnv)); env != "" {
			users = append(users, env)
		}
	}
	if len(users) == 0 {
		if strings.TrimSpace(flagToken) != "" {
			return nil, errors.New("--telegram-token needs --telegram-allow (the trusted @usernames the bot listens to)")
		}
		return nil, nil
	}
	list, err := telegramch.ParseAllowed(strings.Join(users, ","))
	if err != nil {
		return nil, fmt.Errorf("--telegram-allow: %w", err)
	}
	for _, ch := range registered {
		if ch.Type() == telegramch.Type {
			return []types.InputChannel{{Type: telegramch.Type, Ref: strings.Join(list, ",")}}, nil
		}
	}
	return nil, fmt.Errorf("--telegram-allow: no Telegram bot token: pass --telegram-token, set %s or run harness telegram token", telegramch.TokenEnv)
}

// colleagueChannels is the colleague channel when --with-colleagues (or
// HARNESS_WITH_COLLEAGUES) asked for it: every session the command opens gets a
// colleague id of its own, so the agents on this machine can talk.
func colleagueChannels(x extras, withLogger bool) []agent.Channel {
	if !x.colleagues {
		return nil
	}
	var opts []colleaguech.Option
	if withLogger {
		opts = append(opts, colleaguech.WithLogger(logx.NewHarnessLogger()))
	}
	return []agent.Channel{colleaguech.New(opts...)}
}
