package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/internal/version"
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
				Resume: &struct{}{},
				Close:  &struct{}{},
				Delete: &struct{}{},
				List:   &struct{}{},
			},
		},
		AgentInfo: &implementation{Name: "harness", Title: "Harness", Version: version.Version},
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
func (h *handler) handleNewSession(c *conn, p newSessionParams) (newSessionResult, *rpcError) {
	model, err := resolveDefaultModel(h.api)
	if err != nil {
		return newSessionResult{}, internalErr("resolve default model", err)
	}
	sess, err := h.api.CreateSession(model, p.CWD, acpSessionName())
	if err != nil {
		return newSessionResult{}, internalErr("create session", err)
	}
	return h.registerSession(c, sess.ID)
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
	return h.registerSession(c, p.SessionID)
}

// registerSession builds the acpSession tracking entry and the initial
// configOptions payload shared by session/new, session/load, and
// session/resume. It deliberately does NOT send available_commands_update
// itself — see notifyAvailableCommands's doc comment for why that
// notification must be sent by the CALLER, strictly after the session/new /
// session/load / session/resume response has already been written.
func (h *handler) registerSession(c *conn, harnessID string) (newSessionResult, *rpcError) {
	h.mu.Lock()
	h.sessions[harnessID] = &acpSession{harnessID: harnessID}
	h.mu.Unlock()

	opts, err := buildConfigOptions(h.api)
	if err != nil {
		return newSessionResult{}, internalErr("build config options", err)
	}
	return newSessionResult{SessionID: harnessID, ConfigOptions: opts}, nil
}

// notifyAvailableCommands sends the available_commands_update notification
// for a session. It MUST be called strictly after the response to whichever
// method created/loaded/resumed the session has already been written to
// stdout — never before, and never interleaved with building that response.
//
// Why this ordering is load-bearing (not just tidy): Zed reads JSON-RPC as a
// sequential line stream and only learns a sessionId once it has processed
// that method's RESPONSE. A session/update notification for that sessionId
// arriving on the wire before the response line does is silently dropped as
// "notification for unknown session" — this is a confirmed, reported Zed bug
// (zed-industries/zed#60199: "available_commands_update never shown because
// notification arrives before session/new response"). It used to bite this
// transport for exactly that reason: registerSession sent the notification
// from INSIDE the handler, before the handler had even returned its result
// to be written — so on the wire, the notification line preceded the
// response line, and Zed threw it away every time, unconditionally.
func (h *handler) notifyAvailableCommands(c *conn, sessionID string) {
	cmds, err := buildAvailableCommands(h.api, sessionID)
	if err != nil {
		// Not fatal — the session is still fully usable without slash-command
		// suggestions, so this only costs a UX nicety, not the session itself.
		return
	}
	notify(c, sessionID, sessionUpdate{
		SessionUpdate:     "available_commands_update",
		AvailableCommands: cmds,
	})
}

// handleSetConfigOption applies a value change from one of the config
// options advertised in buildConfigOptions ("model" or "thinking" — the only
// two IDs this transport ever hands out, so any other configId is a client
// error) and returns the complete, current config option state, per spec.
func (h *handler) handleSetConfigOption(p setConfigOptionParams) (setConfigOptionResult, *rpcError) {
	h.mu.Lock()
	_, ok := h.sessions[p.SessionID]
	h.mu.Unlock()
	if !ok {
		return setConfigOptionResult{}, &rpcError{Code: errCodeInvalidParams, Message: "unknown sessionId: " + p.SessionID}
	}

	var value string
	if err := json.Unmarshal(p.Value, &value); err != nil {
		return setConfigOptionResult{}, &rpcError{Code: errCodeInvalidParams, Message: "value must be a string for this transport's config options: " + err.Error()}
	}

	// The session command name and its param key are NOT always the same
	// string (see handleExecCommand in internal/server/server.go): "model"
	// takes {"model": ...} but "thinking" takes {"level": ...}.
	var command, paramKey string
	switch p.ConfigID {
	case "model":
		command, paramKey = "model", "model"
	case "thinking":
		command, paramKey = "thinking", "level"
	default:
		return setConfigOptionResult{}, &rpcError{Code: errCodeInvalidParams, Message: "unknown configId: " + p.ConfigID}
	}
	if _, err := h.api.ExecCommand(p.SessionID, command, map[string]any{paramKey: value}); err != nil {
		return setConfigOptionResult{}, internalErr("set "+p.ConfigID, err)
	}

	opts, err := buildConfigOptions(h.api)
	if err != nil {
		return setConfigOptionResult{}, internalErr("build config options", err)
	}
	return setConfigOptionResult{ConfigOptions: opts}, nil
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

// handleResumeSession reconnects to an existing Harness session WITHOUT
// replaying its history — the lighter sibling of handleLoadSession. Same
// registerSession finish (config options + available_commands_update) as
// every other session-creating path.
func (h *handler) handleResumeSession(c *conn, p resumeSessionParams) (resumeSessionResult, *rpcError) {
	if _, err := h.api.ResumeSession(p.SessionID); err != nil {
		return resumeSessionResult{}, internalErr("resume session", err)
	}
	return h.registerSession(c, p.SessionID)
}

// handleCloseSession cancels any in-flight work (same effect as
// session/cancel) and frees the session's resources — but, unlike
// session/delete, leaves it intact for a future session/load or
// session/resume. Also drops this transport's own bookkeeping entry so a
// stale acpSession doesn't linger for a session ACP no longer considers
// live.
func (h *handler) handleCloseSession(p closeSessionParams) (closeSessionResult, *rpcError) {
	if _, err := h.api.CloseSession(p.SessionID); err != nil {
		return closeSessionResult{}, internalErr("close session", err)
	}
	h.mu.Lock()
	delete(h.sessions, p.SessionID)
	h.mu.Unlock()
	return closeSessionResult{}, nil
}

// handleDeleteSession permanently removes a session — it will no longer
// appear in session/list. Per spec, deleting an already-deleted or
// nonexistent session SHOULD succeed silently, matching
// client.DeleteSession's semantics (DELETE is idempotent at the HTTP layer).
func (h *handler) handleDeleteSession(p deleteSessionParams) (deleteSessionResult, *rpcError) {
	if err := h.api.DeleteSession(p.SessionID); err != nil {
		return deleteSessionResult{}, internalErr("delete session", err)
	}
	h.mu.Lock()
	delete(h.sessions, p.SessionID)
	h.mu.Unlock()
	return deleteSessionResult{}, nil
}

// handleListSessions lists sessions known to Harness, optionally filtered by
// cwd. Cursor-based pagination is not implemented — Harness's own session
// count per cwd is small enough that a single page covering everything is
// simpler and still spec-compliant (nextCursor is optional; omitting it
// means "no more results"). Any incoming cursor is accepted but ignored
// rather than rejected, since there is only ever one page to give back.
func (h *handler) handleListSessions(p listSessionsParams) (listSessionsResult, *rpcError) {
	sessions, err := h.api.ListSessions(p.CWD)
	if err != nil {
		return listSessionsResult{}, internalErr("list sessions", err)
	}
	out := make([]sessionInfo, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, sessionInfo{
			SessionID: s.ID,
			CWD:       s.CWD,
			Title:     s.Name,
			UpdatedAt: s.LastActiveAt.UTC().Format(time.RFC3339),
		})
	}
	return listSessionsResult{Sessions: out}, nil
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
