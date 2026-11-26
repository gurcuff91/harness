package rpc

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/types"
)

func adminSessionsOps() []Op {
	return []Op{
		Define("admin/sessions/list", "Lists the stored sessions of a directory (every directory when cwd is empty).", opAdmSessionsList),
		Define("admin/sessions/get", "Returns a stored session's meta.", opAdmSessionsGet),
		Define("admin/sessions/messages", "Reads a window of a stored session's log: from an absolute index, or after the k-th-to-last compaction.", opAdmSessionsMessages),
		Define("admin/sessions/rename", "Renames a stored session. Refused with session_active while it is open in this process.", opAdmSessionsRename),
		Define("admin/sessions/delete", "Permanently deletes a stored session. Refused with session_active while it is open in this process.", opAdmSessionsDelete),
		Define("admin/sessions/export", "Exports a session as JSONL: written to path (a *.export.jsonl file, mode 0600) or, with no path, returned as a string.", opAdmSessionsExport),
		Define("admin/sessions/export_file_name", "Returns the file name an export of the session is saved under by convention.", opAdmSessionsExportFileName),
	}
}

// AdminSessionsListParams are the params of admin/sessions/list.
type AdminSessionsListParams struct {
	// CWD filters by working directory; empty = every directory.
	CWD string `json:"cwd,omitempty"`
}

// AdminSessionsListResult is the result of admin/sessions/list.
type AdminSessionsListResult struct {
	Sessions []store.SessionMeta `json:"sessions"`
}

func opAdmSessionsList(_ context.Context, s *Server, p AdminSessionsListParams) (AdminSessionsListResult, error) {
	list, err := s.Admin().Sessions.List(p.CWD)
	if err != nil {
		return AdminSessionsListResult{}, err
	}
	if list == nil {
		list = []store.SessionMeta{}
	}
	return AdminSessionsListResult{Sessions: list}, nil
}

func admRequireID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errInvalid("id is required")
	}
	return nil
}

func opAdmSessionsGet(_ context.Context, s *Server, p SessionIDParams) (store.SessionMeta, error) {
	if err := admRequireID(p.ID); err != nil {
		return store.SessionMeta{}, err
	}
	return s.Admin().Sessions.Get(p.ID)
}

// AdminSessionsMessagesParams are the params of admin/sessions/messages. From
// and Compactions are mutually exclusive; none = the whole log.
type AdminSessionsMessagesParams struct {
	ID string `json:"id"`
	// From starts the window at this absolute index (>= 0).
	From *int `json:"from,omitempty"`
	// Compactions starts the window just after the k-th-to-last compaction
	// checkpoint (k >= 1; 1 = the last one).
	Compactions *int `json:"compactions,omitempty"`
}

// AdminSessionsMessagesResult is the result of admin/sessions/messages.
type AdminSessionsMessagesResult struct {
	Messages []types.Message `json:"messages"`
	// From is the absolute index of the first message returned.
	From int `json:"from"`
	// Total is the length of the whole log.
	Total int `json:"total"`
}

// admMessageWindow validates the window params like the HTTP API does.
func admMessageWindow(p AdminSessionsMessagesParams) (store.MessageWindow, error) {
	var win store.MessageWindow
	if p.From != nil && p.Compactions != nil {
		return win, errInvalid("from and compactions are mutually exclusive")
	}
	if p.From != nil {
		if *p.From < 0 {
			return win, errInvalid("from must be a non-negative integer")
		}
		win.From = *p.From
	}
	if p.Compactions != nil {
		if *p.Compactions < 1 {
			return win, errInvalid("compactions must be a positive integer")
		}
		win.Compactions = *p.Compactions
	}
	return win, nil
}

func opAdmSessionsMessages(_ context.Context, s *Server, p AdminSessionsMessagesParams) (AdminSessionsMessagesResult, error) {
	if err := admRequireID(p.ID); err != nil {
		return AdminSessionsMessagesResult{}, err
	}
	win, err := admMessageWindow(p)
	if err != nil {
		return AdminSessionsMessagesResult{}, err
	}
	msgs, from, total, err := s.Admin().Sessions.Messages(p.ID, win)
	if err != nil {
		return AdminSessionsMessagesResult{}, err
	}
	if msgs == nil {
		msgs = []types.Message{}
	}
	return AdminSessionsMessagesResult{Messages: msgs, From: from, Total: total}, nil
}

