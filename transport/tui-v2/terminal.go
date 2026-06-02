package tuiv2

import (
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"
)

// Terminal manages raw terminal input/output and resize signals.
type Terminal struct {
	stdin      *os.File
	oldState   *term.State
	width      int
	height     int
	inputCh    chan []byte
	resizeCh   chan struct{}
	sigwinchCh chan os.Signal
	quit       chan struct{}
}

// NewTerminal enters raw mode and starts reading stdin.
func NewTerminal() (*Terminal, error) {
	stdin := os.Stdin
	oldState, err := term.MakeRaw(int(stdin.Fd()))
	if err != nil {
		return nil, err
	}

	w, h, _ := term.GetSize(int(stdin.Fd()))
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}

	t := &Terminal{
		stdin:      stdin,
		oldState:   oldState,
		width:      w,
		height:     h,
		inputCh:    make(chan []byte, 32),
		resizeCh:   make(chan struct{}, 4),
		sigwinchCh: make(chan os.Signal, 1),
		quit:       make(chan struct{}),
	}

	signal.Notify(t.sigwinchCh, syscall.SIGWINCH)
	go t.readStdin()
	go t.watchResize()

	return t, nil
}

func (t *Terminal) Width() int  { return t.width }
func (t *Terminal) Height() int { return t.height }

// Input returns the channel of raw stdin bytes.
func (t *Terminal) Input() <-chan []byte { return t.inputCh }

// Resize returns the channel that fires on terminal resize.
func (t *Terminal) Resize() <-chan struct{} { return t.resizeCh }

// Write writes raw bytes to stdout (ANSI sequences).
func (t *Terminal) Write(data []byte) {
	os.Stdout.Write(data)
}

// WriteString writes a string to stdout.
func (t *Terminal) WriteString(s string) {
	os.Stdout.Write([]byte(s))
}

// Restore returns the terminal to its original mode.
func (t *Terminal) Restore() {
	signal.Stop(t.sigwinchCh)
	close(t.quit)
	term.Restore(int(t.stdin.Fd()), t.oldState)
}

// SuspendRaw temporarily exits raw mode for a subprocess.
// Does NOT close quit or stop goroutines.
func (t *Terminal) SuspendRaw() {
	term.Restore(int(t.stdin.Fd()), t.oldState)
}

// ResumeRaw re-enters raw mode after SuspendRaw.
func (t *Terminal) ResumeRaw() {
	if st, err := term.MakeRaw(int(t.stdin.Fd())); err == nil {
		t.oldState = st
	}
}

// ShowCursor enables the terminal cursor. HideCursor disables it.
func (t *Terminal) ShowCursor() { t.WriteString("\033[?25h") }
func (t *Terminal) HideCursor() { t.WriteString("\033[?25l") }

// Clear clears the entire screen and homes the cursor.
func (t *Terminal) Clear() { t.WriteString("\033[2J\033[H") }

// readStdin reads raw bytes from stdin and sends them to inputCh.
func (t *Terminal) readStdin() {
	buf := make([]byte, 256)
	for {
		select {
		case <-t.quit:
			return
		default:
		}
		n, err := t.stdin.Read(buf)
		if err != nil {
			return
		}
		if n == 0 {
			continue
		}
		data := make([]byte, n)
		copy(data, buf[:n])
		t.inputCh <- data
	}
}

// watchResize polls for SIGWINCH signals and updates terminal size.
func (t *Terminal) watchResize() {
	for range t.sigwinchCh {
		w, h, err := term.GetSize(int(t.stdin.Fd()))
		if err != nil || w <= 0 {
			continue
		}
		t.width = w
		t.height = h
		select {
		case t.resizeCh <- struct{}{}:
		default:
		}
	}
}
