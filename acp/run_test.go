package acp

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRunConfigDefaultsToOSStdinStdout verifies Run's default runConfig (no
// options passed) resolves to os.Stdin/os.Stdout — the real ACP client
// hookup, and what `harness acp`'s CLI Run() relies on implicitly by never
// passing WithStdin/WithStdout at all (see internal/cli/kong_run_acp.go).
func TestRunConfigDefaultsToOSStdinStdout(t *testing.T) {
	cfg := runConfig{stdin: os.Stdin, stdout: os.Stdout}
	for _, opt := range []Option{} {
		opt(&cfg)
	}
	if cfg.stdin != os.Stdin {
		t.Errorf("default stdin = %v, want os.Stdin", cfg.stdin)
	}
	if cfg.stdout != os.Stdout {
		t.Errorf("default stdout = %v, want os.Stdout", cfg.stdout)
	}
}

// TestWithStdinOverridesDefault verifies WithStdin replaces the default
// os.Stdin — the mechanism every acp_test.go dispatch-loop test relies on to
// drive Run with an in-memory pipe instead of the real process stdin.
func TestWithStdinOverridesDefault(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	cfg := runConfig{stdin: os.Stdin, stdout: os.Stdout}
	WithStdin(r)(&cfg)
	if cfg.stdin != r {
		t.Errorf("WithStdin did not override the default")
	}
}

// TestWithStdoutOverridesDefault mirrors TestWithStdinOverridesDefault for
// WithStdout.
func TestWithStdoutOverridesDefault(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	cfg := runConfig{stdin: os.Stdin, stdout: os.Stdout}
	WithStdout(w)(&cfg)
	if cfg.stdout != w {
		t.Errorf("WithStdout did not override the default")
	}
}

// frontends/ was removed in v0.119.0 (acp moved to the module root; chat
// platforms are channels): the directory must not come back.
func TestTheTransportsDirectoryIsGone(t *testing.T) {
	if _, err := os.Stat(filepath.Join("..", "transports")); err == nil {
		t.Error("transports/ exists again: acp lives at the root, chat platforms are channels/")
	}
	if _, err := os.Stat(filepath.Join("..", "internal", "logx")); err == nil {
		t.Error("internal/logx exists again: the logger implementations live in the public logx")
	}
}
