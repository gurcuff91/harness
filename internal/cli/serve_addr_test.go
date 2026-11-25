package cli

import "testing"

// `harness serve :PORT` listens on this machine only; exposing it to the
// network takes an explicit host.
func TestServeAddr(t *testing.T) {
	for in, want := range map[string]string{
		":8080":          "127.0.0.1:8080",
		" :8080 ":        "127.0.0.1:8080",
		"127.0.0.1:8080": "127.0.0.1:8080",
		"localhost:9000": "localhost:9000",
		"0.0.0.0:8080":   "0.0.0.0:8080",
		"[::]:8080":      "[::]:8080",
		"192.168.1.5:80": "192.168.1.5:80",
		":0":             "127.0.0.1:0",
	} {
		got, err := serveAddr(in)
		if err != nil || got != want {
			t.Errorf("serveAddr(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"8080", "", "host"} {
		if _, err := serveAddr(bad); err == nil {
			t.Errorf("serveAddr(%q) must fail", bad)
		}
	}
}
