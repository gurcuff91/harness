package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/types"
)

// sessionMoreOps are the session/* operations beyond opening and closing
// (prompt, stop, compact, …). Every one takes the id of a session this
// connection has open.
//
// Turns run on the ctx the operation receives: it belongs to the connection,
// not to one request (it ends when the connection does), so a prompt's turn
// outlives the request that started it.
func sessionMoreOps() []Op {
	return []Op{
		Define("session/prompt", "Sends a message to an open session; answers at once ('started', or 'queued' behind a running turn). The turn's events arrive as `event` notifications.", opSessionPrompt),
		Define("session/ask", "Sends a message and waits for the turn's final text. Returns early (empty text) when the turn is stopped.", opSessionAsk),
		Define("session/stop", "Cancels the turn in progress; queued prompts continue.", opSessionStop),
		Define("session/compact", "Summarizes the conversation into a checkpoint; replies when done. Conflict while a turn runs.", opSessionCompact),
		Define("session/reset", "Wipes the session's history and stats. Conflict while a turn runs.", opSessionReset),
		Define("session/switch_model", "Switches the session's model (provider/model) and answers its detail.", opSessionSwitchModel),
		Define("session/switch_thinking", "Switches the session's thinking level and answers its detail.", opSessionSwitchThinking),
		Define("session/set_max_iterations", "Sets the session's per-turn iteration budget (1-1000) and answers its detail.", opSessionSetMaxIterations),
		Define("session/rename", "Renames the session and answers its detail.", opSessionRename),
		Define("session/info", "The session's live state: detail, busy, queue depth, MCP and schedule counts, system.", opSessionInfo),
		Define("session/context", "Estimated token usage of the session's context window, per component.", opSessionContext),
		Define("session/sysprompt", "The full system prompt the session sends to the provider.", opSessionSysprompt),
		Define("session/tools", "The tool definitions the session sends to the provider.", opSessionTools),
		Define("session/messages", "A window of the session's complete history (from an index, or after the k-th-to-last compaction).", opSessionMessages),
		Define("session/exec_command", "Runs a command that has no method of its own: skill:<name>.", opSessionExecCommand),
	}
}

// promptArgs validates a prompt's params (as the HTTP handlers do) and builds
// the options.
func promptArgs(sess *agent.Session, p SessionPromptParams) ([]agent.PromptOption, error) {
	if p.Text == "" && len(p.Images) == 0 {
		return nil, errInvalid("text or images is required")
	}
	if len(p.Images) > 0 {
		if meta := sess.ModelMeta(); meta == nil || !meta.Vision {
			return nil, errInvalid("current model does not support images")
		}
	}
	opts := []agent.PromptOption{agent.PromptWithOrigin(p.Origin)}
	if len(p.Images) > 0 {
		opts = append(opts, agent.PromptWithImages(p.Images...))
	}
	if p.DisplayText != "" {
		opts = append(opts, agent.PromptWithDisplayText(p.DisplayText))
	}
	return opts, nil
}

func sessStatus(ps types.PromptStatus) string {
	if ps == types.PromptQueued {
		return "queued"
	}
	return "started"
}

func opSessionPrompt(ctx context.Context, s *Server, p SessionPromptParams) (SessionPromptResult, error) {
	sess, err := s.session(p.ID)
	if err != nil {
		return SessionPromptResult{}, err
	}
	opts, err := promptArgs(sess, p)
	if err != nil {
		return SessionPromptResult{}, err
	}
	return SessionPromptResult{Status: sessStatus(sess.Prompt(ctx, p.Text, opts...))}, nil
}

func opSessionAsk(ctx context.Context, s *Server, p SessionPromptParams) (SessionAskResult, error) {
	sess, err := s.session(p.ID)
	if err != nil {
		return SessionAskResult{}, err
	}
	opts, err := promptArgs(sess, p)
	if err != nil {
		return SessionAskResult{}, err
	}
	type outcome struct {
		text string
		err  error
	}
	// PromptAndWait blocks on its own channel and ignores ctx while the prompt
	// is queued: wait in a goroutine so a closing connection frees this call
	// (the buffered channel lets the goroutine finish whenever the turn does).
	done := make(chan outcome, 1)
	go func() {
		text, err := sess.PromptAndWait(ctx, p.Text, opts...)
		done <- outcome{text, err}
	}()
	select {
	case o := <-done:
		return SessionAskResult{Text: o.text}, o.err
	case <-ctx.Done():
		return SessionAskResult{}, ctx.Err()
	}
}

