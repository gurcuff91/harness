package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/types"
)

// The provider transport is NOT http.DefaultTransport (no shared fate with the
// embedding process) and carries the dead-connection detection settings.
func TestProviderTransportConfig(t *testing.T) {
	tr := ProviderTransport()
	if tr == http.DefaultTransport.(*http.Transport) {
		t.Fatal("provider transport must not be http.DefaultTransport")
	}
	if tr.ResponseHeaderTimeout != ResponseHeaderTimeout {
		t.Errorf("ResponseHeaderTimeout = %v", tr.ResponseHeaderTimeout)
	}
	if tr.HTTP2 == nil || tr.HTTP2.SendPingTimeout != HTTP2ReadIdleTimeout || tr.HTTP2.PingTimeout != HTTP2PingTimeout {
		t.Errorf("HTTP/2 health check not configured: %+v", tr.HTTP2)
	}
	if NewProviderClient(0).Transport != tr {
		t.Error("provider clients must share the provider transport")
	}
}

// A stream that sends headers and some data, then goes silent while keeping
// the connection open, is aborted by the idle timeout with ErrStreamIdle —
// not left hanging.
func TestStreamIdleTimeoutAbortsSilentStream(t *testing.T) {
	old := sseIdleTimeout
	sseIdleTimeout = 200 * time.Millisecond
	t.Cleanup(func() { sseIdleTimeout = old })

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"partial"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		select { // then silence, connection kept open
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	start := time.Now()
	_, err := DoOpenAIStream(context.Background(), NewProviderClient(0), srv.URL, "", &OpenAIRequest{Request: &types.Request{Model: "m"}}, nil, nil)
	if !errors.Is(err, ErrStreamIdle) {
		t.Fatalf("err = %v, want ErrStreamIdle", err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("took %v — must abort right after the idle timeout", el)
	}
}

// A stream that keeps delivering bytes (even slowly, SSE keep-alives) is
// never cut by the idle timeout.
func TestStreamIdleTimeoutSparesActiveStream(t *testing.T) {
	old := sseIdleTimeout
	sseIdleTimeout = 200 * time.Millisecond
	t.Cleanup(func() { sseIdleTimeout = old })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for range 6 { // 6 × 100ms = 600ms total, > 3 idle timeouts, never idle 200ms
			fmt.Fprint(w, ": keep-alive\n\n")
			w.(http.Flusher).Flush()
			time.Sleep(100 * time.Millisecond)
		}
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	resp, err := DoOpenAIStream(context.Background(), NewProviderClient(0), srv.URL, "", &OpenAIRequest{Request: &types.Request{Model: "m"}}, nil, nil)
	if err != nil {
		t.Fatalf("an active (slow) stream was cut: %v", err)
	}
	if resp == nil {
		t.Fatal("no response")
	}
}

// A server that accepts the request and never sends headers trips the
// transport's ResponseHeaderTimeout (a net.Error timeout) instead of hanging.
func TestResponseHeaderTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() { // accept, read the request, never answer
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { buf := make([]byte, 4096); c.Read(buf); time.Sleep(5 * time.Second); c.Close() }()
		}
	}()
	tr := newProviderTransport()
	tr.ResponseHeaderTimeout = 150 * time.Millisecond
	client := &http.Client{Transport: tr}
	start := time.Now()
	_, err = DoOpenAIStream(context.Background(), client, "http://"+ln.Addr().String(), "", &OpenAIRequest{Request: &types.Request{Model: "m"}}, nil, nil)
	var ne net.Error
	if err == nil || !errors.As(err, &ne) || !ne.Timeout() || !strings.Contains(err.Error(), "timeout awaiting response headers") {
		t.Fatalf("err = %v, want a response-header timeout", err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("took %v", el)
	}
}

// blackholeProxy forwards TCP between the client and target until blackhole
// is closed; from then on it silently DROPS everything in both directions
// while keeping both sockets open — a peer that vanished without a FIN/RST
// (the observed incident: the kernel would only give up after ~15 min).
func blackholeProxy(t *testing.T, target string) (addr string, blackhole chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	blackhole = make(chan struct{})
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	pipe := func(dst, src net.Conn) {
		buf := make([]byte, 32*1024)
		for {
			n, err := src.Read(buf)
			if err != nil {
				return
			}
			select {
			case <-blackhole: // drop
			default:
				if _, err := dst.Write(buf[:n]); err != nil {
					return
				}
			}
		}
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			u, err := net.Dial("tcp", target)
			if err != nil {
				c.Close()
				continue
			}
			go func() { <-done; c.Close(); u.Close() }()
			go pipe(u, c)
			go pipe(c, u)
		}
	}()
	return ln.Addr().String(), blackhole
}

// Against a real HTTP/2 server, a connection that goes black mid-stream is
// detected by the transport's PING health check (SendPingTimeout +
// PingTimeout) — with the stream idle timeout set far higher, so it's the
// PING that catches it, not ParseSSE.
func TestHTTP2PingDetectsBlackholedConnection(t *testing.T) {
	oldIdle := sseIdleTimeout
	sseIdleTimeout = time.Minute // out of the way
	t.Cleanup(func() { sseIdleTimeout = oldIdle })

	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"partial"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		close(started)
		select { // the blackhole means the server never learns the client left
		case <-r.Context().Done():
		case <-release:
		}
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	defer close(release) // runs before srv.Close, unblocking the handler

	proxyAddr, blackhole := blackholeProxy(t, srv.Listener.Addr().String())
	tr := newProviderTransport()
	tr.TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	tr.HTTP2.SendPingTimeout = 200 * time.Millisecond
	tr.HTTP2.PingTimeout = 200 * time.Millisecond
	client := &http.Client{Transport: tr}

	go func() { <-started; close(blackhole) }()
	start := time.Now()
	url := "https://" + proxyAddr
	_, err := DoOpenAIStream(context.Background(), client, url, "", &OpenAIRequest{Request: &types.Request{Model: "m"}}, nil, nil)
	el := time.Since(start)
	if err == nil {
		t.Fatal("a blackholed stream must fail")
	}
	if errors.Is(err, ErrStreamIdle) {
		t.Fatalf("caught by the idle timeout, not the HTTP/2 PING: %v", err)
	}
	if el > 5*time.Second {
		t.Fatalf("took %v — the PING health check must detect the dead connection", el)
	}
	t.Logf("blackholed HTTP/2 connection detected in %v: %v", el.Round(time.Millisecond), err)
}
