package server

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/client"
)

// exportServer is a running server over an agent with one session that has a
// couple of turns (the fallback fixture's echo-free fake: a prompt and its
// answer are stored either way).
var exportServers = map[*client.Client]*Server{}

func serverOf(t *testing.T, c *client.Client) *Server { return exportServers[c] }

func exportServer(t *testing.T) (*client.Client, string) {
	t.Helper()
	p := newToggle("expo", "m")
	srv, c, _ := fallbackServer(t, p)
	exportServers[c] = srv
	sess, err := c.CreateSession("expo/m", t.TempDir(), "Export Me")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"one", "two"} {
		if _, err := c.Ask(sess.ID, q, 0); err != nil {
			t.Fatal(err)
		}
	}
	return c, sess.ID
}

// The API: download an export, upload it back as a NEW session (201).
func TestExportImportOverTheAPI(t *testing.T) {
	c, id := exportServer(t)
	var buf bytes.Buffer
	name, n, err := c.ExportSession(id, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if name != "export-me-"+id[:8]+".export.jsonl" || n == 0 || !strings.HasPrefix(buf.String(), `{"format":"harness-session-export"`) {
		t.Fatalf("name=%q n=%d body=%.80s", name, n, buf.String())
	}
	orig, err := c.GetMessages(id)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(buf.String(), "\n"); lines != len(orig)+1 {
		t.Errorf("%d lines, want the header + %d messages", lines, len(orig))
	}
	cwd := t.TempDir()
	imp, err := c.ImportSession(bytes.NewReader(buf.Bytes()), cwd)
	if err != nil {
		t.Fatal(err)
	}
	if imp.ID == id || imp.Name != "Export Me" || imp.CWD != cwd {
		t.Fatalf("imported = %+v", imp)
	}
	msgs, err := c.GetMessages(imp.ID)
	if err != nil || len(msgs) != len(orig) {
		t.Fatalf("imported %d messages, want %d (%v)", len(msgs), len(orig), err)
	}
	// The imported session is live: it answers.
	if _, err := c.Ask(imp.ID, "three", 0); err != nil {
		t.Fatal(err)
	}
}

func TestExportImportErrors(t *testing.T) {
	c, _ := exportServer(t)
	if _, _, err := c.ExportSession("nope", &bytes.Buffer{}); statusOf(err) != http.StatusNotFound {
		t.Errorf("export of a missing session: %v", err)
	}
	if _, err := c.ImportSession(strings.NewReader("not an export\n"), ""); statusOf(err) != http.StatusBadRequest || !strings.Contains(err.Error(), "line 1") {
		t.Errorf("invalid import: %v", err)
	}
}

func TestImportBodyLimit(t *testing.T) {
	c, id := exportServer(t)
	var buf bytes.Buffer
	if _, _, err := c.ExportSession(id, &buf); err != nil {
		t.Fatal(err)
	}
	// A valid export, read past the limit.
	srv := serverOf(t, c)
	srv.importMaxBody = int64(buf.Len() / 2)
	if _, err := c.ImportSession(bytes.NewReader(buf.Bytes()), ""); statusOf(err) != http.StatusRequestEntityTooLarge {
		t.Errorf("over the limit: %v", err)
	}
	srv.importMaxBody = 0 // back to the default: it fits
	if _, err := c.ImportSession(bytes.NewReader(buf.Bytes()), ""); err != nil {
		t.Errorf("under the default limit: %v", err)
	}
}

// The session commands the TUI uses: export writes ~/.harness/exports/<file>
// (0700 dir, 0600 file) and answers its path; import reads a file and answers
// the NEW session's id.
func TestExportAndImportCommands(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "exports")
	prev := exportsDir
	exportsDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { exportsDir = prev })
	c, id := exportServer(t)

	st, err := c.ExecCommand(id, "export", nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "export-me-"+id[:8]+".export.jsonl")
	orig, _ := c.GetMessages(id)
	if st.Message != fmt.Sprintf("%s (%d messages)", path, len(orig)) {
		t.Fatalf("export message = %q", st.Message)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("export file: %v %v", fi, err)
	}
	if di, _ := os.Stat(dir); di.Mode().Perm() != 0o700 {
		t.Errorf("exports dir mode = %v", di.Mode().Perm())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("leftovers in %s: %v", dir, entries)
	}
	// Exporting again replaces the same file.
	if _, err := c.ExecCommand(id, "export", nil); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("a second export added a file: %v", entries)
	}

	// Imported FROM a session of another project: the new session belongs to
	// that one (the caller's), not to the exported session's.
	other, err := c.CreateSession("expo/m", t.TempDir(), "elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	st, err = c.ExecCommand(other.ID, "import", map[string]any{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	newID := st.Message
	if newID == "" || newID == id {
		t.Fatalf("import message = %q", newID)
	}
	if msgs, err := c.GetMessages(newID); err != nil || len(msgs) != len(orig) {
		t.Fatalf("the new session has %d messages, %v", len(msgs), err)
	}
	// It is OPEN on the server (the TUI streams it right away) and belongs to
	// the project of the session the command ran on.
	info, err := c.GetSessionInfo(newID)
	if err != nil {
		t.Fatalf("the imported session is not open: %v", err)
	}
	here, _ := c.GetSession(other.ID)
	if info.Session.CWD != here.CWD {
		t.Errorf("imported into %q, want the calling session's project %q", info.Session.CWD, here.CWD)
	}
	if msgs, _ := c.GetMessages(id); len(msgs) != len(orig) {
		t.Error("import touched the session it ran on")
	}
	// Not an export (by name, by content), missing path.
	for _, params := range []map[string]any{
		{"path": "/etc/passwd"},
		{"path": filepath.Join(t.TempDir(), "missing.export.jsonl")},
		{},
	} {
		if _, err := c.ExecCommand(id, "import", params); statusOf(err) != http.StatusBadRequest {
			t.Errorf("import %v: %v", params, err)
		}
	}
	// A valid export under another name is refused by its name, before it is read.
	renamed := filepath.Join(t.TempDir(), "renamed.json")
	data, _ := os.ReadFile(path)
	_ = os.WriteFile(renamed, data, 0o600)
	if _, err := c.ExecCommand(id, "import", map[string]any{"path": renamed}); statusOf(err) != http.StatusBadRequest || !strings.Contains(err.Error(), ".export.jsonl") {
		t.Errorf("import of a file not named .export.jsonl: %v", err)
	}
	bad := filepath.Join(t.TempDir(), "bad.export.jsonl")
	_ = os.WriteFile(bad, []byte("{\"not\":\"a header\"}\n"), 0o600)
	if _, err := c.ExecCommand(id, "import", map[string]any{"path": bad}); statusOf(err) != http.StatusBadRequest {
		t.Errorf("import of a non-export: %v", err)
	}
}

// Both commands are listed (the TUI palette is built from this list).
func TestExportImportCommandsAreListed(t *testing.T) {
	c, id := exportServer(t)
	defs, err := c.ListCommands(id)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, d := range defs {
		seen[d.Name] = true
	}
	if !seen["export"] || !seen["import"] {
		t.Errorf("commands = %v", seen)
	}
	_ = context.Background
}
