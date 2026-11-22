package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/types"
)

// Channel is an external medium a session can listen to while it is open — a
// Slack conversation today. A session bound to a conversation receives what is
// said there as prompts (origin "<type>:<id>") and the channel publishes the
// turns those prompts start; turns of any other origin never reach it.
//
// Implementations are registered on the agent (AgentOptions.Channels /
// harness.AgentWithChannels) and bound per session, explicitly, with
// WithSessionChannels on NewSession/ResumeSession. A conversation is listened
// to by at most ONE session at a time, across processes (see the claim
// registry in internal/config).
type Channel interface {
	// Type is the implementation's name, the prefix of its prompts' origin
	// and of InputChannel.Type ("slack").
	Type() string
	// Resolve turns a user-facing reference (an id, a name, …) into the
	// conversation's stable id.
	Resolve(ctx context.Context, ref string) (id string, err error)
	// Listen starts delivering the messages of conversation id that are meant
	// for the session. deliver must not block for long (it queues a prompt).
	// stop ends it; it is called exactly once. ctx carries the session (see
	// SessionName); its cancellation is not a stop signal — stop is.
	Listen(ctx context.Context, id string, deliver func(Inbound)) (stop func(), err error)
	// HandleEvent receives the events of the turns started by a prompt from
	// conversation id — from the prompt's echo to turn_end — and publishes
	// what it wants. It is called synchronously from the session's event path
	// and MUST NOT block: queue the work.
	HandleEvent(id string, evt types.Event)
	// Name is a readable name for conversation id (Slack: "#channel" or
	// "@person"), shown to people and to the model; id itself when it cannot
	// tell. Called once, when a session binds the conversation.
	Name(ctx context.Context, id string) string
	// Directive is the system-prompt section for a session bound to ids.
	Directive(ids []string) string
	// Tools are added to a session bound to at least one conversation of this
	// channel. Each one's name must start with the channel's type, first
	// letter upper-cased (Type "slack" → "SlackPost"): the open fails
	// otherwise. ids are the conversations of this channel the session binds
	// (the same ids Directive gets), for tools that act as the session.
	Tools(ids []string) []tools.Tool
}

// SenderOrigins is implemented by a channel whose turns carry the SENDER in
// their origin (Inbound.Origin) instead of the conversation's — colleagues:
// a question from raiden is "colleague:raiden" on the conversation
// "colleague:<me>". OwnsOrigin says whether a turn of origin belongs to
// conversation id, so it is routed there also when it was not delivered in
// this run (a schedule a colleague asked for, fired back after a restart).
// Optional: a channel whose origins are its conversations' needs none.
type SenderOrigins interface {
	OwnsOrigin(id, origin string) bool
}

// ChannelInfo describes one conversation a session listens to.
type ChannelInfo struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AutoChannel is a Channel that joins EVERY session its agent opens — new,
// resumed or forked, whichever frontend opens it — instead of waiting to be
// asked: registering it is the switch (the colleague channel: each session
// becomes a colleague with a name of its own). AutoRef is the reference each
// session is bound with; the channel resolves it (e.g. to a fresh id).
type AutoChannel interface {
	Channel
	AutoRef() string
}

// Inbound is one message a Channel delivers to its session.
type Inbound struct {
	Text   string
	Images []types.ImageData
	// Origin overrides the prompt's origin (default "<type>:<id>" of the
	// conversation). It must start with "<type>:" — a channel whose senders
	// are themselves addressable (colleagues) puts the SENDER there. The turn
	// is still routed to the conversation that delivered it.
	Origin string
}

// Channel errors, for callers that map them (the HTTP server: 409 / 400).
var (
	// ErrChannelTaken: another live session already listens to the conversation.
	ErrChannelTaken = errors.New("channel already taken")
	// ErrChannelUnavailable: no implementation of that type is registered.
	ErrChannelUnavailable = errors.New("channel not available")
	// ErrChannelInvalid: the request is malformed or the reference cannot be resolved.
	ErrChannelInvalid = errors.New("invalid channel")
)

// sessionCtxKey carries the bound session in the ctx a Channel's Listen gets.
// Unexported: a channel can only read what SessionName exposes, never the
// *Session itself.
type sessionCtxKey struct{}

