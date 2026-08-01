package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/internal/server"
)

// Run starts the ACP transport: an in-process HTTP/SSE server (exactly like
// every other interactive transport — see internal/transport/telegram), then
// a JSON-RPC dispatch loop reading newline-delimited messages from stdin and
// writing responses/notifications to stdout. Blocks until ctx is cancelled
// (Ctrl+C/SIGTERM, via the caller's signalContext()) or stdin is closed (the
// ACP client terminated the connection) — both are treated as a clean
// shutdown (nil error, matching `harness serve`'s Ctrl+C handling), not a
// process failure worth a non-zero exit code.
func Run(ctx context.Context, a *agent.Agent, stdin io.Reader, stdout io.Writer) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("acp: bind server: %w", err)
	}
	srv := server.NewServer(a, server.ServerOptions{Verbose: false, Transport: "acp"})
	go srv.Serve(listener) //nolint:errcheck
	defer srv.Close()      //nolint:errcheck

	cwd, _ := os.Getwd()
	h := &handler{
		api:      client.New(listener.Addr().String()),
		cwd:      cwd,
		sessions: make(map[string]*acpSession),
	}
	c := newConn(stdin, stdout)

	err = dispatchLoop(ctx, c, h)
	if err == context.Canceled || err == context.DeadlineExceeded {
		return nil // Ctrl+C/SIGTERM — expected shutdown, not a failure
	}
	return err
}

// readResult carries one readMessage() outcome across the reader-goroutine
// boundary in dispatchLoop.
type readResult struct {
	req *rpcRequest
	err error
}

// dispatchLoop reads one JSON-RPC message at a time and routes it. Prompt
// requests (session/prompt) run in their own goroutine since a turn can take
// minutes and multiple sessions must be able to make progress concurrently —
// every other method (initialize, session/new, session/load, authenticate,
// session/cancel) is quick and handled inline, in read order.
//
// The actual stdin read happens in its own goroutine feeding reads: an
// os.Stdin (or any plain io.Reader) has no way to have its blocking Read()
// call interrupted by ctx — without this, Ctrl+C would never be noticed
// while waiting for the next line (which, once Zed is gone, simply never
// arrives), and the process would hang forever instead of exiting. The
// reader goroutine leaks past cancellation (still blocked in Read()), but
// that's harmless: the process exits right after Run returns.
func dispatchLoop(ctx context.Context, c *conn, h *handler) error {
	reads := make(chan readResult)
	go func() {
		for {
			req, err := c.readMessage()
			reads <- readResult{req, err}
			if err != nil {
				return
			}
		}
	}()

	for {
		var res readResult
		select {
		case <-ctx.Done():
			return ctx.Err()
		case res = <-reads:
		}

		if res.err != nil {
			if res.err == io.EOF {
				return nil // client closed stdin — normal shutdown
			}
			return fmt.Errorf("acp: read message: %w", res.err)
		}
		req := res.req

		isNotification := len(req.ID) == 0

		switch req.Method {
		case "initialize":
			var p initializeParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				_ = c.sendError(req.ID, errCodeInvalidParams, err.Error(), nil)
				continue
			}
			result, rerr := h.handleInitialize(p)
			respond(c, req.ID, result, rerr)

		case "authenticate":
			var p authenticateParams
			_ = json.Unmarshal(req.Params, &p)
			result, rerr := h.handleAuthenticate(p)
			respond(c, req.ID, result, rerr)

		case "session/new":
			var p newSessionParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				_ = c.sendError(req.ID, errCodeInvalidParams, err.Error(), nil)
				continue
			}
			result, rerr := h.handleNewSession(c, p)
			respond(c, req.ID, result, rerr)
			// MUST come after respond() — see notifyAvailableCommands's doc
			// comment: sending this before the response line hits the wire is
			// silently dropped by Zed (a confirmed upstream bug), which is
			// exactly how this transport's slash commands went missing before.
			if rerr == nil {
				h.notifyAvailableCommands(c, result.SessionID)
			}

		case "session/load":
			var p loadSessionParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				_ = c.sendError(req.ID, errCodeInvalidParams, err.Error(), nil)
				continue
			}
			result, rerr := h.handleLoadSession(c, p)
			respond(c, req.ID, result, rerr)
			if rerr == nil {
				h.notifyAvailableCommands(c, result.SessionID)
			}

		case "session/resume":
			var p resumeSessionParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				_ = c.sendError(req.ID, errCodeInvalidParams, err.Error(), nil)
				continue
			}
			result, rerr := h.handleResumeSession(c, p)
			respond(c, req.ID, result, rerr)
			if rerr == nil {
				h.notifyAvailableCommands(c, result.SessionID)
			}

		case "session/close":
			var p closeSessionParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				_ = c.sendError(req.ID, errCodeInvalidParams, err.Error(), nil)
				continue
			}
			result, rerr := h.handleCloseSession(p)
			respond(c, req.ID, result, rerr)

		case "session/delete":
			var p deleteSessionParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				_ = c.sendError(req.ID, errCodeInvalidParams, err.Error(), nil)
				continue
			}
			result, rerr := h.handleDeleteSession(p)
			respond(c, req.ID, result, rerr)

		case "session/list":
			var p listSessionsParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				_ = c.sendError(req.ID, errCodeInvalidParams, err.Error(), nil)
				continue
			}
			result, rerr := h.handleListSessions(p)
			respond(c, req.ID, result, rerr)

		case "session/prompt":
			var p promptParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				_ = c.sendError(req.ID, errCodeInvalidParams, err.Error(), nil)
				continue
			}
			id := req.ID
			go func() {
				result, rerr := h.handlePrompt(ctx, c, p)
				respond(c, id, result, rerr)
			}()

		case "session/cancel":
			var p cancelParams
			_ = json.Unmarshal(req.Params, &p)
			h.handleCancel(p)
			// notification — no response, per spec.

		case "session/set_config_option":
			var p setConfigOptionParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				_ = c.sendError(req.ID, errCodeInvalidParams, err.Error(), nil)
				continue
			}
			result, rerr := h.handleSetConfigOption(p)
			respond(c, req.ID, result, rerr)

		default:
			if isNotification {
				continue // unknown notifications are silently ignored, per JSON-RPC convention
			}
			_ = c.sendError(req.ID, errCodeMethodNotFound, "method not found: "+req.Method, nil)
		}
	}
}

// respond writes either a success or error reply for a request — a no-op for
// notifications (nil ID), since JSON-RPC forbids replying to those.
func respond(c *conn, id json.RawMessage, result any, rerr *rpcError) {
	if len(id) == 0 {
		return
	}
	if rerr != nil {
		_ = c.sendError(id, rerr.Code, rerr.Message, rerr.Data)
		return
	}
	_ = c.sendResponse(id, result)
}
