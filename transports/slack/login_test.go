package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestVerifyAndSaveDoesNotSaveOnAuthTestFailure is the regression test for
// the guarantee `harness slack login` relies on: VerifyAndSave must call
// Slack's real auth.test (exercising workspace + xoxc + xoxd together, since
// NewBot builds its client from all three and apiCall sends them all on
// every request) and refuse to persist anything to ~/.harness/slack.json if
// that call fails — never save a workspace/xoxc/xoxd combination that
// doesn't actually work. Uses a fake Slack server that always rejects
// auth.test to simulate an invalid credential combination without touching
// the real Slack API.
func TestVerifyAndSaveDoesNotSaveOnAuthTestFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate from the real ~/.harness

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid_auth"})
	}))
	defer srv.Close()

	_, err := VerifyAndSave(context.Background(), srv.URL, "xoxc-bad", "xoxd-bad")
	if err == nil {
		t.Fatal("VerifyAndSave succeeded despite auth.test rejecting the credentials")
	}
	if !strings.Contains(err.Error(), "auth.test failed") {
		t.Errorf("error = %q, want it to mention auth.test failing", err.Error())
	}

	saved, loadErr := LoadCredentials()
	if loadErr != nil {
		t.Fatalf("LoadCredentials: %v", loadErr)
	}
	if saved != nil {
		t.Errorf("credentials were saved despite auth.test failing: %+v", saved)
	}
}

// TestVerifyAndSaveSavesOnAuthTestSuccess verifies the other side: with a
// fake Slack server that accepts auth.test, VerifyAndSave both returns the
// verified identity AND persists the credentials — proving the save path
// is genuinely gated by the verification, not skipped/independent of it.
func TestVerifyAndSaveSavesOnAuthTestSuccess(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "user_id": "U123", "team": "Test Team",
		})
	}))
	defer srv.Close()

	creds, err := VerifyAndSave(context.Background(), srv.URL, "xoxc-good", "xoxd-good")
	if err != nil {
		t.Fatalf("VerifyAndSave: %v", err)
	}
	if creds.UserID != "U123" || creds.Team != "Test Team" {
		t.Errorf("creds = %+v, want UserID=U123 Team=\"Test Team\"", creds)
	}

	saved, loadErr := LoadCredentials()
	if loadErr != nil {
		t.Fatalf("LoadCredentials: %v", loadErr)
	}
	if saved == nil {
		t.Fatal("credentials were not saved despite auth.test succeeding")
	}
	if saved.XoxC != "xoxc-good" || saved.XoxD != "xoxd-good" {
		t.Errorf("saved = %+v, want the verified xoxc/xoxd", saved)
	}
}

// TestDeriveXoxCRejectsInvalidWorkspaceOrCookie verifies the FIRST
// verification step of the interactive login flow (before VerifyAndSave is
// even reached): DeriveXoxC — which uses workspace + xoxd — must fail
// cleanly on a workspace that doesn't return a page containing an
// api_token, rather than deriving a bogus xoxc that VerifyAndSave would
// then have to catch.
func TestDeriveXoxCRejectsInvalidWorkspaceOrCookie(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><body>not a real slack workspace page</body></html>"))
	}))
	defer srv.Close()

	_, err := DeriveXoxC(context.Background(), srv.URL, "xoxd-whatever")
	if err == nil {
		t.Fatal("DeriveXoxC succeeded against a page with no api_token")
	}
	if !strings.Contains(err.Error(), "could not find api_token") {
		t.Errorf("error = %q, want it to mention the missing api_token", err.Error())
	}
}
