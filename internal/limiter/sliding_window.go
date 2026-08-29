package limiter

import (
	"context"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

var reqCounter uint64

type SlidingWindow struct {
	rdb      *redis.Client
	failOpen bool
}

type SlidingWindowOption func(*SlidingWindow)

// WithSlidingWindowFailOpen controls what happens when Redis is unreachable.
// true  = allow the request (availability over enforcement)
// false = deny the request (enforcement over availability)
func WithSlidingWindowFailOpen(open bool) SlidingWindowOption {
	return func(s *SlidingWindow) {
		s.failOpen = open
	}
}

func NewSlidingWindow(rdb *redis.Client, opts ...SlidingWindowOption) *SlidingWindow {
	s := &SlidingWindow{
		rdb:      rdb,
		failOpen: true,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

var slidingWindowScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])
local window = tonumber(ARGV[3])
local member = ARGV[4]
local window_start = now - window

redis.call("ZREMRANGEBYSCORE", KEYS[1], "-inf", window_start)

local count = redis.call("ZCARD", KEYS[1])
if count >= limit then
	local oldest = redis.call("ZRANGEBYSCORE", KEYS[1], "-inf", "+inf", "WITHSCORES", "LIMIT", 0, 1)
	local retry_after = window
	if oldest[2] then
		retry_after = tonumber(oldest[2]) + window - now
		if retry_after < 0 then
			retry_after = 0
		end
	end
	return {0, 0, retry_after}
end

redis.call("ZADD", KEYS[1], now, member)
redis.call("PEXPIRE", KEYS[1], window)

return {1, limit - count - 1, 0}
`)

func (l *SlidingWindow) Check(ctx context.Context, key string, limit, window int64) Result {
	if limit <= 0 || window <= 0 {
		return Result{Allowed: false, Remaining: 0, RetryAfter: 0}
	}

	now := time.Now().UnixMilli()
	member := strconv.FormatInt(now, 10) + ":" + strconv.FormatUint(atomic.AddUint64(&reqCounter, 1), 10)

	values, err := slidingWindowScript.Run(ctx, l.rdb, []string{key}, now, limit, window, member).Slice()
	if err != nil || len(values) != 3 {
		return onRedisError(l.failOpen)
	}

	return Result{
		Allowed:    parseScriptInt(values[0]) == 1,
		Remaining:  parseScriptInt(values[1]),
		RetryAfter: parseScriptInt(values[2]),
	}
}
