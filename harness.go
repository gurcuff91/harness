// Package harness is the public SDK entry point for embedding the harness agent.
//
// The agent is the SDK: create one with [New], open a [Session], subscribe to
// its events, and drive it with prompts.
//
//	a := harness.New(harness.Options{
//	    ThinkingLevel: "medium",
//	    EnableMCPs:    true,
//	})
//	defer a.Close()
//
//	sess, err := a.NewSession(cwd, "anthropic/claude-sonnet-4-20250514")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	sess.Subscribe(func(e harness.Event) { /* render */ })
//	sess.Prompt(ctx, "Hello!")
//
// These are convenience aliases over the [agent] package — the canonical home of
// the types. Deeper building blocks live in their own public packages:
//   - agent/tools     — implement custom tools
//   - agent/store     — implement custom session storage
//   - agent/resources — implement custom skill/resource loaders
//   - agent/memory    — the persistent memory store
//   - types           — Event, Message, ModelMeta and other shared types
//
// Everything under internal/ (providers, config, transports, build version) is
// implementation detail and not part of the SDK's compatibility surface.
package harness

import "github.com/gurcuff91/harness/agent"

// Agent is a configured harness agent. See [agent.Agent].
type Agent = agent.Agent

// Options configures a new [Agent]. See [agent.AgentOptions].
type Options = agent.AgentOptions

// Session is a single conversation with an [Agent]. See [agent.Session].
type Session = agent.Session

// Event is an event emitted by a session. See [agent.Event].
type Event = agent.Event

// Handler receives session events. See [agent.Handler].
type Handler = agent.Handler

// New creates an Agent from the given options. See [agent.New].
func New(opts Options) *Agent { return agent.New(opts) }
