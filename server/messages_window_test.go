package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/types"
)

// windowServer is a server over a REAL FileStore (the default, on-disk path
// the window scan exists for), with a session whose history is seeded from a
// layout string: "m" = a message, "c" = a compaction checkpoint. Texts are
// "m<index>". The seeding handle writes through the same store the agent
// reads from.
func windowServer(t *testing.T, layout string) (srv *Server, c *client.Client, id string) {
	t.Helper()
	srv, c, id, _ = windowServerWithHandle(t, layout)
	return srv, c, id
}

// windowServerWithHandle is windowServer that also returns the seeding handle,
// for tests that append more history afterwards.
func windowServerWithHandle(t *testing.T, layout string) (srv *Server, c *client.Client, id string, h *store.Session) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	fs, err := store.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(gw.Close)
	prov := "win-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	if err := agent.NewOpenAIProvider(prov, gw.URL, agent.ProviderWithFetchModels(func() ([]types.ModelMeta, error) {
		return []types.ModelMeta{{ID: "m", ContextWindow: 100000, MaxTokens: 4096}}, nil
	})); err != nil {
		t.Fatal(err)
	}
	a := agent.New(agent.AgentOptions{Store: fs, ResourceLoader: resources.NilLoader{}})
	t.Cleanup(func() { a.Close() })
	srv, err = Start(a, "", ServerOptions{Logger: logx.NewNilLogger(), KeepAgentOpen: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })

	c = client.New(srv.Addr())
	sess, err := c.CreateSession(prov+"/m", "/p", "")
	if err != nil {
		t.Fatal(err)
	}
	h, err = store.OpenSession(fs, sess.ID)
	if err != nil || h == nil {
		t.Fatalf("open: %v", err)
	}
	for i, ch := range layout {
		if ch == 'c' {
			err = h.AddCompactionSummary(fmt.Sprintf("s%d", i))
		} else {
			err = h.AddMessage(types.NewUserTextMessage(fmt.Sprintf("m%d", i)))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	return srv, c, sess.ID, h
}

func rawGet(t *testing.T, srv *Server, path string) (int, http.Header, []byte) {
	t.Helper()
	resp, err := http.Get("http://" + srv.Addr() + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b
}

func firstTexts(msgs []types.Message) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, m.Parts[0].Text)
	}
	return out
}

// No params: byte-for-byte today's behavior — the whole log as a plain array —
// plus the two headers.
func TestMessagesNoParamsIsTheWholeLog(t *testing.T) {
	srv, c, id := windowServer(t, "mmcmm")
	code, hdr, body := rawGet(t, srv, "/api/sessions/"+id+"/messages")
	if code != 200 || hdr.Get("X-Messages-From") != "0" || hdr.Get("X-Messages-Total") != "5" {
		t.Fatalf("code=%d from=%q total=%q", code, hdr.Get("X-Messages-From"), hdr.Get("X-Messages-Total"))
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(body, &arr); err != nil || len(arr) != 5 {
		t.Fatalf("body is not a plain 5-element array: %v %s", err, body)
	}
	full, err := c.GetMessages(id)
	if err != nil || len(full) != 5 {
		t.Fatalf("GetMessages: %v len=%d", err, len(full))
	}
}

func TestMessagesWindowByCheckpointCount(t *testing.T) {
	// checkpoints at 2, 5, 8
	for _, tc := range []struct {
		name   string
		layout string
		k      int
		from   int
		total  int
	}{
		{"0 checkpoints", "mmmm", 2, 0, 4},
		{"1 checkpoint", "mmcmm", 2, 0, 5},
		{"exactly 2", "mmcmmcmm", 2, 3, 8},
		{"many, k=2", "mmcmmcmmcmm", 2, 6, 11},
		{"many, k=1", "mmcmmcmmcmm", 1, 9, 11},
		{"k beyond count", "mmcmmcmm", 9, 0, 8},
		{"empty log", "", 2, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, c, id := windowServer(t, tc.layout)
			msgs, from, total, err := c.GetMessagesWith(id, client.MessagesOptions{Compactions: tc.k})
			if err != nil {
				t.Fatal(err)
			}
			if from != tc.from || total != tc.total || len(msgs) != tc.total-tc.from {
				t.Fatalf("from=%d total=%d len=%d; want from=%d total=%d len=%d", from, total, len(msgs), tc.from, tc.total, tc.total-tc.from)
			}
			// The first returned message really is the one at absolute index `from`.
			if len(msgs) > 0 && tc.layout[from] == 'm' && firstTexts(msgs)[0] != fmt.Sprintf("m%d", from) {
				t.Errorf("first message = %q, want m%d", firstTexts(msgs)[0], from)
			}
			// The window never opens on the checkpoint itself (the TUI rule).
			if tc.from > 0 && msgs[0].Meta != nil && msgs[0].Meta.IsCompaction {
				t.Error("window opens on a checkpoint marker")
			}
			// An empty window still encodes as [], not null.
			if tc.total == tc.from {
				_, _, body := rawGet(t, srv, "/api/sessions/"+id+"/messages?compactions="+strconv.Itoa(tc.k))
				if strings.TrimSpace(string(body)) != "[]" {
					t.Errorf("empty window body = %s, want []", body)
				}
			}
		})
	}
}

