package configstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gurcuff91/harness/internal/filelock"
)

// FileStore is the default implementation of both SettingsStore and
// CredentialsStore: one JSON file per store, shaped as
//
//	{ "<namespace>": { "<key>": <value>, … }, … }
//
// where every value is the JSON document handed to Set/SwapValue, embedded
// as-is (so the file stays human-readable and hand-editable).
//
// Atomicity, as required by the port contract:
//   - Within a process: fs.mu serializes every goroutine.
//   - Across processes: every write is a read-modify-write of the WHOLE file
//     under internal/filelock's cross-process lock, always re-reading the
//     latest disk state first — so a concurrent writer's change to any other
//     entry is never silently discarded.
//   - Readers never see a half-written file: writes go to a temp file that is
//     then renamed over the original.
//
// A malformed file (not valid JSON, or not the {namespace: {key: value}}
// shape) is treated as an EMPTY store, silently: reads see no entries and the
// next write replaces the file with a valid one. Never an error, never
// output on stderr — a broken config file must not stop harness from
// starting, and the TUI owns the terminal, so there is nowhere safe to print.
// This matches how harness always handled a broken settings.json /
// credentials.json before the store existed.
//
// Freshness: reads are served from an in-memory snapshot, reloaded whenever
// the file's modification time moves past the last load — another process's
// write becomes visible on this process's very next read, without paying a
// full re-parse on every call when nothing changed.
type FileStore struct {
	path string
	perm os.FileMode

	mu       sync.RWMutex
	data     map[string]map[string]json.RawMessage
	loadedAt time.Time // mtime of the file as of the last load — see reloadIfStale
}

// Compile-time proof FileStore satisfies both ports.
var (
	_ SettingsStore    = (*FileStore)(nil)
	_ CredentialsStore = (*FileStore)(nil)
)

// NewFileStore opens (without creating) the JSON file at path. perm is the
// mode the file is created with on first write — use 0600 for anything that
// may hold secrets. A missing or malformed file is an empty store (see the
// FileStore doc comment); the only errors are genuine I/O failures, e.g. the
// file exists but can't be read.
func NewFileStore(path string, perm os.FileMode) (*FileStore, error) {
	fs := &FileStore{path: path, perm: perm}
	if err := fs.load(); err != nil {
		return nil, err
	}
	return fs, nil
}

// DefaultSettingsPath / DefaultCredentialsPath are the files harness uses
// when no store has been registered: ~/.harness/settings.json and
// ~/.harness/credentials.json.
func DefaultSettingsPath() (string, error)    { return defaultPath("settings.json") }
func DefaultCredentialsPath() (string, error) { return defaultPath("credentials.json") }

func defaultPath(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("configstore: home dir: %w", err)
	}
	return filepath.Join(home, ".harness", name), nil
}

// ── Reads ───────────────────────────────────────────────────────────────

func (fs *FileStore) Get(namespace, key string) ([]byte, bool, error) {
	if err := fs.reloadIfStale(); err != nil {
		return nil, false, err
	}
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	v, ok := fs.data[namespace][key]
	if !ok {
		return nil, false, nil
	}
	return cloneBytes(v), true, nil
}

func (fs *FileStore) List(namespace string) (map[string][]byte, error) {
	if err := fs.reloadIfStale(); err != nil {
		return nil, err
	}
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	out := make(map[string][]byte, len(fs.data[namespace]))
	for k, v := range fs.data[namespace] {
		out[k] = cloneBytes(v)
	}
	return out, nil
}

// ── Writes ──────────────────────────────────────────────────────────────

func (fs *FileStore) Set(namespace, key string, value []byte) error {
	if !json.Valid(value) {
		return ErrInvalidValue
	}
	return fs.mutate(func() (bool, error) {
		fs.put(namespace, key, value)
		return true, nil
	})
}

func (fs *FileStore) Delete(namespace, key string) error {
	return fs.mutate(func() (bool, error) {
		if _, ok := fs.data[namespace][key]; !ok {
			return false, nil
		}
		delete(fs.data[namespace], key)
		if len(fs.data[namespace]) == 0 {
			delete(fs.data, namespace)
		}
		return true, nil
	})
}

