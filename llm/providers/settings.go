package providers

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type SettingsFile struct {
	Model    string          `json:"model,omitempty"`
	Thinking string          `json:"thinking,omitempty"`
	Ollama   *ollamaSettings `json:"ollama,omitempty"`
}

type ollamaSettings struct {
	URL string `json:"url,omitempty"`
}

func settingsPath() string {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".harness")
	_ = os.MkdirAll(dir, 0700)
	return filepath.Join(dir, "settings.json")
}

func ReadSettings() *SettingsFile { return readSettings() }

func readSettings() *SettingsFile {
	data, err := os.ReadFile(settingsPath())
	if err != nil {
		return &SettingsFile{}
	}
	var s SettingsFile
	json.Unmarshal(data, &s)
	return &s
}

func writeSettings(s *SettingsFile) {
	data, _ := json.MarshalIndent(s, "", "  ")
	os.WriteFile(settingsPath(), data, 0600)
}

func GetActiveModel() string {
	s := readSettings()
	if s.Model != "" {
		return s.Model
	}
	return "claude-oauth/claude-sonnet-4-20250514"
}

func SetActiveModel(model string) {
	s := readSettings()
	s.Model = model
	writeSettings(s)
}

func GetThinking() string {
	// Env var takes precedence
	if v := os.Getenv("HARNESS_THINKING"); v != "" {
		return v
	}
	s := readSettings()
	if s.Thinking != "" {
		return s.Thinking
	}
	return "high"
}

func SetThinking(level string) {
	s := readSettings()
	s.Thinking = level
	writeSettings(s)
}

const defaultOllamaURL = "http://localhost:11434"

func GetOllamaURL() string {
	if v := os.Getenv("OLLAMA_URL"); v != "" {
		return v
	}
	s := readSettings()
	if s.Ollama != nil && s.Ollama.URL != "" {
		return s.Ollama.URL
	}
	return defaultOllamaURL
}

func SetOllamaURL(url string) {
	s := readSettings()
	if s.Ollama == nil {
		s.Ollama = &ollamaSettings{}
	}
	s.Ollama.URL = url
	writeSettings(s)
}
