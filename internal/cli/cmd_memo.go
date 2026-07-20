package cli

import "flag"

// cmdMemo is `harness memo [<query>] [--all] [--global] [--content] [--limit N]
// [--skip N]`. With no query it lists memories; with a bare query it full-text
// searches them. The query is positional and taken before flag parsing.
func cmdMemo(args []string) error {
	opts := MemoOpts{Limit: 10}

	// A leading bare arg (not a flag) is the query.
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		opts.Query = args[0]
		args = args[1:]
	}

	fs := flag.NewFlagSet("memo", flag.ContinueOnError)
	fs.BoolVar(&opts.All, "all", false, "search across ALL projects")
	fs.BoolVar(&opts.Global, "global", false, "only global (cross-project) memories")
	fs.BoolVar(&opts.Content, "content", false, "show each memory's content")
	fs.IntVar(&opts.Limit, "limit", 10, "max results per page")
	fs.IntVar(&opts.Skip, "skip", 0, "pagination offset")
	if err := fs.Parse(args); err != nil {
		return err
	}

	a := newAgent()
	defer a.Close()
	ctx, cancel := signalContext()
	defer cancel()
	return RunMemo(ctx, a, opts, "text")
}
