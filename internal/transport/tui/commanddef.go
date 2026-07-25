package tui

// CommandDef and ParamDef mirror the server's command definitions
// (GET /api/sessions/{id}/commands) — the shape the palette and command
// dispatch use to know what a session's dynamic commands accept. Previously
// lived in this package's own API client; now decoded locally since
// internal/client returns raw bytes for every endpoint.
type CommandDef struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Params      []ParamDef `json:"params"`
}

type ParamDef struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Values   []string `json:"values,omitempty"`
}
