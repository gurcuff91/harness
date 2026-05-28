package cli

import (
	"github.com/gurcuff91/harness/types"
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/term"
)

// out is a buffered writer for all terminal output.
// All rendering goes through this single writer to prevent interleaving.
var out = bufio.NewWriter(os.Stdout)

func flush() { out.Flush() }

func printf(format string, args ...any) {
	fmt.Fprintf(out, format, args...)
	out.Flush() // flush every print so output appears immediately
}

// Renderer has a persistent turn spinner and per-event rendering.
type Renderer struct {
	mu sync.Mutex

	// Turn-level spinner
	turnSpinnerLabel string
	stopTurnSpin     chan struct{}
	turnSpinning     bool
	turnStart        time.Time

	// Per-block state
	startTime     time.Time
	spinner       int
	stopSpin      chan struct{}
	spinning      bool
	streaming     bool // currently printing streamed text
	thinkStreaming bool // currently printing streamed thinking
	col           int  // current column position in the active line

	// Stats from session — set on EventTokens, used in footer
	// Per-turn (last turn only)
	lastInput      int
	lastOutput     int
	lastCacheRead  int
	lastCacheWrite int
	// Accumulated across session (calculated by session, not renderer)
	totalCost     float64 // USD
	contextUsage    float64 // 0.0–1.0
	contextWindow int     // model context window size (tokens)

	// Display config
	providerName  string
	modelID       string
	thinkingLevel string
	subPricing    bool // true = subscription, cost is reference not actual
}

// RendererConfig holds display info for the renderer.
// Pricing and context window are NOT here — they come from session via EventTokens.
type RendererConfig struct {
	ProviderName  string
	ModelID       string
	ThinkingLevel string
	SubPricing    bool
}

func NewRenderer(cfg RendererConfig) *Renderer {
	return &Renderer{
		providerName:  cfg.ProviderName,
		modelID:       cfg.ModelID,
		thinkingLevel: cfg.ThinkingLevel,
		subPricing:    cfg.SubPricing,
	}
}

// Handle processes an agent event and renders it to the terminal.
func (r *Renderer) Handle(e types.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch e.Type {
	case types.EventTurnStart:
		r.startTime = time.Now()
		r.turnSpinnerLabel = spinnerLabel() // one word per turn

	case types.EventTurnEnd:
		r.stopSpinnerNow()

	case types.EventLoopStart:
		r.startTime = time.Now()
		r.streaming = false
		r.thinkStreaming = false
		r.col = 0
		r.startSpinner(e.Loop)

	case types.EventThinkingEnd:
		
		if e.Output != "" {
			r.renderThinking(e.Output)
		}

	case types.EventStreamThinkingDelta:
		
		r.renderThinkingDelta(e.Delta)

	case types.EventStreamThinkingEnd:
		r.finishThinkingStream()

	case types.EventStreamTextDelta:
		
		if r.thinkStreaming {
			// Thinking was streaming — the agent should have closed it,
			// but safety fallback
			r.finishThinkingStream()
		}
		r.renderTextDelta(e.Delta)

	case types.EventStreamTextEnd:
		r.finishTextStream()

	case types.EventStreamToolBuilding:
		r.stopSpinnerNow()
		r.finishAnyStream()
		r.startSpinner(0)

	case types.EventToolCall:
		
		r.finishAnyStream()
		r.renderToolCall(e.ToolName, e.ToolArgs)

	case types.EventToolResult:
		r.renderToolResult(e.ToolName, e.Output, e.Duration, e.IsError)

	case types.EventText:
		
		// Non-streamed final text — rendered by transport, not here

	case types.EventTokens:
		r.finishAnyStream()
		// Per-turn (for display)
		r.lastInput = e.Tokens.Input
		r.lastOutput = e.Tokens.Output
		r.lastCacheRead = e.Tokens.CacheRead
		r.lastCacheWrite = e.Tokens.CacheWrite
			// Accumulated — from session (source of truth)
		r.totalCost = e.Tokens.CostUSD
		r.contextUsage = e.Tokens.ContextUsage
		r.contextWindow = e.Tokens.ContextWindow

	case types.EventLoopEnd:
		r.stopSpinnerNow()
		r.finishAnyStream()

	case types.EventError:
		
		r.finishAnyStream()
		r.renderError(e.Output)
	}
}

// ============================================================
// Spinner
// ============================================================

