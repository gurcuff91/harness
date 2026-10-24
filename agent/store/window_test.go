package store

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/gurcuff91/harness/types"
)

// plainOnly hides a store's WindowReader, standing in for a custom
// SessionStore that predates (or ignores) the optional capability.
type plainOnly struct{ SessionStore }

func textMsg(s string) types.Message {
	return types.NewUserTextMessage(s)
}

// seedLog writes a log shaped by layout: "m" = a normal message, "c" = a
// compaction checkpoint. Message texts are their index, so assertions can
// name positions.
func seedLog(t *testing.T, p SessionStore, id, layout string) []types.Message {
	t.Helper()
	if err := p.SaveMeta(newMeta(id, "/proj")); err != nil {
		t.Fatal(err)
	}
	var log []types.Message
	for i, c := range layout {
		msg := textMsg(fmt.Sprintf("m%d", i))
		if c == 'c' {
			msg = CompactionMessage(fmt.Sprintf("summary %d", i))
		}
		if err := p.AppendMessage(id, msg); err != nil {
			t.Fatal(err)
		}
		log = append(log, msg)
	}
	return log
}

func texts(msgs []types.Message) []string {
	out := []string{}
	for _, m := range msgs {
		if len(m.Parts) > 0 {
			out = append(out, m.Parts[0].Text)
		}
	}
	return out
}

// TestWindowEveryStoreAgrees runs the same windows against FileStore,
// InMemoryStore and a store with no WindowReader (the in-memory fallback), for
// logs with 0, 1, 2 and many checkpoints — all must return exactly what the
// plain full history cut by hand says.
func TestWindowEveryStoreAgrees(t *testing.T) {
	layouts := map[string]string{
		"empty":         "",
		"no checkpoint": "mmmmm",
		"one":           "mmcmm",
		"two":           "mcmmcmm",
		"many":          "mmcmcmmcmmcmcm",
		"ends on ckpt":  "mmcmmc",
		"only ckpts":    "ccc",
	}
	windows := []MessageWindow{
		{}, {From: 1}, {From: 3}, {From: 99},
		{Compactions: 1}, {Compactions: 2}, {Compactions: 3}, {Compactions: 50},
	}
	for lname, layout := range layouts {
		for pname, base := range ports(t) {
			for _, variant := range []string{"native", "fallback"} {
				var p SessionStore = base
				if variant == "fallback" {
					p = plainOnly{base}
				}
				id := strings.ReplaceAll(lname+pname+variant, " ", "-")
				full := seedLog(t, p, id, layout)
				for _, w := range windows {
					t.Run(fmt.Sprintf("%s/%s/%s/%+v", lname, pname, variant, w), func(t *testing.T) {
						got, from, total, err := LoadMessagesWindow(p, id, w)
						if err != nil {
							t.Fatal(err)
						}
						wantFrom := windowCut(full, w)
						if from != wantFrom || total != len(full) {
							t.Fatalf("from,total = %d,%d; want %d,%d", from, total, wantFrom, len(full))
						}
						if want := texts(full[wantFrom:]); !reflect.DeepEqual(texts(got), want) {
							t.Fatalf("messages = %v; want %v", texts(got), want)
						}
						if got == nil {
							t.Fatal("messages must be a non-nil slice (JSON [] not null)")
						}
					})
				}
			}
		}
	}
}

// TestWindowSemantics pins the contract on a concrete log:
//
//	idx: 0 1 2 3 4 5 6 7 8 9
//	     m m C m m C m m m C?   (C = checkpoint)
func TestWindowSemantics(t *testing.T) {
	for pname, p := range ports(t) {
		t.Run(pname, func(t *testing.T) {
			seedLog(t, p, "s", "mmcmmcmmm") // checkpoints at 2 and 5
			cases := []struct {
				w          MessageWindow
				from, size int
			}{
				{MessageWindow{Compactions: 1}, 6, 3}, // just after the LAST checkpoint
				{MessageWindow{Compactions: 2}, 3, 6}, // just after the 2nd-to-last
				{MessageWindow{Compactions: 3}, 0, 9}, // fewer than k → everything
				{MessageWindow{From: 4}, 4, 5},        // absolute
				{MessageWindow{From: 9}, 9, 0},        // == total → empty
				{MessageWindow{From: 400}, 9, 0},      // past the end → empty, from clamped to total
				{MessageWindow{}, 0, 9},               // zero window = everything
			}
			for _, c := range cases {
				got, from, total, err := LoadMessagesWindow(p, "s", c.w)
				if err != nil {
					t.Fatalf("%+v: %v", c.w, err)
				}
				if from != c.from || len(got) != c.size || total != 9 {
					t.Errorf("%+v: from=%d len=%d total=%d; want from=%d len=%d total=9", c.w, from, len(got), total, c.from, c.size)
				}
			}
		})
	}
}

