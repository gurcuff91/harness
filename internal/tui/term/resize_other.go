//go:build !unix && !windows

package term

import "os"

// Fallback for platforms that are neither unix nor windows (e.g. js/wasm,
// plan9) — same no-op shape as resize_windows.go, for the same reason: no
// resize signal to hook into, so onResize simply never fires.
func notifyResize() chan os.Signal { return nil }
func stopResize(ch chan os.Signal) {}
