package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/gurcuff91/harness/admin"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/types"
)

// exportAgent is an agent with the echo model, on the store given (nil = a
// FileStore in a temp dir), plus a session with a few turns of history.
func exportAgent(t *testing.T, st store.SessionStore) (*Agent, *Session, string) {
	t.Helper()
	if st == nil {
		fs, err := store.NewFileStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		st = fs
	}
	ch := newFakeChannel("fake")
	a, model, _ := channelAgent(t, ch)
	a.store = st
	sess, err := a.NewSession(t.TempDir(), model)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	for _, p := range []string{"first question", "second question"} {
		if _, err := sess.PromptAndWait(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	return a, sess, model
}

func exportOf(t *testing.T, a *Agent, id string) ([]byte, int) {
	t.Helper()
	var buf bytes.Buffer
	n, err := a.ExportSession(id, &buf)
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), n
}

func rawLog(t *testing.T, st store.SessionStore, id string) []string {
	t.Helper()
	var out []string
	if err := store.StreamMessages(st, id, func(raw []byte) error {
		out = append(out, string(raw))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// Export → import: a NEW session (new id, now, the cwd given) with the same
// conversation byte for byte and the same name/model/thinking/stats.
func TestExportImportRoundTrip(t *testing.T) {
	a, sess, _ := exportAgent(t, nil)
	if err := sess.Rename("Round Trip"); err != nil {
		t.Fatal(err)
	}
	// Usage as a real provider leaves it (the fake reports none).
	m := sess.store.Meta()
	m.Stats = types.SessionStats{InputTokens: 1200, OutputTokens: 340, CacheRead: 50, CostUSD: 0.0123}
	m.Thinking = "high"
	if err := sess.store.UpdateMeta(m); err != nil {
		t.Fatal(err)
	}
	sess.Close()
	data, n := exportOf(t, a, sess.ID())
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if n != 4 || len(lines) != 5 {
		t.Fatalf("exported %d messages in %d lines, want 4 and 5:\n%s", n, len(lines), data)
	}
	var head admin.ExportHeader
	if err := json.Unmarshal([]byte(lines[0]), &head); err != nil || head.Format != admin.ExportFormat || head.Version != admin.ExportVersion || head.Meta.ID != sess.ID() || head.Meta.Name != "Round Trip" || head.Harness == "" {
		t.Fatalf("header = %+v, %v", head, err)
	}

	cwd := t.TempDir()
	imp, err := a.ImportSession(bytes.NewReader(data), cwd)
	if err != nil {
		t.Fatal(err)
	}
	defer imp.Close()
	if imp.ID() == sess.ID() || imp.CWD() != cwd || imp.Name() != "Round Trip" || imp.Model() != sess.Model() {
		t.Fatalf("imported id=%s cwd=%s name=%q model=%s", imp.ID(), imp.CWD(), imp.Name(), imp.Model())
	}
	if !imp.CreatedAt().After(sess.CreatedAt()) {
		t.Error("CreatedAt must be the import's time")
	}
	// Usage carries over (the context window is the live model's, refreshed
	// when a session opens).
	is := imp.Stats()
	if is.InputTokens != 1200 || is.OutputTokens != 340 || is.CostUSD != 0.0123 || is.CacheRead != 50 || imp.Thinking() != "high" {
		t.Errorf("stats = %+v thinking = %q, want the exported usage and thinking", is, imp.Thinking())
	}
	orig, got := rawLog(t, a.store, sess.ID()), rawLog(t, a.store, imp.ID())
	if strings.Join(orig, "\n") != strings.Join(got, "\n") {
		t.Fatalf("logs differ:\n%v\nvs\n%v", orig, got)
	}
	// The imported session works: a new turn goes on from the history.
	if _, err := imp.PromptAndWait(context.Background(), "after import"); err != nil {
		t.Fatal(err)
	}
	if len(rawLog(t, a.store, imp.ID())) != 6 {
		t.Error("the imported session did not continue")
	}
	// The original is untouched.
	if len(rawLog(t, a.store, sess.ID())) != 4 {
		t.Error("the import touched the exported session")
	}
}

// A compacted session keeps its working window (offset into the imported log).
func TestImportKeepsTheCompactionWindow(t *testing.T) {
	a, sess, _ := exportAgent(t, nil)
	if err := sess.store.AddCompactionSummary("SUMMARY"); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.PromptAndWait(context.Background(), "third"); err != nil {
		t.Fatal(err)
	}
	data, _ := exportOf(t, a, sess.ID())
	imp, err := a.ImportSession(bytes.NewReader(data), "")
	if err != nil {
		t.Fatal(err)
	}
	defer imp.Close()
	if imp.CWD() != sess.CWD() {
		t.Errorf("no cwd given: want the export's %q, got %q", sess.CWD(), imp.CWD())
	}
	got, want := imp.store.Messages(), sess.store.Messages()
	if len(got) != len(want) || imp.store.Meta().CompactOffset != sess.store.Meta().CompactOffset || imp.store.Meta().CompactCount != 1 {
		t.Fatalf("working set %d vs %d, offset %d vs %d", len(got), len(want), imp.store.Meta().CompactOffset, sess.store.Meta().CompactOffset)
	}
	if !strings.Contains(got[0].Parts[0].Text, "SUMMARY") {
		t.Errorf("the working set does not start at the checkpoint: %+v", got[0])
	}
}

// Every invalid export is refused with a reason (and the line), and leaves
// NOTHING behind in the store.
func TestImportRefusesInvalidExportsAndLeavesNothing(t *testing.T) {
	a, sess, _ := exportAgent(t, nil)
	good, _ := exportOf(t, a, sess.ID())
	lines := strings.Split(strings.TrimRight(string(good), "\n"), "\n")
	header := lines[0]
	futureHead := strings.Replace(header, `"version":1`, `"version":9`, 1)
	var h admin.ExportHeader
	_ = json.Unmarshal([]byte(header), &h)
	h.Meta.CompactOffset = 99
	pastHead, _ := json.Marshal(h)
	for name, c := range map[string]struct {
		in, want string
	}{
		"empty":           {"", "empty"},
		"not json":        {"hello\n", "line 1"},
		"plain meta":      {`{"id":"x","model":"a/b","cwd":"/tmp"}` + "\n", "line 1"},
		"future version":  {futureHead + "\n" + lines[1] + "\n", "version 9"},
		"bad message":     {header + "\n" + lines[1] + "\n{\"nope\":1}\n", "line 3"},
		"garbage line":    {header + "\n" + lines[1] + "\nnot json\n", "line 3"},
		"offset past end": {string(pastHead) + "\n" + lines[1] + "\n", "compaction checkpoint"},
	} {
		before, _ := a.store.ListMetas("")
		_, err := a.ImportSession(strings.NewReader(c.in), t.TempDir())
		if !errors.Is(err, admin.ErrInvalidExport) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want admin.ErrInvalidExport mentioning %q", name, err, c.want)
		}
		after, _ := a.store.ListMetas("")
		if len(after) != len(before) {
			t.Errorf("%s: the failed import left a session behind (%d → %d)", name, len(before), len(after))
		}
	}
}

// Blank lines and CRLF are tolerated; a multi-MB message (an image) passes.
func TestImportToleratesBlankLinesAndHugeMessages(t *testing.T) {
	a, sess, _ := exportAgent(t, nil)
	data, _ := exportOf(t, a, sess.ID())
	big := types.NewUserImageMessage("a big image", []types.ImageData{{MimeType: "image/png", Base64: strings.Repeat("A", 12<<20)}})
	bigRaw, _ := json.Marshal(big)
	in := strings.ReplaceAll(string(data), "\n", "\r\n\r\n") + string(bigRaw) + "\n"
	imp, err := a.ImportSession(strings.NewReader(in), "")
	if err != nil {
		t.Fatal(err)
	}
	defer imp.Close()
	log := rawLog(t, a.store, imp.ID())
	if len(log) != 5 || len(log[4]) < 12<<20 {
		t.Fatalf("imported %d messages (last %d bytes)", len(log), len(log[len(log)-1]))
	}
}

// Any SessionStore works: the in-memory store has neither optional
// capability, and goes through the generic fallback both ways.
func TestExportImportOnAStoreWithoutTheCapabilities(t *testing.T) {
	mem := store.NewInMemoryStore()
	var _ store.SessionStore = mem
	if _, ok := any(mem).(store.MessageStreamer); ok {
		t.Fatal("setup: the in-memory store must exercise the fallback")
	}
	a, sess, _ := exportAgent(t, mem)
	data, n := exportOf(t, a, sess.ID())
	imp, err := a.ImportSession(bytes.NewReader(data), "")
	if err != nil {
		t.Fatal(err)
	}
	defer imp.Close()
	if n != 4 || len(imp.store.AllMessages()) != 4 {
		t.Fatalf("exported %d, imported %d", n, len(imp.store.AllMessages()))
	}
}

// The FileStore import writes the log in one pass: a failure part-way leaves
// no log file behind, and a success leaves no temp file.
func TestFileStoreImportIsAtomic(t *testing.T) {
	dir := t.TempDir()
	fs, err := store.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	meta := store.SessionMeta{ID: "s1", CWD: "/proj", Model: "a/b"}
	if err := fs.SaveMeta(meta); err != nil {
		t.Fatal(err)
	}
	lines := [][]byte{[]byte(`{"role":"user","parts":[{"text":"a"}]}`), []byte(`{"role":"assistant","parts":[{"text":"b"}]}`)}
	i := 0
	boom := errors.New("boom")
	err = fs.ImportMessages("s1", func() ([]byte, bool, error) {
		if i == 1 {
			return nil, false, boom
		}
		i++
		return lines[0], true, nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	sdir := filepath.Join(dir, store.CwdSlug("/proj"))
	if entries, _ := os.ReadDir(sdir); len(entries) != 1 {
		t.Fatalf("a failed import left files: %v", entries)
	}
	i = 0
	if err := fs.ImportMessages("s1", func() ([]byte, bool, error) {
		if i == len(lines) {
			return nil, false, nil
		}
		i++
		return lines[i-1], true, nil
	}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(sdir)
	if len(entries) != 2 {
		t.Fatalf("files after import: %v", entries)
	}
	msgs, _ := fs.LoadMessages("s1", 0)
	if len(msgs) != 2 || msgs[1].Parts[0].Text != "b" {
		t.Fatalf("log = %+v", msgs)
	}
	st, _ := os.Stat(filepath.Join(sdir, "s1.jsonl"))
	if st.Mode().Perm() != 0o600 {
		t.Errorf("log mode = %v", st.Mode().Perm())
	}
}

// The FileStore streamer reads line by line (a size-bounded snapshot), and
// skips a torn line.
func TestFileStoreStreamsTheLog(t *testing.T) {
	dir := t.TempDir()
	fs, _ := store.NewFileStore(dir)
	_ = fs.SaveMeta(store.SessionMeta{ID: "s2", CWD: "/p"})
	for _, txt := range []string{"one", "two"} {
		if err := fs.AppendMessage("s2", types.NewUserTextMessage(txt)); err != nil {
			t.Fatal(err)
		}
	}
	f, _ := os.OpenFile(filepath.Join(dir, store.CwdSlug("/p"), "s2.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("{torn\n")
	f.Close()
	var got []string
	if err := fs.StreamMessages("s2", func(raw []byte) error { got = append(got, string(raw)); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !strings.Contains(got[1], "two") {
		t.Fatalf("streamed %v", got)
	}
	// An error from fn stops the walk.
	stop := errors.New("stop")
	calls := 0
	if err := fs.StreamMessages("s2", func([]byte) error { calls++; return stop }); !errors.Is(err, stop) || calls != 1 {
		t.Errorf("err=%v calls=%d", err, calls)
	}
	_ = bufio.ErrTooLong
}

func TestExportAnUnknownSession(t *testing.T) {
	a, _, _ := exportAgent(t, nil)
	if _, err := a.ExportSession("nope", &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v", err)
	}
}

// A closed session exports from the store.
func TestExportAClosedSession(t *testing.T) {
	a, sess, _ := exportAgent(t, nil)
	id := sess.ID()
	sess.Close()
	data, n := exportOf(t, a, id)
	if n != 4 || !bytes.HasPrefix(data, []byte(`{"format":"harness-session-export"`)) {
		t.Fatalf("n=%d data=%.80s", n, data)
	}
}

// The generic import (a store without MessageImporter) replaces the log: a
// session that already had messages ends with exactly the imported ones.
func TestTheGenericImportReplacesTheLog(t *testing.T) {
	mem := store.NewInMemoryStore()
	_ = mem.SaveMeta(store.SessionMeta{ID: "s", CWD: "/p"})
	_ = mem.AppendMessage("s", types.NewUserTextMessage("old"))
	lines := [][]byte{[]byte(`{"role":"user","parts":[{"text":"new"}]}`)}
	i := 0
	if err := store.ImportMessages(mem, "s", func() ([]byte, bool, error) {
		if i == len(lines) {
			return nil, false, nil
		}
		i++
		return lines[i-1], true, nil
	}); err != nil {
		t.Fatal(err)
	}
	msgs, _ := mem.LoadMessages("s", 0)
	if len(msgs) != 1 || msgs[0].Parts[0].Text != "new" {
		t.Fatalf("log = %+v", msgs)
	}
}
