package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	atools "github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/types"
)

// SlackTools returns the Slack-specific tools to inject into the agent when
// running the Slack transport. They are passed via AgentOptions.Tools so they
// appear alongside the built-in tools without touching the tool registry.
// t is needed by SlackAsk to register/wait on pending asks (see asks.go);
// the other tools only need bot/myID.
func SlackTools(bot *Bot, myID string, t *Transport) []atools.Tool {
	return []atools.Tool{
		slackListChannelsTool(bot),
		slackListUsersTool(bot),
		slackPostTool(bot, myID),
		slackMessagesTool(bot),
		slackAskTool(bot, myID, t),
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

// slackAskDefaultTimeout is the default wait when the timeout field isn't
// specified — same value ColleagueAsk/Subagent default to.
const slackAskDefaultTimeout = 120 * time.Second

// ── SlackAsk ──────────────────────────────────────────────────────────────

// slackAskTool asks a specific person a question via direct message and
// BLOCKS until they reply or the timeout expires. Only works for DMs — see
// the channel-prefix check below for why group channels are rejected
// outright. Uses ExecuteRich (not Execute) so a reply carrying an image
// reaches the model as an actual image, not just a filename.
func slackAskTool(bot *Bot, myID string, t *Transport) atools.Tool {
	return atools.Tool{
		Def: types.ToolDef{
			Name: "SlackAsk",
			Description: "Ask a specific person a question via direct message and BLOCK until they reply (default timeout: 120s, override with `timeout`). " +
				"If they don't respond in time, that's a normal outcome, not necessarily an error — you can try again later. " +
				"Only works for DIRECT MESSAGES — a user ID (U...) or an existing DM channel (D...); a channel name (#general) or channel ID (C...) is REJECTED, since \"the\" reply is ambiguous once more than one person can answer — use SlackPost for those (fire-and-forget, no waiting). " +
				"Opens the DM automatically if one doesn't exist yet. Only one SlackAsk can be pending per DM at a time. " +
				"To attach files to your question, embed <slack:uploadFile>/path/to/file</slack:uploadFile> tags anywhere in text — they are stripped from the visible message and uploaded automatically, same as SlackPost. " +
				"The reply's text is returned, along with any image the person attached (visible directly) and any text file (as a `<slack:attach>` path your Read tool can open).",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"channel":{"type":"string","description":"User ID (U...) or an existing DM channel ID (D...). A channel name (#general) or channel ID (C...) is accepted as input but always rejected — SlackAsk cannot target a group channel"},"text":{"type":"string","description":"The question to ask. Embed <slack:uploadFile>/path</slack:uploadFile> tags to attach files."},"timeout":{"type":"integer","description":"Seconds to wait for a reply (default: 120)"}},"required":["channel","text"]}`),
		},
		ExecuteRich: func(ctx context.Context, input json.RawMessage) (string, []types.ImageData, error) {
			var args struct {
				Channel string `json:"channel"`
				Text    string `json:"text"`
				Timeout int    `json:"timeout"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return "", nil, fmt.Errorf("invalid arguments: %w", err)
			}
			if args.Channel == "" {
				return "", nil, fmt.Errorf("channel is required")
			}
			if strings.TrimSpace(args.Text) == "" {
				return "", nil, fmt.Errorf("text is required")
			}

			channelID, err := resolveChannelID(ctx, bot, args.Channel)
			if err != nil {
				return "", nil, err
			}
			if strings.HasPrefix(channelID, "U") && channelID != myID {
				dmID, err := bot.OpenDM(ctx, channelID)
				if err != nil {
					return "", nil, fmt.Errorf("open DM with %s: %w", channelID, err)
				}
				channelID = dmID
			}
			if !strings.HasPrefix(channelID, "D") {
				return "", nil, fmt.Errorf("SlackAsk only works for direct messages (a user ID or existing DM) — use SlackPost for channels")
			}

			replyCh, err := t.registerAsk(channelID)
			if err != nil {
				return "", nil, err
			}
			// Guarantees the pending entry is cleaned up on EVERY exit path
			// (a real reply, the timeout firing, or ctx being cancelled) —
			// without this, a later SlackAsk to the same DM would find one
			// already "pending" forever.
			defer t.unregisterAsk(channelID)

			// Extract <slack:uploadFile> tags from text — identical mechanism
			// to SlackPost, so a question can carry an attachment the same
			// way a fire-and-forget post can.
			filePaths, cleanText := extractUploads(args.Text)
			if len(filePaths) > 0 {
				for i, path := range filePaths {
					comment := ""
					if i == 0 {
						comment = toMrkdwn(cleanText)
					}
					if err := bot.UploadFile(ctx, channelID, path, comment); err != nil {
						return "", nil, fmt.Errorf("upload %s: %w", path, err)
					}
				}
			} else if err := bot.PostMessage(ctx, channelID, toMrkdwn(cleanText)); err != nil {
				return "", nil, fmt.Errorf("post question: %w", err)
			}

			timeout := slackAskDefaultTimeout
			if args.Timeout > 0 {
				timeout = time.Duration(args.Timeout) * time.Second
			}
			timer := time.NewTimer(timeout)
			defer timer.Stop()

			select {
			case reply := <-replyCh:
				text := reply.text
				if len(reply.attachTags) > 0 {
					if text != "" {
						text += "\n\n"
					}
					text += strings.Join(reply.attachTags, "\n")
				}
				if text == "" {
					text = "[no text — see attached]"
				}
				return text, reply.images, nil
			case <-timer.C:
				return fmt.Sprintf("No reply within %s — the person may not have seen it, or chose not to respond. Not necessarily an error; you can ask again later.", timeout), nil, nil
			case <-ctx.Done():
				return "", nil, ctx.Err()
			}
		},
	}
}
