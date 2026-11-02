package dnsofmoney

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		header   map[string]string
		body     string
		sentinel error
		code     string
		msg      string
		check    func(t *testing.T, err error)
	}{
		{
			name: "401", status: 401, body: `{"detail":"Invalid API key"}`,
			sentinel: ErrAuthentication, msg: "Invalid API key",
		},
		{
			name: "404 detail string", status: 404, body: `{"detail":"Not Found"}`,
			sentinel: ErrAliasNotFound, msg: "Not Found",
		},
		{
			name: "404 x402 code", status: 404, body: `{"detail":{"error":"X402_RESOLVE_NOT_RESOLVED","status":"not_found"}}`,
			sentinel: ErrAliasNotFound, code: CodeX402ResolveNotResolved,
		},
		{
			name: "409 cap", status: 409, body: `{"error_code":"CAP_EXCEEDED","message":"cap"}`,
			sentinel: ErrCapReached, code: CodeCapExceeded,
		},
		{
			name: "409 taken", status: 409, body: `{"detail":"alias taken"}`,
			sentinel: ErrAliasTaken,
		},
		{
			name: "409 plan already higher", status: 409, body: `{"detail":{"error":"X402_PLAN_ALREADY_HIGHER","plan":"enterprise"}}`,
			code: CodeX402PlanAlreadyHigher,
			check: func(t *testing.T, err error) {
				if errors.Is(err, ErrAliasTaken) {
					t.Error("plan conflict must not match ErrAliasTaken")
				}
			},
		},
		{
			name: "400 not payable", status: 400, body: `{"detail":{"error":"X402_NOT_PAYABLE","message":"no XRPL address"}}`,
			code: CodeX402NotPayable, msg: "no XRPL address",
		},
		{
			name: "400 bad request key", status: 400, body: `{"detail":{"error":"X402_INVALID_REQUEST_KEY","message":"too short"}}`,
			code: CodeX402InvalidRequestKey,
		},
		{
			name: "503 unsignable", status: 503, body: `{"detail":{"error":"X402_ATTESTATION_UNSIGNABLE","message":"retry later"}}`,
			code: CodeX402AttestationUnsignable,
		},
		{
			name: "500 grant failed", status: 500, body: `{"detail":{"error":"X402_PLAN_GRANT_FAILED","transaction_id":"t9"}}`,
			code: CodeX402PlanGrantFailed,
			check: func(t *testing.T, err error) {
				var e *APIError
				if !errors.As(err, &e) || e.Detail["transaction_id"] != "t9" {
					t.Errorf("detail = %v", e)
				}
			},
		},
		{
			name: "429", status: 429,
			header:   map[string]string{"Retry-After": "17", "X-RateLimit-Limit": "60", "X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1793640066"},
			body:     `{"error":"rate_limit_exceeded","limit":60,"reset_at":"2026-11-02T17:21:06+00:00"}`,
			sentinel: ErrRateLimited, code: CodeRateLimitExceeded,
			check: func(t *testing.T, err error) {
				var e *RateLimitError
				if !errors.As(err, &e) {
					t.Fatalf("err %T, want *RateLimitError", err)
				}
				if e.RetryAfter != 17*time.Second || e.Limit != 60 || e.ResetAt == "" || e.Meta.RateLimit.Remaining != 0 {
					t.Errorf("rate error = %+v", e)
				}
			},
		},
		{
			name: "402 quota", status: 402,
			header: map[string]string{"X-Quota-Limit": "1000", "X-Quota-Remaining": "0", "X-Quota-Reset": "1796083200"},
			body: `{"error":"quota_exceeded","plan":"developer","limit":1000,"used":1001,"reset_at":"2026-12-01T00:00:00+00:00",` +
				`"pay_per_call":"/api/v1/x402/resolve/{alias_name}","upgrade":"/api/v1/x402/plan/business"}`,
			sentinel: ErrQuotaExceeded, code: CodeQuotaExceeded,
			check: func(t *testing.T, err error) {
				var e *QuotaExceededError
				if !errors.As(err, &e) {
					t.Fatalf("err %T, want *QuotaExceededError", err)
				}
				if e.Plan != "developer" || e.Limit != 1000 || e.Used != 1001 || e.Upgrade != "/api/v1/x402/plan/business" ||
					e.PayPerCall != "/api/v1/x402/resolve/{alias_name}" || e.Meta.Quota == nil || e.Meta.Quota.Remaining != 0 {
					t.Errorf("quota error = %+v", e)
				}
				if errors.Is(err, ErrPaymentRequired) {
					t.Error("quota 402 must not match ErrPaymentRequired")
				}
			},
		},
		{
			name: "502 plain", status: 502, body: `<html>bad gateway</html>`,
			check: func(t *testing.T, err error) {
				var e *APIError
				if !errors.As(err, &e) || e.StatusCode != 502 || e.Body == "" {
					t.Errorf("err = %v", err)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range c.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(c.status)
				_, _ = io.WriteString(w, c.body)
			}, WithRetries(0, 0))
			_, err := client.Resolve(context.Background(), "pay:x")
			if err == nil {
				t.Fatal("want error")
			}
			if c.sentinel != nil && !errors.Is(err, c.sentinel) {
				t.Errorf("errors.Is(%v, %v) = false", err, c.sentinel)
			}
			var e *APIError
			if !errors.As(err, &e) {
				t.Fatalf("err %T is not an *APIError", err)
			}
			if e.StatusCode != c.status {
				t.Errorf("status = %d", e.StatusCode)
			}
			if c.code != "" && e.Code != c.code {
				t.Errorf("code = %q, want %q", e.Code, c.code)
			}
			if c.msg != "" && e.Message != c.msg {
				t.Errorf("message = %q, want %q", e.Message, c.msg)
			}
			if c.check != nil {
				c.check(t, err)
			}
		})
	}
}

