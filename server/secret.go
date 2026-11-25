package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
)

// SecretHeader is the request header that carries the server's secret
// (alongside "Authorization: Bearer <secret>", which is also accepted).
const SecretHeader = "X-Harness-Sec"

// secretPrefix marks a generated secret.
const secretPrefix = "hs_"

// GenerateServerSec returns a fresh secret for a server: "hs_" followed by the
// SHA-256 (hex) of 64 random bits — 67 characters, unguessable, safe to pass on
// a command line or in a header. A secret is just a string: one made some other
// way works the same (WithSecret / ServerOptions.Secret take any non-empty one).
func GenerateServerSec() string {
	var seed [8]byte
	if _, err := rand.Read(seed[:]); err != nil {
		// crypto/rand failing means the machine has no entropy source: a
		// secret made from anything else would look secure and not be.
		panic("server: no secure random source: " + err.Error())
	}
	sum := sha256.Sum256(seed[:])
	return secretPrefix + hex.EncodeToString(sum[:])
}

// secretMiddleware lets a request through only when it carries the secret, in
// X-Harness-Sec or as a Bearer token; the answer to anything else is a 401
// that does not say which part was wrong. The open paths (the server's info and
// the API docs) pass without it. CORS preflights (a browser sends them without
// credentials) never get here: corsMiddleware, registered before this one,
// answers every OPTIONS itself (a test pins it). The comparison takes the same
// time whatever the guess.
func secretMiddleware(secret string) func(http.Handler) http.Handler {
	want := []byte(secret)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isOpenPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			got := r.Header.Get(SecretHeader)
			if got == "" {
				if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
					got = strings.TrimSpace(auth[len("Bearer "):])
				}
			}
			if subtle.ConstantTimeCompare([]byte(got), want) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="harness"`)
				writeError(w, http.StatusUnauthorized, "missing or invalid secret", nil)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// isOpenPath: what a secret does not protect — who the server is (GET
// /api/server: name, version, pid; also how a client checks it is up) and its
// API documentation.
func isOpenPath(p string) bool {
	return p == "/api/server" || p == "/api/docs" || p == "/api/docs/openapi.json"
}
