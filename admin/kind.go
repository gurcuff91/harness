package admin

import "errors"

// Kind names the kind of an admin error — the textual form of its sentinel
// ("not_found", "invalid"…) — or "" when err is none of them. It is the ONE
// place the taxonomy lives: the HTTP server maps kinds to statuses and the rpc
// frontend maps them to codes, both on top of this.
//
// Order matters: "unprocessable" is checked BEFORE "invalid" because a few
// errors carry both (a bad thinking level, an invalid MCP server) and the more
// specific one must win.
func Kind(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrUnprocessable):
		return "unprocessable"
	case errors.Is(err, ErrInvalid):
		return "invalid"
	case errors.Is(err, ErrSessionActive):
		return "session_active"
	case errors.Is(err, ErrConflict):
		return "conflict"
	case errors.Is(err, ErrNotConnected):
		return "not_connected"
	case errors.Is(err, ErrUpstream):
		return "upstream"
	case errors.Is(err, ErrNotConfigured):
		return "not_configured"
	case errors.Is(err, ErrProviderNotFound):
		return "provider_not_found"
	case errors.Is(err, ErrProviderNotRemovable):
		return "provider_not_removable"
	case errors.Is(err, ErrProviderNameConflict):
		return "provider_name_conflict"
	case errors.Is(err, ErrInvalidExport):
		return "invalid_export"
	}
	return ""
}
