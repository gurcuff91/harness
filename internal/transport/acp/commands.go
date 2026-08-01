package acp

import (
	"fmt"

	"github.com/gurcuff91/harness/client"
)

// buildConfigOptions returns the session config options this transport
// advertises: the active model (category "model_config", one value per
// connected provider's model) and the thinking level (a plain select). Both
// are read fresh from the server so a freshly created/loaded session always
// reports its true current values.
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
			ID:       "model",
			Category: "model_config",
			Name:     "Model",
			Type:     "select",
			Value:    settings.ActiveModel,
			Options:  modelValues,
		},
		{
			ID:      "thinking",
			Name:    "Thinking",
			Type:    "select",
			Value:   settings.ThinkingLevel,
			Options: thinkingValues,
		},
	}, nil
}

// buildAvailableCommands translates the session's dynamic command set
// (built-ins + discovered skills, from GET /api/sessions/{id}/commands) into
// ACP's available_commands_update shape. Every command Harness exposes is
// included — see the design doc's decision to announce all of them.
func buildAvailableCommands(c *client.Client, sessionID string) ([]availableCommand, error) {
	defs, err := c.ListCommands(sessionID)
	if err != nil {
		return nil, fmt.Errorf("acp: list commands: %w", err)
	}
	out := make([]availableCommand, 0, len(defs))
	for _, d := range defs {
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
