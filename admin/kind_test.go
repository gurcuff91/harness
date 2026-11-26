package admin

import (
	"errors"
	"fmt"
	"testing"
)

func TestKind(t *testing.T) {
	both := fmt.Errorf("x: %w, %w", ErrInvalid, ErrUnprocessable)
	for name, c := range map[string]struct {
		err  error
		want string
	}{
		"nil":                    {nil, ""},
		"not found":              {ErrNotFound, "not_found"},
		"unprocessable":          {ErrUnprocessable, "unprocessable"},
		"invalid":                {ErrInvalid, "invalid"},
		"invalid+unprocessable":  {both, "unprocessable"},
		"unprocessable+invalid":  {fmt.Errorf("%w, %w", ErrUnprocessable, ErrInvalid), "unprocessable"},
		"session active":         {ErrSessionActive, "session_active"},
		"conflict":               {ErrConflict, "conflict"},
		"not connected":          {ErrNotConnected, "not_connected"},
		"upstream":               {ErrUpstream, "upstream"},
		"not configured":         {ErrNotConfigured, "not_configured"},
		"provider not found":     {ErrProviderNotFound, "provider_not_found"},
		"provider not removable": {ErrProviderNotRemovable, "provider_not_removable"},
		"provider name conflict": {ErrProviderNameConflict, "provider_name_conflict"},
		"invalid export":         {ErrInvalidExport, "invalid_export"},
		"wrapped":                {fmt.Errorf("ctx: %w", fmt.Errorf("deeper: %w", ErrSessionActive)), "session_active"},
		"unknown":                {errors.New("boom"), ""},
	} {
		if got := Kind(c.err); got != c.want {
			t.Errorf("%s: Kind = %q, want %q", name, got, c.want)
		}
	}
}
