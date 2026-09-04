//go:build unix

package term

import (
	"os"
	"os/signal"
	"syscall"
)

// notifyResize returns a channel that receives SIGWINCH (the Unix terminal
// resize signal). See terminal.go's Start for how it's consumed —
// resizeLoop's select treats it like any other os.Signal channel.
func notifyResize() chan os.Signal {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	return ch
}

// stopResize unregisters ch from further signal delivery. No-op if ch is nil
// (mirrors the Windows build's notifyResize, which never registers anything).
func stopResize(ch chan os.Signal) {
	if ch != nil {
		signal.Stop(ch)
	}
}
