package rpc

import (
	"time"

	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/internal/jsonrpc"
	"github.com/gurcuff91/harness/logx"
)

// clientResources is a resources.ResourceLoader whose context lives in the
// client: Load is one `resources/load` request. It is stateless, so the one
// instance every session and sub-agent shares is safe for concurrent use; the
// client must serve concurrent requests (sessions can open at the same time).
type clientResources struct {
	conn    *jsonrpc.Conn
	timeout time.Duration
	logger  logx.Logger
}

var _ resources.ResourceLoader = clientResources{}

// Load asks the client for cwd's resources. The interface carries no context,
// so the request is bounded by the timeout alone. A null result is empty
// Resources, never nil.
func (c clientResources) Load(cwd string) (*resources.Resources, error) {
	res := &resources.Resources{}
	if err := callClient(c.conn, c.logger, "resources/load", c.timeout, ResourcesLoadParams{CWD: cwd}, res); err != nil {
		return nil, err
	}
	return res, nil
}