func TestMessagesWindowFrom(t *testing.T) {
	srv, c, id := windowServer(t, "mmcmmcmm")
	msgs, from, total, err := c.GetMessagesWith(id, client.MessagesOptions{From: 5})
	if err != nil || from != 5 || total != 8 || len(msgs) != 3 {
		t.Fatalf("from=5: %v from=%d total=%d len=%d", err, from, total, len(msgs))
	}
	if got := firstTexts(msgs); got[1] != "m6" || got[2] != "m7" {
		t.Errorf("texts = %v", got)
	}
	// from == total and beyond: an empty array, headers say where it is.
	for _, n := range []int{8, 500} {
		code, hdr, body := rawGet(t, srv, "/api/sessions/"+id+"/messages?from="+strconv.Itoa(n))
		if code != 200 || strings.TrimSpace(string(body)) != "[]" || hdr.Get("X-Messages-Total") != "8" || hdr.Get("X-Messages-From") != "8" {
			t.Errorf("from=%d: code=%d body=%s from=%q total=%q", n, code, body, hdr.Get("X-Messages-From"), hdr.Get("X-Messages-Total"))
		}
	}
	// from=0 is the explicit whole log.
	if _, from, total, err := c.GetMessagesWith(id, client.MessagesOptions{}); err != nil || from != 0 || total != 8 {
		t.Errorf("zero options: %v from=%d total=%d", err, from, total)
	}
}

func TestMessagesWindowInvalidParams(t *testing.T) {
	srv, _, id := windowServer(t, "mmcmm")
	base := "/api/sessions/" + id + "/messages"
	for _, q := range []string{
		"?from=-1", "?from=abc", "?from=", "?from=1.5",
		"?compactions=-2", "?compactions=0", "?compactions=x", "?compactions=",
		"?from=1&compactions=2", // mutually exclusive
	} {
		code, _, body := rawGet(t, srv, base+q)
		if code != 400 || !strings.Contains(string(body), `"message"`) {
			t.Errorf("%s: code=%d body=%s; want 400 with an error message", q, code, body)
		}
	}
	if code, _, _ := rawGet(t, srv, "/api/sessions/does-not-exist/messages?compactions=2"); code != 404 {
		t.Errorf("unknown session: code=%d, want 404", code)
	}
}

// A closed (persisted, not active) session is served too — this endpoint used
// to answer 400 "session is not active".
func TestMessagesWindowPersistedSession(t *testing.T) {
	_, c, id := windowServer(t, "mmcmmcmm")
	if _, err := c.CloseSession(id); err != nil {
		t.Fatal(err)
	}
	msgs, from, total, err := c.GetMessagesWith(id, client.MessagesOptions{Compactions: 2})
	if err != nil || from != 3 || total != 8 || len(msgs) != 5 {
		t.Fatalf("persisted: %v from=%d total=%d len=%d", err, from, total, len(msgs))
	}
	if all, err := c.GetMessages(id); err != nil || len(all) != 8 {
		t.Errorf("persisted full: %v len=%d", err, len(all))
	}
}

// X-Messages-From is the absolute index the image endpoint counts from: an
// image in a windowed response is fetched at from + its position in the window.
func TestMessagesWindowIndicesMatchImageEndpoint(t *testing.T) {
	_, c, id, h := windowServerWithHandle(t, "mmcmm")
	img := types.ImageData{MimeType: "image/png", Base64: "iVBORw0KGgo="}
	pic := types.NewUserTextMessage("pic")
	pic.Parts = append(pic.Parts, types.ContentPart{Image: &img})
	if err := h.AddMessage(pic); err != nil { // absolute index 5
		t.Fatal(err)
	}
	if err := h.AddMessage(types.NewUserTextMessage("after")); err != nil {
		t.Fatal(err)
	}
	msgs, from, total, err := c.GetMessagesWith(id, client.MessagesOptions{From: 5})
	if err != nil || from != 5 || total != 7 || len(msgs) != 2 {
		t.Fatalf("%v from=%d total=%d len=%d", err, from, total, len(msgs))
	}
	for pos, m := range msgs {
		for _, p := range m.Parts {
			if p.Image == nil {
				continue
			}
			if _, mime, err := c.GetMessageImage(id, from+pos, 0); err != nil || mime != "image/png" {
				t.Errorf("image at from+%d: mime=%q err=%v", pos, mime, err)
			}
		}
	}
}

// After /reset the log is empty and indices restart — a window reflects that,
// and a fresh message is index 0 again.
func TestMessagesWindowAfterReset(t *testing.T) {
	srv, c, id := windowServer(t, "mmcmmcmm")
	if _, err := c.CloseSession(id); err != nil {
		t.Fatal(err)
	}
	sess, err := srv.agent.ResumeSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Reset(); err != nil {
		t.Fatal(err)
	}
	for _, o := range []client.MessagesOptions{{Compactions: 2}, {From: 3}, {}} {
		msgs, from, total, err := c.GetMessagesWith(id, o)
		if err != nil || len(msgs) != 0 || from != 0 || total != 0 {
			t.Errorf("%+v after reset: %v len=%d from=%d total=%d", o, err, len(msgs), from, total)
		}
	}
}
