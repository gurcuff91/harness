package slack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// A realistic decoded d cookie (what DevTools shows / users paste): contains
// "/", "+" and "=", which must be percent-encoded on the wire.
const rawD = "xoxd-AbC/def+GhI=jkl/MnO=="

func TestNormalizeXoxDEncodesAndIsIdempotent(t *testing.T) {
	enc := url.QueryEscape(rawD)
	cases := map[string]string{
		rawD:                    enc, // raw → encoded
		enc:                     enc, // already encoded → as-is (idempotent)
		"d=" + rawD:             enc, // "d=" prefix tolerated
		"  d=" + enc + "\n":     enc,
		`"` + rawD + `"`:        enc, // quotes from a copy
		"":                      "",
		"xoxd-plainSafeChars09": "xoxd-plainSafeChars09",
	}
	for in, want := range cases {
		if got := NormalizeXoxD(in); got != want {
			t.Errorf("NormalizeXoxD(%q) = %q, want %q", in, got, want)
		}
		if got := NormalizeXoxD(NormalizeXoxD(in)); got != want {
			t.Errorf("NormalizeXoxD not idempotent on %q: %q", in, got)
		}
	}
}

// fakeWorkspace mimics current Slack: browser UA required (403 otherwise),
// the session cookie must arrive URL-encoded, /ssb/redirect embeds the token,
// and the root page does NOT (it's a client shell). rootHasToken simulates an
// older workspace whose root still embeds it.
func fakeWorkspace(t *testing.T, rootHasToken bool, gotCookie *string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.UserAgent(), "AppleWebKit") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		c := r.Header.Get("Cookie")
		if gotCookie != nil {
			*gotCookie = c
		}
		validSession := c == "d="+url.QueryEscape(rawD)
		switch {
		case r.URL.Path == "/ssb/redirect" && validSession && !rootHasToken:
			w.Write([]byte(`<html><script>var boot = {"team_id":"T1","api_token":"xoxc-123-456-789-abc","x":1};</script></html>`))
		case r.URL.Path == "/" && validSession && rootHasToken:
			w.Write([]byte(`<html>{"api_token" : "xoxc-root-token"}</html>`))
		default:
			w.Write([]byte(`<html>client shell / sign-in page, no token here</html>`))
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

// Pasted decoded (as DevTools shows it) or already encoded, with or without
// "d=": the cookie reaches Slack encoded and /ssb/redirect yields the token.
func TestDeriveXoxCFromSSBRedirect(t *testing.T) {
	for _, pasted := range []string{rawD, url.QueryEscape(rawD), "d=" + rawD} {
		var cookie string
		ts := fakeWorkspace(t, false, &cookie)
		tok, err := DeriveXoxC(context.Background(), ts.URL+"/", pasted)
		if err != nil || tok != "xoxc-123-456-789-abc" {
			t.Fatalf("DeriveXoxC(%q) = %q, %v", pasted, tok, err)
		}
		if cookie != "d="+url.QueryEscape(rawD) {
			t.Errorf("cookie on the wire = %q, want it URL-encoded", cookie)
		}
	}
}

// Older workspaces whose root still embeds the token keep working (fallback).
func TestDeriveXoxCFallsBackToRoot(t *testing.T) {
	ts := fakeWorkspace(t, true, nil)
	tok, err := DeriveXoxC(context.Background(), ts.URL, rawD)
	if err != nil || tok != "xoxc-root-token" {
		t.Fatalf("DeriveXoxC = %q, %v; want the root token", tok, err)
	}
}

// Errors distinguish "403" from "no api_token (cookie invalid/expired)".
func TestDeriveXoxCErrors(t *testing.T) {
	// Valid UA, wrong cookie → pages load but carry no token.
	ts := fakeWorkspace(t, false, nil)
	_, err := DeriveXoxC(context.Background(), ts.URL, "xoxd-wrong")
	if err == nil || !strings.Contains(err.Error(), "no api_token found") || !strings.Contains(err.Error(), "invalid or expired") {
		t.Errorf("bad cookie error = %v, want the no-api_token message", err)
	}

	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer forbidden.Close()
	_, err = DeriveXoxC(context.Background(), forbidden.URL, rawD)
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Errorf("403 error = %v, want a workspace-returned-403 message", err)
	}

	if _, err := DeriveXoxC(context.Background(), ts.URL, "  "); err == nil {
		t.Error("an empty cookie must be rejected")
	}
}

// Every API request sends the encoded cookie — including for credentials
// saved RAW by older versions (normalized in NewBot) — and VerifyAndSave
// persists the encoded form.
func TestBotSendsEncodedCookieAndSavesIt(t *testing.T) {
	isolateConfig(t)
	var cookie string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie = r.Header.Get("Cookie")
		w.Header().Set("Content-Type", "application/json")
		if cookie != "d="+url.QueryEscape(rawD) {
			w.Write([]byte(`{"ok":false,"error":"invalid_auth"}`))
			return
		}
		w.Write([]byte(`{"ok":true,"user_id":"U1","team":"T"}`))
	}))
	defer api.Close()

	if _, err := NewBot(api.URL, "xoxc-1", rawD).AuthTest(context.Background()); err != nil {
		t.Fatalf("auth.test with a raw (legacy) saved cookie: %v (sent %q)", err, cookie)
	}
	creds, err := VerifyAndSave(context.Background(), api.URL, "xoxc-1", "d="+rawD)
	if err != nil {
		t.Fatal(err)
	}
	if creds.XoxD != url.QueryEscape(rawD) {
		t.Errorf("returned xoxd = %q, want the encoded form", creds.XoxD)
	}
	saved, _ := LoadCredentials()
	if saved == nil || saved.XoxD != url.QueryEscape(rawD) {
		t.Errorf("persisted xoxd = %+v, want the encoded form", saved)
	}
}