func opSessionStop(_ context.Context, s *Server, p SessionIDParams) (Empty, error) {
	sess, err := s.session(p.ID)
	if err != nil {
		return Empty{}, err
	}
	sess.Stop()
	return Empty{}, nil
}

func opSessionCompact(ctx context.Context, s *Server, p SessionIDParams) (Empty, error) {
	sess, err := s.session(p.ID)
	if err != nil {
		return Empty{}, err
	}
	return Empty{}, sess.Compact(ctx)
}

func opSessionReset(_ context.Context, s *Server, p SessionIDParams) (Empty, error) {
	sess, err := s.session(p.ID)
	if err != nil {
		return Empty{}, err
	}
	return Empty{}, sess.Reset()
}

func opSessionSwitchModel(ctx context.Context, s *Server, p SessionSwitchModelParams) (agent.SessionDetail, error) {
	sess, err := s.session(p.ID)
	if err != nil {
		return agent.SessionDetail{}, err
	}
	if p.Model == "" {
		return agent.SessionDetail{}, errInvalid("model is required")
	}
	if err := sess.SwitchModel(ctx, p.Model); err != nil {
		return agent.SessionDetail{}, err
	}
	return sess.Detail(), nil
}

func opSessionSwitchThinking(_ context.Context, s *Server, p SessionSwitchThinkingParams) (agent.SessionDetail, error) {
	sess, err := s.session(p.ID)
	if err != nil {
		return agent.SessionDetail{}, err
	}
	if p.Level == "" {
		return agent.SessionDetail{}, errInvalid("level is required")
	}
	if err := sess.SwitchThinking(p.Level); err != nil {
		if errors.Is(err, config.ErrInvalidThinkingLevel) {
			return agent.SessionDetail{}, rpcErr("unprocessable", "%s", err.Error())
		}
		return agent.SessionDetail{}, err
	}
	return sess.Detail(), nil
}

func opSessionSetMaxIterations(_ context.Context, s *Server, p SessionSetMaxIterationsParams) (agent.SessionDetail, error) {
	sess, err := s.session(p.ID)
	if err != nil {
		return agent.SessionDetail{}, err
	}
	// SetMaxIterations only fails on an out-of-range value (HTTP: 422).
	if err := sess.SetMaxIterations(p.N); err != nil {
		return agent.SessionDetail{}, rpcErr("unprocessable", "%s", err.Error())
	}
	return sess.Detail(), nil
}

func opSessionRename(_ context.Context, s *Server, p SessionRenameParams) (agent.SessionDetail, error) {
	sess, err := s.session(p.ID)
	if err != nil {
		return agent.SessionDetail{}, err
	}
	if p.Name == "" {
		return agent.SessionDetail{}, errInvalid("name is required")
	}
	if err := sess.Rename(p.Name); err != nil {
		return agent.SessionDetail{}, err
	}
	return sess.Detail(), nil
}

func opSessionInfo(_ context.Context, s *Server, p SessionIDParams) (SessionInfoResult, error) {
	sess, err := s.session(p.ID)
	if err != nil {
		return SessionInfoResult{}, err
	}
	snap := sess.Snapshot()
	return SessionInfoResult{
		Version:       snap.Version,
		Session:       sess.Detail(),
		Busy:          snap.Busy,
		QueueDepth:    snap.QueueDepth,
		MCPConnected:  snap.MCPConnected,
		ScheduleCount: snap.ScheduleCount,
		OS:            snap.OS,
		Arch:          snap.Arch,
		Terminal:      snap.Terminal,
	}, nil
}

