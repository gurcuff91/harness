package cli

import (
	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/memory"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/mcp"
)

// interactiveMaxIterations is the ReAct iteration cap for interactive
// frontends (TUI, standalone `harness serve`, Telegram) — higher than
// agent.defaultMaxIterations (50) because these are where real, complex,
// multi-step work happens (explore code, edit multiple files, run
// verification, iterate on failures), unlike the one-shot commands newAgent()
// serves (mcp/memo), which keep the SDK default.
const interactiveMaxIterations = 120

// defaultMemory opens harness's default persistent memory — the SQLite store
// at ~/.harness/agent/memory.db — for the CLI's agents. On failure it returns
// an UNTYPED nil interface (never the typed-nil *memory.SQLiteStore that
// OpenSQLite returns alongside its error), so the agent runs without memory
// instead of believing it has one: memory is a convenience, never a reason
// for a command to fail.
func defaultMemory() memory.Store {
	m, err := memory.OpenSQLite("")
	if err != nil {
		return nil
	}
	return m
}

// newAgent builds the process's root agent with MCP servers and project-scoped
// memory, but without the scheduler engine — for one-shot commands that
// actually EXECUTE agent work using those tools and rely on the default
// FileStore session persistence: `harness mcp` (its `list` shows live MCP
// connection status via the manager this spawns) and `harness memo` (reads
// the memory store this opens) — neither creates a session at all, so the
// default store is moot for them either way. The caller must Close() it to
// stop MCP subprocesses. ThinkingLevel is left zero; agent.New resolves it
// from settings.
//
// Commands that only shuffle JSON over the HTTP API — providers,
// connect/disconnect, sessions, delete, schedules, settings — don't touch
// tools/MCP/memory at all and use newConfigAgent instead, so they don't pay
// for spawning MCP subprocesses or opening the memory DB just to list/edit
// config. `-p` prompts use newOneShotAgent, not this — see its doc comment
// for why persisting a throwaway session is the wrong default there.
func newAgent() *agent.Agent {
	return agent.New(agent.AgentOptions{
		MCPServers: mcp.ServersFromSettings(),
		Memory:     defaultMemory(),
	})
}

// newOneShotAgent builds the agent for `harness -p <prompt>`: a single turn,
// run once, with no way to resume it (unlike the TUI's sessions, there is no
// `-p --resume`). It otherwise matches newAgent (MCP + memory available to the
// tools the turn might use) but overrides the session Store to
// store.NewInMemoryStore() instead of agent.New's default FileStore.
//
// Without this, every `-p` invocation persisted its session (messages,
// tokens, cost) to ~/.harness/agent/sessions/ — a file nobody would ever read
// again, since there's no way back to it. Over time that's pure litter: an
// audit of an existing installation found the majority of persisted sessions
// were exactly this shape (a "New Session"-named session, one small turn,
// never touched again). The turn's actual work — the reply the user asked
// for — is delivered on stdout regardless of where the session lives, so
// there is nothing lost by not persisting it.
//
// It has WebSearch, like every agent that does real work, and never colleagues or the scheduler: a process
// that exits when its turn ends has nothing to offer another agent, no time to wait for one, and no engine
// to keep running.
func newOneShotAgent() *agent.Agent {
	return agent.New(oneShotOptions())
}

// oneShotOptions is the AgentOptions newOneShotAgent builds, separate so a test can look at the decision.
func oneShotOptions() agent.AgentOptions {
	return agent.AgentOptions{
		MCPServers:      mcp.ServersFromSettings(),
		Memory:          defaultMemory(),
		Store:           store.NewInMemoryStore(),
		EnableWebSearch: true,
	}
}

// newInteractiveAgent builds the root agent for the long-running commands that do agent work: the TUI,
// `harness serve`, Telegram, Slack and ACP. It is always at interactiveMaxIterations and always has
// WebSearch (these are where the model fields real-time and external questions; the tool gates itself on
// the minimax provider being connected and returns a clear "connect the minimax provider" error otherwise,
// so it is safe without a pre-flight check).
//
// The two OPT-IN extras come in through x (see extras.go): colleagues and the scheduler engine. They are
// off unless the command asked for them with --with-colleagues / --with-scheduler or the matching
// environment variable, because each costs every session something even when unused.
//
// directives are extra AGENT-WIDE system-prompt blocks (every session sees them). Frontend-specific ones
// (telegram.Directive, slack.Directive) don't go here: those frontends add them per session.
//
// SessionInfo itself is always registered regardless of this constructor (see agent.go's
// buildSessionTools) — it has no dedicated EnableX flag, unlike the options below.
func newInteractiveAgent(x extras, directives ...string) *agent.Agent {
	return agent.New(interactiveOptions(x, directives))
}

// interactiveOptions is the AgentOptions newInteractiveAgent builds, separate so a test can look at the
// decision without opening an agent.
func interactiveOptions(x extras, directives []string) agent.AgentOptions {
	opts := agent.AgentOptions{
		MCPServers:      mcp.ServersFromSettings(),
		Memory:          defaultMemory(),
		EnableScheduler: x.scheduler,
		EnableWebSearch: true,
		MaxIterations:   interactiveMaxIterations,
		Directives:      directives,
	}
	// --with-colleagues registers the colleague channel (channels/colleague):
	// that alone makes every session this agent opens — whichever frontend
	// opens it — a colleague with an id of its own.
	opts.Channels = colleagueChannels(x, false)
	return opts
}

// newConfigAgent builds a minimal agent for commands that only read/write
// config/state over the HTTP API without executing any agent work: settings,
// providers, connect/disconnect, sessions, delete, schedules. No MCP
// subprocesses spawned, no memory DB opened — cheaper to construct than
// newAgent for commands that never touch tools.
func newConfigAgent() *agent.Agent {
	return agent.New(agent.AgentOptions{})
}
