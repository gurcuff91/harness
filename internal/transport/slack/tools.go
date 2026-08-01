package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	atools "github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/types"
)

// SlackTools returns the Slack-specific tools to inject into the agent when
// running the Slack transport. They are passed via AgentOptions.Tools so they
// appear alongside the built-in tools without touching the tool registry.
func SlackTools(bot *Bot, myID string) []atools.Tool {
	return []atools.Tool{
		slackListChannelsTool(bot),
		slackListUsersTool(bot),
		slackPostTool(bot, myID),
		slackMessagesTool(bot),
	}
}

// ── SlackListChannels ─────────────────────────────────────────────────────

func slackListChannelsTool(bot *Bot) atools.Tool {
	return atools.Tool{
		Def: types.ToolDef{
			Name:        "SlackListChannels",
			Description: "List all Slack channels (public and private) the current user can see, as JSON. Use this to resolve a channel name (e.g. #general) to its channel ID before posting or mentioning.",
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
			b, err := json.MarshalIndent(channels, "", "  ")
			if err != nil {
				return "", fmt.Errorf("format channels: %w", err)
			}
			return string(b), nil
		},
	}
}

// ── SlackListUsers ────────────────────────────────────────────────────────

func slackListUsersTool(bot *Bot) atools.Tool {
	return atools.Tool{
		Def: types.ToolDef{
			Name:        "SlackListUsers",
			Description: "List all active (non-bot, non-deleted) users in the Slack workspace, as JSON. Use this to resolve a person's name to their user ID for @mentions or sending them a direct message. Each entry has the user ID, @handle (name), and profile (real_name, display_name) — prefer display_name, falling back to real_name, then the handle, when showing a person's name.",
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
			b, err := json.MarshalIndent(users, "", "  ")
			if err != nil {
				return "", fmt.Errorf("format users: %w", err)
			}
			return string(b), nil
		},
	}
}

// resolveChannelID resolves a "#name" to its channel ID via SlackListChannels'
// data source; any other input (already a channel ID like C..., or a user ID
// like U...) passes through unchanged. Shared by SlackPost and SlackMessages
// so both accept the same "#general" convenience the user/model would expect.
func resolveChannelID(ctx context.Context, bot *Bot, channel string) (string, error) {
	if !strings.HasPrefix(channel, "#") {
		return channel, nil
	}
	name := strings.TrimPrefix(channel, "#")
	channels, err := bot.ListChannels(ctx, 1000)
	if err != nil {
		return "", fmt.Errorf("resolve channel %q: %w", channel, err)
	}
	for _, c := range channels {
		if c.Name == name {
			return c.ID, nil
		}
	}
	return "", fmt.Errorf("channel #%s not found — use SlackListChannels to discover channels", name)
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
			channelID, err := resolveChannelID(ctx, bot, args.Channel)
			if err != nil {
				return "", err
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

// ── SlackMessages ─────────────────────────────────────────────────────────

func slackMessagesTool(bot *Bot) atools.Tool {
	return atools.Tool{
		Def: types.ToolDef{
			Name:        "SlackMessages",
			Description: "Read recent messages posted in a Slack channel, as JSON — useful to catch up on what the group has been discussing beyond messages sent directly to you. Only meaningful for channels (multiple participants), not DMs (a DM is already the 1:1 conversation you're part of). Each entry has the sender's user ID (resolve names with SlackListUsers), text, timestamp, subtype (empty for a normal message; otherwise an event like channel_join/channel_leave/channel_topic), and any attached files with their URLs.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"channel":{"type":"string","description":"Channel ID (C...) or channel name (#general)"},"limit":{"type":"integer","description":"Maximum messages to return (default 50, max 200)","minimum":1,"maximum":200}},"required":["channel"]}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var args struct {
				Channel string `json:"channel"`
				Limit   int    `json:"limit"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if args.Channel == "" {
				return "", fmt.Errorf("channel is required")
			}

			channelID, err := resolveChannelID(ctx, bot, args.Channel)
			if err != nil {
				return "", err
			}

			messages, err := bot.GetChannelHistory(ctx, channelID, args.Limit)
			if err != nil {
				return "", err
			}
			if len(messages) == 0 {
				return "No messages found in this channel.", nil
			}
			b, err := json.MarshalIndent(messages, "", "  ")
			if err != nil {
				return "", fmt.Errorf("format messages: %w", err)
			}
			return string(b), nil
		},
	}
}
