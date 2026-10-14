package agent

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
	"time"

	"github.com/gurcuff91/harness/internal/providers/llm"
	"github.com/gurcuff91/harness/types"
)

// ErrEmptyResponse: the model's stream completed cleanly but carried no text,
// reasoning or tool call. Transient — retried like a network failure — and
// never persisted.
var ErrEmptyResponse = errors.New("model returned an empty response")

// llmMaxAttempts bounds a ReAct iteration's LLM call: one try plus up to two
// retries on a transient network failure (see isTransientNetworkError).
const llmMaxAttempts = 3

// llmRetryBackoff is the wait before retry n (1-based attempt that failed):
// 2s, then 4s.
var llmRetryBackoff = func(failedAttempt int) time.Duration {
	return time.Duration(1<<failedAttempt) * time.Second
}

// isTransientNetworkError reports whether an LLM call failed for a reason a
// retry on a NEW connection can fix: the connection died or stalled (dial /
// read / write timeouts, resets, refused, broken pipe, unexpected EOF,
// HTTP/2 GOAWAY / RST_STREAM / lost connection, the provider transport's
// header timeout, ParseSSE's idle timeout) or the provider's edge answered
// 502/503/504.
//
// Never transient: context overflow (deterministic — the same request fails
// again), 4xx auth/validation, and context cancellation (the user's Stop or
// a caller deadline — not a network failure).
func isTransientNetworkError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, ErrEmptyResponse) {
		return true
	}
	if isContextOverflowError(err) {
		return false
	}
	var apiErr *types.ProviderAPIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case 502, 503, 504:
			return true
		default:
			return false // 4xx (auth, validation, 429 — handled elsewhere) and other 5xx
		}
	}
	if errors.Is(err, llm.ErrStreamIdle) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ETIMEDOUT) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true // dial/read/write timeout, ResponseHeaderTimeout
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true // dial/read/write failure on the socket
	}
	// Errors that cross package boundaries as plain strings (net/http's
	// HTTP/2 transport, wrapped provider messages).
	msg := strings.ToLower(err.Error())
	for _, s := range []string{
		"connection reset", "connection refused", "broken pipe", "connection timed out",
		"timeout awaiting response headers", "unexpected eof", "server sent goaway",
		"client connection lost", "stream error", "rst_stream", "http2: client connection force closed",
		"use of closed network connection", "no such host", "i/o timeout", "tls handshake timeout",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
