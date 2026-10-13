package llm

import (
	"errors"
	"net/http"
	"time"
)

// ── LLM provider HTTP transport ───────────────────────────────────────────
//
// Every LLM provider talks through this transport instead of
// http.DefaultTransport. Two reasons:
//
//  1. Dead connections must be noticed in seconds, not by the kernel. A
//     connection whose peer vanished (blackholed — packets dropped, no RST)
//     otherwise hangs until Linux's TCP retransmission timeout kills it,
//     ~15 minutes later (tcp_retries2=15): observed live, a turn stuck "active"
//     for 15m46s before failing with "read: connection timed out", while the
//     provider itself was healthy the whole time. HTTP/2 PINGs detect it in
//     ~SendPingTimeout + PingTimeout; ResponseHeaderTimeout bounds a request
//     that never gets headers; ParseSSE's idle timeout covers a stream that
//     stops mid-body (HTTP/1.1 providers have no PINGs).
//  2. No shared fate with the embedding process. http.DefaultTransport is
//     process-global: an SDK consumer's own HTTP traffic to the same host
//     would be multiplexed onto the SAME HTTP/2 connection as the LLM calls —
//     one dead connection froze them all together. Provider traffic owns its
//     connection pool.
//
// Defaults are deliberately generous for reasoning models: PINGs are answered
// by the server's HTTP/2 layer even while the model is thinking, and a model
// can legitimately take minutes before its first byte — but never 15 with no
// headers, nor minutes of total silence mid-stream.
const (
	// HTTP2ReadIdleTimeout: send a PING after this long with no frame read.
	HTTP2ReadIdleTimeout = 30 * time.Second
	// HTTP2PingTimeout: close the connection if a PING isn't answered in time.
	HTTP2PingTimeout = 15 * time.Second
	// HTTP2WriteByteTimeout: close the connection if pending data can't be
	// written for this long (a request body stuck in an un-ACKed send queue —
	// the observed incident had ~66 KB queued against a vanished peer).
	HTTP2WriteByteTimeout = 30 * time.Second
	// ResponseHeaderTimeout bounds the wait for response headers.
	ResponseHeaderTimeout = 3 * time.Minute
	// StreamIdleTimeout aborts a response stream that delivers no bytes at
	// all for this long (see ParseSSE).
	StreamIdleTimeout = 3 * time.Minute
)

// ErrStreamIdle is returned (wrapped) when a response stream delivers no
// bytes for StreamIdleTimeout: the connection is presumed dead. Transient —
// the request is safe to retry on a new connection.
var ErrStreamIdle = errors.New("llm stream idle: no data received")

// providerTransport is the single transport every provider client shares.
var providerTransport = newProviderTransport()

func newProviderTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone() // keep proxy env, dialer, TLS defaults
	t.ResponseHeaderTimeout = ResponseHeaderTimeout
	t.ForceAttemptHTTP2 = true
	t.HTTP2 = &http.HTTP2Config{
		SendPingTimeout:  HTTP2ReadIdleTimeout,
		PingTimeout:      HTTP2PingTimeout,
		WriteByteTimeout: HTTP2WriteByteTimeout,
	}
	return t
}

// NewProviderClient returns an *http.Client on the shared provider transport.
// timeout is the client's TOTAL request timeout — use it only for short,
// non-streaming calls (model listing, key validation); pass 0 for streaming
// completions, which are bounded by the transport's header timeout and
// ParseSSE's idle timeout instead (a total cap would cut a long, healthy
// stream).
func NewProviderClient(timeout time.Duration) *http.Client {
	return &http.Client{Transport: providerTransport, Timeout: timeout}
}

// ProviderTransport exposes the shared transport (tests, diagnostics).
func ProviderTransport() *http.Transport { return providerTransport }

// SetStreamIdleTimeoutForTest lowers ParseSSE's idle timeout for the duration
// of a test (tests in other packages exercising a dead stream end to end).
func SetStreamIdleTimeoutForTest(t interface{ Cleanup(func()) }, d time.Duration) {
	old := sseIdleTimeout
	sseIdleTimeout = d
	t.Cleanup(func() { sseIdleTimeout = old })
}