func (r *Renderer) startSpinner(loop int) {
	if r.spinning {
		return
	}
	r.spinning = true
	r.stopSpin = make(chan struct{})
	r.spinner = 0
	label := r.turnSpinnerLabel // reuse turn label

	go func() {
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-r.stopSpin:
				// Don't ClearLine here — let stopSpinnerNow handle it
				// or the next render will overwrite anyway
				return
			case <-ticker.C:
				r.mu.Lock()
				if !r.spinning {
					r.mu.Unlock()
					return
				}
				frame := SpinnerFrames[r.spinner%len(SpinnerFrames)]
				elapsed := time.Since(r.startTime).Round(time.Millisecond)
				ClearLine()
				printf("  %s %s %s",
					C(BrightCyan, frame),
					C(Dim, label),
					C(Gray, fmt.Sprintf("[%s]", elapsed)),
				)
				r.spinner++
				r.mu.Unlock()
			}
		}
	}()
}

func (r *Renderer) stopSpinnerNow() {
	if r.spinning {
		close(r.stopSpin)
		r.spinning = false
		time.Sleep(10 * time.Millisecond)
		if !r.streaming && !r.thinkStreaming {
			ClearLine()
		}
	}
}

// ── Turn-level spinner ───────────────────────────────────

func (r *Renderer) startTurnSpinner() {
	if r.turnSpinning {
		return
	}
	r.turnSpinning = true
	r.turnStart = time.Now()
	r.stopTurnSpin = make(chan struct{})
	r.turnSpinnerLabel = spinnerLabel()

	go func() {
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()
		spinIdx := 0
		for {
			select {
			case <-r.stopTurnSpin:
				ClearLine()
				return
			case <-ticker.C:
				r.mu.Lock()
				if !r.turnSpinning || r.streaming || r.thinkStreaming {
					r.mu.Unlock()
					continue
				}
				frame := SpinnerFrames[spinIdx%len(SpinnerFrames)]
				ClearLine()
				printf("  %s %s",
					C(BrightCyan, frame),
					C(Dim, r.turnSpinnerLabel),
				)
				flush()
				spinIdx++
				r.mu.Unlock()
			}
		}
	}()
}

func (r *Renderer) stopTurnSpinner() {
	if !r.turnSpinning {
		return
	}
	close(r.stopTurnSpin)
	r.turnSpinning = false
	time.Sleep(10 * time.Millisecond)
	ClearLine()
}

// clearSpinnerLine clears the turn spinner line before printing content.
// The spinner will redraw itself on the next tick.
func (r *Renderer) clearSpinnerLine() {
	if r.turnSpinning {
		ClearLine()
	}
}

// startToolSpinner shows an animated spinner while a tool's input is being generated.
func (r *Renderer) startToolSpinner(toolName string) {
	if r.spinning {
		return
	}
	r.spinning = true
	r.stopSpin = make(chan struct{})
	r.spinner = 0

	icon := toolIcon(toolName)

	go func() {
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-r.stopSpin:
				return
			case <-ticker.C:
				r.mu.Lock()
				frame := SpinnerFrames[r.spinner%len(SpinnerFrames)]
				elapsed := time.Since(r.startTime).Round(time.Millisecond)
				ClearLine()
				printf("  %s %s %s %s",
					C(BrightYellow, icon),
					C(BrightYellow, frame),
					C(Dim, toolName),
					C(Gray, fmt.Sprintf("[%s]", elapsed)),
				)
				r.spinner++
				r.mu.Unlock()
			}
		}
	}()
}

// termWidth returns the terminal width, or 80 as default.
func termWidth() int {
	w, _, err := term.GetSize(os.Stdout.Fd())
	if err != nil || w <= 0 {
		return 80
	}
	return w
}

// maxCol returns max printable columns (terminal width minus bar prefix).
func maxCol() int {
	return termWidth() - 6 // "  │ " prefix
}

// spinnerLabels are Jade's tactical working phrases.
var spinnerLabels = []string{
	"Boostaffing",
	"Maskarizing",
	"Outworlding",
	"Khanifying",
	"Emeraldizing",
	"Razoranging",
	"Guardianing",
	"Edenianating",
	"Tactifying",
	"Perimetering",
	"Loyaltizing",
	"Kitanizing",
	"Brutalizing",
	"Shaolining",
	"Fatalizing",
	"Imperializing",
	"Blazewinding",
	"Shadowkicking",
	"Codexating",
	"Flawlessing",
	"Sentineling",
	"Kombatizing",
	"Vortexing",
	"Chronizing",
	"Dominancing",
}

var spinnerLabelIdx int

