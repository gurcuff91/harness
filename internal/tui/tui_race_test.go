package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/internal/tui/ansi"
	"github.com/gurcuff91/harness/internal/tui/term"
)

// inputTerminal is a recording terminal that keeps the onInput callback render.TUI.Start hands to
// Terminal.Start — the very function the real terminal's reader goroutine calls — so a test can
// type through the REAL input path while the render timer draws on its own goroutine.
type inputTerminal struct {
	recTerminal
	onInput func(string)
	frames  atomic.Int64 // Columns() calls: doRender asks for the width once per frame
}

func (i *inputTerminal) Columns() int { i.frames.Add(1); return i.recTerminal.Columns() }

func (i *inputTerminal) Start(onInput func(string), _ func()) error {
	i.onInput = onInput
	return nil
}

// liveUI builds the real render tree (buildUI) on an inputTerminal and starts the renderer.
func liveUI(t *testing.T) (*TUI, *inputTerminal) {
	t.Helper()
	tu := newTestTUIForEvents()
	it := &inputTerminal{recTerminal: recTerminal{mockTerminal: mockTerminal{cols: 80, rows: 24}}}
	tu.newTerminal = func() term.Terminal { return it }
	tu.buildUI()
	if err := tu.tui.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tu.tui.Stop)
	return tu, it
}

func (i *inputTerminal) key(s string) { i.onInput(s) }

// typeStorm drives keys through the input callback from one goroutine for d while a second
// goroutine keeps requesting renders, so the render timer fires continuously.
func typeStorm(t *testing.T, tu *TUI, it *inputTerminal, d time.Duration, script []string) {
	t.Helper()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // render pressure, as events from the SSE stream would be
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				tu.tui.RequestRender(false)
				time.Sleep(200 * time.Microsecond)
			}
		}
	}()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		for _, k := range script {
			it.key(k)
		}
	}
	close(stop)
	wg.Wait()
}

// liveUIWithCommands is liveUI plus one session command with a fixed list of values, so the
// palette has a second level to push, filter and pop. Its name is unique, so a filter on it matches
// nothing else — an Enter in a test can never reach a command that needs the (absent) server.
func liveUIWithCommands(t *testing.T) (*TUI, *inputTerminal) {
	t.Helper()
	tu := newTestTUIForEvents()
	tu.sessionCmds = []CommandDef{{
		Name: "zzlevels", Description: "two-level test command",
		Params: []ParamDef{{Name: "level", Required: true, Values: []string{"off", "low", "high"}}},
	}}
	it := &inputTerminal{recTerminal: recTerminal{mockTerminal: mockTerminal{cols: 80, rows: 24}}}
	tu.newTerminal = func() term.Terminal { return it }
	tu.buildUI()
	if err := tu.tui.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tu.tui.Stop)
	return tu, it
}

// These tests drive keys through the real input callback from one goroutine while the render timer
// draws on its own and a helper goroutine inserts text (what a clipboard image paste does). Under
// -race they fail on any state the two sides share without a lock — the palette's list, its level
// stack and open flag, the editor's buffer and cursor, the bash-mode flag the separators read.
// Nothing here presses Enter on a command that needs the server.

func TestTypingAndNavigatingThePaletteWhileTheRenderTimerDraws(t *testing.T) {
	tu, it := liveUIWithCommands(t)
	script := []string{
		"/", "t", "h", "i", "n", "k", "\x1b[B", "\x1b[A", "\x7f", "\x7f", "\x7f", "\x7f", "\x7f", "\x7f", "\x1b",
		"/", "r", "e", "s", "\x1b[A", "\x15", "\x1b",
		"/", "z", "z", "l", "e", "v", "\r", // Enter pushes the sub-palette (the only match)
		"l", "o", "\x1b[B", "\x7f", "\x7f", "\x7f", "\x1b", // filter it, then back to the root, then close
		"!", "l", "s", "\x15", // bash mode on, then off
		"\x1b[200~/zzlevels\x1b[201~", "\x15", // a paste that opens the palette
		"a", "\n", "b", "\x1b[D", "\x1b[C", "\x01", "\x05", "\x0b", "\x17", "\x15",
	}
	typeStorm(t, tu, it, 1500*time.Millisecond, script)
}

