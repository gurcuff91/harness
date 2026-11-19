package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	// stop ends it; it is called exactly once.
	Listen(ctx context.Context, id string, deliver func(Inbound)) (stop func(), err error)
	// HandleEvent receives the events of the turns started by a prompt from
	// conversation id — from the prompt's echo to turn_end — and publishes
	// what it wants. It is called synchronously from the session's event path
	// and MUST NOT block: queue the work.
	HandleEvent(id string, evt types.Event)
	// Directive is the system-prompt section for a session bound to ids.
	Directive(ids []string) string
	// Tools are added to a session bound to at least one conversation of this
	// channel.
	Tools() []tools.Tool
}

// Inbound is one message a Channel delivers to its session.
type Inbound struct {
	Text   string
	Images []types.ImageData
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
	stop func()
}

// sessionChannels is everything a session holds because of its channels: the
// claims, the listeners and the routing of its turns' events.
type sessionChannels struct {
	agent     *Agent
	sessionID string
	bound     []*boundChannel
	byOrigin  map[string]*boundChannel

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
	sc := &sessionChannels{agent: a, sessionID: sessionID, byOrigin: map[string]*boundChannel{}}
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
		b := &boundChannel{ch: ch, id: id}
		sc.bound = append(sc.bound, b)
		sc.byOrigin[origin] = b
	}

	// One directive and one tool set per implementation, naming its ids.
	var directives []string
	var extra []tools.Tool
	// Every tool the session will have without the channels: the built-ins
	// (always-on and gated), the agent's (MCP included) and the session's own.
	taken := map[string]bool{}
	for _, n := range tools.BuiltinNames() {
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
		for _, t := range b.ch.Tools() {
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
	for _, b := range sc.bound {
		origin := ChannelOrigin(b.ch.Type(), b.id)
		stop, err := b.ch.Listen(context.Background(), b.id, func(in Inbound) {
			opts := []PromptOption{PromptWithOrigin(origin)}
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
		sc.current = sc.byOrigin[e.Origin]
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

// ids lists the bound conversations as origins ("slack:C0123"), for display.
func (sc *sessionChannels) origins() []string {
	if sc == nil {
		return nil
	}
	out := make([]string, 0, len(sc.bound))
	for _, b := range sc.bound {
		out = append(out, ChannelOrigin(b.ch.Type(), b.id))
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