// SessionName returns the CURRENT name of the session a channel's Listen ctx
// belongs to — read when called, so a rename shows at once (the colleague
// channel answers a ping with it). ok is false for any other ctx.
func SessionName(ctx context.Context) (name string, ok bool) {
	s, isSession := ctx.Value(sessionCtxKey{}).(*Session)
	if !isSession || s == nil {
		return "", false
	}
	return s.Name(), true
}

// ChannelOrigin is the prompt origin of a message from conversation id of a
// channel of type typ.
func ChannelOrigin(typ, id string) string { return typ + ":" + id }

// WithSessionChannels binds the session being created or resumed to the given
// conversations. Explicit and never persisted: a session listens only while
// the handle opened with these options is open, and a later resume without
// them listens to nothing. Every channel is honored or the open fails.
func WithSessionChannels(chs ...types.InputChannel) SessionOption {
	return func(c *sessionConfig) { c.channels = append(c.channels, chs...) }
}

// boundChannel is one conversation a session listens to.
type boundChannel struct {
	ch   Channel
	id   string
	name string
	stop func()
}

// sessionChannels is everything a session holds because of its channels: the
// claims, the listeners and the routing of its turns' events.
type sessionChannels struct {
	agent     *Agent
	sessionID string
	bound     []*boundChannel
	byOrigin  map[string]*boundChannel
	// byPrompt: an origin a channel delivered with (Inbound.Origin) → the
	// conversation it came through, so its turn goes back to that channel.
	byPrompt map[string]*boundChannel

	// directives and tools the channels add to the session — kept so a
	// reconfiguration (ReconfigureSession) re-applies them instead of dropping
	// them.
	directives []string
	tools      []tools.Tool

	mu      sync.Mutex
	current *boundChannel // the channel whose prompt started the running turn
	closed  bool
}

// channelPID is the pid a claim is recorded under (a variable for tests).
var channelPID = os.Getpid()

// prepareChannels resolves and claims the requested conversations for
// sessionID. On any failure nothing stays claimed. The result carries the
// directives and tools to add to the session; listening starts later
// (start), once the session exists.
func (a *Agent) prepareChannels(sessionID, cwd string, reqs []types.InputChannel, existing []tools.Tool) (*sessionChannels, []string, []tools.Tool, error) {
	if len(reqs) == 0 {
		return nil, nil, nil, nil
	}
	sc := &sessionChannels{agent: a, sessionID: sessionID, byOrigin: map[string]*boundChannel{}, byPrompt: map[string]*boundChannel{}}
	ctx := context.Background()
	for _, r := range reqs {
		typ, ref := strings.TrimSpace(r.Type), strings.TrimSpace(r.Ref)
		if typ == "" || ref == "" {
			sc.release()
			return nil, nil, nil, fmt.Errorf("%w: a channel needs a type and a ref", ErrChannelInvalid)
		}
		ch := a.channelOf(typ)
		if ch == nil {
			sc.release()
			return nil, nil, nil, fmt.Errorf("%w: %s", ErrChannelUnavailable, typ)
		}
		id, err := ch.Resolve(ctx, ref)
		if err != nil {
			sc.release()
			return nil, nil, nil, fmt.Errorf("%w: %s %q: %v", ErrChannelInvalid, typ, ref, err)
		}
		origin := ChannelOrigin(typ, id)
		if _, dup := sc.byOrigin[origin]; dup {
			continue // the same conversation asked twice (e.g. by id and by name)
		}
		owner, err := config.GetSettingsManager().ClaimChannel(config.ChannelClaim{
			Type: typ, ID: id, Session: sessionID, PID: channelPID, Started: ownStart, CWD: cwd,
		}, func(o config.ChannelClaim) bool { return ownerAlive(o.PID, o.Started, lookupProc) })
		if err != nil {
			sc.release()
			return nil, nil, nil, fmt.Errorf("claim %s: %w", origin, err)
		}
		if owner != nil {
			sc.release()
			return nil, nil, nil, fmt.Errorf("%w: %s is listened to by session %s (pid %d, %s)",
				ErrChannelTaken, origin, owner.Session, owner.PID, owner.CWD)
		}
		name := ch.Name(ctx, id)
		if name == "" {
			name = id
		}
		b := &boundChannel{ch: ch, id: id, name: name}
		sc.bound = append(sc.bound, b)
		sc.byOrigin[origin] = b
	}

	// One directive and one tool set per implementation, naming its ids.
	var directives []string
	var extra []tools.Tool
	// Every tool the session will have without the channels: the built-ins
	// (always-on and gated), the agent's (MCP included) and the session's own.
	taken := map[string]bool{}
	for _, n := range a.builtinNamesForSession() {
		taken[n] = true
	}
	for _, t := range a.toolReg.Definitions() {
		taken[t.Name] = true
	}
	for _, t := range existing {
		taken[t.Def.Name] = true
	}
	seen := map[string]bool{}
	for _, b := range sc.bound {
		typ := b.ch.Type()
		if seen[typ] {
			continue
		}
		seen[typ] = true
		var ids []string
		for _, o := range sc.bound {
			if o.ch.Type() == typ {
				ids = append(ids, o.id)
			}
		}
		if d := b.ch.Directive(ids); d != "" {
			directives = append(directives, d)
		}
		prefix := channelToolPrefix(typ)
		for _, t := range b.ch.Tools(ids) {
			if !strings.HasPrefix(t.Def.Name, prefix) {
				sc.release()
				return nil, nil, nil, fmt.Errorf("%w: the %s channel's tool %q must be named %s… (a channel's tools carry its type, so a frontend can tell them apart)",
					ErrChannelInvalid, typ, t.Def.Name, prefix)
			}
			if taken[t.Def.Name] {
				sc.release()
				return nil, nil, nil, fmt.Errorf("%w: the %s channel's tool %s is already in the session (a profile or an agent tool provides it)",
					ErrChannelInvalid, typ, t.Def.Name)
			}
			taken[t.Def.Name] = true
			extra = append(extra, t)
		}
	}
	sc.directives, sc.tools = directives, extra
	return sc, directives, extra, nil
}

