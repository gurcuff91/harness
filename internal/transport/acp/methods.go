package acp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/types"
)

// acpSession tracks one live ACP session's mapping onto the underlying
// Harness session. The ACP session ID and the Harness session ID are the
// same value today (registerSession uses the Harness ID directly as the ACP
// ID) — this exists as its own type since a genuinely distinct client-facing
// session ID is a natural place this might grow later.
type acpSession struct {
	harnessID string // the underlying Harness session ID (client.Session.ID)
}

// handler owns the live state for one ACP connection: the shared Harness
// client (talking to the in-process server every other transport also
// uses), and the set of concurrently open ACP sessions this connection has
// created or loaded — Zed can run several "trains of thought" against one
// agent process at once.
type handler struct {
	api *client.Client
	cwd string // process's own cwd — sessions get their OWN cwd from the request, this is just a fallback

	mu       sync.Mutex
	sessions map[string]*acpSession // ACP sessionId → its tracking state
}

// handleInitialize answers the ACP handshake — always advertising
// protocolVersion 1 and the fixed capability set this transport implements
// (see the design doc: trusted execution, no fs/terminal delegation, no
// auth). agentInfo lets Zed display which agent/version is running.
func (h *handler) handleInitialize(_ initializeParams) (initializeResult, *rpcError) {
	return initializeResult{
		ProtocolVersion: protocolVersion,
		AgentCapabilities: agentCapabilities{
			LoadSession: true,
			PromptCapabilities: promptCapabilities{
				Image:           true,
				EmbeddedContext: true,
			},
			SessionCapabilities: sessionCapabilities{
				Resume: nil, // session/resume not implemented — session/load covers our use case
			},
		},
		AgentInfo: &implementation{Name: "harness"},
	}, nil
}

// handleAuthenticate is never meaningfully invoked — this transport
// advertises no authMethods (Harness manages provider credentials out of
// band, outside of any ACP session), but responds cleanly if a client calls
// it anyway rather than erroring.
func (h *handler) handleAuthenticate(_ authenticateParams) (map[string]any, *rpcError) {
	return map[string]any{}, nil
}

// handleNewSession creates a fresh Harness session for the given cwd, using
// the server's currently configured default model (mcpServers from the
// request is deliberately ignored — see the design doc).
func (h *handler) handleNewSession(p newSessionParams) (newSessionResult, *rpcError) {
	model, err := resolveDefaultModel(h.api)
	if err != nil {
		return newSessionResult{}, internalErr("resolve default model", err)
	}
	sess, err := h.api.CreateSession(model, p.CWD, acpSessionName())
	if err != nil {
		return newSessionResult{}, internalErr("create session", err)
	}
	return h.registerSession(sess.ID)
}

// handleLoadSession resumes an existing Harness session and replays its full
// message history as session/update notifications BEFORE returning — per
// spec, all replay notifications must precede the response.
func (h *handler) handleLoadSession(c *conn, p loadSessionParams) (newSessionResult, *rpcError) {
	if _, err := h.api.ResumeSession(p.SessionID); err != nil {
		return newSessionResult{}, internalErr("resume session", err)
	}
	messages, err := h.api.GetMessages(p.SessionID)
	if err != nil {
		return newSessionResult{}, internalErr("get messages", err)
	}
	replayHistory(c, p.SessionID, messages)
	return h.registerSession(p.SessionID)
}

// registerSession builds the acpSession tracking entry and the initial
// configOptions payload shared by both session/new and session/load.
func (h *handler) registerSession(harnessID string) (newSessionResult, *rpcError) {
	h.mu.Lock()
	h.sessions[harnessID] = &acpSession{harnessID: harnessID}
	h.mu.Unlock()

	opts, err := buildConfigOptions(h.api)
	if err != nil {
		return newSessionResult{}, internalErr("build config options", err)
	}
	return newSessionResult{SessionID: harnessID, ConfigOptions: opts}, nil
}

