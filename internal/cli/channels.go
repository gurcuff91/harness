package cli

import (
	"fmt"
	"strings"

	"github.com/gurcuff91/harness/agent"
	slackch "github.com/gurcuff91/harness/channels/slack"
	"github.com/gurcuff91/harness/internal/logx"
	"github.com/gurcuff91/harness/transports/slack"
	"github.com/gurcuff91/harness/types"
)

// errNoSlackLogin is what asking for a Slack channel says when there is no
// stored Slack login to listen with.
const errNoSlackLogin = "slack channel not available: run harness slack login"

// storedChannels are the input-channel implementations the TUI and `serve`
// register: Slack when a login is stored (`harness slack login`). Registering
// opens nothing — a channel only listens for the sessions bound to it.
// loadSlack is a parameter so a test never reads the real credentials.
func storedChannels(loadSlack func() (*slack.Credentials, error), withLogger bool) []agent.Channel {
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
