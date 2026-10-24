package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/gurcuff91/harness/types"
)

// MessageWindow selects a suffix of a session's full message log instead of
// the whole thing. The zero value selects everything. From and Compactions
// are mutually exclusive — setting both is an error (ErrWindowConflict).
type MessageWindow struct {
	// From starts the window at this absolute index of the log (0-based).
	// An index at or past the end selects nothing.
	From int
	// Compactions starts the window just AFTER the Compactions-th-to-last
	// compaction checkpoint (1 = the last one, 2 = the second-to-last, …). A
	// log with fewer checkpoints than that is returned whole, from index 0.
	Compactions int
}

// ErrWindowConflict is returned when a MessageWindow sets both From and
// Compactions.
var ErrWindowConflict = errors.New("message window: from and compactions are mutually exclusive")

func (w MessageWindow) validate() error {
	if w.From < 0 || w.Compactions < 0 {
		return fmt.Errorf("message window: from and compactions must be non-negative")
	}
	if w.From > 0 && w.Compactions > 0 {
		return ErrWindowConflict
	}
	return nil
}

// WindowReader is an OPTIONAL capability of a SessionStore: reading a suffix
// of a session's log without materializing the part before it. FileStore and
// InMemoryStore implement it; a custom store that doesn't still works —
// LoadMessagesWindow falls back to loading the whole log and cutting it in
// memory (correct, just not cheap).
type WindowReader interface {
	// LoadMessagesWindow returns the messages of the window, the absolute index
	// of the first one returned (from) and the number of messages in the full
	// log (total). A window starting at or past the end returns no messages and
	// from == total. Only called with a non-zero, valid window.
	LoadMessagesWindow(sessionID string, w MessageWindow) (msgs []types.Message, from, total int, err error)
}

// LoadMessagesWindow reads a window of a session's log from any SessionStore:
// through its WindowReader when it has one, otherwise by loading everything.
// A zero window is the plain full history (from 0).
func LoadMessagesWindow(port SessionStore, sessionID string, w MessageWindow) (msgs []types.Message, from, total int, err error) {
	if err := w.validate(); err != nil {
		return nil, 0, 0, err
	}
	defer func() {
		if err == nil && msgs == nil {
			msgs = []types.Message{} // always a real slice: it encodes as [] not null
		}
	}()
	if w == (MessageWindow{}) {
		msgs, err = port.LoadMessages(sessionID, 0)
		return msgs, 0, len(msgs), err
	}
	if wr, ok := port.(WindowReader); ok {
		return wr.LoadMessagesWindow(sessionID, w)
	}
	all, err := port.LoadMessages(sessionID, 0)
	if err != nil {
		return nil, 0, 0, err
	}
	cut := windowCut(all, w)
	return all[cut:], cut, len(all), nil
}

// windowCut resolves a window against an in-memory log: the index of its first
// message, clamped to len(log).
func windowCut(log []types.Message, w MessageWindow) int {
	switch {
	case w.From > 0:
		if w.From > len(log) {
			return len(log)
		}
		return w.From
	case w.Compactions > 0:
		seen := 0
		for i := len(log) - 1; i >= 0; i-- {
			if m := log[i].Meta; m != nil && m.IsCompaction {
				if seen++; seen == w.Compactions {
					return i + 1
				}
			}
		}
	}
	return 0
}

// compactionMarker is the byte sequence a checkpoint line carries: a
// message's Meta encodes as {"is_compaction":true,…}. Inside any JSON string
// the quotes are escaped (\"), so this exact sequence can only be real
// structure — a cheap pre-filter that spares unmarshalling every line.
var compactionMarker = []byte(`"is_compaction":true`)

// isCompactionLine reports whether a JSONL line is a compaction checkpoint.
// The byte match is only a candidate filter; the (rare) candidate is parsed
// to confirm.
func isCompactionLine(line []byte) bool {
	if !bytes.Contains(line, compactionMarker) {
		return false
	}
	var msg types.Message
	if err := json.Unmarshal(line, &msg); err != nil {
		return false
	}
	return msg.Meta != nil && msg.Meta.IsCompaction
}

// readJSONLWindow reads a window of a JSONL log of at most `size` bytes
// (the file's size when the caller sampled it). One pass over the bytes finds
// the cut point and counts the lines WITHOUT unmarshalling them; only the
// window itself is then parsed. Positions are line indices — the same ones
// LoadMessages(fromIndex) uses.
func readJSONLWindow(path string, size int64, w MessageWindow) (msgs []types.Message, from, total int, err error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []types.Message{}, 0, 0, nil
		}
		return nil, 0, 0, err
	}
	defer f.Close()

	// Pass 1 — locate the cut: its line index and byte offset.
	type mark struct {
		line   int
		offset int64 // byte offset where the line AFTER the checkpoint starts
	}
	var ring []mark // the last w.Compactions checkpoints (Compactions mode)
	var fromMark mark
	fromFound := false

	var pos int64
	counting := func(data []byte, atEOF bool) (int, []byte, error) {
		adv, tok, err := bufio.ScanLines(data, atEOF)
		pos += int64(adv)
		return adv, tok, err
	}
	sc := bufio.NewScanner(io.LimitReader(f, size))
	sc.Buffer(make([]byte, 1024*1024), 10*1024*1024) // 10MB max line, as readJSONLFrom
	sc.Split(counting)

	line := 0
	for {
		start := pos // byte offset where the line about to be scanned begins
		if !sc.Scan() {
			break
		}
		if w.From > 0 && line == w.From {
			fromMark = mark{line: line, offset: start}
			fromFound = true
		}
		if w.Compactions > 0 && isCompactionLine(sc.Bytes()) {
			ring = append(ring, mark{line: line + 1, offset: pos})
			if len(ring) > w.Compactions {
				ring = ring[1:]
			}
		}
		line++
	}
	if err := sc.Err(); err != nil {
		return nil, 0, 0, err
	}
	total = line

	var cut mark
	switch {
	case w.From > 0:
		if !fromFound {
			return []types.Message{}, total, total, nil // past the end
		}
		cut = fromMark
	case len(ring) == w.Compactions:
		cut = ring[0]
	}
	from = cut.line

	// Pass 2 — parse only the window.
	if _, err := f.Seek(cut.offset, io.SeekStart); err != nil {
		return nil, 0, 0, err
	}
	msgs = []types.Message{}
	sc = bufio.NewScanner(io.LimitReader(f, size-cut.offset))
	sc.Buffer(make([]byte, 1024*1024), 10*1024*1024)
	for sc.Scan() {
		var msg types.Message
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			continue // skip malformed lines, like readJSONLFrom
		}
		msgs = append(msgs, msg)
	}
	return msgs, from, total, sc.Err()
}
