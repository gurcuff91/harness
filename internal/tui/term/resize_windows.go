//go:build windows

package term

import "os"

// notifyResize returns nil on Windows — there is no SIGWINCH-equivalent
// signal for terminal resize. A nil channel blocks forever in resizeLoop's
// select (Go's documented behavior for a nil channel operand), which is
// exactly the desired no-op: onResize simply never fires. The TUI still
// works on Windows; a manual terminal resize just won't trigger a live
// re-render until the next natural redraw (e.g. after the user's next
// keystroke), rather than being a hard requirement for cross-platform
// support.
func notifyResize() chan os.Signal { return nil }

// stopResize is a no-op on Windows — nothing was ever registered by
// notifyResize to unregister.
func stopResize(ch chan os.Signal) {}
