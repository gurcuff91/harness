package acp

import (
	"io"

	"github.com/gurcuff91/harness/internal/jsonrpc"
)

// The JSON-RPC framing and message types live in internal/jsonrpc, shared with
// the rpc frontend. These aliases keep this package's code reading as before.
type (
	conn       = jsonrpc.Conn
	rpcRequest = jsonrpc.Message
	rpcError   = jsonrpc.Error
)

const (
	errCodeParseError     = jsonrpc.CodeParseError
	errCodeInvalidRequest = jsonrpc.CodeInvalidRequest
	errCodeMethodNotFound = jsonrpc.CodeMethodNotFound
	errCodeInvalidParams  = jsonrpc.CodeInvalidParams
	errCodeInternalError  = jsonrpc.CodeInternalError
)

func newConn(r io.Reader, w io.Writer) *conn {
	if r == nil {
		return jsonrpc.NewConn(nil, w)
	}
	return jsonrpc.NewConn(r, w)
}
