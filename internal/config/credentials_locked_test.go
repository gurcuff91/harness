package config

import (
	"path/filepath"
	"testing"
	"time"
)

// The regression: persisting a credential from INSIDE a WithLock closure must
// not deadlock. SetCredential re-acquires the (non-re-entrant) file lock and
// hangs until it times out (~2s), silently dropping the write —
// SetCredentialLocked skips the lock the caller already holds.
func TestSetCredentialLockedDoesNotDeadlockInsideWithLock(t *testing.T) {
	dir := t.TempDir()
	m := &CredentialsManager{path: filepath.Join(dir, "credentials.json")}

	done := make(chan error, 1)
	go func() {
		done <- m.WithLock(func() error {
			return m.SetCredentialLocked("claude-oauth", ProviderCredential{
				Type:         "oauth",
				AccessToken:  "at-new",
				RefreshToken: "rt-new",
				ExpiresAt:    time.Now().Add(time.Hour).UnixMilli(),
			})
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SetCredentialLocked inside WithLock returned error: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("DEADLOCK: SetCredentialLocked inside WithLock did not return within 1s — it re-acquired the non-re-entrant file lock")
	}

	// And it must actually have PERSISTED (the whole point — the dropped write
	// was what poisoned the OAuth refresh token).
	got, ok := m.OAuth("claude-oauth")
	if !ok {
		t.Fatal("credential was not persisted")
	}
	if got.RefreshToken != "rt-new" || got.AccessToken != "at-new" {
		t.Errorf("persisted creds = %+v, want the rotated at-new/rt-new", got)
	}
}

// Sanity: SetCredentialLocked also works standalone (no WithLock), since it's
// just SetCredential minus the file-lock acquisition. The intra-process mutex
// still protects it.
func TestSetCredentialLockedPersistsStandalone(t *testing.T) {
	dir := t.TempDir()
	m := &CredentialsManager{path: filepath.Join(dir, "credentials.json")}
	if err := m.SetCredentialLocked("anthropic", ProviderCredential{Type: "api_key", APIKey: "k"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if k, ok := m.APIKey("anthropic"); !ok || k != "k" {
		t.Errorf("APIKey = %q, ok=%v; want \"k\", true", k, ok)
	}
}