func TestRetries(t *testing.T) {
	cases := []struct {
		name      string
		statuses  []int
		retries   int
		wantCalls int32
		wantOK    bool
	}{
		{"503 then 200", []int{503, 200}, 2, 2, true},
		{"502 504 200", []int{502, 504, 200}, 2, 3, true},
		{"gives up after retries", []int{503, 503, 503, 200}, 2, 3, false},
		{"500 is not retried", []int{500, 200}, 2, 1, false},
		{"429 is not retried", []int{429, 200}, 2, 1, false},
		{"no retries", []int{503, 200}, 0, 1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var n atomic.Int32
			client, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
				i := n.Add(1) - 1
				w.WriteHeader(c.statuses[i])
				_, _ = io.WriteString(w, liveTier0)
			}, WithRetries(c.retries, time.Millisecond))
			_, err := client.Resolve(context.Background(), "pay:x")
			if (err == nil) != c.wantOK {
				t.Fatalf("err = %v, want ok=%v", err, c.wantOK)
			}
			if n.Load() != c.wantCalls {
				t.Fatalf("calls = %d, want %d", n.Load(), c.wantCalls)
			}
		})
	}
}

func TestPOSTNeverRetried(t *testing.T) {
	var n atomic.Int32
	client, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.WriteHeader(503)
	})
	if _, err := client.ResolveWithOptions(context.Background(), ResolveRequest{AliasName: "pay:x"}); err == nil {
		t.Fatal("want error")
	}
	if n.Load() != 1 {
		t.Fatalf("calls = %d, want 1", n.Load())
	}
}

func TestRetryStopsOnCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		cancel()
		w.WriteHeader(503)
	}, WithRetries(5, time.Hour))
	done := make(chan error, 1)
	go func() {
		_, err := client.Resolve(ctx, "pay:x")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retry loop ignored the canceled context")
	}
}

func TestX402PayErrorUnwrap(t *testing.T) {
	inner := errors.New("wallet locked")
	err := &X402PayError{Msg: "signer failed", Err: inner}
	if !errors.Is(err, inner) || err.Error() != "dnsofmoney: x402: signer failed: wallet locked" {
		t.Fatalf("err = %v", err)
	}
}
