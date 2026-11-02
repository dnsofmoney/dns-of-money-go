package dnsofmoney

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

// Tier-0 body recorded from production on 2026-11-02 (GET /resolve/pay:dnsofmoney).
const liveTier0 = `{"success":true,"data":{"resolution_id":"53e19f11-2a65-4caf-928d-7a5f662a40bb","message_id":"FAS-20261102172006-1902B900","alias":"pay:dnsofmoney","status":"resolved","resolved_at":"2026-11-02T17:20:06.224298Z","caller_tier":0},"error_code":null,"message":null,"timestamp":"2026-11-02T17:20:06.660451Z"}`

const tier2Data = `{"resolution_id":"r2","message_id":"m2","alias":"pay:vendor.alpha","status":"resolved",
"resolved_at":"2026-11-02T00:00:00Z","caller_tier":2,"display_name":"Vendor Alpha","entity_type":"business",
"preferred_rail":"xrpl","entity_id":"e1",
"preferred_endpoint":{"endpoint_id":"ep1","rail_type":"xrpl","currency":"XRP","priority":1,"fee_estimate":"0.00001","is_active":true},
"fallback_endpoints":[{"endpoint_id":"ep2","rail_type":"fednow","currency":"USD","priority":2}],
"compliance":{"screened":true,"result":"PASS","provider":"ofac_sdn","cached":false},
"identity":{"nft_token_id":"nft1","generation":3,"tier":"founding"},"x_handle":"@vendor","cache_hit":true,"resolved_from":"cache"}`

func TestResolveTiers(t *testing.T) {
	cases := []struct {
		name      string
		key       string
		body      string
		wantTier  int
		wantEPs   int
		wantRails []string
	}{
		{"tier0 live", "", liveTier0, 0, 0, nil},
		{"tier2 envelope", "fas_test", `{"success":true,"data":` + tier2Data + `}`, 2, 2, []string{"xrpl", "fednow"}},
		{"tier2 bare body", "fas_test", tier2Data, 2, 2, []string{"xrpl", "fednow"}},
		{"legacy endpoints", "fas_test", `{"alias":"pay:old","status":"resolved","caller_tier":1,"endpoints":[{"rail":"ach","currency":"USD"}]}`, 1, 1, []string{"ach"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client, rec := newServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-RateLimit-Limit", "60")
				w.Header().Set("X-RateLimit-Remaining", "58")
				w.Header().Set("X-RateLimit-Reset", "1793640066")
				w.Header().Set("X-Resolved-From", "origin")
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, c.body)
			}, WithAPIKey(c.key))
			got, err := client.Resolve(context.Background(), "pay:vendor.alpha")
			if err != nil {
				t.Fatal(err)
			}
			req := rec.all()[0]
			if req.Method != http.MethodGet || req.URL.Path != "/resolve/pay:vendor.alpha" {
				t.Fatalf("request = %s %s", req.Method, req.URL.Path)
			}
			if req.Header.Get("X-API-Key") != c.key {
				t.Fatalf("X-API-Key = %q, want %q", req.Header.Get("X-API-Key"), c.key)
			}
			if !got.Resolved() || got.CallerTier == nil || *got.CallerTier != c.wantTier {
				t.Fatalf("status %q tier %v", got.Status, got.CallerTier)
			}
			eps := got.Endpoints()
			if len(eps) != c.wantEPs {
				t.Fatalf("endpoints = %d, want %d", len(eps), c.wantEPs)
			}
			for i, rail := range c.wantRails {
				if eps[i].RailName() != rail {
					t.Errorf("endpoint %d rail = %q, want %q", i, eps[i].RailName(), rail)
				}
			}
			m := got.Meta
			if m.StatusCode != 200 || m.RateLimit == nil || m.RateLimit.Limit != 60 || m.RateLimit.Remaining != 58 ||
				m.RateLimit.Reset.Unix() != 1793640066 || m.ResolvedFrom != "origin" {
				t.Fatalf("meta = %+v rate %+v", m, m.RateLimit)
			}
			if m.Quota != nil {
				t.Fatal("quota meta must be nil without X-Quota headers")
			}
			if len(got.Raw) == 0 {
				t.Fatal("Raw is empty")
			}
		})
	}
}

