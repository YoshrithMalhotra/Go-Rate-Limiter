package handlers

import (
	"time"

	"github.com/ritik-kharya/gobouncer/internal/limiter"
	"github.com/ritik-kharya/gobouncer/internal/policy"
)

// CheckRequest is what the caller sends.
type CheckRequest struct {
	Key       string  `json:"key"`
	Policy    string  `json:"policy"`
	Limit     int64   `json:"limit"`
	WindowMs  int64   `json:"window_ms"`
	Algorithm string  `json:"algorithm"`
	Checks    []Check `json:"checks"`
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
	Remaining  int64  `json:"remaining"`
	RetryAfter int64  `json:"retry_after,omitempty"`
}

type MultiCheckResult struct {
	Allowed    bool          `json:"allowed"`
	Remaining  int64         `json:"remaining"`
	RetryAfter int64         `json:"retry_after,omitempty"`
	Checks     []CheckResult `json:"checks"`
}

// Algorithms holds both algorithm instances.
type Algorithms struct {
	SlidingWindow limiter.Algorithm
	GCRA          limiter.Algorithm
}

type PolicyStore interface {
	Get(name string) (policy.Policy, bool)
	List() []policy.Policy
}

type MetricsRecorder interface {
	ObserveCheck(policyName, algorithm, outcome string, duration time.Duration)
}
