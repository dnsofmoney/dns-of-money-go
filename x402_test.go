package dnsofmoney

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestParseLiveChallenge(t *testing.T) {
	body, header := liveChallenge(t)
	resp := &http.Response{StatusCode: 402, Header: http.Header{}}
	resp.Header.Set(HeaderPaymentRequired, header)
	ch, err := ParseChallenge(resp, body)
	if err != nil {
		t.Fatal(err)
	}
	if ch.X402Version != 2 || len(ch.Accepts) != 5 || ch.Header != header {
		t.Fatalf("challenge = v%d %d accepts", ch.X402Version, len(ch.Accepts))
	}
	wantNetworks := []string{"xrpl:0", "algorand:wGHE2Pwdvd7S12BL5FaOP20EGYesN73ktiC1qzkkit8=", "eip155:8453", "eip155:196", "solana:5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp"}
	for i, n := range wantNetworks {
		a := ch.Accepts[i]
		if a.Network != n || a.Scheme != SchemeExact || a.AtomicAmount() != "10000" || a.PayTo == "" || len(a.Raw) == 0 {
			t.Errorf("accept %d = %+v", i, a)
		}
		if a.Resource.URL != "https://api.dnsofmoney.com/api/v1/x402/resolve/pay:dnsofmoney" {
			t.Errorf("accept %d resource = %q", i, a.Resource.URL)
		}
	}
	if got := ch.Accepts[0].InvoiceIDValue(); got != "61c96b31238bb2cc171d51309bdce26c" {
		t.Errorf("invoice = %q", got)
	}
	if _, ok := ch.Accepts[0].SourceTagValue(); ok {
		t.Error("live XRPL accept has no sourceTag")
	}
	if ch.Accepts[4].Extra["feePayer"] == "" {
		t.Error("solana accept lost extra.feePayer")
	}
}

func TestDecodeLiveHeader(t *testing.T) {
	_, header := liveChallenge(t)
	pr, err := DecodePaymentRequiredHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	// XRPL header = bare fields merged with the v2 envelope; resource is an object.
	if pr.PayTo != "rawcpsYWnGTFQQGx19F7YguBXPXugA3ng7" || pr.MaxAmountRequired != "10000" ||
		pr.Resource.URL == "" || pr.X402Version != 2 || len(pr.Accepts) != 1 || pr.Accepts[0].Amount != "10000" {
		t.Fatalf("decoded = %+v", pr)
	}
	m, err := DecodePaymentRequired(header)
	if err != nil || m["payTo"] != "rawcpsYWnGTFQQGx19F7YguBXPXugA3ng7" {
		t.Fatalf("map = %v, %v", m, err)
	}
	if _, err := DecodePaymentRequired("%%%"); err == nil {
		t.Error("bad base64: want error")
	}
}

func TestParseChallengeHeaderFallback(t *testing.T) {
	cases := []struct {
		name    string
		header  map[string]any
		wantNet string
		wantErr bool
	}{
		{"v2 envelope", map[string]any{"x402Version": 2, "accepts": []any{map[string]any{"scheme": "exact", "network": "eip155:8453", "payTo": "0xabc", "asset": "0xusdc", "amount": "10000"}}, "resource": map[string]any{"url": "u"}}, "eip155:8453", false},
		{"bare v1", map[string]any{"scheme": "exact", "network": "xrpl:0", "payTo": "rX", "asset": "XRP", "maxAmountRequired": "5"}, "xrpl:0", false},
		{"empty", map[string]any{"x402Version": 2}, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: 402, Header: http.Header{}}
			resp.Header.Set(HeaderPaymentRequired, b64JSON(c.header))
			ch, err := ParseChallenge(resp, nil)
			if c.wantErr {
				if err == nil {
					t.Fatal("want error")
				}
				return
			}
			if err != nil || ch.Accepts[0].Network != c.wantNet {
				t.Fatalf("ch = %+v, %v", ch, err)
			}
		})
	}
	if _, err := ParseChallenge(&http.Response{StatusCode: 200}, nil); err == nil {
		t.Error("status 200: want error")
	}
}

func TestSelectNetwork(t *testing.T) {
	body, _ := liveChallenge(t)
	var ch Challenge
	if err := json.Unmarshal(body, &ch); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		network, asset, want string
		wantErr              bool
	}{
		{"eip155:8453", "", "eip155:8453", false},
		{"eip155:", "", "eip155:8453", false},
		{"solana:", "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v", "solana:5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp", false},
		{"xrpl:0", "xrp", "xrpl:0", false},
		{"xrpl:0", "RLUSD", "", true},
		{"eip155", "", "", true}, // no colon: exact match only
	}
	for _, c := range cases {
		got, err := SelectNetwork(c.network, c.asset)(ch.Accepts)
		if (err != nil) != c.wantErr || (!c.wantErr && got.Network != c.want) {
			t.Errorf("SelectNetwork(%q, %q) = %q, %v", c.network, c.asset, got.Network, err)
		}
	}
	if _, err := FirstAccept(nil); err == nil {
		t.Error("FirstAccept(nil): want error")
	}
}

func TestBuildXPaymentHeaderShape(t *testing.T) {
	raw, _ := base64.StdEncoding.DecodeString(BuildXPaymentHeader("ABC", "rPayer"))
	if string(raw) != `{"x402Version": 2, "payload": {"txHash": "ABC", "payer": "rPayer"}}` {
		t.Fatalf("header JSON = %s", raw)
	}
	raw, _ = base64.StdEncoding.DecodeString(BuildXPaymentHeader("ABC", ""))
	if string(raw) != `{"x402Version": 2, "payload": {"txHash": "ABC"}}` {
		t.Fatalf("header JSON = %s", raw)
	}
}

