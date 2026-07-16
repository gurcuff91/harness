package cli

import (
	"github.com/gurcuff91/harness/providers/authflow"
	"github.com/gurcuff91/harness/types"
)

// ObtainOAuthCredentials delegates to the shared authflow package (used by both
// the CLI and the tui transports so the OAuth logic lives in one place).
func ObtainOAuthCredentials(provName string) (*types.Credentials, error) {
	return authflow.ObtainOAuthCredentials(provName)
}
