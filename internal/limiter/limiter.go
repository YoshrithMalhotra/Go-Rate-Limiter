package limiter

import "context"

type Result struct {
	Allowed    bool  `json:"allowed"`
	Remaining  int64 `json:"remaining"`
	RetryAfter int64 `json:"retry_after,omitempty"`
}

type Algorithm interface {
	Check(ctx context.Context, key string, limit, window int64) Result
}

// onRedisError builds the Result to return when Redis cannot answer.
// Both algorithms share it so a single FAIL_OPEN setting means the same
// thing no matter which algorithm a policy selects.
func onRedisError(failOpen bool) Result {
	if failOpen {
		return Result{Allowed: true, Remaining: 0, RetryAfter: 0}
	}
	return Result{Allowed: false, Remaining: 0, RetryAfter: 0}
}
