package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// stubBouncer stands in for a running GoBouncer service.
func stubBouncer(t *testing.T, status int, body any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/check" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if body != nil {
			_ = json.NewEncoder(w).Encode(body)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("downstream"))
	})
}

func TestRateLimit_AllowedCallsNextHandler(t *testing.T) {
	srv := stubBouncer(t, http.StatusOK, Result{Allowed: true, Remaining: 9})
	client := NewClient(srv.URL)

	rr := httptest.NewRecorder()
	RateLimit(client, WithLimit(10), WithWindow(time.Minute))(okHandler()).
		ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if rr.Body.String() != "downstream" {
		t.Fatalf("expected downstream handler to run, got %q", rr.Body.String())
	}
	if got := rr.Header().Get("X-RateLimit-Remaining"); got != "9" {
		t.Fatalf("expected remaining header 9, got %q", got)
	}
}

func TestRateLimit_DeniedBlocksNextHandler(t *testing.T) {
	srv := stubBouncer(t, http.StatusTooManyRequests, Result{Allowed: false, Remaining: 0, RetryAfter: 2500})
	client := NewClient(srv.URL)

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })

	rr := httptest.NewRecorder()
	RateLimit(client, WithLimit(10), WithWindow(time.Minute))(next).
		ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	if called {
		t.Fatal("downstream handler must not run when the request is denied")
	}
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rr.Code)
	}
	if got := rr.Header().Get("Retry-After"); got != "2" {
		t.Fatalf("expected Retry-After=2, got %q", got)
	}
}

// A denial the service reports with retry_after=0 must still block.
func TestRateLimit_DeniedWithoutRetryAfterStillBlocks(t *testing.T) {
	srv := stubBouncer(t, http.StatusTooManyRequests, Result{Allowed: false, Remaining: 0})
	client := NewClient(srv.URL)

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })

	rr := httptest.NewRecorder()
	RateLimit(client)(next).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	if called {
		t.Fatal("downstream handler must not run when the request is denied")
	}
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rr.Code)
	}
}

func TestClient_ServiceDown_FailOpenAllows(t *testing.T) {
	srv := stubBouncer(t, http.StatusOK, Result{Allowed: true})
	url := srv.URL
	srv.Close() // service is now unreachable

	client := NewClient(url, WithFailOpen(true))
	result, err := client.Check(context.Background(), "user:1", 10, 60000)
	if err != nil {
		t.Fatalf("fail-open client should swallow the error, got %v", err)
	}
	if !result.Allowed {
		t.Fatal("expected fail-open client to allow when the service is down")
	}
}

func TestClient_ServiceDown_FailClosedDenies(t *testing.T) {
	srv := stubBouncer(t, http.StatusOK, Result{Allowed: true})
	url := srv.URL
	srv.Close()

	client := NewClient(url, WithFailOpen(false))
	result, err := client.Check(context.Background(), "user:1", 10, 60000)
	if err == nil {
		t.Fatal("expected fail-closed client to surface the error")
	}
	if result.Allowed {
		t.Fatal("expected fail-closed client to deny when the service is down")
	}
}

// A 404 "Unknown policy" is a plain-text error page, not a decision. Before the
// status check it decoded into a zero-value Result and read as a silent denial.
func TestClient_UnknownPolicyIsAnErrorNotADenial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Unknown policy", http.StatusNotFound)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, WithFailOpen(false))
	_, err := client.CheckPolicy(context.Background(), "user:1", "typo")
	if err == nil {
		t.Fatal("expected an unknown policy to surface as an error")
	}
}

func TestClient_ServerErrorRespectsFailOpen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	open := NewClient(srv.URL, WithFailOpen(true))
	result, err := open.Check(context.Background(), "user:1", 10, 60000)
	if err != nil || !result.Allowed {
		t.Fatalf("fail-open client should allow on 500: allowed=%v err=%v", result.Allowed, err)
	}

	closed := NewClient(srv.URL, WithFailOpen(false))
	result, err = closed.Check(context.Background(), "user:1", 10, 60000)
	if err == nil || result.Allowed {
		t.Fatalf("fail-closed client should deny on 500: allowed=%v err=%v", result.Allowed, err)
	}
}

func TestCheckStatus(t *testing.T) {
	for _, code := range []int{http.StatusOK, http.StatusTooManyRequests} {
		if err := checkStatus(code); err != nil {
			t.Fatalf("status %d should be a valid decision, got %v", code, err)
		}
	}
	for _, code := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		if err := checkStatus(code); err == nil {
			t.Fatalf("status %d should be rejected", code)
		}
	}
}

func TestClient_CheckPolicySendsPolicyAndReadsHeaders(t *testing.T) {
	var got checkRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("X-RateLimit-Limit", "5")
		w.Header().Set("X-RateLimit-Policy", "login")
		_ = json.NewEncoder(w).Encode(Result{Allowed: true, Remaining: 4})
	}))
	defer srv.Close()

	result, err := NewClient(srv.URL).CheckPolicy(context.Background(), "user:1", "login")
	if err != nil {
		t.Fatal(err)
	}
	if got.Policy != "login" || got.Key != "user:1" {
		t.Fatalf("unexpected request body: %+v", got)
	}
	if result.Limit != 5 || result.Policy != "login" {
		t.Fatalf("expected limit/policy read from headers, got %+v", result)
	}
}

func TestRateLimit_MultiDimensionalChecks(t *testing.T) {
	var got checkRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(MultiResult{Allowed: true, Remaining: 3})
	}))
	defer srv.Close()

	handler := RateLimit(NewClient(srv.URL), WithCheckFunc(func(r *http.Request) []Check {
		return []Check{
			PolicyCheck("ip", IPKey(r), "ip-basic"),
			PolicyCheck("user", "user:7", "user-free"),
		}
	}))(okHandler())

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if len(got.Checks) != 2 {
		t.Fatalf("expected 2 dimensions sent, got %d", len(got.Checks))
	}
	if got.Checks[0].Name != "ip" || got.Checks[1].Policy != "user-free" {
		t.Fatalf("unexpected checks: %+v", got.Checks)
	}
}

func TestIPKey(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.9:54321"
	if got := IPKey(r); got != "ip:203.0.113.9" {
		t.Fatalf("expected ip:203.0.113.9, got %q", got)
	}

	r.Header.Set("X-Forwarded-For", "198.51.100.4")
	if got := IPKey(r); got != "ip:198.51.100.4" {
		t.Fatalf("expected X-Forwarded-For to win, got %q", got)
	}
}

func TestHeaderKey_FallsBackToIP(t *testing.T) {
	keyFunc := HeaderKey("X-API-Key")

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.9:1234"
	r.Header.Set("X-API-Key", "abc123")
	if got := keyFunc(r); got != "X-API-Key:abc123" {
		t.Fatalf("expected header key, got %q", got)
	}

	r.Header.Del("X-API-Key")
	if got := keyFunc(r); got != "ip:203.0.113.9" {
		t.Fatalf("expected fallback to IP, got %q", got)
	}
}