func TestTabAndEscapeWhileTheRenderTimerDraws(t *testing.T) {
	tu, it := liveUIWithCommands(t)
	script := []string{
		"/", "z", "z", "\t", "\x15", // Tab autocompletes the first match into the editor
		"/", "z", "z", "l", "\r", "\t", "\x15", // …and from the sub-palette
		"/", "\x1b[B", "\x1b[B", "\x1b[A", "\x1b", "\x15",
	}
	typeStorm(t, tu, it, 1500*time.Millisecond, script)
}

func TestInsertingFromAnotherGoroutineWhileTheRenderTimerDraws(t *testing.T) {
	tu, it := liveUIWithCommands(t)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // what pasteClipboardImage's goroutine does
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				tu.editor.InsertText("/tmp/image.png")
				_ = tu.editor.Value()
				tu.editor.Clear()
			}
		}
	}()
	typeStorm(t, tu, it, 1000*time.Millisecond, []string{
		"a", "b", "\x7f", "/", "z", "\x15",
		"/", "z", "\x1b[B", "\x1b[A", "\x7f", "\x1b", "\x15", // no Enter: the helper goroutine edits the same buffer, so Enter could land on a command that needs the server
	})
	close(stop)
	wg.Wait()
}

// The third producer: the SSE event stream. A turn streams thinking, text, tool calls and token
// updates (history, spinner, footer, the markdown block) on its own goroutine while the person types
// and the render timer draws. This is the everyday shape of a live session.
func TestAStreamingTurnWhileTypingAndTheRenderTimerDraws(t *testing.T) {
	tu, it := liveUIWithCommands(t)
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan client.Event)
	done := make(chan struct{})
	go func() { tu.consumeEvents(ctx, events); close(done) }()

	go func() {
		defer close(events)
		for turn := 0; turn < 60 && ctx.Err() == nil; turn++ {
			send := func(e client.Event) bool {
				select {
				case events <- e:
					return true
				case <-ctx.Done():
					return false
				}
			}
			id := fmt.Sprintf("t%d", turn)
			seq := []client.Event{
				{Type: "turn_start"},
				{Type: "loop_start"},
				{Type: "thinking", Delta: "weighing the options "},
				{Type: "thinking_end"},
				{Type: "text", Delta: "# Heading\n\nsome **bold** text and a list:\n- one\n- two\n"},
				{Type: "text_end"},
				{Type: "tool_start", ToolID: id, ToolName: "Bash"},
				{Type: "tool_args", ToolID: id, Delta: `{"command":"ls"}`},
				{Type: "tool_call", ToolID: id, ToolArgs: `{"command":"ls"}`},
				{Type: "tool_result", ToolID: id, Output: "file.txt", IsError: false},
				{Type: "loop_end"},
				{Type: "turn_end"},
			}
			for _, e := range seq {
				if !send(e) {
					return
				}
			}
		}
	}()

	typeStorm(t, tu, it, 1500*time.Millisecond, []string{"/", "t", "h", "\x1b[B", "\x7f", "\x7f", "\x1b", "a", "\x15"})
	cancel()
	<-done
	if n := len(blockSummary(tu)); n < 10 {
		t.Fatalf("the stream produced only %d history blocks: the test is not exercising the event path", n)
	}
}

// ── what the palette does, key by key (the level stack the race tests above hammer) ─────────────

// paletteState is what a person can see of the palette: whether it is open, how many levels deep,
// what the editor holds, and which command the current level belongs to.
func paletteState(tu *TUI) string {
	return fmt.Sprintf("open=%v depth=%d editor=%q parent=%q", tu.palette.IsOpen(), tu.palette.depth(), tu.editor.Value(), tu.palette.curParent())
}

