package admin

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/internal/version"
)

// Session export format (docs/plans/2026-11-23-session-export-import-design.md).
//
// An export is a JSONL file: a header line (ExportHeader) and then every
// message of the session's log, exactly as stored.

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

// ErrInvalidExport wraps every reason an import is refused (not an export, an
// unknown version, a bad line…): the caller maps it to "bad input" (400).
var ErrInvalidExport = errors.New("invalid session export")

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// ExportFileName is the file name an export of meta is saved under:
// "<sanitized name>-<first 8 of the id>.export.jsonl" — readable, never
// clashing between two sessions with the same name, and the same each time one
// session is exported again. Only ASCII letters and digits are kept.
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

// Export writes stored session id as an export to w and returns how many
// messages it holds. A running turn is never blocked (the log is streamed from
// a snapshot). ErrNotFound when the session does not exist.
func (s *Sessions) Export(id string, w io.Writer) (int, error) {
	meta, err := s.Get(id)
	if err != nil {
		return 0, err
	}
	return s.ExportAs(meta, w)
}

// ExportAs is Export with the meta supplied by the caller — for a session open
// in this process, whose live meta (model, thinking, stats) is ahead of the
// stored one. The messages are the stored log of meta.ID.
func (s *Sessions) ExportAs(meta store.SessionMeta, w io.Writer) (int, error) {
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
	err = store.StreamMessages(s.store(), meta.ID, func(raw []byte) error {
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