// start begins listening on every bound conversation, delivering into sess.
// On failure everything is undone (listeners stopped, claims released).
func (sc *sessionChannels) start(sess *Session) error {
	ctx := context.WithValue(context.Background(), sessionCtxKey{}, sess)
	for _, b := range sc.bound {
		origin := ChannelOrigin(b.ch.Type(), b.id)
		b := b
		stop, err := b.ch.Listen(ctx, b.id, func(in Inbound) {
			o := origin
			if in.Origin != "" && strings.HasPrefix(in.Origin, b.ch.Type()+":") {
				o = in.Origin
				sc.mu.Lock()
				sc.byPrompt[o] = b
				sc.mu.Unlock()
			}
			opts := []PromptOption{PromptWithOrigin(o)}
			if len(in.Images) > 0 {
				opts = append(opts, PromptWithImages(in.Images...))
			}
			sess.Prompt(context.Background(), in.Text, opts...)
		})
		if err != nil {
			sc.close()
			return fmt.Errorf("listen %s: %w", origin, err)
		}
		b.stop = stop
	}
	return nil
}

// tap routes one session event: a prompt echo selects the channel that owns
// the turn (or none), and every event of that turn goes to it.
func (sc *sessionChannels) tap(e types.Event) {
	sc.mu.Lock()
	if sc.closed {
		sc.mu.Unlock()
		return
	}
	if e.Type == types.EventReceivedPrompt || e.Type == types.EventFollowUpStart {
		sc.current = sc.routeLocked(e.Origin)
	}
	cur := sc.current
	if e.Type == types.EventTurnEnd {
		sc.current = nil
	}
	sc.mu.Unlock()
	if cur != nil {
		cur.ch.HandleEvent(cur.id, e)
	}
}

// routeLocked returns the bound conversation a turn of origin belongs to:
// exactly its conversation ("slack:C1"), the conversation a channel delivered
// it through (Inbound.Origin), or a conversation whose origin is a PREFIX of
// it followed by ":" — a channel whose conversation has sub-conversations
// (Telegram: the bot is claimed, "telegram:<bot>", and each turn carries its
// chat, "telegram:<bot>:<chat>"). nil when none. Caller holds sc.mu.
func (sc *sessionChannels) routeLocked(origin string) *boundChannel {
	if origin == "" {
		return nil
	}
	if b := sc.byPrompt[origin]; b != nil {
		return b
	}
	return sc.durableRouteLocked(origin)
}

// durableRouteLocked is routeLocked without what a channel delivered in this
// run (byPrompt): the routes that hold across restarts — the conversation
// itself, a sub-conversation by prefix, or an origin a channel owns
// (SenderOrigins). What a schedule may record and fire back to. Caller holds
// sc.mu.
func (sc *sessionChannels) durableRouteLocked(origin string) *boundChannel {
	if b := sc.byOrigin[origin]; b != nil {
		return b
	}
	for o, b := range sc.byOrigin {
		if strings.HasPrefix(origin, o+":") {
			return b
		}
	}
	for _, b := range sc.bound {
		if so, ok := b.ch.(SenderOrigins); ok && so.OwnsOrigin(b.id, origin) {
			return b
		}
	}
	return nil
}

