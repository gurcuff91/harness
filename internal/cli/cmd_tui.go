package cli

import (
	"flag"

	"github.com/gurcuff91/harness/internal/transport/tui"
)

// cmdTUI runs the interactive terminal UI. It accepts --model, --thinking,
// --resume, and --scheduler. args may be nil (no flags), or the full argv when
// harness is invoked with leading TUI flags (e.g. `harness --model x`).
func cmdTUI(args []string) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	model := fs.String("model", "", "model to use (provider/model)")
	thinking := fs.String("thinking", "", "thinking level: off|low|medium|high|xhigh")
	resume := fs.String("resume", "", "resume a session by id")
	scheduler := fs.Bool("scheduler", false, "run the cron scheduler engine")
	if err := fs.Parse(args); err != nil {
		return err
	}

	a := newInteractiveAgent(*scheduler, 50)
	defer a.Close()
	ctx, cancel := signalContext()
	defer cancel()

	t := tui.New(a)
	t.SetFlags(*model, *thinking, *resume)
	t.SetScheduler(*scheduler)
	return t.Run(ctx)
}
