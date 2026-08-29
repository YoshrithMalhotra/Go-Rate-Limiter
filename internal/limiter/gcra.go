package limiter

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// GCRA (Generic Cell Rate Algorithm)
//
// Every key stores one Redis value: the theoretical arrival time (TAT).
// The Redis script below checks admission and updates TAT atomically, so
// concurrent requests cannot all read the same old TAT and over-admit.
type GCRA struct {
	rdb      *redis.Client
	failOpen bool
}

type GCRAOption func(*GCRA)

func WithGCRAFailOpen(open bool) GCRAOption {
	return func(g *GCRA) {
		g.failOpen = open
	}
}

func NewGCRA(rdb *redis.Client, opts ...GCRAOption) *GCRA {
	g := &GCRA{
		rdb:      rdb,
		failOpen: true,
	}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

var gcraScript = redis.NewScript(`
local tat = redis.call("GET", KEYS[1])
local now = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])
local window = tonumber(ARGV[3])

local emission = math.floor((window + limit - 1) / limit)
if emission < 1 then
	emission = 1
end

local burst = emission * (limit - 1)

if not tat then
	tat = now
else
	tat = tonumber(tat)
end

local allow_at = tat - burst
if now < allow_at then
	return {0, 0, allow_at - now}
end

local new_tat = tat
if now > new_tat then
	new_tat = now
end
new_tat = new_tat + emission

redis.call("SET", KEYS[1], new_tat, "PX", window)

local remaining = math.floor((now + burst - new_tat) / emission) + 1
if remaining < 0 then
	remaining = 0
end

return {1, remaining, 0}
`)

func (g *GCRA) Check(ctx context.Context, key string, limit, window int64) Result {
	if limit <= 0 || window <= 0 {
		return Result{Allowed: false, Remaining: 0}
	}

	now := time.Now().UnixMilli()
	tatKey := "gcra:" + key

	values, err := gcraScript.Run(ctx, g.rdb, []string{tatKey}, now, limit, window).Slice()
	if err != nil || len(values) != 3 {
		return onRedisError(g.failOpen)
	}

	allowed := parseScriptInt(values[0]) == 1
	remaining := parseScriptInt(values[1])
	retryAfter := parseScriptInt(values[2])

	return Result{
		Allowed:    allowed,
		Remaining:  remaining,
		RetryAfter: retryAfter,
	}
}

func parseScriptInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case string:
		parsed, _ := strconv.ParseInt(n, 10, 64)
		return parsed
	case []byte:
		parsed, _ := strconv.ParseInt(string(n), 10, 64)
		return parsed
	default:
		return 0
	}
}
