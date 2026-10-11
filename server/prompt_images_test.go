package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
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

// visionServer is a shared server over an agent whose fake model supports
// vision, so prompts can carry images.
func visionServer(t *testing.T) (*Server, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	llm := &fakeLLM{}
	ts := httptest.NewServer(http.HandlerFunc(llm.handler))
	t.Cleanup(ts.Close)
	prov := "vision-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	if err := agent.NewOpenAIProvider(prov, ts.URL, agent.ProviderWithFetchModels(func() ([]types.ModelMeta, error) {
		return []types.ModelMeta{{ID: "m", Vision: true, ContextWindow: 100000, MaxTokens: 4096}}, nil
	})); err != nil {
		t.Fatal(err)
	}
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), ResourceLoader: resources.NilLoader{}})
	t.Cleanup(func() { a.Close() })
	srv, err := Start(a, "", ServerOptions{Logger: logx.NewNilLogger(), KeepAgentOpen: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv, prov + "/m"
}

// pngOf returns a real, distinct PNG (w×h, solid color c).
func pngOf(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		for y := range h {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func firstEcho(t *testing.T, ch <-chan client.Event) client.Event {
	t.Helper()
	for _, e := range collect(t, ch) {
		if e.Type == "received_prompt" || e.Type == "follow_up_start" {
			return e
		}
	}
	t.Fatal("no prompt echo event")
	return client.Event{}
}

// A single photo and an album: the live echo carries message_index + image
// metadata (no base64), and the image endpoint ALREADY resolves the bytes at
// event time — fetched from inside the event handler, before the turn ends.
func TestLivePromptImagesResolveAtEventTime(t *testing.T) {
	srv, model := visionServer(t)
	c := client.New(srv.Addr())
	sess, err := c.CreateSession(model, "/p", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ev, err := c.StreamEvents(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		images [][]byte
	}{
		{"single photo", [][]byte{pngOf(t, 3, 2, color.RGBA{255, 0, 0, 255})}},
		{"album", [][]byte{
			pngOf(t, 4, 4, color.RGBA{0, 255, 0, 255}),
			pngOf(t, 5, 5, color.RGBA{0, 0, 255, 255}),
			pngOf(t, 6, 6, color.RGBA{9, 9, 9, 255}),
		}},
	}
	for _, tc := range cases {
		var datas []types.ImageData
		for _, b := range tc.images {
			datas = append(datas, types.ImageData{MimeType: "image/png", Base64: base64.StdEncoding.EncodeToString(b)})
		}
		if _, err := c.SendPromptWithImages(sess.ID, tc.name, datas, client.WithOrigin("telegram")); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}

		// Read events until the echo, and fetch its images RIGHT THEN.
		var echo client.Event
		var fetched [][]byte
		for e := range ev {
			if e.Type == "received_prompt" || e.Type == "follow_up_start" {
				echo = e
				for n := range e.Images {
					data, mime, err := c.GetMessageImage(sess.ID, e.MessageIndex, n)
					if err != nil || mime != "image/png" {
						t.Fatalf("%s: image %d at event time = %v, %q", tc.name, n, err, mime)
					}
					fetched = append(fetched, data)
				}
			}
			if e.Type == "turn_end" {
				break
			}
		}
		if strings.Contains(string(echo.Raw), "base64") {
			t.Errorf("%s: the live event carries image bytes", tc.name)
		}
		if len(echo.Images) != len(tc.images) || echo.Origin != "telegram" {
			t.Fatalf("%s: echo images=%d origin=%q", tc.name, len(echo.Images), echo.Origin)
		}
		for i, want := range tc.images {
			if !bytes.Equal(fetched[i], want) {
				t.Errorf("%s: image %d bytes differ", tc.name, i)
			}
			if echo.Images[i].Bytes != len(want) || echo.Images[i].MimeType != "image/png" {
				t.Errorf("%s: ref %d = %+v, want %d bytes image/png", tc.name, i, echo.Images[i], len(want))
			}
		}
	}
}

// The image endpoint: raw bytes, its own Content-Type, immutable caching with
// an ETag (304 on revalidation), 404s and 400s.
func TestMessageImageEndpointHeaders(t *testing.T) {
	srv, model := visionServer(t)
	c := client.New(srv.Addr())
	sess, _ := c.CreateSession(model, "/p", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ev, _ := c.StreamEvents(ctx, sess.ID)
	want := pngOf(t, 2, 2, color.RGBA{1, 2, 3, 255})
	c.SendPromptWithImages(sess.ID, "x", []types.ImageData{{MimeType: "image/png", Base64: base64.StdEncoding.EncodeToString(want)}})
	echo := firstEcho(t, ev)

	url := srv.Addr()
	get := func(path string, hdr map[string]string) *http.Response {
		req, _ := http.NewRequest("GET", "http://"+url+path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	base := "/api/sessions/" + sess.ID + "/messages/"
	resp := get(base+itoa(echo.MessageIndex)+"/images/0", nil)
	var body bytes.Buffer
	body.ReadFrom(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" || !bytes.Equal(body.Bytes(), want) {
		t.Fatalf("status %d, type %q, %d bytes", resp.StatusCode, resp.Header.Get("Content-Type"), body.Len())
	}
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") || !strings.Contains(cc, "max-age=") {
		t.Errorf("Cache-Control = %q, want immutable + max-age", cc)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	if r := get(base+itoa(echo.MessageIndex)+"/images/0", map[string]string{"If-None-Match": etag}); r.StatusCode != http.StatusNotModified {
		t.Errorf("revalidation = %d, want 304", r.StatusCode)
	}
	for path, want := range map[string]int{
		base + itoa(echo.MessageIndex) + "/images/1": 404, // no second image
		base + "999/images/0":                        404, // no such message
		base + "x/images/0":                          400,
		"/api/sessions/nope/messages/0/images/0":     404,
	} {
		if r := get(path, nil); r.StatusCode != want {
			t.Errorf("GET %s = %d, want %d", path, r.StatusCode, want)
		}
	}

	// A reset reuses indices: the ETag must change (new epoch).
	c.CloseSession(sess.ID)
	if a := srv.agent; a != nil {
		s, _ := a.ResumeSession(sess.ID)
		s.Reset()
	}
	if srv.sessionEpoch(sess.ID) == strings.Split(strings.Trim(etag, `"`), "-")[0] {
		t.Error("a reset must change the image cache epoch")
	}
}

// Non-default origins are persisted on the user message and returned by GET
// /messages; the default "user" is omitted.
func TestPromptOriginPersisted(t *testing.T) {
	srv, _, model := sharedServer(t, nil)
	c := client.New(srv.Addr())
	sess, _ := c.CreateSession(model, "/p", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ev, _ := c.StreamEvents(ctx, sess.ID)

	for _, o := range []string{"slack", "", "kaiban"} {
		c.SendPrompt(sess.ID, "via "+o, client.WithOrigin(o))
		collect(t, ev)
	}
	msgs, err := c.GetMessages(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	var origins []string
	for _, m := range msgs {
		if m.Role != types.RoleUser {
			continue
		}
		o := ""
		if m.Meta != nil {
			o = m.Meta.Origin
		}
		origins = append(origins, o)
	}
	if strings.Join(origins, ",") != "slack,,kaiban" {
		t.Fatalf("persisted origins = %q, want slack,(none),kaiban", origins)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
