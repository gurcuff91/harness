package tools

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"testing"
)

// fakeNetErr is a net.Error whose Timeout() we control — mirrors what
// http.Client surfaces on a request deadline (a *url.Error wrapping something
// that reports Timeout() == true).
type fakeNetErr struct{ timeout bool }

func (e fakeNetErr) Error() string   { return "fake net error" }
func (e fakeNetErr) Timeout() bool   { return e.timeout }
func (e fakeNetErr) Temporary() bool { return false }

func TestIsTimeout(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil is not a timeout", nil, false},
		{"context.DeadlineExceeded is a timeout", context.DeadlineExceeded, true},
		{"context.Canceled (Stop) is NOT a timeout", context.Canceled, false},
		{"plain error is not a timeout", errors.New("boom"), false},
		{"net.Error with Timeout()==true", fakeNetErr{timeout: true}, true},
		{"net.Error with Timeout()==false", fakeNetErr{timeout: false}, false},
		{
			"url.Error wrapping a timeout (http.Client shape)",
			&url.Error{Op: "Get", URL: "http://x", Err: fakeNetErr{timeout: true}},
			true,
		},
		{
			"wrapped DeadlineExceeded via fmt.Errorf %w (client.decodeCtx shape)",
			fmt.Errorf("request: %w", context.DeadlineExceeded),
			true,
		},
		{
			"wrapped Canceled via %w stays not-a-timeout",
			fmt.Errorf("request: %w", context.Canceled),
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTimeout(tc.err); got != tc.want {
				t.Errorf("isTimeout(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// net.Error is the contract isTimeout keys off; assert the two stdlib sentinels
// we rely on behave as documented, so a future Go change can't silently break us.
func TestContextSentinelsSatisfyNetError(t *testing.T) {
	var ne net.Error
	if !errors.As(context.DeadlineExceeded, &ne) || !ne.Timeout() {
		t.Fatal("context.DeadlineExceeded must satisfy net.Error with Timeout()==true")
	}
	ne = nil
	if errors.As(context.Canceled, &ne) && ne.Timeout() {
		t.Fatal("context.Canceled must NOT report Timeout()==true")
	}
}
