package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// reader is the read half shared by configstore.SettingsStore and
// configstore.CredentialsStore — the generic helpers below work over either
// without the managers caring which port they hold.
type reader interface {
	Get(namespace, key string) ([]byte, bool, error)
	List(namespace string) (map[string][]byte, error)
}

// getJSON decodes (namespace, key) into out, reporting whether a value was
// found and decoded. A store error or an undecodable value counts as
// "not found": the managers' read methods have no error return, and a
// missing/unreadable entry falling back to its zero value is exactly what an
// absent config file always meant.
func getJSON(s reader, namespace, key string, out any) bool {
	raw, found, err := s.Get(namespace, key)
	if err != nil || !found {
		return false
	}
	return json.Unmarshal(raw, out) == nil
}

// listJSON decodes every entry of namespace into a fresh map[string]T,
// skipping entries that fail to decode (same "unreadable == absent" rule as
// getJSON). Always returns a non-nil map.
func listJSON[T any](s reader, namespace string) map[string]T {
	out := map[string]T{}
	entries, err := s.List(namespace)
	if err != nil {
		return out
	}
	for k, raw := range entries {
		var v T
		if json.Unmarshal(raw, &v) == nil {
			out[k] = v
		}
	}
	return out
}

// entryKey derives the store key for an entry whose identity is made of
// several parts (e.g. a schedule's owner+slug, a chat binding's cwd+chat).
// The key is a hash, never parsed back: the entry's value always carries its
// own identity fields, so nothing ever needs to split a key apart, and no
// separator character can ever collide with the data.
//
// Deterministic — the same identity always yields the same key, which is what
// makes a lookup a direct Get and keeps two processes writing the same
// identity on ONE entry (serialized by the store) instead of creating
// duplicates. The parts are JSON-encoded before hashing, so boundaries are
// unambiguous: ("a","bc") and ("ab","c") never collide. SHA-256 truncated to
// 128 bits (32 hex chars): an identifier, not a security boundary, and far
// past any realistic collision risk at this scale.
func entryKey(parts ...any) string {
	b, _ := json.Marshal(parts)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}
