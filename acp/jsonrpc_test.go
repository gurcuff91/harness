package acp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestConnReadMessage(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}` + "\n")
	c := newConn(in, &bytes.Buffer{})

	req, err := c.readMessage()
	if err != nil {
		t.Fatalf("readMessage: %v", err)
	}
	if req.Method != "initialize" {
		t.Errorf("Method = %q, want initialize", req.Method)
	}
	if string(req.ID) != "1" {
		t.Errorf("ID = %q, want 1", req.ID)
	}
}

func TestConnReadMessageEOF(t *testing.T) {
	c := newConn(strings.NewReader(""), &bytes.Buffer{})
	_, err := c.readMessage()
	if err == nil {
		t.Fatal("expected EOF, got nil")
	}
}

func TestConnWriteLineIsSingleLineNoPrettyPrint(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(nil, &buf)

	if err := c.writeLine(map[string]any{"a": 1, "b": "two"}); err != nil {
		t.Fatalf("writeLine: %v", err)
	}
	out := buf.String()
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("expected exactly one newline (one message, one line), got %q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("expected trailing newline, got %q", out)
	}
	// No pretty-print: a multi-key object marshals without embedded newlines/indentation.
	if strings.Contains(strings.TrimSuffix(out, "\n"), "\n") {
		t.Fatalf("message body must not contain embedded newlines: %q", out)
	}
}

func TestConnSendResponse(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(nil, &buf)

	if err := c.sendResponse(json.RawMessage("7"), map[string]string{"ok": "yes"}); err != nil {
		t.Fatalf("sendResponse: %v", err)
	}
	var resp rpcResponse
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.JSONRPC != jsonrpcVersion {
		t.Errorf("JSONRPC = %q", resp.JSONRPC)
	}
	if string(resp.ID) != "7" {
		t.Errorf("ID = %q, want 7", resp.ID)
	}
	if resp.Error != nil {
		t.Errorf("unexpected error: %+v", resp.Error)
	}
}

func TestConnSendError(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(nil, &buf)

	if err := c.sendError(json.RawMessage("3"), errCodeMethodNotFound, "nope", nil); err != nil {
		t.Fatalf("sendError: %v", err)
	}
	var resp rpcResponse
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != errCodeMethodNotFound || resp.Error.Message != "nope" {
		t.Errorf("Error = %+v", resp.Error)
	}
}

func TestConnSendNotificationHasNoID(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(nil, &buf)

	if err := c.sendNotification("session/update", map[string]string{"sessionId": "s1"}); err != nil {
		t.Fatalf("sendNotification: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := m["id"]; ok {
		t.Error("notification must not carry an id field")
	}
	if string(m["method"]) != `"session/update"` {
		t.Errorf("method = %s", m["method"])
	}
}
