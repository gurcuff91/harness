// Package colleague implements agent.Channel for the agents of one machine: a
// session bound to a colleague id ("scorpion-hero") receives what other
// sessions ask it, answers automatically, and can ask them in turn.
//
// Transport: a Unix socket per bound id, at <run dir>/c-<id>.sock, in a
// directory only the user can enter (that is the authentication). One
// connection per message, one JSON line each way (see wire.go).
//
// Asynchronous, and loop-free by construction: an "ask" starts a turn whose
// final text is sent back as a "reply"; a "reply" starts a turn that sends
// nothing back.
package colleague

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gurcuff91/harness/agent"
	atools "github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/types"
)

// Type is this channel's name: the InputChannel type and the prefix of its
// prompts' origin ("colleague:<id>").
const Type = "colleague"

// autoID is the reference that asks for a fresh random id, not claimed by a
// live session ("scorpion-hero").
const autoID = "*"

// maxSocketPath is the longest socket path accepted (macOS: 104 bytes with
// the terminating NUL; Linux allows 108).
const maxSocketPath = 103

// Option configures a Channel.
type Option func(*Channel)

// WithRunDir sets the directory the sockets live in (default:
// ~/.harness/run). It is created with mode 0700.
func WithRunDir(dir string) Option { return func(c *Channel) { c.runDir = dir } }

// WithLogger sets what receives the channel's log lines (default: silent).
func WithLogger(l logx.Logger) Option {
	return func(c *Channel) {
		if l != nil {
			c.logger = l
		}
	}
}

// Channel implements agent.Channel for colleagues on this machine.
type Channel struct {
	runDir string
	logger logx.Logger

	mu    sync.Mutex
	bound map[string]*inbox // id → its listener and reply pump
}

var _ agent.AutoChannel = (*Channel)(nil)

// AutoRef implements agent.AutoChannel: registering the colleague channel makes
// every session the agent opens a colleague, each with a fresh random id.
func (c *Channel) AutoRef() string { return autoID }

// New builds a colleague channel. Building it opens nothing: a socket is
// opened when a session binds an id.
func New(opts ...Option) *Channel {
	c := &Channel{logger: logx.NewNilLogger(), bound: map[string]*inbox{}}
	for _, o := range opts {
		if o != nil {
			o(c)
		}
	}
	if c.runDir == "" {
		c.runDir = defaultRunDir()
	}
	return c
}

// longestID is the longest colleague id; the run directory must leave room
// for a socket named after it.
const longestID = 32

// defaultRunDir is ~/.harness/run when a socket path under it fits the OS
// limit, else a short per-user directory in /tmp (/tmp/harness-<uid>, the
// place tmux and ssh-agent use for the same reason: a long home — a network
// path, a test's temporary home — makes a socket path too long). Every
// process of the user derives the same answer, so they find each other.
func defaultRunDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		d := filepath.Join(home, ".harness", "run")
		if len(filepath.Join(d, "c-"+strings.Repeat("x", longestID)+".sock")) <= maxSocketPath {
			return d
		}
	}
	return filepath.Join(shortTempDir(), fmt.Sprintf("harness-%d", os.Getuid()))
}

// shortTempDir is /tmp where it exists (macOS's per-user TMPDIR under
// /var/folders is itself too long for a socket path), else the OS's.
func shortTempDir() string {
	if st, err := os.Stat("/tmp"); err == nil && st.IsDir() {
		return "/tmp"
	}
	return os.TempDir()
}

// Type implements agent.Channel.
func (c *Channel) Type() string { return Type }

// Name implements agent.Channel: a colleague is called by its id.
func (c *Channel) Name(_ context.Context, id string) string { return id }

// socketPath is where id listens.
func (c *Channel) socketPath(id string) string {
	return filepath.Join(c.runDir, "c-"+id+".sock")
}

// Resolve implements agent.Channel: autoID ("*") picks a fresh id no live session
// claims; anything else must be a valid id and is used as is.
func (c *Channel) Resolve(_ context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == autoID {
		for i := 0; i < 64; i++ {
			if id := randomID(); !agent.ChannelClaimTaken(Type, id) {
				return id, nil
			}
		}
		return "", errors.New("no free colleague id")
	}
	if err := checkID(ref); err != nil {
		return "", err
	}
	return ref, nil
}

// Listen implements agent.Channel: opens id's socket. The session already
// holds the 1:1 claim on id, so a socket file left at that path belongs to a
// dead owner and is removed first.
func (c *Channel) Listen(_ context.Context, id string, deliver func(agent.Inbound)) (func(), error) {
	if c.runDir == "" {
		return nil, errors.New("no directory for the colleague socket (no home directory)")
	}
	path := c.socketPath(id)
	if len(path) > maxSocketPath {
		return nil, fmt.Errorf("the colleague socket path is %d bytes, over the %d the OS allows: %s", len(path), maxSocketPath, path)
	}
	if err := os.MkdirAll(c.runDir, 0o700); err != nil {
		return nil, fmt.Errorf("colleague socket directory: %w", err)
	}
	if err := os.Chmod(c.runDir, 0o700); err != nil { // the directory's mode is the authentication
		return nil, fmt.Errorf("colleague socket directory: %w", err)
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen as colleague %s: %w", id, err)
	}
	_ = os.Chmod(path, 0o600)

	in := newInbox(c, id, ln, deliver)
	c.mu.Lock()
	c.bound[id] = in
	c.mu.Unlock()
	go in.serve()

	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			delete(c.bound, id)
			c.mu.Unlock()
			in.close()
			_ = os.Remove(path)
		})
	}, nil
}

// HandleEvent implements agent.Channel: the events of a turn started by a
// message to id go to its reply pump (never blocks).
func (c *Channel) HandleEvent(id string, evt types.Event) {
	c.mu.Lock()
	in := c.bound[id]
	c.mu.Unlock()
	if in != nil {
		in.push(evt)
	}
}

// Tools implements agent.Channel: ColleagueList and ColleagueAsk, speaking as
// the session's own id.
func (c *Channel) Tools(ids []string) []atools.Tool {
	self := ""
	if len(ids) > 0 {
		self = ids[0]
	}
	return []atools.Tool{c.listTool(self), c.askTool(self)}
}
