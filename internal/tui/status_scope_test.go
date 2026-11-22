package tui

import (
	"os/exec"
	"strings"
	"testing"
)

// The status report is for the interactive TUI and nothing else. ACP speaks JSON-RPC on stdout, so
// a stray escape sequence there would corrupt the protocol; serve, telegram, slack and -p have no
// terminal to report to. The structural guarantee is the import graph: the reporter lives in
// internal/tui, so a package that cannot import it cannot emit the sequence.
func TestNoTransportOrServerCanReachTheStatusReporter(t *testing.T) {
	const tuiPkg = "github.com/gurcuff91/harness/internal/tui"
	for _, pkg := range []string{
		"github.com/gurcuff91/harness/transports/acp",
		"github.com/gurcuff91/harness/channels/telegram",
		"github.com/gurcuff91/harness/channels/slack",
		"github.com/gurcuff91/harness/server",
		"github.com/gurcuff91/harness/agent",
		"github.com/gurcuff91/harness/client",
		"github.com/gurcuff91/harness",
	} {
		out, err := exec.Command("go", "list", "-deps", pkg).CombinedOutput()
		if err != nil {
			t.Fatalf("go list -deps %s: %v\n%s", pkg, err, out)
		}
		for _, dep := range strings.Fields(string(out)) {
			if dep == tuiPkg || strings.HasPrefix(dep, tuiPkg+"/") {
				t.Errorf("%s depends on %s: it could emit OSC 7501 — only the interactive TUI may", pkg, dep)
			}
		}
	}
}

// Inside the package, the reporter is built in exactly one place (attachTerminal), which only
// buildUI — the interactive TUI's own setup — reaches. A second construction site would be a second
// way to emit the sequence.
func TestTheReporterIsConstructedInExactlyOnePlace(t *testing.T) {
	out, err := exec.Command("grep", "-rn", "--include=*.go", "--exclude=*_test.go", "newStatusReporter(", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("grep: %v\n%s", err, out)
	}
	var sites []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.Contains(l, "func newStatusReporter") {
			continue // the definition
		}
		sites = append(sites, l)
	}
	if len(sites) != 1 || !strings.Contains(sites[0], "layout.go") {
		t.Errorf("newStatusReporter is called from %d place(s), want exactly one (layout.go attachTerminal): %v", len(sites), sites)
	}
	out, err = exec.Command("grep", "-rn", "--include=*.go", "--exclude=*_test.go", "attachTerminal(", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("grep: %v\n%s", err, out)
	}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if !strings.Contains(l, "func (t *TUI) attachTerminal") && !strings.Contains(l, "layout.go") {
			t.Errorf("attachTerminal is used outside layout.go: %s", l)
		}
	}
}

// The terminal title (OSC 0) travels through the status reporter, so it is built exactly where
// the reporter is: attachTerminal. Nothing else may write the sequence — not another file of the
// TUI, and not any other frontend.
func TestTheTitleWriterIsConstructedOnlyInAttachTerminal(t *testing.T) {
	// 1. ansi.SetTitle (the only function that builds the sequence) is called from one place.
	out, err := exec.Command("grep", "-rn", "--include=*.go", "--exclude=*_test.go", "SetTitle(", "../..").CombinedOutput()
	if err != nil {
		t.Fatalf("grep: %v\\n%s", err, out)
	}
	var calls []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\\n") {
		if strings.Contains(l, "func SetTitle(") || strings.Contains(l, "func (r *statusReporter) SetTitle(") || strings.Contains(l, "//") && strings.Index(l, "//") < strings.Index(l, "SetTitle(") {
			continue // a definition, or a doc comment
		}
		calls = append(calls, l)
	}
	for _, l := range calls {
		if !strings.Contains(l, "internal/tui/status.go") && !strings.Contains(l, "setSessionName") && !strings.Contains(l, "status.SetTitle") {
			t.Errorf("an OSC 0 title is written outside the status reporter: %s", l)
		}
	}
	for _, l := range calls {
		if strings.Contains(l, "ansi.SetTitle(") && !strings.Contains(l, "internal/tui/status.go") {
			t.Errorf("ansi.SetTitle is called outside status.go: %s", l)
		}
	}

	// 2. The only thing that can reach the reporter's SetTitle is TUI.setSessionName.
	out, err = exec.Command("grep", "-rn", "--include=*.go", "--exclude=*_test.go", "status.SetTitle(", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("grep: %v\\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "session.go") {
		t.Errorf("status.SetTitle must be called from exactly one place (setSessionName in session.go): %v", lines)
	}

	// 3. And nothing assigns the name behind its back: every write of sessionName is the setter.
	out, _ = exec.Command("grep", "-rn", "--include=*.go", "--exclude=*_test.go", "-E", `\\.sessionName = `, ".").CombinedOutput()
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\\n") {
		if l != "" && !strings.Contains(l, "t.sessionName = name") {
			t.Errorf("sessionName assigned outside setSessionName, so the title would fall behind: %s", l)
		}
	}
}
