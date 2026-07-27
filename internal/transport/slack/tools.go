package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	atools "github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/types"
)

// SlackTools returns the three Slack-specific tools to inject into the agent
// when running the Slack transport. They are passed via AgentOptions.Tools so
// they appear alongside the built-in tools without touching the tool registry.
func SlackTools(bot *Bot, myID string) []atools.Tool {
	return []atools.Tool{
		slackListChannelsTool(bot),
		slackListUsersTool(bot),
		slackPostTool(bot, myID),
	}
}

// ── SlackListChannels ─────────────────────────────────────────────────────

func slackListChannelsTool(bot *Bot) atools.Tool {
	return atools.Tool{
		Def: types.ToolDef{
			Name:        "SlackListChannels",
			Description: "List all Slack channels (public and private) the current user can see. Use this to resolve a channel name (e.g. #general) to its channel ID before posting or mentioning.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer","description":"Maximum channels to return (default 200, max 1000)","minimum":1,"maximum":1000}}}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var args struct {
				Limit int `json:"limit"`
			}
			_ = json.Unmarshal(input, &args)
			if args.Limit <= 0 {
				args.Limit = 200
			}
			channels, err := bot.ListChannels(ctx, args.Limit)
			if err != nil {
				return "", err
			}
			if len(channels) == 0 {
				return "No channels found.", nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%d channel(s):\n\n", len(channels))
			for _, c := range channels {
				private := ""
				if c.IsPrivate {
					private = " (private)"
				}
				fmt.Fprintf(&b, "- #%s  ID: %s%s  members: %d\n",
					c.Name, c.ID, private, c.NumMembers)
			}
			return b.String(), nil
		},
	}
}

// ── SlackListUsers ────────────────────────────────────────────────────────

func slackListUsersTool(bot *Bot) atools.Tool {
	return atools.Tool{
		Def: types.ToolDef{
			Name:        "SlackListUsers",
			Description: "List all active (non-bot, non-deleted) users in the Slack workspace. Use this to resolve a person's name to their user ID for @mentions or sending them a direct message.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer","description":"Maximum users to return (default 200, max 1000)","minimum":1,"maximum":1000}}}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var args struct {
				Limit int `json:"limit"`
			}
			_ = json.Unmarshal(input, &args)
			if args.Limit <= 0 {
				args.Limit = 200
			}
			users, err := bot.ListUsers(ctx, args.Limit)
			if err != nil {
				return "", err
			}
			if len(users) == 0 {
				return "No users found.", nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%d user(s):\n\n", len(users))
			for _, u := range users {
				display := u.Profile.DisplayName
				if display == "" {
					display = u.Profile.RealName
				}
				if display == "" {
					display = u.Name
				}
				fmt.Fprintf(&b, "- %s  ID: %s  handle: @%s\n",
					display, u.ID, u.Name)
			}
			return b.String(), nil
		},
	}
}

// ── SlackPost ─────────────────────────────────────────────────────────────

func slackPostTool(bot *Bot, myID string) atools.Tool {
	return atools.Tool{
		Def: types.ToolDef{
			Name: "SlackPost",
			Description: "Post a message to a Slack channel or user. " +
				"channel accepts a channel ID (C...), channel name (#general), or user ID (U...) for a direct message. " +
				"To attach files, embed <slack:uploadFile>/path/to/file</slack:uploadFile> tags anywhere in text — " +
				"they are stripped from the visible message and uploaded automatically.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"channel":{"type":"string","description":"Channel ID (C...), channel name (#general), or user ID (U...) for DM"},"text":{"type":"string","description":"Message text. Embed <slack:uploadFile>/path</slack:uploadFile> tags to attach files."}},"required":["channel","text"]}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var args struct {
				Channel string `json:"channel"`
				Text    string `json:"text"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if args.Channel == "" {
				return "", fmt.Errorf("channel is required")
			}

			// Resolve channel name (#name) → channel ID.
			channelID := args.Channel
			if strings.HasPrefix(args.Channel, "#") {
				name := strings.TrimPrefix(args.Channel, "#")
				channels, err := bot.ListChannels(ctx, 1000)
				if err != nil {
					return "", fmt.Errorf("resolve channel %q: %w", args.Channel, err)
				}
				found := false
				for _, c := range channels {
					if c.Name == name {
						channelID = c.ID
						found = true
						break
					}
				}
				if !found {
					return "", fmt.Errorf("channel #%s not found — use SlackListChannels to discover channels", name)
				}
			}

			// User ID (U...) → open DM channel first.
			if strings.HasPrefix(channelID, "U") && channelID != myID {
				dmID, err := bot.OpenDM(ctx, channelID)
				if err != nil {
					return "", fmt.Errorf("open DM with %s: %w", channelID, err)
				}
				channelID = dmID
			}

			// Extract <slack:uploadFile> tags from text — same mechanism as the
			// transport's flush path. This is the single way to attach files,
			// consistent with how the agent attaches files in regular replies.
			filePaths, cleanText := extractUploads(args.Text)

			if len(filePaths) > 0 {
				for i, path := range filePaths {
					comment := ""
					if i == 0 {
						comment = toMrkdwn(cleanText)
					}
					if err := bot.UploadFile(ctx, channelID, path, comment); err != nil {
						return "", fmt.Errorf("upload %s: %w", path, err)
					}
				}
				noun := "file"
				if len(filePaths) > 1 {
					noun = "files"
				}
				return fmt.Sprintf("Posted message with %d %s to %s.", len(filePaths), noun, args.Channel), nil
			}

			// Text-only post.
			if err := bot.PostMessage(ctx, channelID, toMrkdwn(cleanText)); err != nil {
				return "", fmt.Errorf("post message: %w", err)
			}
			return fmt.Sprintf("Message posted to %s.", args.Channel), nil
		},
	}
}
