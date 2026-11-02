package dnsofmoney

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
)

// paidHandler serves a 402 on the unpaid leg and okBody on the paid leg.
func paidHandler(t *testing.T, okBody string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(HeaderPayment) == "" && r.Header.Get(HeaderPaymentSignature) == "" {
			writeLiveChallenge(t, w)
			return
		}
		w.Header().Set(HeaderPaymentResponse, settleProofHeader())
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, okBody)
	}
}

const paidResolveBody = `{"success":true,"settled":true,"idempotent":false,"transaction_id":"txn-1",
"resolution":{"alias":"pay:vendor.alpha","status":"resolved","caller_tier":2,"resolution_id":"r","message_id":"m","resolved_at":"t",
"preferred_endpoint":{"rail_type":"xrpl","currency":"XRP","priority":1}},
"proof":{"success":true,"transaction":"TXHASH","network":"eip155:8453","payer":"0xpayer"}}`

const screenBody = `{"success":true,"settled":true,"idempotent":true,"transaction_id":"txn-2",
"attestation":{"credentialSubject":{"screen":{"verdict":"CLEAR"}},"proof":{"type":"DataIntegrityProof"}},
"proof":{"success":true,"transaction":"TXHASH","network":"eip155:8453"}}`

const planBody = `{"success":true,"settled":true,"idempotent":false,"transaction_id":"txn-3","plan":"business",
"plan_expires_at":"2026-12-02T00:00:00+00:00","proof":{"success":true,"transaction":"TXHASH","network":"eip155:8453"}}`

const payBody = `{"success":true,"settled":true,"idempotent":false,
"attestation":{"screen":{"verdict":"CLEAR","payee":{"verdict":"CLEAR"}},"settlement":{"txid":"XTX"}},
"proof":{"success":true,"transaction":"XTX","network":"xrpl:0","payer":"rPayer"}}`

