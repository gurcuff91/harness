package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	atools "github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/types"
)

// slackTools are the tools a session bound to a Slack conversation gets, all
// bound to the channel's own bot. SlackAsk is deliberately absent (see the
// design doc).
//
// answering reports whether conversation id is the one the session's running
// turn came from (its answer is published there automatically): SlackPost
// refuses it, so a reply is never posted twice.
func slackTools(bot *Bot, myID string, answering func(id string) bool) []atools.Tool {
	return []atools.Tool{
		slackListChannelsTool(bot),
		slackListUsersTool(bot),
		slackPostTool(bot, myID, answering),
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
	return resolveChannelName(ctx, bot, strings.TrimPrefix(channel, "#"))
}

// resolveChannelName finds the channel called name (without "#").
func resolveChannelName(ctx context.Context, bot *Bot, name string) (string, error) {
	channels, err := bot.ListChannels(ctx, 1000)
	if err != nil {
		return "", fmt.Errorf("resolve channel #%s: %w", name, err)
	}
	for _, c := range channels {
		if c.Name == name {
			return c.ID, nil
		}
	}
	return "", fmt.Errorf("channel #%s not found — use SlackListChannels to discover channels", name)
}

// ── SlackPost ─────────────────────────────────────────────────────────────

func slackPostTool(bot *Bot, myID string, answering func(id string) bool) atools.Tool {
	return atools.Tool{
		Def: types.ToolDef{
			Name: "SlackPost",
			Description: "Post a message to ANOTHER Slack channel or user — not to the conversation the message you are answering came from: your reply is posted there automatically. " +
				"channel accepts a channel ID (C...), channel name (#general), or user ID (U...) for a direct message. " +
				"To attach files, put a <slack-upload path=\"/abs/path\"/> tag on its own line in text — " +
				"it is stripped from the visible message and the file is uploaded.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"channel":{"type":"string","description":"Channel ID (C...), channel name (#general), or user ID (U...) for DM"},"text":{"type":"string","description":"Message text. Put <slack-upload path=\"/abs/path\"/> tags on their own lines to attach files."}},"required":["channel","text"]}`),
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

			// The conversation this turn answers gets the turn's text by itself:
			// posting there too would say everything twice.
			if answering != nil && answering(channelID) {
				return "", fmt.Errorf("not posted: the message you are answering came from %s, and your reply is posted there automatically — just write your answer instead of calling SlackPost", channelID)
			}

			// Extract <slack-upload/> tags — the same mechanism as replies.
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

// maxHistoryImages caps the images SlackMessages hands to the model as images
// in one call (every downloaded file still gets its path).
const maxHistoryImages = 8

// historyFile is an attachment as SlackMessages reports it: never the private
// URL (the model cannot open it without the session — it only invites a
// failing curl), but the downloaded path when files were asked for.
type historyFile struct {
	Name    string `json:"name"`
	Size    int64  `json:"size,omitempty"`
	Path    string `json:"path,omitempty"`
	Image   bool   `json:"image,omitempty"`
	Skipped string `json:"skipped,omitempty"`
}

type historyMessage struct {
	User    string        `json:"user,omitempty"`
	Text    string        `json:"text"`
	TS      string        `json:"ts"`
	Subtype string        `json:"subtype,omitempty"`
	BotID   string        `json:"bot_id,omitempty"`
	Files   []historyFile `json:"files,omitempty"`
}

func slackMessagesTool(bot *Bot) atools.Tool {
	return atools.Tool{
		Def: types.ToolDef{
			Name: "SlackMessages",
			Description: "Read recent messages posted in a Slack channel, as JSON — useful to catch up on what the group has been discussing beyond messages sent directly to you. " +
				"Each entry has the sender's user ID (resolve names with SlackListUsers), text, timestamp, subtype (empty for a normal message; otherwise an event like channel_join/channel_leave/channel_topic), and its attached files by name and size. " +
				"Pass files: true to also download those files: each gets a local path (read it with the tool its format needs), and images are shown to you as well (up to 8 per call). " +
				"Files are private to the Slack session: a file URL cannot be fetched any other way.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"channel":{"type":"string","description":"Channel ID (C...), DM ID (D...) or channel name (#general)"},"limit":{"type":"integer","description":"Maximum messages to return (default 50, max 200)","minimum":1,"maximum":200},"files":{"type":"boolean","description":"Download the messages' attachments (default false): each gets a local path, images are also shown to you"}},"required":["channel"]}`),
		},
		ExecuteRich: func(ctx context.Context, input json.RawMessage) (string, []types.ImageData, error) {
			var args struct {
				Channel string `json:"channel"`
				Limit   int    `json:"limit"`
				Files   bool   `json:"files"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return "", nil, fmt.Errorf("invalid arguments: %w", err)
			}
			if args.Channel == "" {
				return "", nil, fmt.Errorf("channel is required")
			}
			channelID, err := resolveChannelID(ctx, bot, args.Channel)
			if err != nil {
				return "", nil, err
			}
			messages, err := bot.GetChannelHistory(ctx, channelID, args.Limit)
			if err != nil {
				return "", nil, err
			}
			if len(messages) == 0 {
				return "No messages found in this channel.", nil, nil
			}
			var images []types.ImageData
			out := make([]historyMessage, 0, len(messages))
			for _, m := range messages {
				hm := historyMessage{User: m.User, Text: m.Text, TS: m.TS, Subtype: m.Subtype, BotID: m.BotID}
				for _, f := range m.Files {
					hf := historyFile{Name: f.Name, Size: f.Size}
					if hf.Name == "" {
						hf.Name = f.ID
					}
					if args.Files {
						imgs, atts := fetchFiles(ctx, bot, []SlackFile{f})
						if len(atts) > 0 {
							hf.Path, hf.Skipped, hf.Image = atts[0].Path, atts[0].Skipped, atts[0].Image
						}
						if len(imgs) > 0 && len(images) < maxHistoryImages {
							images = append(images, imgs...)
						}
					}
					hm.Files = append(hm.Files, hf)
				}
				out = append(out, hm)
			}
			b, err := json.MarshalIndent(out, "", "  ")
			if err != nil {
				return "", nil, fmt.Errorf("format messages: %w", err)
			}
			text := string(b)
			if len(images) > 0 {
				text += fmt.Sprintf("\n\n%d image(s) attached, in message order.", len(images))
			}
			return text, images, nil
		},
	}
}