// AdminSessionsRenameParams are the params of admin/sessions/rename.
type AdminSessionsRenameParams struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func opAdmSessionsRename(_ context.Context, s *Server, p AdminSessionsRenameParams) (Empty, error) {
	if err := admRequireID(p.ID); err != nil {
		return Empty{}, err
	}
	return Empty{}, s.Admin().Sessions.Rename(p.ID, p.Name)
}

func opAdmSessionsDelete(_ context.Context, s *Server, p SessionIDParams) (Empty, error) {
	if err := admRequireID(p.ID); err != nil {
		return Empty{}, err
	}
	return Empty{}, s.Admin().Sessions.Delete(p.ID)
}

// AdminSessionsExportParams are the params of admin/sessions/export.
type AdminSessionsExportParams struct {
	ID string `json:"id"`
	// Path, when set, is where the export is written: it must end in
	// .export.jsonl; the directory is created (0700) if missing, the file is
	// 0600 and replaced atomically. Without it the JSONL is returned in
	// `content` (not for very large sessions).
	Path string `json:"path,omitempty"`
}

// AdminSessionsExportResult is the result of admin/sessions/export: Path is
// set when the export was written to a file, Content when it was returned.
type AdminSessionsExportResult struct {
	Path     string `json:"path,omitempty"`
	Content  string `json:"content,omitempty"`
	Messages int    `json:"messages"`
}

// admExportMeta is the meta an export carries: the live one for a session open
// here (its model, thinking and stats are ahead of the stored ones), else the
// stored one.
func admExportMeta(s *Server, id string) (store.SessionMeta, error) {
	if sess, err := s.session(id); err == nil {
		return sess.Meta(), nil
	}
	return s.Admin().Sessions.Get(id)
}

func opAdmSessionsExport(_ context.Context, s *Server, p AdminSessionsExportParams) (AdminSessionsExportResult, error) {
	if err := admRequireID(p.ID); err != nil {
		return AdminSessionsExportResult{}, err
	}
	if p.Path != "" && !strings.HasSuffix(p.Path, ".export.jsonl") {
		return AdminSessionsExportResult{}, errInvalid("path must end in .export.jsonl")
	}
	meta, err := admExportMeta(s, p.ID)
	if err != nil {
		return AdminSessionsExportResult{}, err
	}
	sessions := s.Admin().Sessions
	if p.Path == "" {
		var buf bytes.Buffer
		n, err := sessions.ExportAs(meta, &buf)
		if err != nil {
			return AdminSessionsExportResult{}, err
		}
		return AdminSessionsExportResult{Content: buf.String(), Messages: n}, nil
	}
	path, err := filepath.Abs(p.Path)
	if err != nil {
		return AdminSessionsExportResult{}, errInvalid("path: %v", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return AdminSessionsExportResult{}, errInvalid("cannot create %s: %v", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".export-*")
	if err != nil {
		return AdminSessionsExportResult{}, errInvalid("cannot write in %s: %v", dir, err)
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	n, err := sessions.ExportAs(meta, tmp)
	if err == nil {
		err = tmp.Chmod(0o600)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		return AdminSessionsExportResult{}, err
	}
	return AdminSessionsExportResult{Path: path, Messages: n}, nil
}

// AdminSessionsExportFileNameResult is the result of
// admin/sessions/export_file_name.
type AdminSessionsExportFileNameResult struct {
	FileName string `json:"file_name"`
}

func opAdmSessionsExportFileName(_ context.Context, s *Server, p SessionIDParams) (AdminSessionsExportFileNameResult, error) {
	if err := admRequireID(p.ID); err != nil {
		return AdminSessionsExportFileNameResult{}, err
	}
	meta, err := admExportMeta(s, p.ID)
	if err != nil {
		return AdminSessionsExportFileNameResult{}, err
	}
	return AdminSessionsExportFileNameResult{FileName: admin.ExportFileName(meta)}, nil
}
