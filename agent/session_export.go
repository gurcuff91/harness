package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/types"
)

// Session export / import (docs/plans/2026-11-23-session-export-import-design.md).
//
// An export is a JSONL file: a header line (ExportHeader: format, version, the
// session's meta) and then every message of the session's log, exactly as
// stored. Importing one creates a NEW session identical to the exported one —
// never touching an existing session, like a fork.

// ImportSession reads an export from r and opens it as a NEW session (a new
// id, created now) holding the same conversation: name, model, thinking,
// iteration budget, profiles, compaction state and stats come from the export.
// cwd is the project it belongs to ("" = the exported session's). The model
// falls back like ResumeSession's when its provider is unavailable; opts may
// bind channels (none are inherited). Nothing is created when the export is
// invalid (errors wrap admin.ErrInvalidExport and name the line).
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
		return nil, fmt.Errorf("%w: the file is empty", admin.ErrInvalidExport)
	}
	var head admin.ExportHeader
	if json.Unmarshal(raw, &head) != nil || head.Format != admin.ExportFormat {
		return nil, fmt.Errorf("%w: line 1 is not a harness session export header", admin.ErrInvalidExport)
	}
	if head.Version > admin.ExportVersion || head.Version < 1 {
		return nil, fmt.Errorf("%w: export version %d is not supported (this harness reads version %d)", admin.ErrInvalidExport, head.Version, admin.ExportVersion)
	}
	if head.Meta.ID == "" && head.Meta.Model == "" {
		return nil, fmt.Errorf("%w: line 1 has no session meta", admin.ErrInvalidExport)
	}

	now := time.Now()
	meta := head.Meta
	meta.ID = uuid.New().String()
	if cwd != "" {
		meta.CWD = cwd
	}
	if meta.CWD == "" {
		return nil, fmt.Errorf("%w: no project directory (the export has none; pass one)", admin.ErrInvalidExport)
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
			return nil, false, fmt.Errorf("%w: line %d is not a message", admin.ErrInvalidExport, line)
		}
		count++
		return raw, true, nil
	})
	if err != nil {
		if errors.Is(err, admin.ErrInvalidExport) {
			return fail(err)
		}
		return fail(fmt.Errorf("import: %w", err))
	}
	if meta.CompactOffset > count {
		return fail(fmt.Errorf("%w: its compaction checkpoint (message %d) is past its %d messages", admin.ErrInvalidExport, meta.CompactOffset, count))
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
