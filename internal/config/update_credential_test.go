package config

import (
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// UpdateCredential is the ONLY read-modify-write primitive for credentials —
// there is deliberately no lower-level "give me the lock" API (the removed
// WithLock). These tests exercise the guarantees that replaced it.

// A missing credential is reported via ok=false, and fn can choose not to write.
func TestUpdateCredential_MissingIsReportedNotWritten(t *testing.T) {
	dir := t.TempDir()
	m := &CredentialsManager{path: filepath.Join(dir, "credentials.json")}

	var sawOK bool
	err := m.UpdateCredential("claude-oauth", func(cur ProviderCredential, ok bool) (ProviderCredential, bool, error) {
		sawOK = ok
		return cur, false, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sawOK {
		t.Error("ok should be false for a provider with no stored credential")
	}
	if _, ok := m.Credential("claude-oauth"); ok {
		t.Error("nothing should have been persisted")
	}
}

// write=true persists fn's returned value; write=false does not, even if fn
// computed something.
func TestUpdateCredential_WriteFlagControlsPersistence(t *testing.T) {
	dir := t.TempDir()
	m := &CredentialsManager{path: filepath.Join(dir, "credentials.json")}

	cred := ProviderCredential{Type: "oauth", AccessToken: "at", RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}

	// write=false: must not persist.
	if err := m.UpdateCredential("claude-oauth", func(ProviderCredential, bool) (ProviderCredential, bool, error) {
		return cred, false, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Credential("claude-oauth"); ok {
		t.Fatal("write=false must not persist")
	}

	// write=true: must persist.
	if err := m.UpdateCredential("claude-oauth", func(ProviderCredential, bool) (ProviderCredential, bool, error) {
		return cred, true, nil
	}); err != nil {
		t.Fatal(err)
	}
	got, ok := m.Credential("claude-oauth")
	if !ok || got.AccessToken != "at" {
		t.Fatalf("write=true should have persisted, got %+v ok=%v", got, ok)
	}
}

// fn's own error propagates and aborts the write, even if fn also returned write=true.
func TestUpdateCredential_FnErrorPropagatesAndSkipsWrite(t *testing.T) {
	dir := t.TempDir()
	m := &CredentialsManager{path: filepath.Join(dir, "credentials.json")}
	sentinel := errors.New("refresh failed")

	err := m.UpdateCredential("claude-oauth", func(ProviderCredential, bool) (ProviderCredential, bool, error) {
		return ProviderCredential{Type: "oauth", AccessToken: "x", RefreshToken: "y", ExpiresAt: 1}, true, sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected the sentinel error, got %v", err)
	}
	if _, ok := m.Credential("claude-oauth"); ok {
		t.Error("an fn error must prevent the write")
	}
}

// An invalid credential returned with write=true is rejected (validated like
// SetCredential) and not persisted.
func TestUpdateCredential_ValidatesBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	m := &CredentialsManager{path: filepath.Join(dir, "credentials.json")}

	err := m.UpdateCredential("claude-oauth", func(ProviderCredential, bool) (ProviderCredential, bool, error) {
		return ProviderCredential{Type: "oauth"}, true, nil // missing access/refresh/expires
	})
	if !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("expected ErrInvalidCredential, got %v", err)
	}
	if _, ok := m.Credential("claude-oauth"); ok {
		t.Error("invalid credential must not be persisted")
	}
}

// The core regression this whole redesign fixes: fn must see the FRESHEST
// on-disk state (written by a "concurrent" writer between two UpdateCredential
// calls), so a caller can decide "someone already refreshed, don't redeem
// again" — this is what prevents a double redemption of a single-use OAuth
// refresh token.
func TestUpdateCredential_SeesFreshestDiskStateAcrossCalls(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	writer := &CredentialsManager{path: path}
	reader := &CredentialsManager{path: path} // simulates a second process

	// "Process A" persists a fresh token.
	if err := writer.SetCredential("claude-oauth", ProviderCredential{
		Type: "oauth", AccessToken: "at-fresh", RefreshToken: "rt-fresh",
		ExpiresAt: time.Now().Add(time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}

	// "Process B" (a separate manager instance, own in-memory cache) must see
	// A's write via UpdateCredential's mandatory reload, not a stale copy.
	var seenToken string
	err := reader.UpdateCredential("claude-oauth", func(cur ProviderCredential, ok bool) (ProviderCredential, bool, error) {
		seenToken = cur.AccessToken
		return cur, false, nil // just observing — the point is what it SAW
	})
	if err != nil {
		t.Fatal(err)
	}
	if seenToken != "at-fresh" {
		t.Fatalf("fn saw stale token %q, want the freshly-written \"at-fresh\"", seenToken)
	}
}

// No deadlock, no re-entrant lock possible: UpdateCredential takes the lock
// once internally and there is no API to ask for it again from inside fn.
// This also stresses that concurrent UpdateCredential calls serialize instead
// of corrupting each other (mutual exclusion via the file lock).
func TestUpdateCredential_ConcurrentCallsSerializeCleanly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	var inside atomic.Int32
	var wg sync.WaitGroup

	for i := range 10 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m := &CredentialsManager{path: path} // separate instance per goroutine, like separate processes
			err := m.UpdateCredential("claude-oauth", func(cur ProviderCredential, ok bool) (ProviderCredential, bool, error) {
				n := inside.Add(1)
				defer inside.Add(-1)
				if n > 1 {
					t.Errorf("two callers inside UpdateCredential's critical section at once: %d", n)
				}
				time.Sleep(time.Millisecond)
				return ProviderCredential{
					Type: "oauth", AccessToken: "at", RefreshToken: "rt",
					ExpiresAt: time.Now().Add(time.Hour).UnixMilli(),
				}, true, nil
			})
			if err != nil {
				t.Errorf("goroutine %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
}