// handlePrompt converts the ACP content blocks into text/images, submits the
// prompt, opens the SSE stream BEFORE sending it (so no events are missed —
// same ordering as internal/cli/cli.go), pumps events until the turn ends,
// and returns the resolved stopReason. This call blocks for the whole turn —
// the caller runs it in its own goroutine per session/prompt request so
// concurrent sessions' turns don't block each other.
func (h *handler) handlePrompt(ctx context.Context, c *conn, p promptParams) (promptResult, *rpcError) {
	h.mu.Lock()
	_, ok := h.sessions[p.SessionID]
	h.mu.Unlock()
	if !ok {
		return promptResult{}, &rpcError{Code: errCodeInvalidParams, Message: "unknown sessionId: " + p.SessionID}
	}

	text, images := flattenPrompt(p.Prompt)

	events, err := h.api.StreamEvents(ctx, p.SessionID)
	if err != nil {
		return promptResult{}, internalErr("stream events", err)
	}

	var sendErr error
	if len(images) > 0 {
		_, sendErr = h.api.SendPromptWithImages(p.SessionID, text, images)
	} else {
		_, sendErr = h.api.SendPrompt(p.SessionID, text)
	}
	if sendErr != nil {
		return promptResult{}, internalErr("send prompt", sendErr)
	}

	outcome := pumpEvents(c, p.SessionID, events)
	if outcome.err != nil {
		return promptResult{}, outcome.err
	}
	return promptResult{StopReason: outcome.stopReason}, nil
}

// handleCancel is a notification (no response expected) — it stops the
// underlying Harness session, whose in-flight turn will emit EventStop,
// which pumpEvents (running in the goroutine handling the matching
// session/prompt) translates into stopReason "cancelled".
func (h *handler) handleCancel(p cancelParams) {
	_, _ = h.api.StopSession(p.SessionID)
}

// flattenPrompt reduces ACP's ContentBlock[] prompt into the flat
// (text, images) shape the Harness client API takes: text blocks and
// embedded resource text are concatenated (resource content is already
// embedded by the client — see the design doc), image blocks are collected
// separately for SendPromptWithImages. resource_link (URI-only, no content)
// is intentionally not read — out of scope for the first cut.
func flattenPrompt(blocks []contentBlock) (text string, images []types.ImageData) {
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if text != "" {
				text += "\n\n"
			}
			text += b.Text
		case "resource":
			if b.Resource != nil && b.Resource.Text != "" {
				if text != "" {
					text += "\n\n"
				}
				text += b.Resource.Text
			}
		case "image":
			images = append(images, types.ImageData{Base64: b.Data, MimeType: b.MimeType})
		}
	}
	return text, images
}

func internalErr(action string, err error) *rpcError {
	return &rpcError{Code: errCodeInternalError, Message: fmt.Sprintf("acp: %s: %v", action, err)}
}

// resolveDefaultModel picks the model a new session should start with —
// the persisted active model if it's still available, else the first
// connected provider's first model. Mirrors the same fallback every other
// transport uses (internal/cli/cli.go's Run, internal/transport/telegram's
// resolveModel) since CreateSession requires a non-empty model and ACP's
// session/new carries none of its own.
func resolveDefaultModel(c *client.Client) (string, error) {
	models, err := c.ListModels()
	if err != nil {
		return "", fmt.Errorf("list models: %w", err)
	}
	if len(models) == 0 {
		return "", fmt.Errorf("no active providers — connect one first (harness connect ...)")
	}
	active := make(map[string]bool, len(models))
	for _, m := range models {
		active[m.Model] = true
	}
	if settings, err := c.GetSettings(); err == nil && active[settings.ActiveModel] {
		return settings.ActiveModel, nil
	}
	return models[0].Model, nil
}

// acpSessionName returns the default name for sessions created via the ACP
// transport, e.g. "Acp 2026-08-01 18:30" — mirrors telegramSessionName/
// slackSessionName so `harness sessions`/the TUI can tell at a glance which
// transport/protocol a session came from.
func acpSessionName() string {
	return "Acp " + time.Now().Format("2006-01-02 15:04")
}
