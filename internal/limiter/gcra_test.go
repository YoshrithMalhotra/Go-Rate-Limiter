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

func setupGCRA(t *testing.T) (*limiter.GCRA, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return limiter.NewGCRA(rdb), mr
}

func TestGCRA_FirstRequest_Allowed(t *testing.T) {
	g, _ := setupGCRA(t)
	ctx := context.Background()

	result := g.Check(ctx, "user:1", 10, 60000) // 10 req per 60s

	if !result.Allowed {
		t.Fatal("expected first request to be allowed")
	}
	if result.RetryAfter != 0 {
		t.Fatalf("expected retry_after=0, got %d", result.RetryAfter)
	}
}

func TestGCRA_BurstAllowed(t *testing.T) {
	g, _ := setupGCRA(t)
	ctx := context.Background()

	limit := int64(5)
	window := int64(60000)

	// GCRA allows a burst of up to `limit` requests
	for i := int64(0); i < limit; i++ {
		result := g.Check(ctx, "user:burst", limit, window)
		if !result.Allowed {
			t.Fatalf("burst request %d should be allowed", i+1)
		}
	}
}

func TestGCRA_BeyondBurst_Denied(t *testing.T) {
	g, _ := setupGCRA(t)
	ctx := context.Background()

	limit := int64(5)
	window := int64(60000)

	// Fill burst
	for i := int64(0); i < limit; i++ {
		g.Check(ctx, "user:deny", limit, window)
	}

	// Next request should be denied
	result := g.Check(ctx, "user:deny", limit, window)
	if result.Allowed {
		t.Fatal("request beyond burst should be denied")
	}
	if result.RetryAfter <= 0 {
		t.Fatal("expected retry_after > 0 when denied")
	}
}

func TestGCRA_DifferentKeys_Independent(t *testing.T) {
	g, _ := setupGCRA(t)
	ctx := context.Background()

	// Fill up key A
	for i := 0; i < 3; i++ {
		g.Check(ctx, "user:A", 3, 60000)
	}

	// Key B should still be allowed
	result := g.Check(ctx, "user:B", 3, 60000)
	if !result.Allowed {
		t.Fatal("different keys should be independent")
	}
}

func TestGCRA_Recovery_AfterWindow(t *testing.T) {
	g, mr := setupGCRA(t)
	ctx := context.Background()

	limit := int64(3)
	window := int64(3000) // 3 second window

	// Fill burst
	for i := int64(0); i < limit; i++ {
		g.Check(ctx, "user:recover", limit, window)
	}

	// Should be denied
	result := g.Check(ctx, "user:recover", limit, window)
	if result.Allowed {
		t.Fatal("should be denied after burst")
	}

	// Fast-forward past the full window
	mr.FastForward(4 * time.Second)

	// Should be allowed again
	result = g.Check(ctx, "user:recover", limit, window)
	if !result.Allowed {
		t.Fatal("should be allowed after window passes")
	}
}

func TestGCRA_RemainingDecreases(t *testing.T) {
	g, _ := setupGCRA(t)
	ctx := context.Background()

	limit := int64(5)
	window := int64(86400000) // 24 hours to prevent millisecond elapsed from leaking bucket during test execution

	lastRemaining := limit
	for i := int64(0); i < limit; i++ {
		result := g.Check(ctx, "user:remaining", limit, window)
		if result.Remaining >= lastRemaining && i > 0 {
			t.Fatalf("remaining should decrease: was %d, now %d at request %d",
				lastRemaining, result.Remaining, i+1)
		}
		lastRemaining = result.Remaining
	}
}

func TestGCRA_RemainingReflectsBurstSlots(t *testing.T) {
	g, _ := setupGCRA(t)
	ctx := context.Background()

	limit := int64(5)
	window := int64(60000)

	for i := int64(0); i < limit; i++ {
		result := g.Check(ctx, "user:burst-remaining", limit, window)
		expected := limit - i - 1
		if !result.Allowed {
			t.Fatalf("request %d should be allowed", i+1)
		}
		if result.Remaining != expected {
			t.Fatalf("request %d: expected remaining=%d, got %d", i+1, expected, result.Remaining)
		}
	}
}

func TestGCRA_ConcurrentBurst_AllowsOnlyLimit(t *testing.T) {
	g, _ := setupGCRA(t)

	allowed := runConcurrentGCRAChecks(t, g, "user:concurrent-burst", 20, 60000, 50)
	if allowed != 20 {
		t.Fatalf("expected exactly 20 allowed requests, got %d", allowed)
	}
}

func TestGCRA_ConcurrentLoad_AllowsOnlyLimit(t *testing.T) {
	g, _ := setupGCRA(t)

	allowed := runConcurrentGCRAChecks(t, g, "user:concurrent-load", 20, 60000, 1000)
	if allowed != 20 {
		t.Fatalf("expected exactly 20 allowed requests, got %d", allowed)
	}
}

func TestGCRA_RedisError_FailOpenAllows(t *testing.T) {
	rdb := unreachableRedisClient()
	g := limiter.NewGCRA(rdb, limiter.WithGCRAFailOpen(true))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result := g.Check(ctx, "user:redis-down-open", 10, 60000)
	if !result.Allowed {
		t.Fatal("expected fail-open GCRA to allow when Redis is unavailable")
	}
}

func TestGCRA_RedisError_FailClosedDenies(t *testing.T) {
	rdb := unreachableRedisClient()
	g := limiter.NewGCRA(rdb, limiter.WithGCRAFailOpen(false))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result := g.Check(ctx, "user:redis-down-closed", 10, 60000)
	if result.Allowed {
		t.Fatal("expected fail-closed GCRA to deny when Redis is unavailable")
	}
}

func runConcurrentGCRAChecks(t *testing.T, g *limiter.GCRA, key string, limit, window int64, count int) int64 {
	t.Helper()

	var allowed int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(count)

	for i := 0; i < count; i++ {
		go func() {
			defer wg.Done()
			<-start
			result := g.Check(context.Background(), key, limit, window)
			if result.Allowed {
				atomic.AddInt64(&allowed, 1)
			}
		}()
	}

	close(start)
	wg.Wait()
	return allowed
}

func unreachableRedisClient() *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:         "127.0.0.1:1",
		DialTimeout:  10 * time.Millisecond,
		ReadTimeout:  10 * time.Millisecond,
		WriteTimeout: 10 * time.Millisecond,
		MaxRetries:   0,
	})
}
