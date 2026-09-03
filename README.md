# Governor

Governor is a Redis-backed rate-limiting service written in Go. It runs as a standalone HTTP API and ships with a Go client + middleware, so a Go service can enforce limits without talking to Redis directly. It supports two limiting algorithms, named policies, request-time overrides, multi-dimensional checks (e.g. IP *and* user *and* route in one call), and Prometheus-style metrics.

## Why

Most rate limiters either run in-process (fine for one instance, wrong once you scale horizontally) or get bolted onto Redis with a `GET`-then-`SET` pattern that races under concurrent load: two requests can both read "under the limit" and both get admitted, letting traffic through above the configured cap right when the limiter matters most. Governor runs each check as a single Redis Lua script, so the read-check-write cycle is atomic — safe for many instances hitting the same key at once.

## Features

- **Two algorithms.** Sliding window (Redis sorted sets, strict rolling-window accuracy) and GCRA (Generic Cell Rate Algorithm — one Redis string per key, smoother admission, cheaper memory at high volume). Both run as atomic Lua scripts.
- **Named policies.** Define limits once (`config/policies.example.json`) and reference them by name instead of passing raw `limit`/`window_ms` from every caller.
- **Multi-dimensional checks.** Send a list of checks (IP, user, route, ...) in one `/check` call; Governor evaluates them in order and short-circuits on the first denial.
- **Configurable fail-open / fail-closed.** Choose whether a Redis outage lets traffic through or blocks it, via `FAIL_OPEN`.
- **Go middleware + client SDK.** Wrap any `net/http` handler with `middleware.RateLimit(...)` instead of hand-rolling HTTP calls to the service.
- **Operational basics.** `/health` (liveness) and `/ready` (checks Redis) are separate, so an orchestrator doesn't restart the process for a Redis blip. Graceful shutdown drains in-flight requests. `/metrics` exposes Prometheus-format counters and a duration histogram per policy/algorithm/outcome.

## API

### `POST /check`

Single check, either inline or via a named policy:

```bash
curl -X POST localhost:8080/check \
  -d '{"key":"user:123","limit":100,"window_ms":60000}'

curl -X POST localhost:8080/check \
  -d '{"key":"user:123","policy":"login"}'
```

Response (200 when allowed, 429 when denied):

```json
{"allowed": true, "remaining": 99}
```

Headers: `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Policy` (when a policy was used), and `Retry-After` (seconds, only set on a denial).

Multiple dimensions in one call:

```bash
curl -X POST localhost:8080/check -d '{
  "checks": [
    {"name": "ip",   "key": "ip:1.2.3.4", "policy": "ip-basic"},
    {"name": "user", "key": "user:123",   "policy": "user-free"}
  ]
}'
```

The response is denied as a whole if any dimension is denied, using that dimension's `Retry-After`; `checks` stops at the first denial rather than evaluating the rest.

### `GET /policies`

Lists the configured named policies.

### `GET /metrics`

Prometheus exposition format: `governor_checks_total{policy,algorithm,outcome}` and `governor_check_duration_seconds` (histogram).

### `GET /health` vs `GET /ready`

`/health` is liveness only — it never touches Redis, so a Redis outage won't get the process restarted. `/ready` pings Redis with a 500ms timeout and returns 503 when it's unreachable, so a load balancer or orchestrator can stop routing traffic there.

## Configuration

Set via environment variables (a local `.env` file is also picked up):

| Variable | Default | Meaning |
|---|---|---|
| `REDIS_ADDR` | `localhost:6379` | Redis address |
| `PORT` | `8080` | HTTP port |
| `POLICY_FILE` | *(built-in defaults)* | Path to a JSON policy file — see `config/policies.example.json` |
| `FAIL_OPEN` | `true` | `true` = allow requests through on a Redis error, `false` = deny them |

## Running it

```bash
docker compose up
```

Starts Redis and the API together (`docker-compose.yml`), with the example policy file loaded. The API is then at `localhost:8080`.

Locally without Docker:

```bash
make run   # builds and runs cmd/api against REDIS_ADDR/PORT from the environment
```

## Using it from a Go service

Instead of calling the HTTP API by hand, wrap your handlers with the middleware:

```go
import "github.com/YoshrithMalhotra/Go-Rate-Limiter/pkg/middleware"

client := middleware.NewClient("http://localhost:8080")

handler := middleware.RateLimit(client,
    middleware.WithPolicy("login"),
    middleware.WithKeyFunc(middleware.IPKey),
)(loginHandler)
```

`middleware.WithLimit` / `middleware.WithWindow` work the same way for an inline limit instead of a named policy, and `middleware.WithCheckFunc` builds multi-dimensional checks per request. The client fails open by default (`middleware.WithFailOpen(false)` to change that) and uses a 150ms timeout so a slow rate limiter can't become a slow app.

## Testing

```bash
make test              # unit tests, in-memory Redis (miniredis)
make test-integration   # real Redis required, build-tagged
```

CI (`.github/workflows/ci.yml`) runs `go test -race`, `golangci-lint`, and a build on every push and PR.

## Project layout

```
cmd/api/            entrypoint — wiring, routes, graceful shutdown
internal/limiter/    sliding window + GCRA, both as atomic Redis Lua scripts
internal/policy/     named policy definitions, loading, validation
internal/handlers/   HTTP handlers for /check and /policies
internal/metrics/    Prometheus-format metrics registry
internal/config/     environment-driven configuration
pkg/middleware/       Go middleware + HTTP client for consuming Governor
```
