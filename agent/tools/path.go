package tools

import "path/filepath"

// resolvePath resolves a path a model passed to a file tool (Read/Write/Edit)
// against the session's cwd. An absolute path is returned as-is — no
// sandboxing, same trust model harness has always had for Bash/file access;
// this only fixes RELATIVE resolution, which used to silently resolve against
// the hosting OS process's real working directory instead of the session's
// logical one (see agent/tools/bash.go's cmd.Dir for the Bash-side twin of
// this fix). Harmless no-op when cwd == the process's own cwd, which is every
// harness transport's actual usage pattern (CLI/TUI/Telegram/Slack/ACP/server
// all call os.Getwd() once per process) — the gap only mattered to an SDK
// consumer running multiple sessions with different real cwds in one process.
func resolvePath(cwd, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(cwd, path)
}
