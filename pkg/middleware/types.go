package middleware

import (
	"net/http"
)

type Result struct {
	Allowed    bool   `json:"allowed"`
	Remaining  int    `json:"remaining"`
	RetryAfter int64  `json:"retry_after,omitempty"`
	Limit      int64  `json:"-"`
	Policy     string `json:"-"`
}

type Check struct {
	Name      string `json:"name,omitempty"`
	Key       string `json:"key"`
	Policy    string `json:"policy,omitempty"`
	Limit     int64  `json:"limit,omitempty"`
	WindowMs  int64  `json:"window_ms,omitempty"`
	Algorithm string `json:"algorithm,omitempty"`
}

type CheckResult struct {
	Name       string `json:"name,omitempty"`
	Key        string `json:"key"`
	Policy     string `json:"policy,omitempty"`
	Allowed    bool   `json:"allowed"`
	Remaining  int    `json:"remaining"`
	RetryAfter int64  `json:"retry_after,omitempty"`
}

type MultiResult struct {
	Allowed    bool          `json:"allowed"`
	Remaining  int           `json:"remaining"`
	RetryAfter int64         `json:"retry_after,omitempty"`
	Checks     []CheckResult `json:"checks"`
}

// checkRequest is what we POST to /check
type checkRequest struct {
	Key      string  `json:"key"`
	Policy   string  `json:"policy,omitempty"`
	Limit    int64   `json:"limit"`
	WindowMs int64   `json:"window_ms"`
	Checks   []Check `json:"checks,omitempty"`
}

// Client is the GoBouncer HTTP client.
// Create once at startup, reuse everywhere.
// It is safe for concurrent use.
type Client struct {
	baseURL    string
	httpClient *http.Client
	failOpen   bool
}

// config holds all middleware configuration
type config struct {
	client    *Client
	limit     int64
	windowMs  int64
	policy    string
	keyFunc   KeyFunc
	checkFunc CheckFunc
}