func TestResolveTier2Fields(t *testing.T) {
	client, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"success":true,"data":`+tier2Data+`}`)
	})
	got, err := client.Resolve(context.Background(), "pay:vendor.alpha")
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != "Vendor Alpha" || got.Compliance == nil || got.Compliance.Result != "PASS" ||
		got.Identity == nil || got.Identity.Generation != 3 || got.XHandle != "@vendor" || !got.CacheHit ||
		got.PreferredEndpoint.IsActive == nil || !*got.PreferredEndpoint.IsActive {
		t.Fatalf("decoded = %+v", got)
	}
}

func TestResolveQuotaHeaders(t *testing.T) {
	client, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Quota-Limit", "1000")
		w.Header().Set("X-Quota-Remaining", "999")
		w.Header().Set("X-Quota-Reset", "1796083200")
		_, _ = io.WriteString(w, liveTier0)
	}, WithAPIKey("k"))
	got, err := client.Resolve(context.Background(), "pay:dnsofmoney")
	if err != nil {
		t.Fatal(err)
	}
	q := got.Meta.Quota
	if q == nil || q.Limit != 1000 || q.Remaining != 999 || !q.Reset.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("quota = %+v", q)
	}
}

func TestResolveWithOptionsPOST(t *testing.T) {
	client, rec := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"success":true,"data":`+tier2Data+`}`)
	})
	_, err := client.ResolveWithOptions(context.Background(), ResolveRequest{AliasName: "pay:vendor.alpha", Amount: "2.50", Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	req := rec.all()[0]
	if req.Method != http.MethodPost || req.URL.Path != "/resolve" || req.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("request = %s %s %s", req.Method, req.URL.Path, req.Header.Get("Content-Type"))
	}
}

func TestResolveWithOptionsBody(t *testing.T) {
	var body map[string]any
	client, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, liveTier0)
	})
	if _, err := client.ResolveWithOptions(context.Background(), ResolveRequest{AliasName: "pay:a.b", Amount: "1"}); err != nil {
		t.Fatal(err)
	}
	if body["alias_name"] != "pay:a.b" || body["amount"] != "1" || body["currency"] != nil {
		t.Fatalf("body = %v", body)
	}
}

// Production answers POST /resolve with an nginx 301 to /resolve/ (checked
// 2026-11-02). net/http would resend it as a GET with no body. The Client
// reports that instead of returning a wrong answer.
func TestResolveWithOptionsRedirectGuard(t *testing.T) {
	client, rec := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/resolve" {
			http.Redirect(w, r, "/resolve/", http.StatusMovedPermanently)
			return
		}
		_, _ = io.WriteString(w, liveTier0)
	})
	_, err := client.ResolveWithOptions(context.Background(), ResolveRequest{AliasName: "pay:a"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if n := len(rec.all()); n != 2 {
		t.Fatalf("requests = %d, want 2", n)
	}
}

func TestResolveSuccessFalse(t *testing.T) {
	client, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"success":false,"data":null,"error_code":"ALIAS_INVALID","message":"bad alias"}`)
	})
	_, err := client.Resolve(context.Background(), "pay:x")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "ALIAS_INVALID" || apiErr.Message != "bad alias" {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveEmptyAlias(t *testing.T) {
	c := NewClient()
	if _, err := c.Resolve(context.Background(), ""); err == nil {
		t.Fatal("want error")
	}
	if _, err := c.ResolveWithOptions(context.Background(), ResolveRequest{}); err == nil {
		t.Fatal("want error")
	}
}