func spinnerLabel() string {
	l := spinnerLabels[spinnerLabelIdx%len(spinnerLabels)]
	spinnerLabelIdx++
	return l
}

// ============================================================
// Streaming Renderers
// ============================================================

// renderThinkingDelta prints thinking fragments with a gray left border.
func (r *Renderer) renderThinkingDelta(delta string) {
	r.stopSpinnerNow()
	clean := strings.ReplaceAll(delta, "\r", "")
	if clean == "" {
		return
	}
	bar := C(Gray, "│")
	mc := maxCol()
	if !r.thinkStreaming {
		r.thinkStreaming = true
		printf("  %s ", bar)
		r.col = 0
	}
	buf := make([]byte, 0, len(clean))
	for _, ch := range clean {
		if ch == '\n' {
			if len(buf) > 0 { fmt.Print(C(Dim+Italic, string(buf))); buf = buf[:0] }
			printf("\n  %s ", bar)
			r.col = 0
			continue
		}
		if r.col >= mc {
			if len(buf) > 0 { fmt.Print(C(Dim+Italic, string(buf))); buf = buf[:0] }
			printf("\n  %s ", bar)
			r.col = 0
		}
		buf = append(buf, string(ch)...)
		r.col++
	}
	if len(buf) > 0 { fmt.Print(C(Dim+Italic, string(buf))) }
}

// finishThinkingStream closes the thinking block.
func (r *Renderer) finishThinkingStream() {
	if r.thinkStreaming {
		r.stopSpinnerNow()
		printf("\n  %s\n\n", C(Gray, "╰"))
		r.thinkStreaming = false
		r.col = 0 // cursor now at line start
	}
}

// renderTextDelta prints text fragments with a cyan left border.
func (r *Renderer) renderTextDelta(delta string) {
	r.stopSpinnerNow()
	clean := strings.ReplaceAll(delta, "\r", "")
	if clean == "" {
		return // don't open a block for empty deltas
	}
	bar := C(BrightCyan, "│")
	mc := maxCol()
	if !r.streaming {
		r.streaming = true
		printf("  %s ", bar)
		r.col = 0
	}
	// Buffer chunks between wraps for fewer Print calls
	buf := make([]byte, 0, len(clean))
	for _, ch := range clean {
		if ch == '\n' {
			if len(buf) > 0 { fmt.Print(string(buf)); buf = buf[:0] }
			printf("\n  %s ", bar)
			r.col = 0
			continue
		}
		if r.col >= mc {
			if len(buf) > 0 { fmt.Print(string(buf)); buf = buf[:0] }
			printf("\n  %s ", bar)
			r.col = 0
		}
		buf = append(buf, string(ch)...)
		r.col++
	}
	if len(buf) > 0 { fmt.Print(string(buf)) }
}

// finishTextStream closes the text block and prints the compact footer.
func (r *Renderer) finishTextStream() {
	if !r.streaming {
		// No text was rendered — skip entirely, tokens already shown in previous footer
		return
	}
	dur := time.Since(r.startTime)
	footer := r.buildFooter(dur)
	printf("\n  %s\n  %s %s\n\n", C(BrightCyan, "│"), C(BrightCyan, "╰"), C(Gray, footer))
	r.streaming = false
	r.col = 0 // cursor now at line start
}

// finishAnyStream closes any active stream output.
func (r *Renderer) finishAnyStream() {
	r.finishThinkingStream()
}

// buildFooter creates a compact PI-style footer:
// 1.5s ↑829 ↓79 R1.2k W213 $0.012 20.0%/200k (claude-oauth) claude-sonnet-4-20250514
func (r *Renderer) buildFooter(dur time.Duration) string {
	parts := []string{fmt.Sprintf("%.1fs", dur.Seconds())}

	if r.lastInput > 0 {
		parts = append(parts, "↑"+compactNum(r.lastInput))
	}
	if r.lastOutput > 0 {
		parts = append(parts, "↓"+compactNum(r.lastOutput))
	}
	if r.lastCacheRead > 0 {
		parts = append(parts, "R"+compactNum(r.lastCacheRead))
	}
	if r.lastCacheWrite > 0 {
		parts = append(parts, "W"+compactNum(r.lastCacheWrite))
	}

	// Accumulated session cost — from session (source of truth)
	if r.totalCost > 0 {
		costStr := formatCost(r.totalCost)
		if r.subPricing {
			costStr += " (sub)"
		}
		parts = append(parts, costStr)
	}

	// Context window usage % / size — from session
	if r.contextUsage > 0 {
		if r.contextWindow > 0 {
			parts = append(parts, fmt.Sprintf("%.1f%%/%s", r.contextUsage*100, compactNum(r.contextWindow)))
		} else {
			parts = append(parts, fmt.Sprintf("%.1f%%", r.contextUsage*100))
		}
	}

	// Model and thinking level
	var tag string
	if r.modelID != "" {
		tag = r.modelID
	}
	if r.thinkingLevel != "" {
		tag += " • " + r.thinkingLevel
	}
	if tag != "" {
		parts = append(parts, tag)
	}

	return joinParts(parts, " ")
}

