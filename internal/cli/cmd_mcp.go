package cli

import (
	"flag"
	"strings"
)

// kvFlag is a repeatable "key<sep>value" flag (e.g. --env KEY=VAL, --header
// KEY:VAL). Values accumulate into a map; the value may itself contain the
// separator (a header value with a colon).
type kvFlag struct {
	sep string
	m   map[string]string
}

func (f *kvFlag) String() string { return "" }
func (f *kvFlag) Set(s string) error {
	idx := strings.Index(s, f.sep)
	if idx < 0 {
		return errf("expected key%svalue, got %q", f.sep, s)
	}
	if f.m == nil {
		f.m = map[string]string{}
	}
	f.m[strings.TrimSpace(s[:idx])] = strings.TrimSpace(s[idx+len(f.sep):])
	return nil
}
func (f *kvFlag) result() map[string]string {
	if len(f.m) == 0 {
		return nil
	}
	return f.m
}

// cmdMCP dispatches `harness mcp [list | add <name> ... | rm <name>]`.
func cmdMCP(args []string) error {
	a := newAgent()
	defer a.Close()
	ctx, cancel := signalContext()
	defer cancel()

	if len(args) == 0 || args[0] == "list" {
		return RunMCPList(ctx, a, "text")
	}
	switch args[0] {
	case "add":
		if len(args) < 2 {
			return errUsage("mcp add <name> [--local|--remote] [flags]")
		}
		name, opts, err := parseMCPAdd(args[1:])
		if err != nil {
			return err
		}
		return RunMCPAdd(ctx, a, name, opts, "text")
	case "rm", "remove":
		if len(args) < 2 {
			return errUsage("mcp rm <name>")
		}
		return RunMCPRemove(ctx, a, args[1], "text")
	default:
		return errf("unknown mcp subcommand: %s\nusage: harness mcp [list | add <name> ... | rm <name>]", args[0])
	}
}

// parseMCPAdd parses `mcp add` args: the server name is the first positional,
// taken before flag parsing (Go's flag stops at the first non-flag argument).
func parseMCPAdd(args []string) (string, MCPAddOpts, error) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "", MCPAddOpts{}, errUsage("mcp add <name> [--local|--remote] [flags]")
	}
	name := args[0]

	fs := flag.NewFlagSet("mcp add", flag.ContinueOnError)
	env := &kvFlag{sep: "="}
	headers := &kvFlag{sep: ":"}
	var opts MCPAddOpts
	fs.BoolVar(&opts.Local, "local", false, "local server (spawns a command)")
	fs.BoolVar(&opts.Remote, "remote", false, "remote server (dials a URL)")
	fs.BoolVar(&opts.Disabled, "disabled", false, "add the server disabled")
	fs.StringVar(&opts.Command, "command", "", "local: command + args")
	fs.StringVar(&opts.URL, "url", "", "remote: server URL")
	fs.StringVar(&opts.Bearer, "bearer", "", "remote: Authorization Bearer token")
	fs.Var(env, "env", "local: env var KEY=VAL (repeatable)")
	fs.Var(headers, "header", "remote: HTTP header KEY:VAL (repeatable)")
	if err := fs.Parse(args[1:]); err != nil {
		return "", MCPAddOpts{}, err
	}
	opts.Env = env.result()
	opts.Headers = headers.result()
	return name, opts, nil
}
