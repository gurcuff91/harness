package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/internal/version"
	"github.com/gurcuff91/harness/types"
)

// Session export / import (docs/plans/2026-11-23-session-export-import-design.md).
//
// An export is a JSONL file: a header line (ExportHeader: format, version, the
// session's meta) and then every message of the session's log, exactly as
// stored. Importing one creates a NEW session identical to the exported one —
// never touching an existing session, like a fork.

// ExportFormat and ExportVersion identify a session export's header.
const (
	ExportFormat  = "harness-session-export"
	ExportVersion = 1
)

// ExportHeader is the first line of a session export.
type ExportHeader struct {
	Format     string            `json:"format"`
	Version    int               `json:"version"`
	ExportedAt time.Time         `json:"exported_at"`
	Harness    string            `json:"harness"`
	Meta       store.SessionMeta `json:"meta"`
}

// ErrInvalidExport wraps every reason an import is refused (not an export,
// an unknown version, a bad line…): the caller maps it to "bad input" (400).
var ErrInvalidExport = errors.New("invalid session export")

// ExportFileName is the file name an export of meta is saved under:
// "<sanitized name>-<first 8 of the id>.export.jsonl" — readable, never
// clashing between two sessions with the same name, and the same each time
// one session is exported again (so it replaces its own previous export).
func ExportFileName(meta store.SessionMeta) string {
	name := nonSlug.ReplaceAllString(strings.ToLower(meta.Name), "-")
	name = strings.Trim(name, "-")
	if len(name) > 50 {
		name = strings.TrimRight(name[:50], "-")
	}
	if name == "" {
		name = "session"
	}
	id := meta.ID
	if len(id) > 8 {
		id = id[:8]
	}
	return name + "-" + id + ".export.jsonl"
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// ExportFileName keeps ASCII letters and digits only: "¿qué?" becomes "qu".

// ExportSession writes session id as an export to w and returns how many
// messages it holds. It works on an open session (its live meta — model,
// thinking, stats — and the log as written so far; a running turn is never
// blocked) and on a closed one.
func (a *Agent) ExportSession(id string, w io.Writer) (int, error) {
	meta, err := a.exportMeta(id)
	if err != nil {
		return 0, err
	}
	bw := bufio.NewWriterSize(w, 1<<20)
	head, err := json.Marshal(ExportHeader{
		Format: ExportFormat, Version: ExportVersion,
		ExportedAt: time.Now().UTC().Truncate(time.Second),
		Harness:    version.Version, Meta: meta,
	})
	if err != nil {
		return 0, err
	}
	if _, err := bw.Write(append(head, '\n')); err != nil {
		return 0, err
	}
	n := 0
	err = store.StreamMessages(a.store, id, func(raw []byte) error {
		n++
		if _, err := bw.Write(raw); err != nil {
			return err
		}
		return bw.WriteByte('\n')
	})
	if err != nil {
		return n, err
	}
	return n, bw.Flush()
}

// exportMeta is the meta an export carries (see SessionMetaOf).
func (a *Agent) exportMeta(id string) (store.SessionMeta, error) {
	meta, found, err := a.SessionMetaOf(id)
	if err != nil {
		return store.SessionMeta{}, err
	}
	if !found {
		return store.SessionMeta{}, fmt.Errorf("session %s not found", id)
	}
	return meta, nil
}

// SessionMetaOf returns a session's meta: the LIVE one (current model,
// thinking, stats) for an open session, else the stored one; found is false
// when the session does not exist.
func (a *Agent) SessionMetaOf(id string) (store.SessionMeta, bool, error) {
	a.sessMu.Lock()
	live := a.activeSessions[id]
	a.sessMu.Unlock()
	if live != nil {
		return live.Meta(), true, nil
	}
	return a.store.LoadMeta(id)
}

// ImportSession reads an export from r and opens it as a NEW session (a new
// id, created now) holding the same conversation: name, model, thinking,
// iteration budget, profiles, compaction state and stats come from the export.
// cwd is the project it belongs to ("" = the exported session's). The model
// falls back like ResumeSession's when its provider is unavailable; opts may
// bind channels (none are inherited). Nothing is created when the export is
// invalid (errors wrap ErrInvalidExport and name the line).
func (a *Agent) ImportSession(r io.Reader, cwd string, opts ...SessionOption) (*Session, error) {
	cfg := applySessionOptions(opts)
	rd := bufio.NewReaderSize(r, 1<<20)
	line := 0
	// readLine returns the next non-blank line. A read error (a body cut at
	// the server's limit, a broken connection) is returned AS SUCH — never
	// mistaken for the end of the file, whose last line may lack a newline.
	readLine := func() ([]byte, bool, error) {
		for {
			b, err := readFullLine(rd)
			if err != nil && err != io.EOF {
				return nil, false, err
			}
			if len(b) == 0 && err == io.EOF {
				return nil, false, nil
			}
			line++
			if t := trimLine(b); len(t) > 0 {
				return t, true, nil
			}
			if err == io.EOF {
				return nil, false, nil
			}
		}
	}

	raw, ok, err := readLine()
	if err != nil {
		return nil, fmt.Errorf("import: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("%w: the file is empty", ErrInvalidExport)
	}
	var head ExportHeader
	if json.Unmarshal(raw, &head) != nil || head.Format != ExportFormat {
		return nil, fmt.Errorf("%w: line 1 is not a harness session export header", ErrInvalidExport)
	}
	if head.Version > ExportVersion || head.Version < 1 {
		return nil, fmt.Errorf("%w: export version %d is not supported (this harness reads version %d)", ErrInvalidExport, head.Version, ExportVersion)
	}
	if head.Meta.ID == "" && head.Meta.Model == "" {
		return nil, fmt.Errorf("%w: line 1 has no session meta", ErrInvalidExport)
	}

	now := time.Now()
	meta := head.Meta
	meta.ID = uuid.New().String()
	if cwd != "" {
		meta.CWD = cwd
	}
	if meta.CWD == "" {
		return nil, fmt.Errorf("%w: no project directory (the export has none; pass one)", ErrInvalidExport)
	}
	meta.CreatedAt, meta.LastActiveAt = now, now
	if err := a.store.SaveMeta(meta); err != nil {
		return nil, fmt.Errorf("import: save meta: %w", err)
	}
	fail := func(err error) (*Session, error) {
		_ = a.store.DeleteSession(meta.ID)
		return nil, err
	}

	count := 0
	err = store.ImportMessages(a.store, meta.ID, func() ([]byte, bool, error) {
		raw, ok, err := readLine()
		if err != nil || !ok {
			return nil, ok, err
		}
		var m types.Message
		if json.Unmarshal(raw, &m) != nil || m.Role == "" {
			return nil, false, fmt.Errorf("%w: line %d is not a message", ErrInvalidExport, line)
		}
		count++
		return raw, true, nil
	})
	if err != nil {
		if errors.Is(err, ErrInvalidExport) {
			return fail(err)
		}
		return fail(fmt.Errorf("import: %w", err))
	}
	if meta.CompactOffset > count {
		return fail(fmt.Errorf("%w: its compaction checkpoint (message %d) is past its %d messages", ErrInvalidExport, meta.CompactOffset, count))
	}

	st, err := store.OpenSession(a.store, meta.ID)
	if err != nil || st == nil {
		return fail(fmt.Errorf("import: open: %v", err))
	}
	return a.openStoredCopy(st, cfg, "import")
}

// readFullLine reads one line of any length, without its newline. err is nil
// for a complete line, io.EOF for the last line when it has no newline, or the
// reader's own error — in which case the partial line must not be used (a body
// cut at a size limit would otherwise look like a short, invalid message).
func readFullLine(rd *bufio.Reader) ([]byte, error) {
	var out []byte
	for {
		chunk, err := rd.ReadSlice('\n')
		out = append(out, chunk...)
		switch {
		case err == nil:
			return bytes.TrimSuffix(out, []byte{'\n'}), nil
		case err == bufio.ErrBufferFull:
			continue
		default:
			return out, err
		}
	}
}

// trimLine drops a trailing "\r" and surrounding spaces (no copy: a line
// can be many MB).
func trimLine(b []byte) []byte { return bytes.TrimSpace(b) }