func TestPaidRoutes(t *testing.T) {
	cases := []struct {
		name     string
		key      string
		body     string
		opts     PayOptions
		call     func(c *Client, opts PayOptions) (*PaidResult, error)
		path     string
		check    func(t *testing.T, res *PaidResult)
		wantQ    string
		wantHdr  string
		wantNet  string
		wantIdem string
	}{
		{
			name: "paid resolve", body: paidResolveBody,
			opts: PayOptions{Select: SelectNetwork("eip155:8453", ""), IdempotencyKey: "k-1"},
			call: func(c *Client, o PayOptions) (*PaidResult, error) {
				r, err := c.PaidResolve(context.Background(), "pay:vendor.alpha", o)
				if err != nil {
					return nil, err
				}
				if r.Resolution == nil || r.Resolution.Alias != "pay:vendor.alpha" || r.Resolution.Endpoints()[0].RailName() != "xrpl" {
					t.Errorf("resolution = %+v", r.Resolution)
				}
				return &r.PaidResult, nil
			},
			path: "/api/v1/x402/resolve/pay:vendor.alpha", wantHdr: HeaderPayment, wantNet: "eip155:8453", wantIdem: "k-1",
		},
		{
			name: "screen pinned currency", body: screenBody,
			opts: PayOptions{Currency: "USDC", UsePaymentSignature: true},
			call: func(c *Client, o PayOptions) (*PaidResult, error) {
				r, err := c.Screen(context.Background(), "rawcpsYWnGTFQQGx19F7YguBXPXugA3ng7", o)
				if err != nil {
					return nil, err
				}
				if r.Verdict != "CLEAR" || !r.Summary.Signed || r.Target != "rawcpsYWnGTFQQGx19F7YguBXPXugA3ng7" {
					t.Errorf("screen = %+v", r)
				}
				return &r.PaidResult, nil
			},
			path: "/api/v1/x402/screen/rawcpsYWnGTFQQGx19F7YguBXPXugA3ng7", wantQ: "currency=USDC", wantHdr: HeaderPaymentSignature, wantNet: "xrpl:0",
		},
		{
			name: "buy plan", key: "fas_test", body: planBody,
			opts: PayOptions{Currency: "USDC-BASE", Select: SelectNetwork("eip155:", "")},
			call: func(c *Client, o PayOptions) (*PaidResult, error) {
				r, err := c.BuyPlan(context.Background(), "business", o)
				if err != nil {
					return nil, err
				}
				if r.Plan != "business" || r.PlanExpiresAt == "" {
					t.Errorf("plan = %+v", r)
				}
				return &r.PaidResult, nil
			},
			path: "/api/v1/x402/plan/business", wantQ: "currency=USDC-BASE", wantHdr: HeaderPayment, wantNet: "eip155:8453",
		},
		{
			name: "pay alias", key: "fas_test", body: payBody,
			opts: PayOptions{Currency: "XRP"},
			call: func(c *Client, o PayOptions) (*PaidResult, error) {
				r, err := c.PayAlias(context.Background(), "pay:dnsofmoney", "0.01", o)
				if err != nil {
					return nil, err
				}
				if r.TxHash != "XTX" || r.Payer != "rPayer" || r.Summary.TxID != "XTX" || r.Summary.PayeeVerdict != "CLEAR" {
					t.Errorf("pay = %+v", r)
				}
				return &r.PaidResult, nil
			},
			path: "/api/v1/x402/pay/pay:dnsofmoney", wantQ: "amount=0.01&currency=XRP", wantHdr: HeaderPayment, wantNet: "xrpl:0",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client, rec := newServer(t, paidHandler(t, c.body), WithAPIKey(c.key))
			signer := &staticSigner{header: "SIGNED-HEADER"}
			c.opts.Signer = signer
			res, err := c.call(client, c.opts)
			if err != nil {
				t.Fatal(err)
			}
			reqs := rec.all()
			if len(reqs) != 2 {
				t.Fatalf("requests = %d, want 2", len(reqs))
			}
			for i, r := range reqs {
				if r.Method != http.MethodGet || r.URL.Path != c.path || r.URL.RawQuery != c.wantQ {
					t.Errorf("leg %d = %s %s?%s", i, r.Method, r.URL.Path, r.URL.RawQuery)
				}
				if r.Header.Get("X-API-Key") != c.key {
					t.Errorf("leg %d X-API-Key = %q", i, r.Header.Get("X-API-Key"))
				}
			}
			if reqs[0].Header.Get(HeaderPayment) != "" || reqs[0].Header.Get(HeaderPaymentSignature) != "" {
				t.Error("unpaid leg carried a payment header")
			}
			if reqs[1].Header.Get(c.wantHdr) != "SIGNED-HEADER" {
				t.Errorf("paid leg %s = %q", c.wantHdr, reqs[1].Header.Get(c.wantHdr))
			}
			if reqs[1].Header.Get(HeaderIdempotencyKey) != c.wantIdem {
				t.Errorf("Idempotency-Key = %q", reqs[1].Header.Get(HeaderIdempotencyKey))
			}
			if signer.calls != 1 || signer.got.Network != c.wantNet || res.Accepted.Network != c.wantNet {
				t.Errorf("signed %d times, network %q / %q", signer.calls, signer.got.Network, res.Accepted.Network)
			}
			if !res.Settled || res.PaymentResponse == nil || res.PaymentResponse.State != "settled" || res.Challenge == nil {
				t.Errorf("result = %+v", res)
			}
			if res.Proof["transaction"] == nil {
				t.Error("proof lost")
			}
		})
	}
}

func TestPaidNoSigner(t *testing.T) {
	client, rec := newServer(t, paidHandler(t, paidResolveBody))
	_, err := client.PaidResolve(context.Background(), "pay:dnsofmoney", PayOptions{})
	var pr *PaymentRequiredError
	if !errors.As(err, &pr) || !errors.Is(err, ErrPaymentRequired) || len(pr.Challenge.Accepts) != 5 {
		t.Fatalf("err = %v", err)
	}
	if len(rec.all()) != 1 {
		t.Fatal("no Signer must stop after the challenge")
	}
}