func TestWindowInvalid(t *testing.T) {
	p := NewInMemoryStore()
	seedLog(t, p, "s", "mmc")
	for _, w := range []MessageWindow{{From: -1}, {Compactions: -1}} {
		if _, _, _, err := LoadMessagesWindow(p, "s", w); err == nil {
			t.Errorf("%+v: want an error", w)
		}
	}
	if _, _, _, err := LoadMessagesWindow(p, "s", MessageWindow{From: 1, Compactions: 1}); err != ErrWindowConflict {
		t.Errorf("from+compactions: err = %v, want ErrWindowConflict", err)
	}
}

// TestWindowMissingLog: a session whose log was never written reads as empty.
func TestWindowMissingLog(t *testing.T) {
	for pname, p := range ports(t) {
		t.Run(pname, func(t *testing.T) {
			if err := p.SaveMeta(newMeta("s", "/proj")); err != nil {
				t.Fatal(err)
			}
			for _, w := range []MessageWindow{{From: 2}, {Compactions: 2}} {
				got, from, total, err := LoadMessagesWindow(p, "s", w)
				if err != nil || len(got) != 0 || from != 0 || total != 0 {
					t.Errorf("%+v: got=%v from=%d total=%d err=%v", w, got, from, total, err)
				}
			}
		})
	}
}

// TestWindowCheckpointMarkerInsideText: the cheap byte filter must not take
// ordinary content that merely MENTIONS the marker (a tool result quoting a
// session file, a user pasting JSON) for a checkpoint — inside a JSON string
// the quotes are escaped, so the raw bytes never match; and a line that does
// match is parsed to confirm.
func TestWindowCheckpointMarkerInsideText(t *testing.T) {
	for pname, p := range ports(t) {
		t.Run(pname, func(t *testing.T) {
			if err := p.SaveMeta(newMeta("s", "/proj")); err != nil {
				t.Fatal(err)
			}
			for _, m := range []types.Message{
				textMsg("a"),
				textMsg(`here is a checkpoint: {"is_compaction":true}`),
				CompactionMessage("real"),
				textMsg(`{"meta":{"is_compaction":true}} quoted again`),
				textMsg("z"),
			} {
				if err := p.AppendMessage("s", m); err != nil {
					t.Fatal(err)
				}
			}
			_, from, total, err := LoadMessagesWindow(p, "s", MessageWindow{Compactions: 1})
			if err != nil || from != 3 || total != 5 {
				t.Fatalf("from=%d total=%d err=%v; want from=3 (after the one real checkpoint) total=5", from, total, err)
			}
			// Only ONE real checkpoint: asking for the 2nd-to-last returns everything.
			_, from, _, _ = LoadMessagesWindow(p, "s", MessageWindow{Compactions: 2})
			if from != 0 {
				t.Fatalf("compactions=2 from=%d, want 0 — text mentioning the marker was counted as a checkpoint", from)
			}
		})
	}
}

