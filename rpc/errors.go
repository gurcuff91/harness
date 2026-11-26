package rpc

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/internal/jsonrpc"
)

// Error codes beyond the standard JSON-RPC ones, one per family of kinds.
const (
	CodeNotFound        = -32000
	CodeInvalid         = -32001
	CodeConflict        = -32002
	CodeNotConnected    = -32003
	CodeUpstream        = -32004
	CodeNotConfigured   = -32005
	CodeProtocolVersion = -32006
)

// kindCodes is the code of each error kind. Kinds are admin.Kind's plus the
// two this frontend adds ("protocol_version", "internal"); error.data.kind
// always carries the specific kind, so a client tells apart the kinds that
// share a code.
var kindCodes = map[string]int{
	"not_found":              CodeNotFound,
	"provider_not_found":     CodeNotFound,
	"invalid":                CodeInvalid,
	"unprocessable":          CodeInvalid,
	"invalid_export":         CodeInvalid,
	"session_active":         CodeConflict,
	"conflict":               CodeConflict,
	"provider_not_removable": CodeConflict,
	"provider_name_conflict": CodeConflict,
	"not_connected":          CodeNotConnected,
	"upstream":               CodeUpstream,
	"not_configured":         CodeNotConfigured,
	"protocol_version":       CodeProtocolVersion,
	"internal":               jsonrpc.CodeInternalError,
}

// rpcErr builds an error of the given kind (a key of kindCodes).
func rpcErr(kind, format string, args ...any) *jsonrpc.Error {
	code, ok := kindCodes[kind]
	if !ok {
		panic("rpc: unknown error kind " + kind)
	}
	return &jsonrpc.Error{Code: code, Message: fmt.Sprintf(format, args...), Data: map[string]any{"kind": kind}}
}

// errInvalid is the "invalid" kind (-32001): well-formed params the operation
// cannot accept. Malformed params are invalidParams (-32602) instead.
func errInvalid(format string, args ...any) *jsonrpc.Error { return rpcErr("invalid", format, args...) }

// rpcError is the ONE mapping from a Go error to its wire error.
//
//   - a *jsonrpc.Error passes through;
//   - admin errors go through admin.Kind (the taxonomy shared with HTTP);
//   - agent errors: ErrUnknownProfile, ErrChannelInvalid and
//     ErrChannelUnavailable → invalid (-32001); ErrChannelTaken → conflict
//     (-32002); ErrNoModelAvailable → not_connected (-32003: nothing usable
//     because no provider is connected — the same remedy as the HTTP 409);
//     agent.ErrBusy (a fork of a busy session) → conflict;
//   - a session that does not exist (the agent and store report it as text
//     containing "not found", the way the HTTP server reads it, or as
//     os.ErrNotExist) → not_found (-32000);
//   - anything else → -32603 with kind "internal".
func rpcError(err error) *jsonrpc.Error {
	var je *jsonrpc.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &je):
		return je
	}
	if kind := admin.Kind(err); kind != "" {
		return rpcErr(kind, "%s", err.Error())
	}
	switch {
	case errors.Is(err, agent.ErrUnknownProfile),
		errors.Is(err, agent.ErrChannelInvalid),
		errors.Is(err, agent.ErrChannelUnavailable):
		return rpcErr("invalid", "%s", err.Error())
	case errors.Is(err, agent.ErrChannelTaken), errors.Is(err, agent.ErrBusy):
		return rpcErr("conflict", "%s", err.Error())
	case errors.Is(err, agent.ErrNoModelAvailable):
		return rpcErr("not_connected", "%s", err.Error())
	case errors.Is(err, os.ErrNotExist), containsNotFound(err):
		return rpcErr("not_found", "%s", err.Error())
	}
	return rpcErr("internal", "%s", err.Error())
}

func containsNotFound(err error) bool { return strings.Contains(err.Error(), "not found") }