func TestPaletteLevelsPushPopAndClose(t *testing.T) {
	tu, it := liveUIWithCommands(t)
	step := func(key, want string) {
		t.Helper()
		it.key(key)
		if got := paletteState(tu); got != want {
			t.Fatalf("after %q: %s, want %s", key, got, want)
		}
	}
	if got, want := paletteState(tu), `open=false depth=0 editor="" parent=""`; got != want {
		t.Fatalf("initially %s, want %s", got, want)
	}
	step("/", `open=true depth=1 editor="/" parent=""`)
	step("z", `open=true depth=1 editor="/z" parent=""`)
	step("z", `open=true depth=1 editor="/zz" parent=""`)
	step("l", `open=true depth=1 editor="/zzl" parent=""`)
	// Enter on a list command pushes its sub-palette and empties the editor (the filter is typed anew).
	step("\r", `open=true depth=2 editor="" parent="zzlevels"`)
	if n := tu.palette.list.Count(); n != 3 {
		t.Fatalf("the sub-palette has %d entries, want 3 (off, low, high)", n)
	}
	step("l", `open=true depth=2 editor="l" parent="zzlevels"`)
	if n := tu.palette.list.Count(); n != 1 {
		t.Fatalf("filtering %q leaves %d entries, want 1 (low)", "l", n)
	}
	// Backspace on a NON-empty filter edits it; on an empty one it pops back to the root.
	step("\x7f", `open=true depth=2 editor="" parent="zzlevels"`)
	step("\x7f", `open=true depth=1 editor="" parent=""`)
	if n := tu.palette.list.Count(); n < 10 {
		t.Fatalf("the root palette shows %d entries after popping, want all the commands", n)
	}
	// Escape at a sub-level pops one level and leaves "/" in the editor; at the root it closes.
	step("/", `open=true depth=1 editor="/" parent=""`)
	step("z", `open=true depth=1 editor="/z" parent=""`)
	step("z", `open=true depth=1 editor="/zz" parent=""`)
	step("\r", `open=true depth=2 editor="" parent="zzlevels"`)
	step("\x1b", `open=true depth=1 editor="/" parent=""`)
	step("\x1b", `open=false depth=0 editor="" parent=""`)
}

func TestPaletteTabAutocompletes(t *testing.T) {
	tu, it := liveUIWithCommands(t)
	for _, k := range []string{"/", "z", "z", "l"} {
		it.key(k)
	}
	// Tab on a list command opens its sub-palette (it does not fill the editor).
	it.key("\t")
	if got, want := paletteState(tu), `open=true depth=2 editor="" parent="zzlevels"`; got != want {
		t.Fatalf("after Tab on a list command: %s, want %s", got, want)
	}
	// Tab at the sub-level fills "/parent token" and closes.
	it.key("\t")
	if got, want := paletteState(tu), `open=false depth=0 editor="/zzlevels off" parent=""`; got != want {
		t.Fatalf("after Tab in the sub-palette: %s, want %s", got, want)
	}
}

func TestPaletteClosesWhenTheTextStopsBeingACommand(t *testing.T) {
	tu, it := liveUIWithCommands(t)
	for _, k := range []string{"/", "z"} {
		it.key(k)
	}
	if !tu.palette.IsOpen() {
		t.Fatal("the palette did not open on '/'")
	}
	it.key(" ") // a space ends the command word: free-form arguments follow
	if got, want := paletteState(tu), `open=false depth=0 editor="/z " parent=""`; got != want {
		t.Fatalf("after a space: %s, want %s", got, want)
	}
	// closing again, with nothing open, is a no-op and must not panic
	tu.palette.close()
	tu.palette.close()
	if tu.palette.IsOpen() || tu.palette.depth() != 0 {
		t.Fatalf("a double close left %s", paletteState(tu))
	}
}