// TestFileWindowIndexMatchesLoadMessages: the absolute index a window reports
// is the same line index LoadMessages(fromIndex) counts — including past a
// malformed line, which is skipped when parsing but still occupies a line.
func TestFileWindowIndexMatchesLoadMessages(t *testing.T) {
	fs, err := NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	seedLog(t, fs, "s", "mmcmm")
	path, _ := fs.findJSONLPath("s")
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("{this is not json\n") // line 5
	f.Close()
	fs.AppendMessage("s", CompactionMessage("late")) // line 6
	fs.AppendMessage("s", textMsg("tail"))           // line 7

	got, from, total, err := fs.LoadMessagesWindow("s", MessageWindow{Compactions: 1})
	if err != nil || from != 7 || total != 8 || !reflect.DeepEqual(texts(got), []string{"tail"}) {
		t.Fatalf("compactions=1: from=%d total=%d got=%v err=%v", from, total, texts(got), err)
	}
	got, from, _, _ = fs.LoadMessagesWindow("s", MessageWindow{Compactions: 2})
	ref, _ := fs.LoadMessages("s", from)
	if from != 3 || !reflect.DeepEqual(texts(got), texts(ref)) {
		t.Fatalf("compactions=2: from=%d got=%v, LoadMessages(%d)=%v", from, texts(got), from, texts(ref))
	}
	got, from, _, _ = fs.LoadMessagesWindow("s", MessageWindow{From: 6})
	ref, _ = fs.LoadMessages("s", 6)
	if from != 6 || !reflect.DeepEqual(texts(got), texts(ref)) {
		t.Fatalf("from=6: got=%v want=%v", texts(got), texts(ref))
	}
}

// TestFileWindowNeverParsesSkippedLines: lines before the cut are not
// unmarshalled. A line before the cut that is valid JSON for the byte filter
// but a poison pill for the parser would be tolerated either way, so instead
// prove it by size: a giant skipped prefix costs a scan, not a parse — here, a
// skipped line that is NOT valid JSON must not matter, and one that carries the
// marker but isn't parseable must not count as a checkpoint.
func TestFileWindowSkippedPrefixIsNotParsed(t *testing.T) {
	fs, err := NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	seedLog(t, fs, "s", "m")
	path, _ := fs.findJSONLPath("s")
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(`{"is_compaction":true broken` + "\n") // marker bytes, unparseable
	f.Close()
	fs.AppendMessage("s", textMsg("after"))

	_, from, total, err := fs.LoadMessagesWindow("s", MessageWindow{Compactions: 1})
	if err != nil || from != 0 || total != 3 {
		t.Fatalf("from=%d total=%d err=%v; an unparseable candidate must not be a checkpoint", from, total, err)
	}
}

// TestFileWindowConcurrentAppend: appends racing a window read neither block it
// nor corrupt it — the read sees a consistent prefix of the log.
func TestFileWindowConcurrentAppend(t *testing.T) {
	fs, err := NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	seedLog(t, fs, "s", "mmcmmcmm")
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			fs.AppendMessage("s", textMsg(fmt.Sprintf("x%d", i)))
		}
	}()
	for i := 0; i < 100; i++ {
		got, from, total, err := fs.LoadMessagesWindow("s", MessageWindow{Compactions: 1})
		if err != nil {
			t.Fatal(err)
		}
		if from != 6 || total < 8 || len(got) != total-from {
			t.Fatalf("iteration %d: from=%d total=%d len=%d — inconsistent snapshot", i, from, total, len(got))
		}
	}
	wg.Wait()
}

// TestSessionMessagesWindowAfterReset: a /reset empties the log, so a window
// over it is empty with total 0, and indices restart from 0 afterwards.
func TestSessionMessagesWindowAfterReset(t *testing.T) {
	for pname, p := range ports(t) {
		t.Run(pname, func(t *testing.T) {
			s, err := CreateSession(p, newMeta("s", "/proj"))
			if err != nil {
				t.Fatal(err)
			}
			for _, txt := range []string{"a", "b"} {
				s.AddMessage(textMsg(txt))
			}
			s.AddCompactionSummary("sum")
			s.AddMessage(textMsg("c"))
			if _, from, total, _ := s.MessagesWindow(MessageWindow{Compactions: 1}); from != 3 || total != 4 {
				t.Fatalf("before reset: from=%d total=%d", from, total)
			}
			if err := s.Reset(); err != nil {
				t.Fatal(err)
			}
			got, from, total, err := s.MessagesWindow(MessageWindow{Compactions: 1})
			if err != nil || len(got) != 0 || from != 0 || total != 0 {
				t.Fatalf("after reset: got=%v from=%d total=%d err=%v", got, from, total, err)
			}
			s.AddMessage(textMsg("fresh"))
			got, from, total, _ = s.MessagesWindow(MessageWindow{From: 0, Compactions: 0})
			if from != 0 || total != 1 || len(got) != 1 {
				t.Fatalf("after reset + append: from=%d total=%d len=%d", from, total, len(got))
			}
		})
	}
}
