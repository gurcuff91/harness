package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/gurcuff91/harness/types"
)

// Moving a whole log in and out of a store — session export/import. Logs can
// be hundreds of MB, so both directions stream. Each direction is an OPTIONAL
// capability (like WindowReader): FileStore implements both efficiently; any
// other SessionStore still works through the generic fallback.

// MessageStreamer is an OPTIONAL capability: hand a session's log to fn one
// message at a time, as its raw JSON (one line, no newline), without loading
// it whole. An error from fn stops the walk and is returned.
type MessageStreamer interface {
	StreamMessages(sessionID string, fn func(raw []byte) error) error
}

// MessageImporter is an OPTIONAL capability: write a session's ENTIRE log in
// one pass, replacing whatever it had. next returns the raw JSON of the next
// message (one line, no newline), ok=false at the end, or an error that
// aborts the import (the log is then left as it was).
type MessageImporter interface {
	ImportMessages(sessionID string, next func() (raw []byte, ok bool, err error)) error
}

// StreamMessages walks a session's log from any SessionStore: through its
// MessageStreamer when it has one, otherwise by loading it and encoding each
// message (correct, just not cheap).
func StreamMessages(port SessionStore, sessionID string, fn func(raw []byte) error) error {
	if s, ok := port.(MessageStreamer); ok {
		return s.StreamMessages(sessionID, fn)
	}
	msgs, err := port.LoadMessages(sessionID, 0)
	if err != nil {
		return err
	}
	for _, m := range msgs {
		raw, err := json.Marshal(m)
		if err != nil {
			return err
		}
		if err := fn(raw); err != nil {
			return err
		}
	}
	return nil
}

// ImportMessages writes a session's whole log into any SessionStore: through
// its MessageImporter when it has one, otherwise one AppendMessage per
// message. The session's meta must have been saved first.
func ImportMessages(port SessionStore, sessionID string, next func() ([]byte, bool, error)) error {
	if s, ok := port.(MessageImporter); ok {
		return s.ImportMessages(sessionID, next)
	}
	if err := port.TruncateMessages(sessionID); err != nil {
		return err
	}
	for {
		raw, ok, err := next()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		var m types.Message
		if err := json.Unmarshal(raw, &m); err != nil {
			return err
		}
		if err := port.AppendMessage(sessionID, m); err != nil {
			return err
		}
	}
}

// maxLogLine bounds one message line in every reader of a log (an image
// travels base64 inside it). It was 10 MB, repeated in each reader: a message
// carrying a larger image made its whole session unreadable ("token too
// long") — an export/import test found it.
const maxLogLine = 64 << 20

// StreamMessages implements MessageStreamer: the raw lines of the JSONL, in
// order, never the whole file in memory. It reads the log as it is when the
// call starts (a size sampled under the lock, like LoadMessagesWindow), so a
// running turn appending meanwhile never blocks it.
func (m *FileStore) StreamMessages(sessionID string, fn func(raw []byte) error) error {
	m.mu.Lock()
	path, found := m.findJSONLPath(sessionID)
	var size int64
	if found {
		if st, err := os.Stat(path); err == nil {
			size = st.Size()
		}
	}
	m.mu.Unlock()
	if !found || size == 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, size))
	sc.Buffer(make([]byte, 0, 1<<20), maxLogLine)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || !json.Valid(line) {
			continue // a torn or blank line is not a message (as readJSONLFrom skips it)
		}
		if err := fn(line); err != nil {
			return err
		}
	}
	return sc.Err()
}

// ImportMessages implements MessageImporter: the whole log is written to a
// temp file in the session's directory (buffered, one pass), synced, and
// renamed over the log — so a failure part-way leaves the log untouched, and
// the session's directory is looked up once, not per message.
func (m *FileStore) ImportMessages(sessionID string, next func() ([]byte, bool, error)) error {
	m.mu.Lock()
	metaPath, found := m.findMetaPath(sessionID)
	m.mu.Unlock()
	if !found {
		return fmt.Errorf("session %s not found (save meta before importing)", sessionID)
	}
	dir := filepath.Dir(metaPath)
	tmp, err := os.CreateTemp(dir, sessionID+".jsonl.import-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed
	w := bufio.NewWriterSize(tmp, 1<<20)
	for {
		raw, ok, err := next()
		if err != nil {
			tmp.Close()
			return err
		}
		if !ok {
			break
		}
		if _, err := w.Write(raw); err != nil {
			tmp.Close()
			return err
		}
		if err := w.WriteByte('\n'); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return os.Rename(tmpName, filepath.Join(dir, sessionID+".jsonl"))
}

// ErrNotExport is returned for a stream that is not a session export.
var ErrNotExport = errors.New("not a harness session export")
