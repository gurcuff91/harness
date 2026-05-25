package config

import (
	"encoding/json"
	"os"
)

// Config holds all harness configuration.
type Config struct {
	Model        string `json:"model"`
	SystemPrompt string `json:"system_prompt"`
	MaxLoops     int    `json:"max_loops"`
	MaxTokens    int    `json:"max_tokens"`
}

const defaultSystemPrompt = `You are a helpful AI assistant with access to tools. 
Use them when needed to accomplish the user's request. 
Think step by step before acting.`

// Load reads config. No env vars — everything lives in ~/.harness/
func Load() (*Config, error) {
	cfg := &Config{
		Model:        "claude-oauth/claude-sonnet-4-20250514",
		SystemPrompt: defaultSystemPrompt,
		MaxLoops:     25,
		MaxTokens:    8192,
	}

	// Try loading config file (optional)
	if data, err := os.ReadFile("harness.json"); err == nil {
		json.Unmarshal(data, cfg)
	}

	return cfg, nil
}
