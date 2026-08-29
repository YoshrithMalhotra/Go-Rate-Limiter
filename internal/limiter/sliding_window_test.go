package limiter_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/ritik-kharya/gobouncer/internal/limiter"
)

func setupSlidingWindow(t *testing.T) (*limiter.SlidingWindow, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return limiter.NewSlidingWindow(rdb), mr
}

func TestSlidingWindow_FirstRequest_Allowed(t *testing.T) {
	sw, _ := setupSlidingWindow(t)
	ctx := context.Background()

	result := sw.Check(ctx, "user:1", 10, 60000) // 10 req per 60s

	if !result.Allowed {
		t.Fatal("expected first request to be allowed")
	}
	if result.Remaining != 9 {
		t.Fatalf("expected remaining=9, got %d", result.Remaining)
	}
	if result.RetryAfter != 0 {
		t.Fatalf("expected retry_after=0, got %d", result.RetryAfter)
	}
}

func TestSlidingWindow_ExactLimit_Denied(t *testing.T) {
	sw, _ := setupSlidingWindow(t)
	ctx := context.Background()

	limit := int64(5)
	window := int64(60000)

	// Use up all 5 slots
	for i := int64(0); i < limit; i++ {
		result := sw.Check(ctx, "user:2", limit, window)
		if !result.Allowed {
			t.Fatalf("request %d should be allowed", i+1)
		}
		expectedRemaining := limit - i - 1
		if result.Remaining != expectedRemaining {
			t.Fatalf("request %d: expected remaining=%d, got %d", i+1, expectedRemaining, result.Remaining)
		}
	}

	// 6th request should be denied
	result := sw.Check(ctx, "user:2", limit, window)
	if result.Allowed {
		t.Fatal("request beyond limit should be denied")
	}
	if result.Remaining != 0 {
		t.Fatalf("expected remaining=0 when denied, got %d", result.Remaining)
	}
	if result.RetryAfter <= 0 {
		t.Fatal("expected retry_after > 0 when denied")
	}
}

func TestSlidingWindow_DifferentKeys_Independent(t *testing.T) {
	sw, _ := setupSlidingWindow(t)
	ctx := context.Background()

	// Fill up key A
	for i := 0; i < 3; i++ {
		sw.Check(ctx, "user:A", 3, 60000)
	}

	// Key B should still be allowed
	result := sw.Check(ctx, "user:B", 3, 60000)
	if !result.Allowed {
		t.Fatal("different keys should be independent")
	}
	if result.Remaining != 2 {
		t.Fatalf("expected remaining=2 for fresh key, got %d", result.Remaining)
	}
}

func TestSlidingWindow_WindowExpiry_Resets(t *testing.T) {
	sw, mr := setupSlidingWindow(t)
	ctx := context.Background()

	// Use up all slots
	for i := 0; i < 3; i++ {
		sw.Check(ctx, "user:3", 3, 1000) // 1 second window
	}

	// Should be denied
	result := sw.Check(ctx, "user:3", 3, 1000)
	if result.Allowed {
		t.Fatal("should be denied at limit")
	}

	// Fast-forward time past the window
	mr.FastForward(2 * time.Second)

	// Should be allowed again
	result = sw.Check(ctx, "user:3", 3, 1000)
	if !result.Allowed {
		t.Fatal("should be allowed after window expiry")
	}
}

func TestSlidingWindow_RemainingCountsDown(t *testing.T) {
	sw, _ := setupSlidingWindow(t)
	ctx := context.Background()

	limit := int64(5)
	for i := int64(0); i < limit; i++ {
		result := sw.Check(ctx, "user:countdown", limit, 60000)
		expected := limit - i - 1
		if result.Remaining != expected {
			t.Fatalf("request %d: expected remaining=%d, got %d", i+1, expected, result.Remaining)
		}
	}
}

func TestSlidingWindow_ConcurrentBurst_AllowsOnlyLimit(t *testing.T) {
	sw, _ := setupSlidingWindow(t)

	allowed := runConcurrentSlidingWindowChecks(t, sw, "user:concurrent-burst", 20, 60000, 50)
	if allowed != 20 {
		t.Fatalf("expected exactly 20 allowed requests, got %d", allowed)
	}
}

func TestSlidingWindow_ConcurrentLoad_AllowsOnlyLimit(t *testing.T) {
	sw, _ := setupSlidingWindow(t)

	allowed := runConcurrentSlidingWindowChecks(t, sw, "user:concurrent-load", 20, 60000, 1000)
	if allowed != 20 {
		t.Fatalf("expected exactly 20 allowed requests, got %d", allowed)
	}
}

func runConcurrentSlidingWindowChecks(t *testing.T, sw *limiter.SlidingWindow, key string, limit, window int64, count int) int64 {
	t.Helper()

	var allowed int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(count)

	for i := 0; i < count; i++ {
		go func() {
			defer wg.Done()
			<-start
			result := sw.Check(context.Background(), key, limit, window)
			if result.Allowed {
				atomic.AddInt64(&allowed, 1)
			}
		}()
	}

	close(start)
	wg.Wait()
	return allowed
}

func TestSlidingWindow_RedisError_FailOpenAllows(t *testing.T) {
	rdb := unreachableRedisClient()
	sw := limiter.NewSlidingWindow(rdb, limiter.WithSlidingWindowFailOpen(true))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result := sw.Check(ctx, "user:redis-down-open", 10, 60000)
	if !result.Allowed {
		t.Fatal("expected fail-open sliding window to allow when Redis is unavailable")
	}
}

func TestSlidingWindow_RedisError_FailClosedDenies(t *testing.T) {
	rdb := unreachableRedisClient()
	sw := limiter.NewSlidingWindow(rdb, limiter.WithSlidingWindowFailOpen(false))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result := sw.Check(ctx, "user:redis-down-closed", 10, 60000)
	if result.Allowed {
		t.Fatal("expected fail-closed sliding window to deny when Redis is unavailable")
	}
}

// Both algorithms must read a single FAIL_OPEN setting the same way, otherwise
// a Redis outage silently blocks sliding-window policies while GCRA policies
// sail through.
func TestBothAlgorithms_AgreeOnFailOpenBehaviour(t *testing.T) {
	for _, failOpen := range []bool{true, false} {
		rdb := unreachableRedisClient()
		sw := limiter.NewSlidingWindow(rdb, limiter.WithSlidingWindowFailOpen(failOpen))
		g := limiter.NewGCRA(rdb, limiter.WithGCRAFailOpen(failOpen))

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		swResult := sw.Check(ctx, "user:agree", 10, 60000)
		gResult := g.Check(ctx, "user:agree", 10, 60000)
		cancel()

		if swResult.Allowed != failOpen || gResult.Allowed != failOpen {
			t.Fatalf("fail_open=%v: sliding_window allowed=%v, gcra allowed=%v — both should be %v",
				failOpen, swResult.Allowed, gResult.Allowed, failOpen)
		}
	}
}
