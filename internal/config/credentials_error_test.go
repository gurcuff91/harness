package config

import (
	"errors"
	"testing"

	"github.com/gurcuff91/harness/configstore"
)

// failingCreds is a credentials store whose reads can be made to fail, like a
// remote store that did not answer.
type failingCreds struct {
	configstore.CredentialsStore
	err error
}

func (f *failingCreds) Get(ns, k string) ([]byte, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	return f.CredentialsStore.Get(ns, k)
}

func TestCredentialDistinguishesErrorFromNotFound(t *testing.T) {
	boom := errors.New("transport down")
	st := &failingCreds{CredentialsStore: configstore.NewInMemoryStore()}
	m := NewCredentialsManager(st)
	if err := m.SetCredential("p", ProviderCredential{Type: "api_key", APIKey: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetCredential("o", ProviderCredential{Type: "oauth", AccessToken: "a", RefreshToken: "r", ExpiresAt: 1}); err != nil {
		t.Fatal(err)
	}

	// Not found: (false, nil) on all three.
	if _, ok, err := m.Credential("missing"); ok || err != nil {
		t.Errorf("Credential(missing) = %v, %v; want false, nil", ok, err)
	}
	if _, ok, err := m.APIKey("missing"); ok || err != nil {
		t.Errorf("APIKey(missing) = %v, %v; want false, nil", ok, err)
	}
	if _, ok, err := m.OAuth("missing"); ok || err != nil {
		t.Errorf("OAuth(missing) = %v, %v; want false, nil", ok, err)
	}
	// A credential of the other type is not an error either.
	if _, ok, err := m.APIKey("o"); ok || err != nil {
		t.Errorf("APIKey(oauth cred) = %v, %v; want false, nil", ok, err)
	}

	// Store failure: the error comes back, found=false.
	st.err = boom
	if _, ok, err := m.Credential("p"); ok || !errors.Is(err, boom) {
		t.Errorf("Credential(failing) = %v, %v; want false, the store's error", ok, err)
	}
	if _, ok, err := m.APIKey("p"); ok || !errors.Is(err, boom) {
		t.Errorf("APIKey(failing) = %v, %v; want false, the store's error", ok, err)
	}
	if _, ok, err := m.OAuth("o"); ok || !errors.Is(err, boom) {
		t.Errorf("OAuth(failing) = %v, %v; want false, the store's error", ok, err)
	}
}

func TestCredentialUndecodableValueIsAnError(t *testing.T) {
	st := configstore.NewInMemoryStore()
	if err := st.Set(nsProviders, "p", []byte(`"not an object"`)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := NewCredentialsManager(st).Credential("p"); ok || err == nil {
		t.Errorf("Credential(undecodable) = %v, %v; want false, an error", ok, err)
	}
}