// A frame is drawn only when the palette is open, and an open palette must already hold its
// commands: opening publishes the items BEFORE the open flag, so the render timer can never draw an
// open-but-stale list (the one a previous use left narrowed). The list is narrowed while the palette
// is closed, then "/" reopens it, and every observation of "open" must find the full root list.
func TestAnOpenPaletteAlwaysHoldsItsCommands(t *testing.T) {
	tu, it := liveUIWithCommands(t)
	total := len(tu.rootCommandItems())
	var bad, epoch atomic.Int64 // epoch counts the moments the test narrows the list
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				// Judge only a pair of reads no narrowing came between: otherwise "open" may be
				// from before the palette closed and the count from after the test narrowed it.
				e1 := epoch.Load()
				open := tu.palette.IsOpen()
				n := tu.palette.list.Count()
				if open && n != total && epoch.Load() == e1 {
					bad.Add(1)
				}
			}
		}
	}()
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		epoch.Add(1)
		tu.palette.list.SetFilter("zzl") // what a previous use leaves behind: a narrowed list, palette closed
		it.key("/")                      // reopens: must show ALL the commands from its first frame
		it.key("\x1b")
	}
	close(stop)
	<-done
	if n := bad.Load(); n > 0 {
		t.Fatalf("%d observations of an open palette whose list was not the full %d commands", n, total)
	}
}

// Typing "!" switches both separators to the warning color (direct bash mode); clearing the text
// switches them back.
func TestBashModeColorsTheSeparators(t *testing.T) {
	tu, it := liveUIWithCommands(t)
	rule := func() string { return strings.Join(tu.tui.Render(40), "\n") }
	normal := rule()
	if !strings.Contains(normal, ansi.FG(ansi.HexPrimary, strings.Repeat("─", 40))) {
		t.Fatalf("the separators are not drawn in the primary color: %q", normal)
	}
	it.key("!")
	bash := rule()
	if !strings.Contains(bash, ansi.FG(ansi.HexWarn, strings.Repeat("─", 40))) || strings.Contains(bash, ansi.FG(ansi.HexPrimary, strings.Repeat("─", 40))) {
		t.Fatalf("typing '!' did not switch the separators to the warning color: %q", bash)
	}
	it.key("\x7f")
	if got := rule(); got != normal {
		t.Fatalf("clearing the text did not restore the separators:\n%q\nwant\n%q", got, normal)
	}
}

// The palette's level stack is moved by the input goroutine (Enter, Escape, Backspace) and read by
// whichever goroutine edits the editor — the helper goroutine of a clipboard image paste goes
// editor.InsertText → OnChange → onEditorChange → depth. The methods are called directly (no Enter,
// which could land on a command that needs the server) so that the levels, the open flag and the
// list are exercised against each other at full speed, with the render timer drawing as well.
func TestPaletteLevelsFromTwoGoroutines(t *testing.T) {
	tu, it := liveUIWithCommands(t)
	items := tu.rootCommandItems()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // the paste goroutine
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				tu.editor.InsertText("/z")
				_ = tu.palette.curParent()
				tu.editor.Clear()
			}
		}
	}()
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		tu.palette.openRoot()
		tu.palette.pushSub("zzlevels", items[:3])
		_ = tu.palette.curParent()
		tu.palette.popLevel()
		tu.palette.close()
		it.key("\x1b[B")
	}
	close(stop)
	wg.Wait()
}

// The stack can be deeper than the product ever makes it (two levels today); popping must refresh
// the list with the level that becomes the top, not with the root.
func TestPopLevelShowsTheLevelThatBecomesTheTop(t *testing.T) {
	tu, _ := liveUIWithCommands(t)
	all := tu.rootCommandItems()
	tu.palette.openRoot()
	tu.palette.pushSub("one", all[:2])
	tu.palette.pushSub("two", all[:5])
	tu.palette.pushSub("three", all[:7])
	if d := tu.palette.depth(); d != 4 {
		t.Fatalf("depth %d, want 4", d)
	}
	for _, want := range []struct {
		count  int
		parent string
	}{{5, "two"}, {2, "one"}, {len(all), ""}} {
		if !tu.palette.popLevel() {
			t.Fatal("popLevel refused to pop")
		}
		if got := tu.palette.list.Count(); got != want.count || tu.palette.curParent() != want.parent {
			t.Fatalf("after a pop: %d entries, parent %q; want %d, %q", got, tu.palette.curParent(), want.count, want.parent)
		}
	}
	if tu.palette.popLevel() {
		t.Fatal("the root cannot be popped")
	}
}

