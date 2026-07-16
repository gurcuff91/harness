package components

import (
	"sync"
	"time"

	"github.com/gurcuff91/harness/transport/tui/ansi"
)

// Renderer is the minimal surface a component needs to drive animation: the
// ability to request a re-render. The render.TUI satisfies this. Declaring it
// here keeps components decoupled from the render package.
type Renderer interface {
	RequestRender(force bool)
}

// spinnerFrames is the braille spinner used by the existing harness TUI.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinnerInterval matches the harness TUI (80ms).
const spinnerInterval = 80 * time.Millisecond

// Spinner is an animated loader with a themed label. When running it advances
// a braille frame every 80ms and asks the renderer to repaint. Port of PI's
// Loader, styled to match transport/tui (primary spinner + dim message).
type Spinner struct {
	mu       sync.Mutex
	renderer Renderer
	frame    int
	message  string
	running  bool
	ticker   *time.Ticker
	stopCh   chan struct{}
	start    time.Time
}

// NewSpinner creates a spinner bound to a renderer for animation repaints.
func NewSpinner(r Renderer, message string) *Spinner {
	return &Spinner{renderer: r, message: message}
}

// SetMessage updates the label shown next to the spinner.
func (s *Spinner) SetMessage(msg string) {
	s.mu.Lock()
	s.message = msg
	s.mu.Unlock()
	if s.renderer != nil {
		s.renderer.RequestRender(false)
	}
}

// Start begins the animation. Safe to call repeatedly.
func (s *Spinner) Start() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.start = time.Now()
	s.stopCh = make(chan struct{})
	s.ticker = time.NewTicker(spinnerInterval)
	stopCh := s.stopCh
	ticker := s.ticker
	s.mu.Unlock()

	go func() {
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				s.mu.Lock()
				s.frame = (s.frame + 1) % len(spinnerFrames)
				s.mu.Unlock()
				if s.renderer != nil {
					s.renderer.RequestRender(false)
				}
			}
		}
	}()
}

// Stop halts the animation.
func (s *Spinner) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	if s.ticker != nil {
		s.ticker.Stop()
		s.ticker = nil
	}
	if s.stopCh != nil {
		close(s.stopCh)
		s.stopCh = nil
	}
	s.mu.Unlock()
}

// Running reports whether the spinner is animating.
func (s *Spinner) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Elapsed returns how long the spinner has been running.
func (s *Spinner) Elapsed() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return 0
	}
	return time.Since(s.start)
}

// Render keeps ONE blank line permanently between the conversation and the
// editor so output never butts against the input separator. While running it
// adds the spinner line below that blank; the editor's own separator provides
// the lower margin. Stopped: just the single blank line.
//
//	running -> ["", "⠋ message", ""]   (blank + spinner + blank)
//	stopped -> [""]                     (blank only)
func (s *Spinner) Render(width int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return []string{""}
	}
	frame := ansi.Primary(spinnerFrames[s.frame])
	line := frame + " " + ansi.Dimmed(s.message)
	if ansi.VisibleWidth(line) > width {
		line = ansi.TruncateToWidth(line, width, "…", false)
	}
	return []string{"", line, ""}
}
