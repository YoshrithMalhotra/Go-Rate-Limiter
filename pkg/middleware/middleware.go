// pkg/middleware/middleware.go
package middleware

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"
)

// KeyFunc extracts a unique key from the request.
// e.g. "ip:1.2.3.4", "user:123", "apikey:abc".
type KeyFunc func(r *http.Request) string

// CheckFunc builds one or more dimensions for a request.
type CheckFunc func(r *http.Request) []Check

// MiddlewareOption configures the middleware.
type MiddlewareOption func(*config)

// WithLimit sets the max requests allowed in the window.
func WithLimit(n int64) MiddlewareOption {
	return func(c *config) {
		c.limit = n
	}
}

// WithWindow sets the time window.
func WithWindow(d time.Duration) MiddlewareOption {
	return func(c *config) {
		c.windowMs = d.Milliseconds()
	}
}

// WithPolicy uses a named GoBouncer policy instead of inline limit/window values.
func WithPolicy(name string) MiddlewareOption {
	return func(c *config) {
		c.policy = name
	}
}

// WithCheckFunc enables multi-dimensional limits such as IP + user + route.
func WithCheckFunc(fn CheckFunc) MiddlewareOption {
	return func(c *config) {
		c.checkFunc = fn
	}
}

// WithKeyFunc sets how the middleware identifies each caller.
func WithKeyFunc(fn KeyFunc) MiddlewareOption {
	return func(c *config) {
		c.keyFunc = fn
	}
}

func PolicyCheck(name, key, policy string) Check {
	return Check{Name: name, Key: key, Policy: policy}
}

// IPKey extracts the client IP from the request.
var IPKey KeyFunc = func(r *http.Request) string {
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		return "ip:" + ip
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return "ip:" + r.RemoteAddr
	}
	return "ip:" + ip
}

// HeaderKey returns a KeyFunc that uses a specific header value as the key.
func HeaderKey(header string) KeyFunc {
	return func(r *http.Request) string {
		val := r.Header.Get(header)
		if val == "" {
			return IPKey(r)
		}
		return header + ":" + val
	}
}

func RateLimit(client *Client, opts ...MiddlewareOption) func(http.Handler) http.Handler {
	cfg := &config{
		client:   client,
		limit:    100,
		windowMs: time.Minute.Milliseconds(),
		keyFunc:  IPKey,
	}

	for _, opt := range opts {
		opt(cfg)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cfg.checkFunc != nil {
				result, err := cfg.client.CheckMany(r.Context(), cfg.checkFunc(r))
				if err != nil {
					http.Error(w, "rate limiter unavailable", http.StatusServiceUnavailable)
					return
				}

				setHeaders(w, 0, Result{
					Allowed:    result.Allowed,
					Remaining:  result.Remaining,
					RetryAfter: result.RetryAfter,
				})
				if !result.Allowed {
					writeDenied(w, Result{RetryAfter: result.RetryAfter})
					return
				}

				next.ServeHTTP(w, r)
				return
			}

			key := cfg.keyFunc(r)

			var (
				result Result
				err    error
			)
			if cfg.policy != "" {
				result, err = cfg.client.CheckPolicy(r.Context(), key, cfg.policy)
			} else {
				result, err = cfg.client.Check(r.Context(), key, cfg.limit, cfg.windowMs)
			}
			if err != nil {
				http.Error(w, "rate limiter unavailable", http.StatusServiceUnavailable)
				return
			}

			setHeaders(w, result.Limit, result)

			if !result.Allowed {
				writeDenied(w, result)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func setHeaders(w http.ResponseWriter, limit int64, result Result) {
	if limit > 0 {
		w.Header().Set("X-RateLimit-Limit", strconv.FormatInt(limit, 10))
	}
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(result.Remaining))
	if result.Policy != "" {
		w.Header().Set("X-RateLimit-Policy", result.Policy)
	}

	if result.RetryAfter > 0 {
		retrySeconds := result.RetryAfter / 1000
		if retrySeconds < 1 {
			retrySeconds = 1
		}
		w.Header().Set("Retry-After", strconv.FormatInt(retrySeconds, 10))
	}
}

func writeDenied(w http.ResponseWriter, result Result) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)

	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":          "too many requests",
		"retry_after_ms": result.RetryAfter,
		"message":        fmt.Sprintf("slow down. retry after %dms", result.RetryAfter),
	})
}
