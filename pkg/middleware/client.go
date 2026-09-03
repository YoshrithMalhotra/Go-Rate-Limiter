// pkg/middleware/client.go
package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// Option is the functional options type
type Option func(*Client)

// WithTimeout sets the max time to wait for Governor to respond.
// Default: 150ms. Keep this tight — a slow rate limiter is worse than none.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		c.httpClient.Timeout = d
	}
}

// WithFailOpen controls what happens if Governor is unreachable.
// true  = allow the request through (default — availability over security)
// false = deny the request (security over availability)
func WithFailOpen(open bool) Option {
	return func(c *Client) {
		c.failOpen = open
	}
}

// NewClient creates a Governor HTTP client.
// baseURL is the address of your running Governor service e.g. "http://localhost:8080"
func NewClient(baseURL string, opts ...Option) *Client {
	c := &Client{
		baseURL:  baseURL,
		failOpen: true, // safe default
		httpClient: &http.Client{
			Timeout: 150 * time.Millisecond,
			// reuse TCP connections — critical for performance
			// net/http.DefaultTransport already does this,
			// but we set it explicitly so nobody accidentally replaces it
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 100,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}

	// apply any options the caller passed
	for _, opt := range opts {
		opt(c)
	}

	return c
}

// Check asks Governor: should this key be allowed right now?
// ctx carries the deadline from the incoming request — if the user
// cancelled their request, we cancel the Governor call too.
func (c *Client) Check(ctx context.Context, key string, limit, windowMs int64) (Result, error) {
	result, err := c.doCheck(ctx, checkRequest{
		Key:      key,
		Limit:    limit,
		WindowMs: windowMs,
	})
	result.Limit = limit
	return result, err
}

func (c *Client) CheckPolicy(ctx context.Context, key, policy string) (Result, error) {
	result, err := c.doCheck(ctx, checkRequest{
		Key:    key,
		Policy: policy,
	})
	result.Policy = policy
	return result, err
}

func (c *Client) CheckMany(ctx context.Context, checks []Check) (MultiResult, error) {
	body, err := json.Marshal(checkRequest{Checks: checks})
	if err != nil {
		return c.onMultiError(fmt.Errorf("governor: marshal error: %w", err))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/check", bytes.NewReader(body))
	if err != nil {
		return c.onMultiError(fmt.Errorf("governor: build request error: %w", err))
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return c.onMultiError(fmt.Errorf("governor: request failed: %w", err))
	}
	defer func() { _ = resp.Body.Close() }()

	if err := checkStatus(resp.StatusCode); err != nil {
		return c.onMultiError(err)
	}

	var result MultiResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return c.onMultiError(fmt.Errorf("governor: decode error: %w", err))
	}
	return result, nil
}

func (c *Client) doCheck(ctx context.Context, check checkRequest) (Result, error) {
	// build the request body
	body, err := json.Marshal(check)
	if err != nil {
		return c.onError(fmt.Errorf("governor: marshal error: %w", err))
	}

	// build the HTTP request — attach ctx so cancellation propagates
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+"/check",
		bytes.NewReader(body),
	)
	if err != nil {
		return c.onError(fmt.Errorf("governor: build request error: %w", err))
	}
	req.Header.Set("Content-Type", "application/json")

	// fire the request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return c.onError(fmt.Errorf("governor: request failed: %w", err))
	}

	// run this function at last -- since using defer function
	defer func() { _ = resp.Body.Close() }()

	// A rate-limit answer is only ever 200 or 429. Anything else is an error
	// page (e.g. 404 "Unknown policy", 500) whose plain-text body would either
	// fail to decode or decode into a zero-value Result — which reads as a
	// denial we never actually made. Catch it on the status instead.
	if err := checkStatus(resp.StatusCode); err != nil {
		return c.onError(err)
	}

	// parse the response
	var result Result
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return c.onError(fmt.Errorf("governor: decode error: %w", err))
	}
	if limit := resp.Header.Get("X-RateLimit-Limit"); limit != "" {
		if parsed, err := strconv.ParseInt(limit, 10, 64); err == nil {
			result.Limit = parsed
		}
	}
	if policy := resp.Header.Get("X-RateLimit-Policy"); policy != "" {
		result.Policy = policy
	}

	return result, nil
}

// checkStatus rejects any status that is not a real rate-limit decision.
// 200 = allowed, 429 = denied. Everything else means the request never
// reached the limiter.
func checkStatus(code int) error {
	if code == http.StatusOK || code == http.StatusTooManyRequests {
		return nil
	}
	return fmt.Errorf("governor: unexpected status %d", code)
}

// onError is called whenever the Governor service is unreachable or broken.
// failOpen = true  → allow the request, return no error to the middleware
// failOpen = false → deny the request, caller gets the error
func (c *Client) onError(err error) (Result, error) {
	if c.failOpen {
		// Governor is down — let traffic through, log the error upstream
		return Result{Allowed: true, Remaining: -1}, nil
	}
	return Result{Allowed: false}, err
}

func (c *Client) onMultiError(err error) (MultiResult, error) {
	if c.failOpen {
		return MultiResult{Allowed: true, Remaining: -1}, nil
	}
	return MultiResult{Allowed: false}, err
}
