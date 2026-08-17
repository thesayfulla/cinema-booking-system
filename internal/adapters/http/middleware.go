package http

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/thesayfulla/cinema-booking-system/internal/logger"
	"github.com/thesayfulla/cinema-booking-system/internal/metrics"
)

// Middleware wraps a handler with cross-cutting behaviour.
type Middleware func(http.Handler) http.Handler

// Chain applies middleware so that the first argument is the outermost layer,
// which is the order they appear to a request.
func Chain(h http.Handler, mw ...Middleware) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

// RequestIDHeader propagates a trace id across services.
const RequestIDHeader = "X-Request-Id"

// UserHeader identifies the caller.
//
// This stands in for real authentication: a production deployment would put an
// authenticated subject here (from a JWT or session) instead of trusting the
// client. Everything downstream reads the user from the context, so replacing
// this middleware with token verification is a local change.
const UserHeader = "X-User-Id"

type userKey struct{}

// UserID returns the caller's id, or "" when the request is anonymous.
func UserID(ctx context.Context) string {
	id, _ := ctx.Value(userKey{}).(string)
	return id
}

// RequestID assigns each request an id, reusing an inbound one when present,
// and echoes it on the response.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(RequestIDHeader)
			if id == "" || len(id) > 64 {
				id = newRequestID()
			}
			w.Header().Set(RequestIDHeader, id)
			next.ServeHTTP(w, r.WithContext(logger.WithRequestID(r.Context(), id)))
		})
	}
}

// Identify reads the caller id from UserHeader into the request context.
func Identify() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID := strings.TrimSpace(r.Header.Get(UserHeader))
			if len(userID) > 128 {
				writeError(w, r, http.StatusBadRequest, codeValidation,
					"user id is too long", UserHeader)
				return
			}
			if userID != "" {
				r = r.WithContext(context.WithValue(r.Context(), userKey{}, userID))
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireUser rejects requests that carry no caller identity.
func RequireUser() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if UserID(r.Context()) == "" {
				writeError(w, r, http.StatusUnauthorized, codeUnauthorized,
					"missing "+UserHeader+" header", UserHeader)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Recover turns a panic into a 500 instead of a dropped connection, and logs
// the stack so the bug is still visible.
func Recover(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					// A client that hangs up mid-write triggers this too;
					// there is nothing useful to send back in that case.
					if rec == http.ErrAbortHandler {
						panic(rec)
					}
					log.ErrorContext(r.Context(), "panic recovered",
						"panic", rec, "method", r.Method, "path", r.URL.Path,
						"stack", stackTrace())
					writeError(w, r, http.StatusInternalServerError, codeInternal,
						"an unexpected error occurred", "")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// AccessLog records one structured line per request.
func AccessLog(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rec, r)

			level := slog.LevelInfo
			if rec.status >= 500 {
				level = slog.LevelError
			} else if rec.status >= 400 {
				level = slog.LevelWarn
			}

			log.Log(r.Context(), level, "http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.written,
				"duration_ms", time.Since(start).Milliseconds(),
				"remote_ip", clientIP(r),
				"user_agent", r.UserAgent(),
			)
		})
	}
}

// Metrics records request counts and latencies for Prometheus.
//
// routeOf resolves the request to its registered route template. It cannot be
// read back from r.Pattern here: the layers below hand the mux a copy of the
// request, so the pattern the mux fills in never reaches this one.
func Metrics(m *metrics.Collector, routeOf func(*http.Request) string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rec, r)

			// Label with the route pattern, never the raw path: per-id labels
			// would blow up cardinality in the metrics store.
			route := "unmatched"
			if routeOf != nil {
				if p := routeOf(r); p != "" {
					route = p
				}
			}
			m.ObserveRequest(r.Method, route, strconv.Itoa(rec.status), time.Since(start))
		})
	}
}

// Timeout bounds how long a handler may run.
func Timeout(d time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		if d <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// MaxBody caps request bodies so a large upload cannot exhaust memory.
func MaxBody(limit int64) Middleware {
	return func(next http.Handler) http.Handler {
		if limit <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	}
}

// SecurityHeaders sets conservative defaults for the browser client.
func SecurityHeaders() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "same-origin")
			h.Set("Content-Security-Policy",
				"default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'")
			next.ServeHTTP(w, r)
		})
	}
}

// CORS answers preflights and allows the configured origins.
func CORS(origins []string) Middleware {
	allowAll := len(origins) == 1 && origins[0] == "*"
	allowed := make(map[string]bool, len(origins))
	for _, o := range origins {
		allowed[o] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && (allowAll || allowed[origin]) {
				h := w.Header()
				if allowAll {
					h.Set("Access-Control-Allow-Origin", "*")
				} else {
					h.Set("Access-Control-Allow-Origin", origin)
					// Caches must not serve one origin's response to another.
					h.Add("Vary", "Origin")
				}
				h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
				h.Set("Access-Control-Allow-Headers",
					strings.Join([]string{"Content-Type", UserHeader, RequestIDHeader, "Idempotency-Key"}, ", "))
				h.Set("Access-Control-Max-Age", "600")
			}

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RateLimit throttles each client IP with a token bucket.
//
// In-process state is enough for a single instance; behind several replicas
// this becomes per-replica and a shared limiter (Redis, or the ingress) should
// take over.
func RateLimit(rps float64, burst int) Middleware {
	limiter := newIPLimiter(rps, burst)

	return func(next http.Handler) http.Handler {
		if rps <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !limiter.allow(clientIP(r)) {
				w.Header().Set("Retry-After", "1")
				writeError(w, r, http.StatusTooManyRequests, codeRateLimited,
					"too many requests, slow down", "")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ipLimiter keeps one token bucket per client IP and evicts idle buckets so
// memory does not grow with the number of addresses seen.
type ipLimiter struct {
	rps   rate.Limit
	burst int

	mu      sync.Mutex
	buckets map[string]*ipBucket
	lastGC  time.Time
}

type ipBucket struct {
	limiter *rate.Limiter
	seen    time.Time
}

const ipBucketTTL = 10 * time.Minute

func newIPLimiter(rps float64, burst int) *ipLimiter {
	if burst <= 0 {
		burst = 1
	}
	return &ipLimiter{
		rps:     rate.Limit(rps),
		burst:   burst,
		buckets: make(map[string]*ipBucket),
		lastGC:  time.Now(),
	}
}

func (l *ipLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	if now.Sub(l.lastGC) > ipBucketTTL {
		for key, b := range l.buckets {
			if now.Sub(b.seen) > ipBucketTTL {
				delete(l.buckets, key)
			}
		}
		l.lastGC = now
	}

	b, ok := l.buckets[ip]
	if !ok {
		b = &ipBucket{limiter: rate.NewLimiter(l.rps, l.burst)}
		l.buckets[ip] = b
	}
	b.seen = now

	return b.limiter.Allow()
}

// statusRecorder captures the status code and size for logging and metrics.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	written     int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.written += n
	return n, err
}

// Flush keeps streaming responses working through the wrapper.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// clientIP prefers the left-most X-Forwarded-For entry when running behind a
// trusted proxy, and falls back to the socket address.
func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if first, _, found := strings.Cut(fwd, ","); found || first != "" {
			if ip := strings.TrimSpace(first); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func newRequestID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buf)
}

func stackTrace() string {
	buf := make([]byte, 8<<10)
	n := runtime.Stack(buf, false)
	return string(buf[:n])
}
