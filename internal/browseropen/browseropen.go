// Package browseropen opens a URL in the user's default browser. It exists
// so CLI and TUI — the two clients that drive OAuth logins via
// client.Client's StartOAuth — can open the auth_url the server returns,
// without either of them (or the server) depending on the other's copy of
// this logic. The server NEVER opens a browser; that's exclusively a
// client-side concern (see internal/oauthflow's package doc for the
// server/client split this enforces).
package browseropen

import (
	"os/exec"
	"runtime"
)

// Open opens u in the user's default browser (best-effort; errors are
// ignored — callers always print the URL too, so a headless/failed open
// still lets the user copy it manually).
//
// It's a package var, not a plain func, purely so tests can stub it:
// exercising a caller's URL-handling logic must NOT actually launch a
// browser on every `go test` run.
var Open = func(u string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd, args = "open", []string{u}
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler", u}
	default:
		cmd, args = "xdg-open", []string{u}
	}
	_ = exec.Command(cmd, args...).Start()
}
