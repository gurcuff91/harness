package admin

import (
	"errors"

	"github.com/gurcuff91/harness/internal/config"
)

// Settings administers the global defaults (the active model and thinking
// level). It persists the DEFAULT only; it never touches a live session.
type Settings struct{ ad *Admin }

func newSettings(ad *Admin) *Settings { return &Settings{ad: ad} }

// SettingsValues are the global defaults.
type SettingsValues struct {
	ActiveModel   string `json:"active_model"`
	ThinkingLevel string `json:"thinking_level"`
}

// Get returns the settings with defaults (thinking "off" when unset).
func (s *Settings) Get() SettingsValues {
	sm := config.GetSettingsManager()
	thinking := sm.ThinkingLevel()
	if thinking == "" {
		thinking = "off"
	}
	return SettingsValues{ActiveModel: sm.ActiveModel(), ThinkingLevel: thinking}
}

// SetActiveModel stores the default "provider/model".
func (s *Settings) SetActiveModel(model string) error {
	return config.GetSettingsManager().SetActiveModel(model)
}

// SetThinkingLevel stores the default level (off|low|medium|high|xhigh|max).
// A bad level wraps ErrInvalid, ErrUnprocessable (422) and
// config.ErrInvalidThinkingLevel.
func (s *Settings) SetThinkingLevel(level string) error {
	err := config.GetSettingsManager().SetThinkingLevel(level)
	if errors.Is(err, config.ErrInvalidThinkingLevel) {
		return &marked2{kinds: []error{ErrInvalid, ErrUnprocessable}, err: err}
	}
	return err
}

// marked2 is marked with several kinds.
type marked2 struct {
	kinds []error
	err   error
}

func (m *marked2) Error() string   { return m.err.Error() }
func (m *marked2) Unwrap() []error { return append(append([]error(nil), m.kinds...), m.err) }
