package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/maapan/mool/logx"
)

// HeaderRequestID carries the correlation ID across services.
const HeaderRequestID = "X-Request-ID"

type reqIDKey struct{}

// WithRequestID stores the request ID in ctx so outbound calls (mool/svc) and
// event publishers can carry it on.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, reqIDKey{}, id)
}

// RequestIDFrom returns the request ID stored by the RequestID middleware ("" if none).
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(reqIDKey{}).(string)
	return id
}

// Middleware wraps a handler.
type Middleware func(http.Handler) http.Handler

// Chain applies middlewares so the first one listed runs first.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusWriter) WriteHeader(code int) { s.status = code; s.ResponseWriter.WriteHeader(code) }
func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}
func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// RequestID ensures every request has an X-Request-ID and a request-scoped logger.
func RequestID(base *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(HeaderRequestID)
			if id == "" || len(id) > 128 {
				id = uuid.NewString()
				r.Header.Set(HeaderRequestID, id)
			}
			w.Header().Set(HeaderRequestID, id)
			ctx := logx.With(WithRequestID(r.Context(), id), base.With("request_id", id))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// AccessLog logs one line per request (health checks are skipped).
func AccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		logx.From(r.Context()).Info("http",
			"method", r.Method, "path", r.URL.Path, "status", sw.status,
			"bytes", sw.bytes, "ms", time.Since(start).Milliseconds())
	})
}

// Recover turns panics into 500s.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logx.From(r.Context()).Error("panic", "panic", rec, "stack", string(debug.Stack()))
				WriteError(w, r, Internal("internal error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// CORS allows the listed origins ("*" allows any, without credentials).
func CORS(origins []string) Middleware {
	anyOrigin := slices.Contains(origins, "*")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && (anyOrigin || slices.Contains(origins, origin)) {
				h := w.Header()
				h.Set("Vary", "Origin")
				if anyOrigin {
					h.Set("Access-Control-Allow-Origin", "*")
				} else {
					h.Set("Access-Control-Allow-Origin", origin)
					h.Set("Access-Control-Allow-Credentials", "true")
				}
				h.Set("Access-Control-Expose-Headers", HeaderRequestID)
				if r.Method == http.MethodOptions {
					h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
					reqHeaders := r.Header.Get("Access-Control-Request-Headers")
					if reqHeaders == "" {
						reqHeaders = "Authorization, Content-Type, Idempotency-Key, X-Request-ID, X-Store-ID"
					}
					h.Set("Access-Control-Allow-Headers", reqHeaders)
					h.Set("Access-Control-Max-Age", "600")
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ClientIP returns the caller IP, trusting the first X-Forwarded-For hop
// (Cloud Run / Google load balancer set it).
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		ip, _, _ := strings.Cut(xff, ",")
		return strings.TrimSpace(ip)
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return strings.Trim(host, "[]")
}