func TestBuildXPaymentHeaderAccepted(t *testing.T) {
	body, _ := liveChallenge(t)
	var ch Challenge
	_ = json.Unmarshal(body, &ch)
	h, err := BuildXPaymentHeaderAccepted(ch.Accepts[0], "TX", "rP")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(h)
	var got struct {
		X402Version int            `json:"x402Version"`
		Network     string         `json:"network"`
		Accepted    map[string]any `json:"accepted"`
		Payload     map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	extra, _ := got.Accepted["extra"].(map[string]any)
	if got.X402Version != 2 || got.Network != "xrpl:0" || got.Accepted["network"] != "xrpl:0" ||
		got.Accepted["amount"] != "10000" || extra["invoiceId"] != "61c96b31238bb2cc171d51309bdce26c" ||
		got.Payload["txHash"] != "TX" || got.Payload["payer"] != "rP" {
		t.Fatalf("decoded = %+v", got)
	}
}

func TestXRPLTxHashSigner(t *testing.T) {
	body, _ := liveChallenge(t)
	var ch Challenge
	_ = json.Unmarshal(body, &ch)
	var seen PaymentRequirements
	s := XRPLTxHashSigner(func(_ context.Context, req PaymentRequirements) (string, string, error) {
		seen = req
		return "TX1", "rPayer", nil
	})
	h, err := s.SignPayment(context.Background(), ch.Accepts[0], &ch)
	if err != nil || h == "" || seen.PayTo != ch.Accepts[0].PayTo {
		t.Fatalf("h = %q err = %v", h, err)
	}
	fail := XRPLTxHashSigner(func(context.Context, PaymentRequirements) (string, string, error) {
		return "", "", errors.New("tecUNFUNDED")
	})
	if _, err := fail.SignPayment(context.Background(), ch.Accepts[0], &ch); err == nil {
		t.Fatal("want error")
	}
}

func TestDecodePaymentResponse(t *testing.T) {
	p, err := DecodePaymentResponse(settleProofHeader())
	if err != nil || !p.Success || p.Transaction != "TXHASH" || p.State != "settled" || p.RequestKey != "k-1" {
		t.Fatalf("proof = %+v, %v", p, err)
	}
	// Unpadded base64 also decodes.
	unpadded := strings.TrimRight(settleProofHeader(), "=")
	if _, err := DecodePaymentResponse(unpadded); err != nil {
		t.Fatalf("unpadded: %v", err)
	}
}

func TestSummarizeAttestation(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want AttestationSummary
	}{
		{"signed VC", `{"credentialSubject":{"screen":{"verdict":"CLEAR","payee":{"verdict":"CLEAR"},"payer":{"verdict":"REVIEW"}},"settlement":{"txid":"T"}},"proof":{"type":"DataIntegrityProof"}}`,
			AttestationSummary{Verdict: "CLEAR", PayeeVerdict: "CLEAR", PayerVerdict: "REVIEW", TxID: "T", Signed: true}},
		{"unsigned flat", `{"screen":{"verdict":"BLOCKED"}}`, AttestationSummary{Verdict: "BLOCKED"}},
		{"empty proof is unsigned", `{"screen":{"verdict":"CLEAR"},"proof":{}}`, AttestationSummary{Verdict: "CLEAR"}},
		{"empty", `{}`, AttestationSummary{}},
	}
	for _, c := range cases {
		var m map[string]any
		_ = json.Unmarshal([]byte(c.in), &m)
		if got := SummarizeAttestation(m); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestResourceJSON(t *testing.T) {
	var r Resource
	for _, in := range []string{`"https://x"`, `{"url":"https://x"}`} {
		if err := json.Unmarshal([]byte(in), &r); err != nil || r.URL != "https://x" {
			t.Errorf("%s -> %+v, %v", in, r, err)
		}
	}
	if err := json.Unmarshal([]byte(`5`), &r); err == nil {
		t.Error("number: want error")
	}
	b, _ := json.Marshal(r)
	if !bytes.Equal(b, []byte(`"https://x"`)) {
		t.Errorf("marshal = %s", b)
	}
}

func TestSourceTagValue(t *testing.T) {
	top := int64(7)
	cases := []struct {
		name string
		p    PaymentRequirements
		want int64
		ok   bool
	}{
		{"extra float", PaymentRequirements{Extra: map[string]any{"sourceTag": float64(804681468)}}, 804681468, true},
		{"extra number", PaymentRequirements{Extra: map[string]any{"sourceTag": json.Number("9")}}, 9, true},
		{"top level", PaymentRequirements{SourceTag: &top}, 7, true},
		{"extra wins", PaymentRequirements{Extra: map[string]any{"sourceTag": float64(1)}, SourceTag: &top}, 1, true},
		{"none", PaymentRequirements{}, 0, false},
	}
	for _, c := range cases {
		got, ok := c.p.SourceTagValue()
		if got != c.want || ok != c.ok {
			t.Errorf("%s: %d %v", c.name, got, ok)
		}
	}
	p := PaymentRequirements{InvoiceID: "top"}
	if p.InvoiceIDValue() != "top" {
		t.Error("invoice fallback to top level")
	}
}
