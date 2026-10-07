package agent

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gurcuff91/harness/agent/memory"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/agent/tools"
)

// fakeMemory is a minimal custom memory.Store — proof the agent depends only
// on the port, never on the SQLite implementation.
type fakeMemory struct {
	mu      sync.Mutex
	slugs   []string
	closed  int
	written []string
}

func (f *fakeMemory) Write(cwd, slug, content string, global bool) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.written = append(f.written, slug)
	return true, nil
}

func (f *fakeMemory) Search(cwd, query string, includeContent bool, skip, limit int) (memory.SearchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := memory.SearchResult{Total: len(f.slugs), Limit: limit}
	for _, s := range f.slugs {
		out.Results = append(out.Results, memory.Memory{Slug: s, CWD: cwd})
	}
	out.Returned = len(out.Results)
	return out, nil
}

func (f *fakeMemory) Delete(cwd, slug string, global bool) (bool, error) { return false, nil }

func (f *fakeMemory) Get(cwd, slug string, global bool) (memory.Memory, bool, error) {
	return memory.Memory{}, false, nil
}

func (f *fakeMemory) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return nil
}

func memoToolNames(a *Agent, t *testing.T) map[string]bool {
	t.Helper()
	var sess *Session
	reg, _ := a.buildSessionTools("sess-1", t.TempDir(), &sess, &resources.Resources{}, resources.NilLoader{})
	got := map[string]bool{}
	for _, d := range reg.Definitions() {
		switch d.Name {
		case tools.ToolMemoWrite, tools.ToolMemoSearch, tools.ToolMemoDelete:
			got[d.Name] = true
		}
	}
	return got
}

// No store → no memory at all: no Memo* tools, no "## Memory" prompt block.
func TestNoMemoryStoreMeansNoMemory(t *testing.T) {
	a := New(AgentOptions{Store: store.NewInMemoryStore()})
	defer a.Close()

	if a.Memory() != nil {
		t.Fatalf("Memory() = %v, want nil", a.Memory())
	}
	if got := memoToolNames(a, t); len(got) != 0 {
		t.Errorf("Memo* tools registered without a store: %v", got)
	}
	if prompt, _ := a.buildSystemPrompt(t.TempDir(), &resources.Resources{}); strings.Contains(prompt, "## Memory") {
		t.Error("system prompt has a ## Memory block without a store")
	}
}

// A custom store turns memory on: tools registered, prompt lists its slugs,
// and the agent owns it — Close closes it exactly once (Close is idempotent).
func TestCustomMemoryStoreIsUsedAndClosedByAgent(t *testing.T) {
	mem := &fakeMemory{slugs: []string{"api-auth-flow"}}
	a := New(AgentOptions{Store: store.NewInMemoryStore(), Memory: mem})

	if a.Memory() != memory.Store(mem) {
		t.Fatal("Memory() must return the injected store")
	}
	got := memoToolNames(a, t)
	for _, n := range []string{tools.ToolMemoWrite, tools.ToolMemoSearch, tools.ToolMemoDelete} {
		if !got[n] {
			t.Errorf("tool %q not registered with a store", n)
		}
	}
	prompt, _ := a.buildSystemPrompt(t.TempDir(), &resources.Resources{})
	if !strings.Contains(prompt, "## Memory") || !strings.Contains(prompt, "- api-auth-flow") {
		t.Errorf("system prompt must list the store's memories, got:\n%s", prompt)
	}

	a.Close()
	a.Close()
	if mem.closed != 1 {
		t.Errorf("store closed %d times, want 1", mem.closed)
	}
}

// A subagent shares its parent's store but never owns it.
func TestSubagentSharesButDoesNotCloseMemory(t *testing.T) {
	mem := &fakeMemory{}
	sub := New(AgentOptions{Store: store.NewInMemoryStore(), sharedMemory: mem})
	if sub.Memory() != memory.Store(mem) {
		t.Fatal("subagent must see the shared store")
	}
	sub.Close()
	if mem.closed != 0 {
		t.Errorf("subagent closed the parent's store (%d times)", mem.closed)
	}
}

// A typed-nil store (e.g. a failed memory.OpenSQLite passed along without
// checking its error) must mean "no memory", not a store that panics on use.
func TestTypedNilMemoryStoreMeansNoMemory(t *testing.T) {
	var typedNil *memory.SQLiteStore
	a := New(AgentOptions{Store: store.NewInMemoryStore(), Memory: typedNil})
	defer a.Close() // must not panic closing a nil *SQLiteStore

	if a.Memory() != nil {
		t.Fatal("typed-nil store must leave the agent without memory")
	}
	if got := memoToolNames(a, t); len(got) != 0 {
		t.Errorf("Memo* tools registered for a typed-nil store: %v", got)
	}
}

// End to end with the real default backend.
func TestSQLiteMemoryStoreWiresIntoAgent(t *testing.T) {
	mem, err := memory.OpenSQLite(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	if _, err := mem.Write("/proj", "deploy-notes", "use make release", false); err != nil {
		t.Fatal(err)
	}
	a := New(AgentOptions{Store: store.NewInMemoryStore(), Memory: mem})
	prompt, _ := a.buildSystemPrompt("/proj", &resources.Resources{})
	if !strings.Contains(prompt, "- deploy-notes") {
		t.Errorf("prompt must list the SQLite store's memory, got:\n%s", prompt)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := mem.Search("", "", false, 0, 1); err == nil {
		t.Error("store must be closed by Agent.Close")
	}
}

// A Subagent sub-agent gets READ-ONLY memory: it shares the parent's store
// and keeps MemoSearch, but MemoWrite/MemoDelete are never registered —
// readonly or not. Built exactly like the Subagent executor builds it
// (sharedMemory + subagentDisallowedTools), minus the live provider call.
func TestSubagentMemoryIsReadOnly(t *testing.T) {
	for _, readonly := range []bool{false, true} {
		mem := &fakeMemory{}
		sub := New(AgentOptions{
			Store:           store.NewInMemoryStore(),
			DisallowedTools: subagentDisallowedTools(readonly),
			sharedMemory:    mem,
		})
		got := memoToolNames(sub, t)
		if !got[tools.ToolMemoSearch] {
			t.Errorf("readonly=%v: MemoSearch must stay available to sub-agents", readonly)
		}
		for _, n := range []string{tools.ToolMemoWrite, tools.ToolMemoDelete} {
			if got[n] {
				t.Errorf("readonly=%v: %s must NOT be registered for a sub-agent", readonly, n)
			}
		}
		sub.Close()
	}
}
