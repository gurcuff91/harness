package config

import (
	"encoding/json"
	"os"
)

// Config holds all harness configuration.
type Config struct {
	Model        string `json:"model"`
	SystemPrompt string `json:"system_prompt"`
	MaxTurns     int    `json:"max_turns"`
	MaxTokens    int    `json:"max_tokens"`
}

const defaultSystemPrompt = `You are an expert coding agent working directly in the user's codebase. You have access to tools for reading, writing, and editing files, running shell commands, and fetching URLs.`

// Load reads config. No env vars — everything lives in ~/.harness/
func Load() (*Config, error) {
	cfg := &Config{
		Model:        "claude-oauth/claude-sonnet-4-20250514",
		SystemPrompt: defaultSystemPrompt,
		MaxTurns:     25,
		MaxTokens:    8192,
	}

	// Try loading config file (optional)
	if data, err := os.ReadFile("harness.json"); err == nil {
		json.Unmarshal(data, cfg)
	}

	return cfg, nil
}
