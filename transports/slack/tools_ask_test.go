package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func newFakeSlackServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
}

// newFakeSlackServerRecordingCalls is like newFakeSlackServer but records
// which API methods (by URL path suffix) were called — used to verify
// SlackAsk's upload path (files.getUploadURLExternal → PUT →
// files.completeUploadExternal) runs instead of a plain chat.postMessage
// when the question text carries a <slack:uploadFile> tag. The server also
// serves the pre-signed "upload_url" it hands back, pointing right back at
// itself, so the PUT step (which needs no Slack auth) has somewhere to go.
func newFakeSlackServerRecordingCalls(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var calls []string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "files.getUploadURLExternal"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true, "upload_url": srv.URL + "/upload", "file_id": "F123",
			})
		case r.URL.Path == "/upload":
			w.WriteHeader(http.StatusOK)
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		}
	}))
	return srv, &calls
}

// newFakeSlackServerWithChannel is like newFakeSlackServer but resolves
// conversations.list to a single named channel — used to verify SlackAsk's
// rejection also applies to a "#name" input that RESOLVES to a real
// channel via resolveChannelID, not just a bare channel ID passed directly.
func newFakeSlackServerWithChannel(t *testing.T, channelName, channelID string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "conversations.list") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"channels": []map[string]any{
					{"id": channelID, "name": channelName},
				},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
}

// TestSlackAskRejectsChannels verifies the explicit design rule: SlackAsk
// only works for direct messages — a channel ID/name must be rejected with
// a clear error pointing at SlackPost instead, never silently "ask into
// the void" of a multi-person channel where "the" reply is ambiguous.
func TestSlackAskRejectsChannels(t *testing.T) {
	srv := newFakeSlackServer(t)
	defer srv.Close()

	bot := NewBot(srv.URL, "xoxc", "xoxd")
	tr := newTestTransportForAsks()
	tool := slackAskTool(bot, "UMYSELF", tr)

	input, _ := json.Marshal(map[string]any{"channel": "C123CHANNEL", "text": "are you free?"})
	_, _, err := tool.ExecuteRich(context.Background(), input)
	if err == nil {
		t.Fatal("SlackAsk succeeded against a channel ID, want a rejection")
	}
	if !strings.Contains(err.Error(), "SlackAsk only works for direct messages") {
		t.Errorf("error = %q, want it to mention direct-messages-only", err.Error())
	}
}

// TestSlackAskRejectsChannelNameSyntax verifies the rejection ALSO applies
// when the model passes a "#name" instead of a bare channel ID — this is
// the exact case the Khan flagged: resolveChannelID DOES accept "#name"
// syntax and resolves it to a real channel ID via conversations.list (the
// same helper SlackPost/SlackMessages use), so SlackAsk's description must
// not claim "#name" is rejected as invalid INPUT — it's valid syntax that
// still gets rejected downstream because it resolves to a channel, not a
// DM. This test locks in that exact behavior end-to-end.
func TestSlackAskRejectsChannelNameSyntax(t *testing.T) {
	srv := newFakeSlackServerWithChannel(t, "general", "C999GENERAL")
	defer srv.Close()

	bot := NewBot(srv.URL, "xoxc", "xoxd")
	tr := newTestTransportForAsks()
	tool := slackAskTool(bot, "UMYSELF", tr)

	input, _ := json.Marshal(map[string]any{"channel": "#general", "text": "are you free?"})
	_, _, err := tool.ExecuteRich(context.Background(), input)
	if err == nil {
		t.Fatal("SlackAsk succeeded against a '#name' channel, want a rejection")
	}
	if !strings.Contains(err.Error(), "SlackAsk only works for direct messages") {
		t.Errorf("error = %q, want the same direct-messages-only rejection resolveChannelID's result gets", err.Error())
	}
}

// TestSlackAskTimesOutAndUnregisters verifies the Khan-confirmed behavior:
// no reply within the timeout is a NORMAL outcome (no error), and the
// pending ask is cleaned up afterward (via defer) so a later SlackAsk to
// the same DM isn't blocked by a stale entry.
func TestSlackAskTimesOutAndUnregisters(t *testing.T) {
	srv := newFakeSlackServer(t)
	defer srv.Close()

	bot := NewBot(srv.URL, "xoxc", "xoxd")
	tr := newTestTransportForAsks()
	tool := slackAskTool(bot, "UMYSELF", tr)

	input, _ := json.Marshal(map[string]any{"channel": "D123", "text": "are you free?", "timeout": 1})

	start := time.Now()
	text, images, err := tool.ExecuteRich(context.Background(), input)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("timeout should not be an error, got: %v", err)
	}
	if images != nil {
		t.Errorf("expected no images on timeout, got: %v", images)
	}
	if !strings.Contains(text, "No reply within") {
		t.Errorf("text = %q, want a clear timeout message", text)
	}
	if elapsed < time.Second {
		t.Errorf("returned after %v, want it to have actually waited out the 1s timeout", elapsed)
	}

	// The pending ask must be gone now — a fresh SlackAsk to the same DM
	// must succeed immediately (not blocked by a stale "already pending"
	// entry from the one that just timed out).
	if _, err := tr.registerAsk("D123"); err != nil {
		t.Fatalf("registerAsk after timeout should succeed (cleanup via defer), got: %v", err)
	}
}