// Closing a palette that is already closed changes nothing on screen, so it must not schedule a
// frame (and must not touch the level stack). A frame is observable: doRender asks the terminal for
// its width exactly once per frame.
func TestClosingAClosedPaletteSchedulesNoFrame(t *testing.T) {
	tu, it := liveUIWithCommands(t)
	it.key("/")
	it.key("\x1b")                     // opens, then closes
	time.Sleep(150 * time.Millisecond) // let the renderer go idle
	before := it.frames.Load()
	for i := 0; i < 5; i++ {
		tu.palette.close()
	}
	time.Sleep(150 * time.Millisecond)
	if n := it.frames.Load() - before; n != 0 {
		t.Fatalf("closing a closed palette drew %d frames, want 0", n)
	}
	// the same probe must see a frame when something DID change, or it would prove nothing
	before = it.frames.Load()
	it.key("/")
	time.Sleep(150 * time.Millisecond)
	if it.frames.Load() == before {
		t.Fatal("opening the palette drew no frame: the probe cannot see frames")
	}
}

// ── pasting into the palette ────────────────────────────────────────────────────────────────────

// A paste arrives as ONE input sequence, so the palette sees the whole text at once: it must open,
// filter and close exactly as it does for the same text typed key by key. (The palette itself is
// rendered, not the whole tree: how the editor wraps a huge unbroken token is a separate matter.)
func TestPastingIntoThePalette(t *testing.T) {
	paste := func(s string) string { return "\x1b[200~" + s + "\x1b[201~" }
	total := func(tu *TUI) int { return len(tu.rootCommandItems()) }

	t.Run("a bare slash opens the full list with the (n/total) indicator", func(t *testing.T) {
		tu, it := liveUIWithCommands(t)
		it.key(paste("/"))
		if !tu.palette.IsOpen() || tu.palette.list.Count() != total(tu) {
			t.Fatalf("open=%v, %d entries; want open with %d", tu.palette.IsOpen(), tu.palette.list.Count(), total(tu))
		}
		out := strings.Join(tu.palette.Render(80), "\n")
		if want := fmt.Sprintf("(1/%d)", total(tu)); !strings.Contains(out, want) {
			t.Fatalf("no %s indicator:\n%s", want, out)
		}
	})

	t.Run("a pasted filter narrows the list and the indicator follows", func(t *testing.T) {
		tu, it := liveUIWithCommands(t)
		it.key(paste("/co")) // connect, context, … and whatever has "co" in its description
		n := tu.palette.list.Count()
		if n == 0 || n >= total(tu) {
			t.Fatalf("'/co' left %d of %d entries", n, total(tu))
		}
		out := strings.Join(tu.palette.Render(80), "\n")
		// the list shows 8 rows: the (n/total) indicator exists only when there are more matches
		if got := strings.Contains(out, fmt.Sprintf("(1/%d)", n)); got != (n > 8) {
			t.Fatalf("%d matches, indicator present=%v:\n%s", n, got, out)
		}
		it.key("\x1b[B")
		if n > 8 && !strings.Contains(strings.Join(tu.palette.Render(80), "\n"), fmt.Sprintf("(2/%d)", n)) {
			t.Fatal("moving down did not move the indicator to (2/n)")
		}
	})

	t.Run("a paste with no match shows No matches and never panics", func(t *testing.T) {
		tu, it := liveUIWithCommands(t)
		it.key(paste("/" + strings.Repeat("q", 300)))
		if !tu.palette.IsOpen() || tu.palette.list.Count() != 0 {
			t.Fatalf("open=%v, %d entries", tu.palette.IsOpen(), tu.palette.list.Count())
		}
		if out := strings.Join(tu.palette.Render(80), "\n"); !strings.Contains(out, "No matches") {
			t.Fatalf("no 'No matches':\n%s", out)
		}
		// Enter / arrows / Tab on an empty list are harmless
		for _, k := range []string{"\x1b[B", "\x1b[A", "\t", "\r"} {
			it.key(k)
		}
		if !tu.palette.IsOpen() {
			t.Fatal("a key on an empty list closed the palette")
		}
	})

	t.Run("a large multi-line paste starting with a slash filters on all of it", func(t *testing.T) {
		tu, it := liveUIWithCommands(t)
		it.key(paste("/" + strings.Repeat("line\n", 2000)))
		if !tu.palette.IsOpen() || tu.palette.list.Count() != 0 {
			t.Fatalf("open=%v, %d entries", tu.palette.IsOpen(), tu.palette.list.Count())
		}
		_ = tu.palette.Render(80)
	})

	t.Run("a paste with a space closes the palette (free-form arguments follow)", func(t *testing.T) {
		tu, it := liveUIWithCommands(t)
		it.key("/")
		it.key(paste("model some argument"))
		if tu.palette.IsOpen() {
			t.Fatalf("still open with editor %q", tu.tui.Focused())
		}
		if tu.editor.Value() != "/model some argument" {
			t.Fatalf("editor %q", tu.editor.Value())
		}
	})

	t.Run("a paste into an open sub-palette filters that level", func(t *testing.T) {
		tu, it := liveUIWithCommands(t)
		for _, k := range []string{"/", "z", "z", "l", "\r"} {
			it.key(k)
		}
		it.key(paste("hi"))
		if got, want := paletteState(tu), `open=true depth=2 editor="hi" parent="zzlevels"`; got != want {
			t.Fatalf("%s, want %s", got, want)
		}
		if tu.palette.list.Count() != 1 {
			t.Fatalf("filter 'hi' left %d entries, want 1 (high)", tu.palette.list.Count())
		}
	})

	t.Run("pasting the same text key by key gives the same palette", func(t *testing.T) {
		a, ita := liveUIWithCommands(t)
		b, itb := liveUIWithCommands(t)
		ita.key(paste("/th"))
		for _, k := range []string{"/", "t", "h"} {
			itb.key(k)
		}
		if paletteState(a) != paletteState(b) || a.palette.list.Count() != b.palette.list.Count() ||
			strings.Join(a.palette.Render(80), "\n") != strings.Join(b.palette.Render(80), "\n") {
			t.Fatalf("pasted: %s (%d)\ntyped:  %s (%d)", paletteState(a), a.palette.list.Count(), paletteState(b), b.palette.list.Count())
		}
	})
}

