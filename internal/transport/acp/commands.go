package acp

import (
	"fmt"

	"github.com/gurcuff91/harness/client"
)

// buildConfigOptions returns the session config options this transport
// advertises: the active model (category "model" — ACP's semantic category
// for a model selector; "model_config" is reserved for model-related
// PARAMETERS like context size, not the selector itself) and the thinking
// level (category "thought_level", ACP's dedicated category for a
// reasoning-level selector). Both are read fresh from the server so a
// freshly created/loaded session always reports its true current values.
func buildConfigOptions(c *client.Client) ([]sessionConfigOption, error) {
	settings, err := c.GetSettings()
	if err != nil {
		return nil, fmt.Errorf("acp: get settings: %w", err)
	}
	models, err := c.ListModels()
	if err != nil {
		return nil, fmt.Errorf("acp: list models: %w", err)
	}

	// m.Model is already the full "provider/model" identifier (see
	// GET /api/models) — not a bare model name needing the provider prefix.
	modelValues := make([]sessionConfigOptionValue, 0, len(models))
	for _, m := range models {
		modelValues = append(modelValues, sessionConfigOptionValue{Value: m.Model, Name: m.Model})
	}

	thinkingValues := []sessionConfigOptionValue{
		{Value: "off", Name: "Off"},
		{Value: "low", Name: "Low"},
		{Value: "medium", Name: "Medium"},
		{Value: "high", Name: "High"},
		{Value: "xhigh", Name: "XHigh"},
	}

	return []sessionConfigOption{
		{
			ID:           "model",
			Category:     "model",
			Name:         "Model",
			Type:         "select",
			CurrentValue: settings.ActiveModel,
			Options:      modelValues,
		},
		{
			ID:           "thinking",
			Category:     "thought_level",
			Name:         "Thinking",
			Type:         "select",
			CurrentValue: settings.ThinkingLevel,
			Options:      thinkingValues,
		},
	}, nil
}

// commandsCoveredByConfigOptions are session commands that buildConfigOptions
// already exposes as native selectors (ACP's configOptions — a proper
// dropdown with validated values) — advertising them AGAIN as slash commands
// would mean two different, redundant ways to do the same thing in Zed's UI,
// with the slash-command path being strictly worse (free-text value, no
// autocomplete, no validation against the actual option list). Filtered out
// of buildAvailableCommands below; everything else Harness exposes
// (rename/compact/reset/skills) has no config-option equivalent and stays.
var commandsCoveredByConfigOptions = map[string]bool{
	"model":    true,
	"thinking": true,
}

// buildAvailableCommands translates the session's dynamic command set
// (built-ins + discovered skills, from GET /api/sessions/{id}/commands) into
// ACP's available_commands_update shape, excluding whatever
// commandsCoveredByConfigOptions already covers.
func buildAvailableCommands(c *client.Client, sessionID string) ([]availableCommand, error) {
	defs, err := c.ListCommands(sessionID)
	if err != nil {
		return nil, fmt.Errorf("acp: list commands: %w", err)
	}
	out := make([]availableCommand, 0, len(defs))
	for _, d := range defs {
		if commandsCoveredByConfigOptions[d.Name] {
			continue
		}
		out = append(out, availableCommand{
			Name:        d.Name,
			Description: d.Description,
			Input:       commandInputHint(d),
		})
	}
	return out, nil
}

// commandInputHint builds a human-readable input hint from a command's
// declared params — e.g. "level (off|low|medium|high|xhigh)" for /thinking,
// "prompt" for a skill command — or nil if the command takes no params.
func commandInputHint(d client.CommandDef) *availableCommandInput {
	if len(d.Params) == 0 {
		return nil
	}
	hint := ""
	for i, p := range d.Params {
		if i > 0 {
			hint += ", "
		}
		hint += p.Name
		if len(p.Values) > 0 {
			hint += " ("
			for j, v := range p.Values {
				if j > 0 {
					hint += "|"
				}
				hint += v
			}
			hint += ")"
		}
	}
	return &availableCommandInput{Hint: hint}
}
