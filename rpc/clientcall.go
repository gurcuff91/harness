package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gurcuff91/harness/internal/jsonrpc"
	"github.com/gurcuff91/harness/logx"
)

// ErrClientBackend marks a failure of something the CLIENT serves (a memory, a
// resource loader, a store): it timed out, answered an error or went away. It is
// not a fault of the binary, so it maps to the `upstream` kind and an SDK can
// tell it from an internal error.
var ErrClientBackend = errors.New("the client's backend failed")

// callClient sends one request to the client under a deadline and decodes the
// result into out. Every failure is logged on stderr and returned: a deadline
// expiry as `the client did not answer <method> within <timeout>`, a JSON-RPC
// error as `<method>: client error <code>: <message>`, a closed connection as
// `<method>: the client disconnected`. Used by the per-agent adapters (memory,
// resources); the config stores keep their own wording.
func callClient(conn *jsonrpc.Conn, logger logx.Logger, method string, timeout time.Duration, params, out any) error {
	if timeout <= 0 {
		timeout = clientStoreTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	raw, err := conn.Call(ctx, method, params)
	if err == nil && out != nil {
		if uerr := json.Unmarshal(raw, out); uerr != nil {
			err = fmt.Errorf("invalid %s result: %w", method, uerr)
		}
	}
	if err == nil {
		return nil
	}
	var je *jsonrpc.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		err = fmt.Errorf("the client did not answer %s within %s", method, timeout)
	case errors.As(err, &je):
		err = fmt.Errorf("%s: client error %d: %s", method, je.Code, je.Message)
	case errors.Is(err, jsonrpc.ErrClosed):
		err = fmt.Errorf("%s: the client disconnected", method)
	}
	logger.Warn(component, method+" failed", "error", err.Error())
	return fmt.Errorf("%w: %w", ErrClientBackend, err)
}