// The same pastes while the render timer draws and a stream of keys arrives.
func TestPastesWhileTheRenderTimerDraws(t *testing.T) {
	tu, it := liveUIWithCommands(t)
	typeStorm(t, tu, it, 1500*time.Millisecond, []string{
		"\x1b[200~/" + strings.Repeat("q", 200) + "\x1b[201~", "\x15",
		"\x1b[200~/e\x1b[201~", "\x1b[B", "\x1b[B", "\x15",
		"\x1b[200~/z\x1b[201~", "\x1b[A", "\x1b", "\x15",
		"\x1b[200~line one\nline two\x1b[201~", "\x15",
	})
}

// The reported case, through the real palette: typing "/thi" and pressing Tab must complete the
// command whose NAME matches (thinking), not an unrelated one whose description mentions the letters
// (fork: "Fork this session…").
func TestTabCompletesTheCommandWhoseNameMatches(t *testing.T) {
	tu := newTestTUIForEvents()
	tu.sessionCmds = []CommandDef{{Name: "thinking", Description: "Set the thinking level"}}
	it := &inputTerminal{recTerminal: recTerminal{mockTerminal: mockTerminal{cols: 80, rows: 24}}}
	tu.newTerminal = func() term.Terminal { return it }
	tu.buildUI()
	if err := tu.tui.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tu.tui.Stop)

	for _, k := range []string{"/", "t", "h", "i"} {
		it.key(k)
	}
	if sel, ok := tu.palette.list.Selected(); !ok || sel.Value != "thinking" {
		t.Fatalf("after '/thi' the highlighted command is %v (ok=%v), want thinking", sel.Value, ok)
	}
	it.key("\t")
	if got := tu.editor.Value(); got != "/thinking" {
		t.Fatalf("Tab on '/thi' left %q in the editor, want /thinking", got)
	}
}
