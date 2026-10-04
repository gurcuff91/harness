package configstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Two FileStore instances on the same file stand in for two harness
// processes: each has its own in-memory snapshot, and only the file (plus
// the cross-process lock) connects them.
func twoStores(t *testing.T) (a, b *FileStore, path string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "settings.json")
	var err error
	if a, err = NewFileStore(path, 0600); err != nil {
		t.Fatal(err)
	}
	if b, err = NewFileStore(path, 0600); err != nil {
		t.Fatal(err)
	}
	return a, b, path
}

// bumpMtime pushes the file's mtime forward so reload detection doesn't
// depend on the filesystem's mtime resolution or how fast the test runs.
func bumpMtime(t *testing.T, path string, d time.Duration) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	ts := info.ModTime().Add(d)
	if err := os.Chtimes(path, ts, ts); err != nil {
		t.Fatal(err)
	}
}

// A write in one "process" is visible to the other on its next read.
func TestFileStore_CrossInstanceVisibility(t *testing.T) {
	a, b, path := twoStores(t)
	if err := a.Set("core", "active_model", []byte(`"x/1"`)); err != nil {
		t.Fatal(err)
	}
	bumpMtime(t, path, 2*time.Second)
	if v, found, _ := b.Get("core", "active_model"); !found || string(v) != `"x/1"` {
		t.Fatalf("b did not see a's write: %s found=%v", v, found)
	}
}

// Writers touching DIFFERENT keys never clobber each other — every write
// re-reads the latest file under the lock before applying its own change.
func TestFileStore_ConcurrentWritersKeepEachOthersChanges(t *testing.T) {
	a, b, _ := twoStores(t)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(2)
		go func() { defer wg.Done(); _ = a.Set("mcp", "a"+itoa(i), []byte(`{}`)) }()
		go func() { defer wg.Done(); _ = b.Set("mcp", "b"+itoa(i), []byte(`{}`)) }()
	}
	wg.Wait()
	c, err := NewFileStore(a.path, 0600)
	if err != nil {
		t.Fatal(err)
	}
	all, _ := c.List("mcp")
	if len(all) != 40 {
		t.Fatalf("entries = %d, want 40 — a concurrent writer's change was lost", len(all))
	}
}

// The single-use OAuth refresh-token regression: two "processes" racing
// SwapValue on the same key must never both act on the same stale value.
// Each increments a counter; with N total swaps the final value must be N.
func TestFileStore_SwapValueAtomicAcrossInstances(t *testing.T) {
	a, b, _ := twoStores(t)
	_ = a.Set("providers", "counter", []byte(`0`))
	var wg sync.WaitGroup
	for i := range 30 {
		wg.Add(1)
		s := a
		if i%2 == 1 {
			s = b
		}
		go func() {
			defer wg.Done()
			_ = s.SwapValue("providers", "counter", func(cur []byte, _ bool) ([]byte, bool, error) {
				return incr(cur), true, nil
			})
		}()
	}
	wg.Wait()
	if v, _, _ := a.Get("providers", "counter"); string(v) != "30" {
		t.Fatalf("counter = %s, want 30 — two instances swapped against the same stale value", v)
	}
}

// The on-disk layout is the clean namespaced shape, values embedded as real
// JSON (human-readable, hand-editable), with the requested permissions.
func TestFileStore_OnDiskLayoutAndPerm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	s, err := NewFileStore(path, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("providers", "minimax", []byte(`{"type":"api_key","api_key":"k"}`)); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var doc map[string]map[string]map[string]string
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("file is not the namespaced layout: %v\n%s", err, raw)
	}
	if doc["providers"]["minimax"]["api_key"] != "k" {
		t.Errorf("value not embedded as JSON: %s", raw)
	}
	info, _ := os.Stat(path)
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("perm = %o, want 600", perm)
	}
}

// A malformed file — broken JSON, or valid JSON in the wrong shape — opens
// as an empty store with no error, and the next write replaces it with a
// valid file. harness never refuses to start over a broken config file.
func TestFileStore_MalformedFileIsEmptyAndOverwritten(t *testing.T) {
	for name, content := range map[string]string{
		"broken JSON":   `{"core": {`,
		"wrong shape":   `{"active_model":"x/1"}`,
		"not an object": `[1, 2, 3]`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			s, err := NewFileStore(path, 0600)
			if err != nil {
				t.Fatalf("NewFileStore on a malformed file = %v, want no error", err)
			}
			if all, _ := s.List("core"); len(all) != 0 {
				t.Errorf("malformed file should read as empty, got %v", all)
			}
			if err := s.Set("core", "active_model", []byte(`"y/2"`)); err != nil {
				t.Fatalf("Set over a malformed file: %v", err)
			}
			raw, _ := os.ReadFile(path)
			var doc map[string]map[string]string
			if err := json.Unmarshal(raw, &doc); err != nil || doc["core"]["active_model"] != "y/2" {
				t.Errorf("file not replaced with a valid store document: %s", raw)
			}
		})
	}
}

// Deleting a namespace's last entry drops the namespace from the file.
func TestFileStore_DeleteDropsEmptyNamespace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s, _ := NewFileStore(path, 0600)
	_ = s.Set("mcp", "only", []byte(`{}`))
	_ = s.Delete("mcp", "only")
	raw, _ := os.ReadFile(path)
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	if _, ok := doc["mcp"]; ok {
		t.Errorf("empty namespace left behind: %s", raw)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var out []byte
	for ; i > 0; i /= 10 {
		out = append([]byte{byte('0' + i%10)}, out...)
	}
	return string(out)
}