// TestSlackAskReceivesReplyBeforeTimeout verifies the success path
// end-to-end: a reply delivered via tryDeliverAsk (simulating what
// handleEvent does when the human answers) reaches SlackAsk's blocked
// executor, which returns immediately with that text — well before the
// timeout would have fired.
func TestSlackAskReceivesReplyBeforeTimeout(t *testing.T) {
	srv := newFakeSlackServer(t)
	defer srv.Close()

	bot := NewBot(srv.URL, "xoxc", "xoxd")
	tr := newTestTransportForAsks()
	tool := slackAskTool(bot, "UMYSELF", tr)

	input, _ := json.Marshal(map[string]any{"channel": "D123", "text": "are you free?", "timeout": 30})

	done := make(chan struct {
		text string
		err  error
	}, 1)
	go func() {
		text, _, err := tool.ExecuteRich(context.Background(), input)
		done <- struct {
			text string
			err  error
		}{text, err}
	}()

	// Give the tool a moment to register its ask, then deliver a reply the
	// same way handleEvent would.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if tr.tryDeliverAsk(context.Background(), &RTMEvent{Channel: "D123", Text: "yes, free at 3pm"}) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("unexpected error: %v", res.err)
		}
		if res.text != "yes, free at 3pm" {
			t.Errorf("text = %q, want %q", res.text, "yes, free at 3pm")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SlackAsk never returned after the reply was delivered")
	}
}

// TestSlackAskUploadsFileFromUploadFileTag verifies SlackAsk supports the
// same <slack:uploadFile> attachment mechanism SlackPost already does: a
// question carrying the tag must upload the file via the
// getUploadURLExternal → PUT → completeUploadExternal flow (bot.UploadFile)
// instead of a plain chat.postMessage, and the tag itself must not end up
// literally posted as part of the question text.
func TestSlackAskUploadsFileFromUploadFileTag(t *testing.T) {
	srv, calls := newFakeSlackServerRecordingCalls(t)
	defer srv.Close()

	tmpFile, err := os.CreateTemp(t.TempDir(), "report-*.txt")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := tmpFile.WriteString("hello"); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	tmpFile.Close()

	bot := NewBot(srv.URL, "xoxc", "xoxd")
	tr := newTestTransportForAsks()
	tool := slackAskTool(bot, "UMYSELF", tr)

	question := "Can you check this? <slack:uploadFile>" + tmpFile.Name() + "</slack:uploadFile>"
	input, _ := json.Marshal(map[string]any{"channel": "D123", "text": question, "timeout": 1})

	_, _, err = tool.ExecuteRich(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err) // times out after 1s — that's fine, we're checking the post mechanism
	}

	var sawUploadFlow, sawPlainPost bool
	for _, c := range *calls {
		if strings.Contains(c, "files.getUploadURLExternal") || strings.Contains(c, "files.completeUploadExternal") {
			sawUploadFlow = true
		}
		if strings.Contains(c, "chat.postMessage") {
			sawPlainPost = true
		}
	}
	if !sawUploadFlow {
		t.Errorf("expected the upload flow (getUploadURLExternal/completeUploadExternal) to run, calls: %v", *calls)
	}
	if sawPlainPost {
		t.Errorf("expected chat.postMessage NOT to run when an upload tag is present, calls: %v", *calls)
	}
}

// TestSlackAskRequiresNonEmptyText verifies the input-validation guard.
func TestSlackAskRequiresNonEmptyText(t *testing.T) {
	srv := newFakeSlackServer(t)
	defer srv.Close()

	bot := NewBot(srv.URL, "xoxc", "xoxd")
	tr := newTestTransportForAsks()
	tool := slackAskTool(bot, "UMYSELF", tr)

	input, _ := json.Marshal(map[string]any{"channel": "D123", "text": "   "})
	_, _, err := tool.ExecuteRich(context.Background(), input)
	if err == nil {
		t.Fatal("SlackAsk succeeded with blank text, want an error")
	}
}
