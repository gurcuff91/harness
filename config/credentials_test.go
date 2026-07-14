package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newTestCreds(t *testing.T, initial string) *CredentialsManager {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	if initial != "" {
		if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	m := &CredentialsManager{path: path}
	m.load()
	return m
}

// TestCredentialValidation checks per-type required fields.
func TestCredentialValidation(t *testing.T) {
	m := newTestCreds(t, "")
	bad := map[string]ProviderCredential{
		"unknown-type":     {Type: "bogus"},
		"empty-type":       {Type: ""},
		"apikey-missing":   {Type: "api_key"},
		"oauth-no-access":  {Type: "oauth", RefreshToken: "r", ExpiresAt: 1},
		"oauth-no-refresh": {Type: "oauth", AccessToken: "a", ExpiresAt: 1},
		"oauth-no-expiry":  {Type: "oauth", AccessToken: "a", RefreshToken: "r"},
	}
	for name, c := range bad {
		if err := m.SetCredential(name, c); err == nil {
			t.Errorf("%s: expected error", name)
		} else if !errors.Is(err, ErrInvalidCredential) {
			t.Errorf("%s: expected ErrInvalidCredential, got %v", name, err)
		}
		if _, ok := m.Credential(name); ok {
			t.Errorf("%s: invalid credential was persisted", name)
		}
	}
	// subscription_type is optional for oauth.
	good := map[string]ProviderCredential{
		"api":   {Type: "api_key", APIKey: "k"},
		"oauth": {Type: "oauth", AccessToken: "a", RefreshToken: "r", ExpiresAt: 1},
	}
	for name, c := range good {
		if err := m.SetCredential(name, c); err != nil {
			t.Errorf("%s: expected accepted, got %v", name, err)
		}
	}
}

// TestCredentialRoundTrip verifies typed store/load/delete across a reload.
func TestCredentialRoundTrip(t *testing.T) {
	m := newTestCreds(t, "")
	m.SetCredential("minimax", ProviderCredential{Type: "api_key", APIKey: "kb_x"})
	m.SetCredential("claude-oauth", ProviderCredential{
		Type: "oauth", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 123, SubscriptionType: "team",
	})

	m2 := &CredentialsManager{path: m.path}
	m2.load()
	if c, ok := m2.Credential("minimax"); !ok || c.APIKey != "kb_x" {
		t.Errorf("minimax not persisted: %+v", c)
	}
	if c, ok := m2.Credential("claude-oauth"); !ok || c.AccessToken != "at" || c.SubscriptionType != "team" {
		t.Errorf("oauth not persisted: %+v", c)
	}
	if err := m2.DeleteCredential("minimax"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := m2.Credential("minimax"); ok {
		t.Errorf("minimax still present after delete")
	}
}

// TestCredentialTypedGetters verifies APIKey/OAuth return only matching types.
func TestCredentialTypedGetters(t *testing.T) {
	m := newTestCreds(t, `{"providers":{
		"minimax":{"type":"api_key","api_key":"k"},
		"claude-oauth":{"type":"oauth","access_token":"a","refresh_token":"r","expires_at":1}
	}}`)
	// APIKey returns the key only for api_key credentials.
	if k, ok := m.APIKey("minimax"); !ok || k != "k" {
		t.Errorf("APIKey(minimax) = %q,%v", k, ok)
	}
	if _, ok := m.APIKey("claude-oauth"); ok {
		t.Errorf("APIKey should not return an oauth credential")
	}
	// OAuth returns only oauth credentials.
	if c, ok := m.OAuth("claude-oauth"); !ok || c.AccessToken != "a" {
		t.Errorf("OAuth(claude-oauth) = %+v,%v", c, ok)
	}
	if _, ok := m.OAuth("minimax"); ok {
		t.Errorf("OAuth should not return an api_key credential")
	}
}
