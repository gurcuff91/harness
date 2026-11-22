package colleague

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/types"
)

// inbox is one bound id: it accepts messages on the socket and delivers them
// to the session, and it sends each ask's answer back when its turn ends.
type inbox struct {
	c       *Channel
	id      string
	ln      net.Listener
	deliver func(agent.Inbound)
	// sessionName reads the bound session's current name (agent.SessionName
	// on the Listen ctx); answers a ping.
	sessionName func() string
	wg          sync.WaitGroup

	mu     sync.Mutex
	queue  []types.Event
	wake   chan struct{}
	closed bool
	done   chan struct{}

	// the turn being answered: who asked (empty for a reply's turn, which
	// sends nothing back), and its text so far
	asker string
	text  strings.Builder
	err   string
}

func newInbox(c *Channel, id string, ln net.Listener, deliver func(agent.Inbound), sessionName func() string) *inbox {
	in := &inbox{c: c, id: id, ln: ln, deliver: deliver, sessionName: sessionName, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go in.pump()
	return in
}

// serve accepts connections until the listener closes.
func (in *inbox) serve() {
	for {
		conn, err := in.ln.Accept()
		if err != nil {
			return
		}
		in.wg.Add(1)
		go func() {
			defer in.wg.Done()
			in.handle(conn)
		}()
	}
}

// handle reads one message and queues it as a prompt of the session.
func (in *inbox) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(ioTimeout))
	var m message
	if err := readLine(conn, &m); err != nil {
		respond(conn, fmt.Errorf("bad message: %v", err))
		return
	}
	if err := checkID(m.From); err != nil {
		respond(conn, err)
		return
	}
	var text string
	switch m.Kind {
	case kindPing:
		a := ack{OK: true}
		if in.sessionName != nil {
			a.Name = in.sessionName()
		}
		line, _ := json.Marshal(a)
		_, _ = conn.Write(append(line, '\n'))
		return
	case kindAsk:
		text = wrap(m.From, false, m.Text)
	case kindReply:
		text = wrap(m.From, true, m.Text)
	default:
		respond(conn, fmt.Errorf("unknown message kind %q", m.Kind))
		return
	}
	in.c.logger.Info("colleague", "received", "to", in.id, "from", m.From, "kind", m.Kind)
	in.deliver(agent.Inbound{Text: text, Images: m.Images, Origin: kindOrigin(m.Kind, m.From)})
	respond(conn, nil)
}

// wrap puts a colleague's message in its tag. The sender is a checked id
// (a-z0-9-), so it needs no escaping.
func wrap(from string, reply bool, text string) string {
	open := `<colleague from="` + from + `"`
	if reply {
		open += ` reply`
	}
	return open + ">\n" + strings.TrimSpace(text) + "\n</colleague>"
}

// kindOrigin is the prompt origin of a message: "colleague:<sender>" for an
// ask; a reply's carries a marker so its turn sends nothing back.
func kindOrigin(kind, from string) string {
	if kind == kindReply {
		return Type + ":" + from + replySuffix
	}
	return Type + ":" + from
}

// replySuffix marks the origin of a reply's turn (display stays "colleague:…").
const replySuffix = "#reply"

// askerOf is who must get this turn's answer: the sender of an ASK, nobody
// for a reply (that is what makes a loop impossible) or for any other origin.
func askerOf(origin string) string {
	from, ok := strings.CutPrefix(origin, Type+":")
	if !ok || strings.HasSuffix(from, replySuffix) || checkID(from) != nil {
		return ""
	}
	return from
}

// push queues one event of a turn this inbox owns (never blocks).
func (in *inbox) push(e types.Event) {
	in.mu.Lock()
	if in.closed {
		in.mu.Unlock()
		return
	}
	in.queue = append(in.queue, e)
	in.mu.Unlock()
	select {
	case in.wake <- struct{}{}:
	default:
	}
}

// pump consumes the turn events in order and answers each ask.
func (in *inbox) pump() {
	defer close(in.done)
	for {
		in.mu.Lock()
		batch := in.queue
		in.queue = nil
		closed := in.closed
		in.mu.Unlock()
		for _, e := range batch {
			in.handleEvent(e)
		}
		if closed && len(batch) == 0 {
			return
		}
		if len(batch) == 0 {
			<-in.wake
		}
	}
}

func (in *inbox) handleEvent(e types.Event) {
	switch e.Type {
	case types.EventReceivedPrompt, types.EventFollowUpStart:
		in.asker, in.err = "", ""
		in.text.Reset()
		in.asker = askerOf(e.Origin)
	case types.EventStreamTextDelta:
		in.text.WriteString(e.Delta)
	case types.EventStreamTextEnd, types.EventToolCall:
		// The answer is the turn's FINAL text: text before a tool call was
		// commentary, not the answer.
		if e.Type == types.EventToolCall {
			in.text.Reset()
		}
	case types.EventError:
		in.err = e.Message
	case types.EventTurnEnd:
		asker := in.asker
		in.asker = ""
		if asker == "" {
			return
		}
		answer := strings.TrimSpace(in.text.String())
		if in.err != "" {
			answer = strings.TrimSpace(answer + "\n\n[the colleague's turn failed: " + in.err + "]")
		}
		if answer == "" {
			answer = "[the colleague answered nothing]"
		}
		in.sendReply(asker, answer)
	}
}

// repliesSent counts the replies this process tried to send (tests).
var repliesSent atomic.Int64

// sendReply delivers an answer; a colleague that left loses it (logged).
func (in *inbox) sendReply(to, text string) {
	repliesSent.Add(1)
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout+ioTimeout)
	defer cancel()
	err := send(ctx, in.c.socketPath(to), message{Kind: kindReply, From: in.id, Text: text})
	if err != nil {
		if errors.Is(err, errGone) {
			in.c.logger.Info("colleague", "reply_lost", "from", in.id, "to", to, "reason", "not online")
			return
		}
		in.c.logger.Error("colleague", "reply_failed", "from", in.id, "to", to, "error", err.Error())
	}
}

// close stops accepting, waits for in-flight messages and for the queued
// turn events to be handled.
func (in *inbox) close() {
	_ = in.ln.Close()
	in.wg.Wait()
	in.mu.Lock()
	if !in.closed {
		in.closed = true
	}
	in.mu.Unlock()
	select {
	case in.wake <- struct{}{}:
	default:
	}
	<-in.done
}
