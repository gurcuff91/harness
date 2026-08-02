package server

import (
	"net/http"
	"time"

	"github.com/gurcuff91/harness/logx"
)

// statusRecorder wraps http.ResponseWriter to capture the status code and the
// number of bytes written, so the request logger can report them.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Flush forwards to the underlying writer so SSE streaming still works when the
// request is wrapped by this middleware.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// requestLogger logs one line per HTTP request in the shared logx format:
//
//	INFO  [server] request method=GET path=/api/server status=200 bytes=128 dur=80µs
//
// Always registered as middleware (see handler()) regardless of what logger
// the Server was built with — with the default logx.NilLogger{} this is a
// no-op call per request, not a conditional branch, which is what let
// Server.verbose disappear entirely in favor of always-on middleware plus an
// injected Logger.
func requestLogger(logger logx.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			start := time.Now()
			next.ServeHTTP(rec, r)
			logger.Info("server", "request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.bytes,
				"dur", time.Since(start).Round(time.Microsecond).String())
		})
	}
}
