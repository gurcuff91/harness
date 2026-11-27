package rpc

import "encoding/json"

// The wire types of the `settings/*` and `credentials/*` requests the binary
// sends when `config.settings` / `config.credentials` has kind "client". The
// two domains have the same five methods with the same types.

// StoreKeyParams are the params of settings/get, settings/delete and the
// credentials equivalents.
type StoreKeyParams struct {
	// Namespace groups one kind of entry (e.g. "core", "provider").
	Namespace string `json:"namespace"`
	// Key identifies the entry inside the namespace.
	Key string `json:"key"`
}

// StoreGetResult is the result of get.
type StoreGetResult struct {
	// Found is false when there is no such entry (Value and Version are then
	// ignored).
	Found bool `json:"found"`
	// Value is the stored JSON document, verbatim.
	Value json.RawMessage `json:"value,omitempty"`
	// Version is an opaque string the client chooses (a counter, an etag, a
	// hash, a row version); the binary only hands it back in swap.
	Version string `json:"version,omitempty"`
}

// StoreSetParams are the params of set: an upsert.
type StoreSetParams struct {
	Namespace string          `json:"namespace"`
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value"`
}

// StoreListParams are the params of list.
type StoreListParams struct {
	Namespace string `json:"namespace"`
}

// StoreEntry is one entry of a list result.
type StoreEntry struct {
	Value   json.RawMessage `json:"value"`
	Version string          `json:"version"`
}

// StoreListResult is the result of list: every entry of the namespace, keyed
// by key (an empty object when there are none).
type StoreListResult struct {
	Entries map[string]StoreEntry `json:"entries"`
}

// StoreSwapParams are the params of swap, a conditional write.
type StoreSwapParams struct {
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
	// ExpectedVersion is the version the entry must have for the write to
	// happen; null means the entry must not exist.
	ExpectedVersion *string `json:"expected_version"`
	// Value is the new document; null deletes the entry.
	Value json.RawMessage `json:"value"`
}

// StoreSwapResult is the result of swap.
type StoreSwapResult struct {
	// Swapped is false when the version did not match; nothing changed.
	Swapped bool `json:"swapped"`
}
