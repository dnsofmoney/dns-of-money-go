package dnsofmoney

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder keeps the requests a test server received.
type recorder struct {
	mu   sync.Mutex
	reqs []*http.Request
}

func (r *recorder) add(req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req.Clone(req.Context()))
}

func (r *recorder) all() []*http.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*http.Request(nil), r.reqs...)
}

// newServer starts an httptest server and a Client for it, with fast retries.
func newServer(t *testing.T, h http.HandlerFunc, opts ...Option) (*Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	all := append([]Option{WithBaseURL(srv.URL + "/"), WithRetries(DefaultRetries, time.Millisecond)}, opts...)
	return NewClient(all...), rec
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func b64JSON(v any) string {
	b, _ := json.Marshal(v)
	return base64.StdEncoding.EncodeToString(b)
}

// liveChallenge is a 402 recorded from production on 2026-11-02:
// GET https://api.dnsofmoney.com/api/v1/x402/resolve/pay:dnsofmoney.
// Five accepts (XRPL, Algorand, Base, X Layer, Solana). Public addresses only.
func liveChallenge(t *testing.T) (body []byte, header string) {
	t.Helper()
	return readFile(t, "testdata/live_resolve_402_body.json"),
		strings.TrimSpace(string(readFile(t, "testdata/live_resolve_402_header.txt")))
}

func writeLiveChallenge(t *testing.T, w http.ResponseWriter) {
	body, header := liveChallenge(t)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(HeaderPaymentRequired, header)
	w.WriteHeader(http.StatusPaymentRequired)
	_, _ = w.Write(body)
}

func settleProofHeader() string {
	return b64JSON(map[string]any{
		"success":     true,
		"transaction": "TXHASH",
		"network":     "eip155:8453",
		"payer":       "0xpayer",
		"requestKey":  "k-1",
		"state":       "settled",
	})
}

// staticSigner returns a fixed header and records the accept it signed.
type staticSigner struct {
	header string
	got    PaymentRequirements
	calls  int
}

func (s *staticSigner) SignPayment(_ context.Context, req PaymentRequirements, _ *Challenge) (string, error) {
	s.calls++
	s.got = req
	return s.header, nil
}