func TestPaidRejectedPayment(t *testing.T) {
	client, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(HeaderPayment) == "" {
			writeLiveChallenge(t, w)
			return
		}
		writeJSON(w, http.StatusPaymentRequired, map[string]any{
			"x402Version": 2,
			"accepts":     []any{map[string]any{"scheme": "exact", "network": "xrpl:0", "payTo": "r", "asset": "XRP", "amount": "1", "resource": "u"}},
			"error":       "transaction not found on ledger",
		})
	})
	_, err := client.Screen(context.Background(), "pay:x", PayOptions{Signer: &staticSigner{header: "H"}})
	var pr *PaymentRequiredError
	if !errors.As(err, &pr) || pr.Challenge.Error != "transaction not found on ledger" || pr.Message != pr.Challenge.Error {
		t.Fatalf("err = %v", err)
	}
}

func TestPaidSignerFailures(t *testing.T) {
	cases := []struct {
		name   string
		signer Signer
		sel    Selector
	}{
		{"signer error", SignerFunc(func(context.Context, PaymentRequirements, *Challenge) (string, error) {
			return "", errors.New("hardware wallet declined")
		}), nil},
		{"empty header", &staticSigner{header: ""}, nil},
		{"no matching accept", &staticSigner{header: "H"}, SelectNetwork("cosmos:", "")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client, rec := newServer(t, paidHandler(t, paidResolveBody))
			_, err := client.PaidResolve(context.Background(), "pay:x", PayOptions{Signer: c.signer, Select: c.sel})
			var xe *X402PayError
			if !errors.As(err, &xe) {
				t.Fatalf("err = %v, want *X402PayError", err)
			}
			if len(rec.all()) != 1 {
				t.Fatal("a failed signature must not send a settle leg")
			}
		})
	}
}

func TestPaidErrorsBeforeChallenge(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		is     error
		code   string
	}{
		{"alias not resolved", 404, `{"detail":{"error":"X402_RESOLVE_NOT_RESOLVED","status":"not_found"}}`, ErrAliasNotFound, CodeX402ResolveNotResolved},
		{"route dark", 404, `{"detail":"x402 paid resolve is not enabled"}`, ErrAliasNotFound, ""},
		{"unpriced", 400, `{"detail":{"error":"X402_RESOLVE_UNPRICED","message":"no price"}}`, nil, CodeX402ResolveUnpriced},
		{"rate limited", 429, `{"error":"rate_limit_exceeded","limit":60}`, ErrRateLimited, CodeRateLimitExceeded},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.status)
				_, _ = io.WriteString(w, c.body)
			})
			signer := &staticSigner{header: "H"}
			_, err := client.PaidResolve(context.Background(), "pay:x", PayOptions{Signer: signer})
			if c.is != nil && !errors.Is(err, c.is) {
				t.Errorf("errors.Is(%v, %v) = false", err, c.is)
			}
			var e *APIError
			if !errors.As(err, &e) || e.Code != c.code {
				t.Errorf("err = %v", err)
			}
			if signer.calls != 0 {
				t.Error("signed without a challenge")
			}
		})
	}
}

func TestBuyPlanNeedsKey(t *testing.T) {
	client, rec := newServer(t, paidHandler(t, planBody))
	_, err := client.BuyPlan(context.Background(), "business", PayOptions{Signer: &staticSigner{header: "H"}})
	if !errors.Is(err, ErrAuthentication) {
		t.Fatalf("err = %v", err)
	}
	if len(rec.all()) != 0 {
		t.Fatal("BuyPlan without a key must not call the network")
	}
}

