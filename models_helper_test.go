package harness

import (
	"context"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/types"
)

// modelsOf lists every model of every active provider (test helper).
func modelsOf(a *agent.Agent) []types.ModelListing {
	list, _ := a.Admin().Providers.Models(context.Background(), "")
	return list
}
