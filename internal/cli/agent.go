package cli

import (
	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/internal/transport/telegram"
)

// newAgent builds the process's root agent with MCP servers and project-scoped
// memory, but without the scheduler engine — the default for one-shot commands
// and the passive HTTP server. The caller must Close() it to stop MCP
// subprocesses. ThinkingLevel is left zero; agent.New resolves it from settings.
func newAgent() *agent.Agent {
	return agent.New(agent.AgentOptions{
		EnableMCPs:   true,
		EnableMemory: true,
	})
}

// newInteractiveAgent is like newAgent but optionally runs the cron scheduler
// engine — used by interactive transports (TUI) that provide a session for a
// fired schedule to run in.
func newInteractiveAgent(scheduler bool) *agent.Agent {
	return agent.New(agent.AgentOptions{
		EnableMCPs:      true,
		EnableMemory:    true,
		EnableScheduler: scheduler,
	})
}

// newTelegramAgent is the root agent for the Telegram transport: like the
// interactive agent plus the Telegram directive, which teaches the agent to
// send files back to the chat via <tel:uploadFile> tags the transport parses.
func newTelegramAgent(scheduler bool) *agent.Agent {
	return agent.New(agent.AgentOptions{
		EnableMCPs:      true,
		EnableMemory:    true,
		EnableScheduler: scheduler,
		Directives:      []string{telegram.Directive},
	})
}

// newConfigAgent builds a minimal agent for commands that only read/write config
// (settings) — no MCP subprocesses, no memory.
func newConfigAgent() *agent.Agent {
	return agent.New(agent.AgentOptions{})
}