func opSessionContext(_ context.Context, s *Server, p SessionIDParams) (agent.ContextBreakdown, error) {
	sess, err := s.session(p.ID)
	if err != nil {
		return agent.ContextBreakdown{}, err
	}
	return sess.ContextBreakdown(), nil
}

func opSessionSysprompt(_ context.Context, s *Server, p SessionIDParams) (SessionSyspromptResult, error) {
	sess, err := s.session(p.ID)
	if err != nil {
		return SessionSyspromptResult{}, err
	}
	return SessionSyspromptResult{Text: sess.SystemPrompt()}, nil
}

func opSessionTools(_ context.Context, s *Server, p SessionIDParams) (SessionToolsResult, error) {
	sess, err := s.session(p.ID)
	if err != nil {
		return SessionToolsResult{}, err
	}
	defs := sess.Tools()
	if defs == nil {
		defs = []types.ToolDef{}
	}
	return SessionToolsResult{Tools: defs}, nil
}

func opSessionMessages(_ context.Context, s *Server, p SessionMessagesParams) (SessionMessagesResult, error) {
	sess, err := s.session(p.ID)
	if err != nil {
		return SessionMessagesResult{}, err
	}
	var win store.MessageWindow
	if p.From != nil && p.Compactions != nil {
		return SessionMessagesResult{}, errInvalid("from and compactions are mutually exclusive")
	}
	if p.From != nil {
		if *p.From < 0 {
			return SessionMessagesResult{}, errInvalid("from must be a non-negative integer")
		}
		win.From = *p.From
	}
	if p.Compactions != nil {
		if *p.Compactions < 1 {
			return SessionMessagesResult{}, errInvalid("compactions must be a positive integer")
		}
		win.Compactions = *p.Compactions
	}
	msgs, from, total, err := sess.MessagesWindow(win)
	if err != nil {
		return SessionMessagesResult{}, err
	}
	if msgs == nil {
		msgs = []types.Message{}
	}
	return SessionMessagesResult{Messages: msgs, From: from, Total: total}, nil
}

// sessDedicated names the method that replaces each command the HTTP exec
// handler knows and this protocol gives its own method.
var sessDedicated = map[string]string{
	"rename":   "session/rename",
	"thinking": "session/switch_thinking",
	"model":    "session/switch_model",
	"max-iter": "session/set_max_iterations",
	"compact":  "session/compact",
	"reset":    "session/reset",
	"export":   "admin/sessions/export",
	"import":   "session/import",
}

func opSessionExecCommand(ctx context.Context, s *Server, p SessionExecCommandParams) (SessionExecCommandResult, error) {
	sess, err := s.session(p.ID)
	if err != nil {
		return SessionExecCommandResult{}, err
	}
	name, isSkill := strings.CutPrefix(p.Command, "skill:")
	if !isSkill {
		if m, ok := sessDedicated[p.Command]; ok {
			return SessionExecCommandResult{}, errInvalid("command %q has its own method: use %s", p.Command, m)
		}
		return SessionExecCommandResult{}, errInvalid("unknown command: %q (only skill:<name> is accepted)", p.Command)
	}
	if name == "" {
		return SessionExecCommandResult{}, errInvalid("skill name is required")
	}
	content, dir, err := sess.ReadSkill(name)
	if err != nil {
		return SessionExecCommandResult{}, rpcErr("not_found", "skill not found: %s", name)
	}
	// Same prompt and echo as the HTTP handler: the location note lets the
	// model resolve relative paths; the echo shows "Skill:<name> <text>".
	userPrompt, _ := p.Params["prompt"].(string)
	prompt := fmt.Sprintf("This skill is located at %s\nAny relative paths it references are relative to this directory.\n\n%s", dir, content)
	if userPrompt != "" {
		prompt += "\n\n---\n\n" + userPrompt
	}
	display := "Skill:" + name
	if userPrompt != "" {
		display += " " + userPrompt
	}
	ps := sess.Prompt(ctx, prompt, agent.PromptWithDisplayText(display))
	return SessionExecCommandResult{Status: sessStatus(ps)}, nil
}
