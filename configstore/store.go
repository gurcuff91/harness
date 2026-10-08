// Package configstore defines the persistence ports for harness's
// process-global configuration — the same "dumb store + smart manager"
// split agent/store already uses for sessions (SessionStore underneath,
// *Session on top).
//
// Two ports, deliberately separate types even though their method sets are
// identical:
//
//   - SettingsStore holds non-sensitive configuration (active model,
//     thinking level, MCP servers, custom providers, …).
//   - CredentialsStore holds secrets (provider API keys, OAuth tokens, …).
//
// Keeping them as distinct types lets the compiler stop a settings backend
// from being wired where a secrets backend was expected — e.g. plain files
// for settings, a cloud secret manager for credentials.
//
// Both ports are a flat, namespaced key/value store and NOTHING more. All
// typed access, validation, and domain rules live in harness's own managers
// layered on top (internal/config's SettingsManager/CredentialsManager), so a
// new kind of setting only ever extends a manager — never these interfaces.
//
// Register a custom implementation once, early in main() and before any
// Agent is built, via harness.SetSettingsStore / harness.SetCredentialsStore
// (aliases of agent.SetSettingsStore / agent.SetCredentialsStore). When none
// is registered, harness uses FileStore over ~/.harness/settings.json and
// ~/.harness/credentials.json.
package configstore

import "errors"

// ErrInvalidValue is returned when a value handed to Set/SwapValue is not a
// valid JSON document (see the value contract on SettingsStore).
var ErrInvalidValue = errors.New("configstore: value is not a valid JSON document")

// ErrAlreadyInitialized is returned when registering a store after harness
// has already started using the previous one — swapping the backend under a
// live process would silently split its state across two stores.
var ErrAlreadyInitialized = errors.New("configstore: store already in use; register it before any Agent is built")

// SettingsStore is the persistence port for non-sensitive, process-global
// configuration.
//
// Data model: values are addressed by (namespace, key). A namespace groups
// one kind of entry (e.g. "core", "mcp", "provider"); a key identifies one
// entry inside it. Values are JSON documents, opaque to the store — it never
// inspects them, only stores and returns them verbatim. Implementations may
// rely on them being valid JSON (e.g. to keep a file human-readable, or to
// use a JSON column) and must reject anything else with ErrInvalidValue.
//
// Atomicity is the implementation's job, never the caller's: every method
// must be safe for concurrent use, including across processes sharing the
// same backend (several harness processes commonly run at once), and
// SwapValue must run its read-callback-write cycle atomically per key.
type SettingsStore interface {
	// Get returns the value stored under (namespace, key). found is false,
	// with a nil error, when there is no such entry.
	Get(namespace, key string) (value []byte, found bool, err error)

	// Set stores value under (namespace, key), creating or replacing it
	// (upsert).
	Set(namespace, key string, value []byte) error

	// Delete removes (namespace, key). Deleting a missing entry is not an
	// error.
	Delete(namespace, key string) error

	// List returns every entry in namespace, keyed by key. A namespace with
	// no entries yields an empty (non-nil) map.
	List(namespace string) (map[string][]byte, error)

	// SwapValue performs an atomic read-modify-write of (namespace, key):
	// it reads the current value, hands it to fn, and persists fn's result —
	// all as one atomic step with respect to every other writer of that
	// key, in this process or any other.
	//
	// fn receives the current value (nil, found=false if absent) and
	// returns:
	//   - next:  the value to store (ignored unless write is true); a nil
	//     next with write=true DELETES the entry instead — the atomic
	//     compare-and-delete (deleting a missing entry is a no-op)
	//   - write: whether to store (or delete) at all; false makes the call a
	//     pure read-then-decide with no write
	//   - err:   fn's own failure — returned by SwapValue unchanged, with
	//     nothing written
	//
	// fn runs while the implementation holds whatever guarantees atomicity,
	// so it must not call back into the store.
	SwapValue(namespace, key string, fn func(current []byte, found bool) (next []byte, write bool, err error)) error

	// Close releases any backend resources (connections, handles, …).
	Close() error
}

// CredentialsStore is the persistence port for secrets — same data model and
// contract as SettingsStore (see its documentation), kept as a separate type
// so secrets and ordinary settings can never be wired to the wrong backend
// by accident.
type CredentialsStore interface {
	// Get returns the value stored under (namespace, key). found is false,
	// with a nil error, when there is no such entry.
	Get(namespace, key string) (value []byte, found bool, err error)

	// Set stores value under (namespace, key), creating or replacing it
	// (upsert).
	Set(namespace, key string, value []byte) error

	// Delete removes (namespace, key). Deleting a missing entry is not an
	// error.
	Delete(namespace, key string) error

	// List returns every entry in namespace, keyed by key. A namespace with
	// no entries yields an empty (non-nil) map.
	List(namespace string) (map[string][]byte, error)

	// SwapValue performs an atomic read-modify-write of (namespace, key),
	// including the delete form (nil next with write=true). See
	// SettingsStore.SwapValue for the full contract — this is the
	// primitive that keeps a single-use OAuth refresh token from being
	// redeemed twice by two processes at once.
	SwapValue(namespace, key string, fn func(current []byte, found bool) (next []byte, write bool, err error)) error

	// Close releases any backend resources (connections, handles, …).
	Close() error
}