// SetThinkingLevel updates the footer label at runtime (called on /thinking change).
func (r *Renderer) SetThinkingLevel(level string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if level == "disable" {
		r.thinkingLevel = ""
	} else {
		r.thinkingLevel = level
	}
}



// ============================================================
// Non-streaming Renderers
// ============================================================

func (r *Renderer) renderThinking(content string) {
	if content == "" {
		return
	}
	// Non-streamed thinking: show with gray bar
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		printf("  %s %s\n", C(Gray, "│"), C(Dim+Italic, line))
	}
	printf("\n") // spacer
}

func (r *Renderer) renderToolCall(name, args string) {
	r.stopSpinnerNow()
	icon := toolIcon(name)
	argsSummary := formatToolArgs(name, args)
	printf("  %s %s %s\n",
		C(BrightYellow, icon),
		C(Bold+Yellow, name),
		C(Dim, argsSummary),
	)
	os.Stdout.Sync() // force flush so tool appears immediately
}

func (r *Renderer) renderToolResult(name, output string, dur time.Duration, isErr bool) {
	r.stopSpinnerNow()
	durStr := C(Gray, fmt.Sprintf("[%s]", dur.Round(time.Millisecond)))

	if isErr {
		summary := OneLiner(output, 100)
		printf("    %s %s %s\n\n", C(Red, "✗"), C(Red, summary), durStr)
		r.col = 0
		return
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	lineCount := len(lines)

	if lineCount == 0 || output == "(no output)" {
		printf("    %s %s %s\n\n", C(Green, "✓"), C(Dim, "(no output)"), durStr)
		r.col = 0
		return
	}

	if lineCount == 1 {
		summary := OneLiner(output, 100)
		printf("    %s %s %s\n\n", C(Green, "✓"), C(Dim, summary), durStr)
		r.col = 0
		return
	}

	first := OneLiner(lines[0], 80)
	printf("    %s %s %s %s\n\n",
		C(Green, "✓"),
		C(Dim, first),
		C(Gray, fmt.Sprintf("(+%d lines)", lineCount-1)),
		durStr,
	)
}

// renderTokens is no longer used for streaming (tokens go in footer).
// Kept for non-streaming fallback.
func (r *Renderer) renderTokens(input, output int) {
	// no-op — tokens are now shown in the footer
}

func (r *Renderer) renderError(msg string) {
	printf("  %s %s\n", C(Red, "⚠"), C(Red, msg))
}

// ============================================================
// Helpers
// ============================================================

func toolIcon(name string) string {
	switch strings.ToLower(name) {
	case "bash":
		return "⚡"
	case "read_file", "read":
		return "📄"
	case "write_file", "write":
		return "✏️"
	case "edit":
		return "🔧"
	case "web_search", "websearch":
		return "🔍"
	case "grep", "glob":
		return "🔎"
	default:
		return "🔧"
	}
}

func formatToolArgs(toolName string, rawArgs string) string {
	var args map[string]any
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return OneLiner(rawArgs, 80)
	}

	switch strings.ToLower(toolName) {
	case "bash":
		if cmd, ok := args["command"].(string); ok {
			return OneLiner(cmd, 80)
		}
	case "read_file", "read":
		if path, ok := args["path"].(string); ok {
			return path
		}
	case "write_file", "write":
		if path, ok := args["path"].(string); ok {
			size := ""
			if content, ok := args["content"].(string); ok {
				size = fmt.Sprintf(" (%d bytes)", len(content))
			}
			return path + size
		}
	case "edit":
		if path, ok := args["path"].(string); ok {
			return path
		}
	case "web_search", "websearch":
		if q, ok := args["query"].(string); ok {
			return fmt.Sprintf("%q", q)
		}
	}

	var parts []string
	for k, v := range args {
		vs := fmt.Sprintf("%v", v)
		parts = append(parts, k+"="+OneLiner(vs, 30))
	}
	return strings.Join(parts, " ")
}