// SwapValue runs fn under the cross-process file lock, against the value
// freshly re-read from disk — the guarantee that lets a single-use OAuth
// refresh token be redeemed exactly once even with several harness
// processes racing: whoever wins the lock either finds a token another
// process already refreshed (and writes nothing) or is the one that
// refreshes and persists it, never both. There is deliberately no
// lower-level "give me the lock" primitive: fn never holds a lock handle it
// could try to re-acquire (the lock isn't re-entrant — a nested acquisition
// would just time out and silently drop the write).
func (fs *FileStore) SwapValue(namespace, key string, fn func(current []byte, found bool) (next []byte, write bool, err error)) error {
	return fs.mutate(func() (bool, error) {
		cur, found := fs.data[namespace][key]
		next, write, err := fn(cloneBytes(cur), found)
		if err != nil || !write {
			return false, err
		}
		if !json.Valid(next) {
			return false, ErrInvalidValue
		}
		fs.put(namespace, key, next)
		return true, nil
	})
}

func (fs *FileStore) Close() error { return nil }

// ── Internals ───────────────────────────────────────────────────────────

// mutate is the single write path: take the cross-process lock, re-read the
// latest disk state (another process may have written since this snapshot
// was taken), let apply change fs.data, and persist only if apply asked to.
// On any failure the in-memory snapshot is restored from disk on the next
// read, never left holding a change that didn't land.
func (fs *FileStore) mutate(apply func() (changed bool, err error)) error {
	if err := os.MkdirAll(filepath.Dir(fs.path), 0700); err != nil {
		return fmt.Errorf("configstore: create dir: %w", err)
	}
	release, err := filelock.Acquire(fs.path)
	if err != nil {
		return err
	}
	defer release()

	fs.mu.Lock()
	defer fs.mu.Unlock()
	if err := fs.load(); err != nil {
		return err
	}
	changed, err := apply()
	if err != nil || !changed {
		return err
	}
	if err := fs.save(); err != nil {
		_ = fs.load() // drop the unsaved change; the save error is what matters
		return err
	}
	return nil
}

// put stores value (already validated) under (namespace, key). Caller holds fs.mu.
func (fs *FileStore) put(namespace, key string, value []byte) {
	if fs.data[namespace] == nil {
		fs.data[namespace] = map[string]json.RawMessage{}
	}
	fs.data[namespace][key] = json.RawMessage(cloneBytes(value))
}

// reloadIfStale re-reads the file only when its mtime moved past the last
// load — os.Stat is far cheaper than a full read+parse, so the common case
// (nothing changed) costs almost nothing.
func (fs *FileStore) reloadIfStale() error {
	info, err := os.Stat(fs.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("configstore: stat %s: %w", fs.path, err)
	}
	fs.mu.RLock()
	stale := info.ModTime().After(fs.loadedAt) || fs.loadedAt.IsZero()
	fs.mu.RUnlock()
	if !stale {
		return nil
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if !info.ModTime().After(fs.loadedAt) && !fs.loadedAt.IsZero() {
		return nil // another goroutine reloaded while we waited for the lock
	}
	return fs.load()
}

// load replaces fs.data with the file's current contents and records its
// mtime. A missing or malformed file is an empty store (see the FileStore doc
// comment) — only a genuine read failure is an error. Caller holds fs.mu for
// writing (or is the constructor).
func (fs *FileStore) load() error {
	info, statErr := os.Stat(fs.path)
	raw, err := os.ReadFile(fs.path)
	if err != nil {
		if os.IsNotExist(err) {
			fs.data = map[string]map[string]json.RawMessage{}
			fs.loadedAt = time.Time{}
			return nil
		}
		return fmt.Errorf("configstore: read %s: %w", fs.path, err)
	}
	data := map[string]map[string]json.RawMessage{}
	if len(raw) > 0 && json.Unmarshal(raw, &data) != nil {
		// Malformed: start empty; the next write replaces the file.
		data = map[string]map[string]json.RawMessage{}
	}
	fs.data = data
	if statErr == nil {
		fs.loadedAt = info.ModTime()
	}
	return nil
}

// save writes fs.data atomically: a temp file in the same directory, then a
// rename over the original, so a concurrent reader sees either the old or
// the new file, never a partial write. Caller holds fs.mu and the file lock.
func (fs *FileStore) save() error {
	raw, err := json.MarshalIndent(fs.data, "", "  ")
	if err != nil {
		return fmt.Errorf("configstore: encode: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(fs.path), filepath.Base(fs.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("configstore: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("configstore: write: %w", err)
	}
	if err := tmp.Chmod(fs.perm); err != nil {
		tmp.Close()
		return fmt.Errorf("configstore: chmod: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("configstore: close: %w", err)
	}
	if err := os.Rename(tmpName, fs.path); err != nil {
		return fmt.Errorf("configstore: replace %s: %w", fs.path, err)
	}
	if info, err := os.Stat(fs.path); err == nil {
		fs.loadedAt = info.ModTime()
	}
	return nil
}

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	return append([]byte(nil), b...)
}
