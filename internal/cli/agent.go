package cli

import (
	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/store"
)

// interactiveMaxIterations is the ReAct iteration cap for interactive
// transports (TUI, standalone `harness serve`, Telegram) — higher than
// agent.defaultMaxIterations (50) because these are where real, complex,
// multi-step work happens (explore code, edit multiple files, run
// verification, iterate on failures), unlike the one-shot commands newAgent()
// serves (mcp/memo), which keep the SDK default.
const interactiveMaxIterations = 120

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
		EnableMCPs:   true,
		EnableMemory: true,
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
func newOneShotAgent() *agent.Agent {
	return agent.New(agent.AgentOptions{
		EnableMCPs:   true,
		EnableMemory: true,
		Store:        store.NewInMemoryStore(),
	})
}

// newInteractiveAgent builds the root agent for interactive transports (TUI,
// standalone `harness serve`, Telegram) — always at interactiveMaxIterations,
// and optionally running the cron scheduler engine so a fired schedule has a
// session to run in. directives are extra system-prompt blocks a transport
// needs (e.g. Telegram's file-upload instructions); omit for TUI/serve, pass
// telegram.Directive for Telegram.
func newInteractiveAgent(scheduler bool, directives ...string) *agent.Agent {
	return agent.New(agent.AgentOptions{
		EnableMCPs:      true,
		EnableMemory:    true,
		EnableScheduler: scheduler,
		MaxIterations:   interactiveMaxIterations,
		Directives:      directives,
	})
}

// newConfigAgent builds a minimal agent for commands that only read/write
// config/state over the HTTP API without executing any agent work: settings,
// providers, connect/disconnect, sessions, delete, schedules. No MCP
// subprocesses spawned, no memory DB opened — cheaper to construct than
// newAgent for commands that never touch tools.
func newConfigAgent() *agent.Agent {
	return agent.New(agent.AgentOptions{})
}
