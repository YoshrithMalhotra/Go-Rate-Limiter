//go:build integration

package limiter_test

import (
	"context"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/ritik-kharya/gobouncer/internal/limiter"
)

// Integration tests — require a real Redis instance.
// Run with: go test -tags=integration ./internal/limiter/...
//
// Set REDIS_ADDR env var to point to your Redis instance.
// Default: localhost:6379

func redisAddr() string {
	if addr := os.Getenv("REDIS_ADDR"); addr != "" {
		return addr
	}
	return "localhost:6379"
}

func setupRealRedis(t *testing.T) *redis.Client {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: redisAddr()})
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("skipping integration test: cannot connect to Redis at %s: %v", redisAddr(), err)
	}
	t.Cleanup(func() {
		rdb.Close()
	})
	return rdb
}

func TestIntegration_SlidingWindow_EndToEnd(t *testing.T) {
	rdb := setupRealRedis(t)
	ctx := context.Background()

	// Clean up test keys
	rdb.Del(ctx, "integration:sw:test")

	sw := limiter.NewSlidingWindow(rdb)
	limit := int64(5)
	window := int64(60000)

	// All 5 should be allowed
	for i := int64(0); i < limit; i++ {
		result := sw.Check(ctx, "integration:sw:test", limit, window)
		if !result.Allowed {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}

	// 6th should be denied
	result := sw.Check(ctx, "integration:sw:test", limit, window)
	if result.Allowed {
		t.Fatal("6th request should be denied")
	}

	// Clean up
	rdb.Del(ctx, "integration:sw:test")
}

func TestIntegration_GCRA_EndToEnd(t *testing.T) {
	rdb := setupRealRedis(t)
	ctx := context.Background()

	// Clean up test keys
	rdb.Del(ctx, "gcra:integration:gcra:test")

	g := limiter.NewGCRA(rdb)
	limit := int64(5)
	window := int64(60000)

	// Burst should be allowed
	for i := int64(0); i < limit; i++ {
		result := g.Check(ctx, "integration:gcra:test", limit, window)
		if !result.Allowed {
			t.Fatalf("burst request %d should be allowed", i+1)
		}
	}

	// Beyond burst should be denied
	result := g.Check(ctx, "integration:gcra:test", limit, window)
	if result.Allowed {
		t.Fatal("request beyond burst should be denied")
	}

	// Clean up
	rdb.Del(ctx, "gcra:integration:gcra:test")
}

func TestIntegration_BothAlgorithms_SatisfyInterface(t *testing.T) {
	rdb := setupRealRedis(t)

	// Compile-time check that both satisfy the Algorithm interface
	var _ limiter.Algorithm = limiter.NewSlidingWindow(rdb)
	var _ limiter.Algorithm = limiter.NewGCRA(rdb)
}