func TestBuyPlanAlreadyHigher(t *testing.T) {
	client, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 409, map[string]any{"detail": map[string]any{"error": CodeX402PlanAlreadyHigher, "plan": "enterprise"}})
	}, WithAPIKey("k"))
	_, err := client.BuyPlan(context.Background(), "business", PayOptions{Signer: &staticSigner{header: "H"}})
	var e *APIError
	if !errors.As(err, &e) || e.Code != CodeX402PlanAlreadyHigher || e.Detail["plan"] != "enterprise" {
		t.Fatalf("err = %v", err)
	}
}

func TestPaidNoPaymentNeeded(t *testing.T) {
	client, rec := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, screenBody)
	})
	res, err := client.Screen(context.Background(), "pay:x", PayOptions{Signer: &staticSigner{header: "H"}})
	if err != nil || res.Verdict != "CLEAR" || res.Challenge != nil || len(rec.all()) != 1 {
		t.Fatalf("res = %+v err = %v", res, err)
	}
}

func TestSettleLegRetriesOn503(t *testing.T) {
	paid := 0
	client, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(HeaderPayment) == "" {
			writeLiveChallenge(t, w)
			return
		}
		paid++
		if paid == 1 {
			w.WriteHeader(503)
			return
		}
		_, _ = io.WriteString(w, screenBody)
	})
	if _, err := client.Screen(context.Background(), "pay:x", PayOptions{Signer: &staticSigner{header: "H"}}); err != nil {
		t.Fatal(err)
	}
	if paid != 2 {
		t.Fatalf("settle attempts = %d, want 2", paid)
	}
}

func TestFetchChallenge(t *testing.T) {
	client, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok" {
			_, _ = io.WriteString(w, "{}")
			return
		}
		writeLiveChallenge(t, w)
	})
	ch, err := client.FetchChallenge(context.Background(), "/api/v1/x402/screen/pay:x", nil)
	if err != nil || len(ch.Accepts) != 5 {
		t.Fatalf("ch = %v err = %v", ch, err)
	}
	if _, err := client.FetchChallenge(context.Background(), "/ok", nil); err == nil {
		t.Fatal("200: want error")
	}
}

func TestScreenWithPaymentHeader(t *testing.T) {
	client, rec := newServer(t, paidHandler(t, screenBody), WithAPIKey("k"))
	res, err := client.ScreenWithPaymentHeader(context.Background(), "pay:x", "PREBUILT", PayOptions{Currency: "USDC", IdempotencyKey: "idem"})
	if err != nil || res.Verdict != "CLEAR" || !res.Idempotent {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	r := rec.all()[0]
	if r.Header.Get(HeaderPayment) != "PREBUILT" || r.URL.RawQuery != "currency=USDC" || r.Header.Get(HeaderIdempotencyKey) != "idem" {
		t.Fatalf("request = %v %v", r.URL, r.Header)
	}
}

func TestAttestSettledPayment(t *testing.T) {
	client, rec := newServer(t, paidHandler(t, payBody), WithAPIKey("k"))
	res, err := client.AttestSettledPayment(context.Background(), "pay:dnsofmoney", "0.10", "XTX", "rPayer", PayOptions{})
	if err != nil || res.TxHash != "XTX" {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	r := rec.all()[0]
	if r.URL.RawQuery != "amount=0.10&currency=XRP" || r.Header.Get(HeaderPayment) != BuildXPaymentHeader("XTX", "rPayer") {
		t.Fatalf("request = %v %v", r.URL, r.Header)
	}
}

func TestPaidEmptyArgs(t *testing.T) {
	c := NewClient(WithAPIKey("k"))
	ctx := context.Background()
	if _, err := c.PaidResolve(ctx, "", PayOptions{}); err == nil {
		t.Error("PaidResolve")
	}
	if _, err := c.Screen(ctx, "", PayOptions{}); err == nil {
		t.Error("Screen")
	}
	if _, err := c.BuyPlan(ctx, "", PayOptions{}); err == nil {
		t.Error("BuyPlan")
	}
	if _, err := c.PayAlias(ctx, "", "", PayOptions{}); err == nil {
		t.Error("PayAlias")
	}
}
