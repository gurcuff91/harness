package providers

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
)

// fakeJWT builds an unsigned JWT whose payload carries the given account id in
// the namespaced auth claim, the shape a real Codex access token has.
func fakeJWT(t *testing.T, accountID string) string {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": accountID},
	})
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"none"}`)) + "." + enc.EncodeToString(payload) + ".sig"
}

func TestCodexCredFromDiskRecoversAccountIDFromAccessToken(t *testing.T) {
	got := codexCredFromDisk(config.ProviderCredential{Type: "oauth", AccessToken: fakeJWT(t, "acct-from-token"), RefreshToken: "r"})
	if got.AccountID != "acct-from-token" {
		t.Fatalf("AccountID = %q, want it recovered from the access token", got.AccountID)
	}
	stored := codexCredFromDisk(config.ProviderCredential{Type: "oauth", AccessToken: fakeJWT(t, "other"), AccountID: "acct-stored"})
	if stored.AccountID != "acct-stored" {
		t.Fatalf("AccountID = %q, a stored account id must win", stored.AccountID)
	}
}

// A refresh used to persist through claude's oauthProviderCredential, which has
// no AccountID: every refresh erased it from disk. It must survive.
func TestCodexRefreshKeepsAccountIDOnDisk(t *testing.T) {
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No id_token: the account id must come from what was already known.
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "new-opaque", "refresh_token": "r2", "expires_in": 3600})
	}))
	t.Cleanup(srv.Close)
	prev := codexTokenURL
	codexTokenURL = srv.URL
	t.Cleanup(func() { codexTokenURL = prev })

	if err := config.GetCredentialsManager().SetCredential("codex-oauth", config.ProviderCredential{
		Type: "oauth", AccessToken: "old", RefreshToken: "r1", AccountID: "acct-1",
		ExpiresAt: time.Now().Add(-time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	tm := newCodexTokenManager()
	tok, err := tm.getValidToken()
	if err != nil || tok != "new-opaque" {
		t.Fatalf("getValidToken = %q, %v", tok, err)
	}
	cred, _, _ := config.GetCredentialsManager().Credential("codex-oauth")
	if cred.AccountID != "acct-1" || cred.AccessToken != "new-opaque" {
		t.Fatalf("stored after refresh: account=%q token=%q, want acct-1/new-opaque", cred.AccountID, cred.AccessToken)
	}
}
