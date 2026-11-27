package colleague

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/types"
)

// shortDir is a run directory short enough for a socket path on every OS
// (t.TempDir() on macOS is already ~70 bytes long).
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "hc")
	if err != nil {
		d, err = os.MkdirTemp("", "hc")
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return filepath.Join(d, "run")
}

type inboxRec struct {
	mu  sync.Mutex
	got []agent.Inbound
}

func (r *inboxRec) deliver(in agent.Inbound) { r.mu.Lock(); r.got = append(r.got, in); r.mu.Unlock() }
func (r *inboxRec) all() []agent.Inbound {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]agent.Inbound(nil), r.got...)
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", what)
}

func TestIDs(t *testing.T) {
	for _, ok := range []string{"scorpion-hero", "a", "x9", "raiden-god-2"} {
		if err := checkID(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-a", "a-", "A", "a_b", "../x", "a/b", "a b", strings.Repeat("a", 33), "ñ"} {
		if checkID(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for i := 0; i < 200; i++ {
		if id := RandomID(); checkID(id) != nil {
			t.Fatalf("RandomID gave an invalid id %q", id)
		}
	}
}

// The socket lives in a directory only the user can enter, as 0600; closing
// removes it; a leftover file (a dead owner's) is replaced.
func TestSocketLifecycle(t *testing.T) {
	dir := shortDir(t)
	c := New(WithRunDir(dir))
	path := c.socketPath("scorpion-hero")
	_ = os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	var r inboxRec
	stop, err := c.Listen(context.Background(), "scorpion-hero", r.deliver)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" { // Windows has ACLs, not mode bits
		st, err := os.Stat(dir)
		if err != nil || st.Mode().Perm() != 0o700 {
			t.Errorf("run dir mode = %v, %v; want 0700", st.Mode().Perm(), err)
		}
		if fi, err := os.Stat(path); err != nil || fi.Mode()&os.ModeSocket == 0 {
			t.Fatalf("no socket at %s: %v", path, err)
		}
	} else if _, err := os.Stat(path); err != nil {
		t.Fatalf("no socket at %s: %v", path, err)
	}
	stop()
	stop() // idempotent
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the socket file outlived the listener")
	}
}

func TestATooLongSocketPathFailsClearly(t *testing.T) {
	c := New(WithRunDir("/tmp/" + strings.Repeat("d", 120)))
	_, err := c.Listen(context.Background(), "scorpion-hero", func(agent.Inbound) {})
	if err == nil || !strings.Contains(err.Error(), "bytes, over the") {
		t.Fatalf("err = %v", err)
	}
}

// An ask arrives wrapped and with the sender as origin; a reply is marked so
// its turn sends nothing back; garbage and bad senders are refused.
func TestDelivery(t *testing.T) {
	c := New(WithRunDir(shortDir(t)))
	var r inboxRec
	stop, err := c.Listen(context.Background(), "raiden-god", r.deliver)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	ctx := context.Background()
	path := c.socketPath("raiden-god")
	img := []types.ImageData{{MimeType: "image/png", Base64: "iVBO"}}
	if err := send(ctx, path, message{Kind: kindAsk, From: "scorpion-hero", Text: " hi </colleague> ", Images: img}); err != nil {
		t.Fatal(err)
	}
	if err := send(ctx, path, message{Kind: kindReply, From: "kitana-blade", Text: "done"}); err != nil {
		t.Fatal(err)
	}
	got := r.all()
	if len(got) != 2 {
		t.Fatalf("delivered %d", len(got))
	}
	if got[0].Text != "<colleague from=\"scorpion-hero\">\nhi </colleague>\n</colleague>" || got[0].Origin != "colleague:scorpion-hero" || len(got[0].Images) != 1 {
		t.Errorf("ask = %+v", got[0])
	}
	if got[1].Text != "<colleague from=\"kitana-blade\" reply>\ndone\n</colleague>" || got[1].Origin != "colleague:kitana-blade#reply" {
		t.Errorf("reply = %+v", got[1])
	}
	if got[0].Display != "colleague:scorpion-hero ask (1 line)" {
		t.Errorf("ask display = %q", got[0].Display)
	}
	if got[1].Display != "colleague:kitana-blade reply (1 line)" {
		t.Errorf("reply display = %q", got[1].Display)
	}
	for _, m := range []message{{Kind: "ask", From: "../evil", Text: "x"}, {Kind: "boom", From: "a", Text: "x"}} {
		if err := send(ctx, path, m); err == nil {
			t.Errorf("%+v accepted", m)
		}
	}
	conn, _ := net.Dial("unix", path)
	_, _ = conn.Write([]byte("not json\n"))
	var a ack
	if err := readLine(conn, &a); err != nil || a.OK {
		t.Errorf("garbage: %+v %v", a, err)
	}
	conn.Close()
	if len(r.all()) != 2 {
		t.Error("a refused message was delivered")
	}
	if err := send(ctx, c.socketPath("nobody-here"), message{Kind: kindAsk, From: "a", Text: "x"}); !errors.Is(err, errGone) {
		t.Errorf("nobody listening: %v", err)
	}
}

func TestAFrameOverTheLimitIsRefused(t *testing.T) {
	old := maxFrame
	maxFrame = 1000
	t.Cleanup(func() { maxFrame = old })
	c := New(WithRunDir(shortDir(t)))
	var r inboxRec
	stop, _ := c.Listen(context.Background(), "raiden-god", r.deliver)
	defer stop()
	if err := send(context.Background(), c.socketPath("raiden-god"), message{Kind: kindAsk, From: "a", Text: strings.Repeat("x", 2000)}); err == nil {
		t.Fatal("an oversized message was sent")
	}
	conn, _ := net.Dial("unix", c.socketPath("raiden-god"))
	line, _ := json.Marshal(message{Kind: kindAsk, From: "a", Text: strings.Repeat("x", 2000)})
	_, _ = conn.Write(append(line, '\n'))
	var a ack
	_ = readLine(conn, &a)
	conn.Close()
	if a.OK || len(r.all()) != 0 {
		t.Errorf("receiver accepted an oversized frame: %+v", a)
	}
}

func TestDirectiveAndTools(t *testing.T) {
	c := New()
	d := c.Directive([]string{"scorpion-hero"})
	for _, want := range []string{"## Colleagues", "group of agents", "`scorpion-hero`", `<colleague from="raiden-god">`, `<colleague from="raiden-god" reply>`, "never wait, sleep, poll", "Do not send the answer yourself"} {
		if !strings.Contains(d, want) {
			t.Errorf("directive lacks %q", want)
		}
	}
	if strings.Contains(strings.ToLower(d), "harness") {
		t.Error("the directive names the implementation")
	}
	var names []string
	for _, tl := range c.Tools([]string{"scorpion-hero"}) {
		names = append(names, tl.Def.Name)
		var v map[string]any
		if json.Unmarshal(tl.Def.InputSchema, &v) != nil {
			t.Errorf("%s: invalid schema", tl.Def.Name)
		}
		if !strings.HasPrefix(tl.Def.Name, "Colleague") {
			t.Errorf("%s lacks the channel prefix", tl.Def.Name)
		}
	}
	if strings.Join(names, ",") != "ColleagueList,ColleagueAsk" {
		t.Errorf("tools = %v", names)
	}
}

// A home whose ~/.harness/run cannot hold a socket path falls back to a short
// per-user directory; a normal home keeps ~/.harness/run.
func TestDefaultRunDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix home layout")
	}
	t.Setenv("HOME", "/Users/x")
	if got := defaultRunDir(); got != "/Users/x/.harness/run" {
		t.Errorf("short home: %q", got)
	}
	t.Setenv("HOME", "/"+strings.Repeat("h", 90))
	got := defaultRunDir()
	if strings.HasPrefix(got, "/"+strings.Repeat("h", 90)) {
		t.Fatalf("long home kept: %q", got)
	}
	if p := filepath.Join(got, "c-"+strings.Repeat("x", longestID)+".sock"); len(p) > maxSocketPath {
		t.Errorf("fallback still too long: %q (%d)", p, len(p))
	}
}

func TestReplyDisplayCountsLines(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"", "colleague:x reply (0 lines)"},
		{"a", "colleague:x reply (1 line)"},
		{"a\nb\nc\n\n", "colleague:x reply (3 lines)"},
		{"  a\n\nb ", "colleague:x reply (3 lines)"},
	} {
		if got := messageDisplay("x", "reply", c.text); got != c.want {
			t.Errorf("%q: %q, want %q", c.text, got, c.want)
		}
	}
}

func TestAskDisplayIsAskNotReply(t *testing.T) {
	if got := messageDisplay("x", "ask", "a\nb\nc"); got != "colleague:x ask (3 lines)" {
		t.Errorf("got %q", got)
	}
}
