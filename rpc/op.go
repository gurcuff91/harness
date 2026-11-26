package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"

	"github.com/gurcuff91/harness/internal/jsonrpc"
)

// Op is one operation of the protocol: a method, its documentation and its
// typed function. The table of every Op (table.go) is the single source of the
// dispatch, the params validation and the JSON Schema (schema.go).
type Op struct {
	// Method is the JSON-RPC method name ("session/new").
	Method string
	// Doc is one or two sentences, shown in the schema.
	Doc string
	// Params and Result are the Go types of the operation, kept for the schema.
	Params, Result reflect.Type

	call func(ctx context.Context, s *Server, raw json.RawMessage) (any, error)
	// after, when set, runs once the reply has been written (shutdown).
	after func(*Server)
}

// afterReply makes op run f once its reply is on the wire.
func afterReply(op Op, f func(*Server)) Op {
	op.after = f
	return op
}

// replied wraps a result so the reply's writer runs the op's after hook.
type replied struct {
	res any
	run func()
}

func (r replied) MarshalJSON() ([]byte, error) { return json.Marshal(r.res) }
func (r replied) AfterSend()                   { r.run() }

// Define builds an Op from a typed function. P is decoded from the request's
// params with DisallowUnknownFields (an unknown field or a wrong type is a
// -32602 error naming the problem); absent or null params leave P at its zero
// value; a struct{} P means "no params". R is the result as it serializes
// (return non-nil slices: a nil one is `null` on the wire).
func Define[P, R any](method, doc string, fn func(ctx context.Context, s *Server, p P) (R, error)) Op {
	return Op{
		Method: method,
		Doc:    doc,
		Params: reflect.TypeFor[P](),
		Result: reflect.TypeFor[R](),
		call: func(ctx context.Context, s *Server, raw json.RawMessage) (any, error) {
			var p P
			if err := decodeParams(raw, &p); err != nil {
				return nil, err
			}
			r, err := fn(ctx, s, p)
			if err != nil {
				return nil, err
			}
			return r, nil
		},
	}
}

// decodeParams decodes raw into p strictly.
func decodeParams(raw json.RawMessage, p any) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(p); err != nil {
		return invalidParams("%v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return invalidParams("unexpected data after the params")
	}
	return nil
}

// invalidParams is the -32602 error for params that do not fit the method.
func invalidParams(format string, args ...any) *jsonrpc.Error {
	return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid params: " + fmt.Sprintf(format, args...)}
}