// listensTo reports whether a turn of origin reaches one of the session's
// channels by a DURABLE route (durableRouteLocked) — the rule the scheduler
// uses both to record a schedule's origin and to fire it back there, so the
// two agree even across restarts.
func (sc *sessionChannels) listensTo(origin string) bool {
	if sc == nil || origin == "" {
		return false
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return !sc.closed && sc.durableRouteLocked(origin) != nil
}

// close stops listening and releases every claim. Idempotent.
func (sc *sessionChannels) close() {
	if sc == nil {
		return
	}
	sc.mu.Lock()
	if sc.closed {
		sc.mu.Unlock()
		return
	}
	sc.closed = true
	sc.mu.Unlock()
	for _, b := range sc.bound {
		if b.stop != nil {
			b.stop()
			b.stop = nil
		}
	}
	sc.release()
}

// release drops the claims (only those still ours).
func (sc *sessionChannels) release() {
	for _, b := range sc.bound {
		_ = config.GetSettingsManager().ReleaseChannel(b.ch.Type(), b.id, sc.sessionID, channelPID)
	}
}

// infos lists the bound conversations, in binding order.
func (sc *sessionChannels) infos() []ChannelInfo {
	if sc == nil {
		return nil
	}
	out := make([]ChannelInfo, 0, len(sc.bound))
	for _, b := range sc.bound {
		out = append(out, ChannelInfo{Type: b.ch.Type(), ID: b.id, Name: b.name})
	}
	return out
}

// channelOf returns the registered implementation of typ, or nil.
func (a *Agent) channelOf(typ string) Channel {
	for _, ch := range a.opts.Channels {
		if ch != nil && ch.Type() == typ {
			return ch
		}
	}
	return nil
}

// channelToolPrefix is the prefix every tool of a channel of type typ must
// carry: the type with its first letter upper-cased ("slack" → "Slack").
// Enforced when a session binds the channel, so a frontend can rely on it.
func channelToolPrefix(typ string) string {
	if typ == "" {
		return ""
	}
	return strings.ToUpper(typ[:1]) + typ[1:]
}

// ChannelClaim is one recorded claim: which session (and process) listens to a
// conversation. ListedChannels returns them for a channel that offers a
// directory of its own peers (colleagues).
type ChannelClaim struct {
	Type    string
	ID      string
	Session string
	CWD     string
}

// LiveChannelClaims lists the claims on conversations of type typ whose owner
// process is alive (the same check that guards taking a claim over: pid plus
// its start marker), sorted by id.
func LiveChannelClaims(typ string) []ChannelClaim {
	var out []ChannelClaim
	for _, c := range config.GetSettingsManager().ChannelClaims() {
		if c.Type == typ && ownerAlive(c.PID, c.Started, lookupProc) {
			out = append(out, ChannelClaim{Type: c.Type, ID: c.ID, Session: c.Session, CWD: c.CWD})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ChannelClaimTaken reports whether conversation (typ, id) is claimed by a
// live owner — for a channel that picks a free id itself.
func ChannelClaimTaken(typ, id string) bool {
	for _, c := range LiveChannelClaims(typ) {
		if c.ID == id {
			return true
		}
	}
	return false
}

// builtinNamesForSession lists the built-in tools a session of this agent
// gets — the always-on ones and those whose switch is on (the scheduler,
// memory, WebSearch) — so a channel's tool clashes only with what
// the session will really have.
func (a *Agent) builtinNamesForSession() []string {
	names := []string{tools.ToolBash, tools.ToolRead, tools.ToolWrite, tools.ToolEdit, tools.ToolFetch,
		tools.ToolSkill, tools.ToolSubagent, tools.ToolSessionInfo}
	if a.memStore != nil {
		names = append(names, tools.ToolMemoWrite, tools.ToolMemoSearch, tools.ToolMemoDelete)
	}
	if a.scheduleAdapter() != nil {
		names = append(names, tools.ToolSchedule, tools.ToolScheduleList, tools.ToolScheduleDelete)
	}
	if a.opts.EnableWebSearch {
		names = append(names, tools.ToolWebSearch)
	}
	return names
}
