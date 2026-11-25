package cli

import (
	"strings"
	"testing"
)

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

// Reachable from the network with no secret is refused unless --insecure says it is meant;
// this machine only needs nothing; a secret always passes.
func TestServeSecurityRule(t *testing.T) {
	for _, c := range []struct {
		addr, secret string
		insecure     bool
		ok           bool
	}{
		{"127.0.0.1:8080", "", false, true},
		{"localhost:8080", "", false, true},
		{"[::1]:8080", "", false, true},
		{"127.0.0.2:8080", "", false, true},
		{"0.0.0.0:8080", "", false, false},
		{"[::]:8080", "", false, false},
		{"192.168.1.5:8080", "", false, false},
		{"example.com:8080", "", false, false},
		{"0.0.0.0:8080", "hs_x", false, true},
		{"0.0.0.0:8080", "", true, true},
		{"192.168.1.5:8080", "hs_x", false, true},
	} {
		err := checkServeSecurity(c.addr, c.secret, c.insecure)
		if (err == nil) != c.ok {
			t.Errorf("checkServeSecurity(%q, secret=%q, insecure=%v) = %v, want ok=%v", c.addr, c.secret, c.insecure, err, c.ok)
		}
		if err != nil && (!strings.Contains(err.Error(), "--secret") || !strings.Contains(err.Error(), "gen-secret") || !strings.Contains(err.Error(), "--insecure")) {
			t.Errorf("the refusal must say how to proceed: %v", err)
		}
	}
}

// The secret is the flag, else HARNESS_SECRET; blank is none.
func TestServeSecretSources(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k != SecretEnv {
				t.Errorf("read %q", k)
			}
			return v
		}
	}
	for _, c := range []struct{ flag, env, want string }{
		{"hs_flag", "hs_env", "hs_flag"},
		{"", "hs_env", "hs_env"},
		{"  ", " hs_env ", "hs_env"},
		{"", "", ""},
		{" ", "  ", ""},
	} {
		if got := serveSecret(c.flag, env(c.env)); got != c.want {
			t.Errorf("serveSecret(%q, env %q) = %q, want %q", c.flag, c.env, got, c.want)
		}
	}
}

// Kong: --secret and --insecure belong to serve; gen-secret is a root command.
func TestServeSecretFlagsParse(t *testing.T) {
	p, err := newParser()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseArgs(p, []string{"serve", "0.0.0.0:8080", "--secret", "hs_x", "--insecure"}); err != nil {
		t.Fatal(err)
	}
	if CLI.Serve.Secret != "hs_x" || !CLI.Serve.Insecure {
		t.Errorf("serve flags = %+v", CLI.Serve)
	}
	for _, args := range [][]string{{"--secret", "x"}, {"--insecure"}, {"acp", "--secret", "x"}} {
		p, _ := newParser()
		if _, err := p.Parse(args); err == nil {
			t.Errorf("harness %v must be an error: the secret belongs to serve", args)
		}
	}
	p, _ = newParser()
	ctx, err := p.Parse([]string{"gen-secret"})
	if err != nil || ctx.Command() != "gen-secret" {
		t.Errorf("gen-secret: %v %v", ctx, err)
	}
}
