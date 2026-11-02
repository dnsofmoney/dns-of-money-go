package dnsofmoney

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// PayOptions controls one paid call.
type PayOptions struct {
	// Signer pays the selected accept. Without a Signer the call stops at the
	// challenge and returns a *PaymentRequiredError that carries it.
	Signer Signer
	// Select chooses the accept to pay. The default is FirstAccept.
	Select Selector
	// Currency pins ?currency= on both legs (for example "USDC", "USDC-BASE",
	// "XRP"). Empty lets the server advertise every rail it accepts.
	Currency string
	// IdempotencyKey is sent as Idempotency-Key on the settle leg. The server
	// echoes it as requestKey in the proof. It needs at least 128 bits of entropy.
	IdempotencyKey string
	// UsePaymentSignature sends the proof in PAYMENT-SIGNATURE (x402 v2)
	// instead of X-PAYMENT. The server reads both.
	UsePaymentSignature bool
}

// PaidResult is the common part of a settled paid call.
type PaidResult struct {
	Settled       bool
	Idempotent    bool
	TransactionID string
	// Proof is the "proof" object of the body.
	Proof map[string]any
	// PaymentResponse is the decoded PAYMENT-RESPONSE header, or nil.
	PaymentResponse *SettlementProof
	// Accepted is the requirement that was paid. Zero when no payment was needed.
	Accepted PaymentRequirements
	// Challenge is the 402 that was paid, or nil.
	Challenge *Challenge
	// Body is the full decoded response body.
	Body map[string]any
	Meta ResponseMeta
}

// PaidResolveResult is the result of PaidResolve.
type PaidResolveResult struct {
	PaidResult
	Resolution *ResolutionResponse
}

// ScreenResult is the result of Screen.
type ScreenResult struct {
	PaidResult
	Target      string
	Verdict     string // CLEAR, REVIEW, BLOCKED or UNKNOWN
	Attestation map[string]any
	Summary     AttestationSummary
}

// PlanResult is the result of BuyPlan.
type PlanResult struct {
	PaidResult
	Plan          string // the tenant's effective plan after the purchase
	PlanExpiresAt string // ISO 8601, or empty
}

// X402PaymentResult is the result of PayAlias and AttestSettledPayment.
type X402PaymentResult struct {
	PaidResult
	TxHash      string
	Payer       string
	Attestation map[string]any
	Summary     AttestationSummary
}

// Paid route paths.
const (
	pathX402Pay     = "/api/v1/x402/pay/"
	pathX402Resolve = "/api/v1/x402/resolve/"
	pathX402Screen  = "/api/v1/x402/screen/"
	pathX402Plan    = "/api/v1/x402/plan/"
)

// FetchChallenge sends the unpaid GET to a paid path and returns its 402
// challenge. Any other status is an error.
func (c *Client) FetchChallenge(ctx context.Context, path string, q url.Values) (*Challenge, error) {
	resp, err := c.get(ctx, path, q, nil)
	if err != nil {
		return nil, err
	}
	if resp.status != http.StatusPaymentRequired {
		if resp.status < 300 {
			return nil, &X402PayError{Msg: fmt.Sprintf("expected a 402 challenge, got %d", resp.status)}
		}
		return nil, newAPIError(resp.status, resp.header, resp.body)
	}
	err = newAPIError(resp.status, resp.header, resp.body)
	if pr, ok := err.(*PaymentRequiredError); ok {
		return pr.Challenge, nil
	}
	return nil, err
}

// SettleWithHeader sends the paid GET with a payment header the caller built.
func (c *Client) SettleWithHeader(ctx context.Context, path string, q url.Values, paymentHeader string, opts PayOptions) (*PaidResult, error) {
	h := http.Header{}
	name := HeaderPayment
	if opts.UsePaymentSignature {
		name = HeaderPaymentSignature
	}
	h.Set(name, paymentHeader)
	if opts.IdempotencyKey != "" {
		h.Set(HeaderIdempotencyKey, opts.IdempotencyKey)
	}
	// Retry-safe: the server checks idempotency before it verifies, so a
	// re-sent proof returns the recorded outcome and never settles twice.
	resp, err := c.get(ctx, path, q, h)
	if err != nil {
		return nil, err
	}
	if resp.status != http.StatusOK {
		return nil, newAPIError(resp.status, resp.header, resp.body)
	}
	return paidResultFrom(resp)
}

// pay runs the x402 handshake on path: GET, pay the 402, GET again.
func (c *Client) pay(ctx context.Context, path string, q url.Values, opts PayOptions) (*PaidResult, error) {
	if opts.Currency != "" {
		q.Set("currency", opts.Currency)
	}
	resp, err := c.get(ctx, path, q, nil)
	if err != nil {
		return nil, err
	}
	if resp.status == http.StatusOK {
		// Nothing to pay (for example an exempt caller).
		return paidResultFrom(resp)
	}
	err = newAPIError(resp.status, resp.header, resp.body)
	pr, ok := err.(*PaymentRequiredError)
	if !ok {
		return nil, err
	}
	if opts.Signer == nil {
		return nil, pr
	}
	sel := opts.Select
	if sel == nil {
		sel = FirstAccept
	}
	accepted, err := sel(pr.Challenge.Accepts)
	if err != nil {
		return nil, err
	}
	header, err := opts.Signer.SignPayment(ctx, accepted, pr.Challenge)
	if err != nil {
		return nil, &X402PayError{Msg: "signer failed", Err: err}
	}
	if header == "" {
		return nil, &X402PayError{Msg: "signer returned an empty payment header"}
	}
	res, err := c.SettleWithHeader(ctx, path, q, header, opts)
	if err != nil {
		return nil, err
	}
	res.Accepted, res.Challenge = accepted, pr.Challenge
	return res, nil
}

func paidResultFrom(resp *response) (*PaidResult, error) {
	var body map[string]any
	dec := json.NewDecoder(strings.NewReader(string(resp.body)))
	if err := dec.Decode(&body); err != nil {
		return nil, fmt.Errorf("dnsofmoney: decode paid response: %w", err)
	}
	res := &PaidResult{Body: body, Meta: resp.meta()}
	res.Settled, _ = body["settled"].(bool)
	res.Idempotent, _ = body["idempotent"].(bool)
	switch t := body["transaction_id"].(type) {
	case string:
		res.TransactionID = t
	case float64:
		res.TransactionID = fmt.Sprintf("%.0f", t)
	}
	res.Proof, _ = body["proof"].(map[string]any)
	if res.Proof == nil {
		res.Proof = map[string]any{}
	}
	h := resp.header.Get(HeaderPaymentResponse)
	if h == "" {
		h = resp.header.Get(HeaderXPaymentResponse)
	}
	if h != "" {
		p, err := DecodePaymentResponse(h)
		if err != nil {
			return nil, err
		}
		res.PaymentResponse = p
	}
	return res, nil
}

// PaidResolve resolves an alias at the paid tier (tier 2) with
// GET /api/v1/x402/resolve/{alias}. No API key is needed: the payment is the
// credential. This is the route a key over its monthly quota can use.
// The server returns 404 X402_RESOLVE_NOT_RESOLVED before any 402 when the
// alias does not resolve, so a caller never pays for nothing.
func (c *Client) PaidResolve(ctx context.Context, aliasURI string, opts PayOptions) (*PaidResolveResult, error) {
	if aliasURI == "" {
		return nil, errEmptyArg
	}
	res, err := c.pay(ctx, pathX402Resolve+pathSegment(aliasURI), url.Values{}, opts)
	if err != nil {
		return nil, err
	}
	out := &PaidResolveResult{PaidResult: *res}
	if r, ok := res.Body["resolution"]; ok && r != nil {
		raw, err := json.Marshal(r)
		if err != nil {
			return nil, err
		}
		out.Resolution, err = decodeResolution(raw)
		if err != nil {
			return nil, err
		}
		out.Resolution.Meta = res.Meta
	}
	return out, nil
}

// Screen pays the screening fee with GET /api/v1/x402/screen/{target} and
// returns the OFAC and resolution attestation about target: a pay: alias or
// a raw address on any chain. The fee price is set by the server.
//
// The reference SDKs default the fee currency to USDC. Set opts.Currency to
// pin it the same way. Leave it empty to see every rail.
func (c *Client) Screen(ctx context.Context, target string, opts PayOptions) (*ScreenResult, error) {
	if target == "" {
		return nil, errEmptyArg
	}
	res, err := c.pay(ctx, pathX402Screen+pathSegment(target), url.Values{}, opts)
	if err != nil {
		return nil, err
	}
	return screenResultFrom(target, res), nil
}

// ScreenWithPaymentHeader settles the screen fee with a header the caller's
// own wallet stack built, like screen_with_payment_header in Python.
func (c *Client) ScreenWithPaymentHeader(ctx context.Context, target, paymentHeader string, opts PayOptions) (*ScreenResult, error) {
	q := url.Values{}
	if opts.Currency != "" {
		q.Set("currency", opts.Currency)
	}
	res, err := c.SettleWithHeader(ctx, pathX402Screen+pathSegment(target), q, paymentHeader, opts)
	if err != nil {
		return nil, err
	}
	return screenResultFrom(target, res), nil
}

func screenResultFrom(target string, res *PaidResult) *ScreenResult {
	att, _ := res.Body["attestation"].(map[string]any)
	if att == nil {
		att = map[string]any{}
	}
	sum := SummarizeAttestation(att)
	return &ScreenResult{PaidResult: *res, Target: target, Verdict: sum.Verdict, Attestation: att, Summary: sum}
}

// BuyPlan buys an API plan for the caller's tenant with
// GET /api/v1/x402/plan/{plan} (for example "business"). It needs an API
// key, because the key names the tenant. The server sets the price and
// takes USD stablecoins only: set opts.Currency to "USDC-BASE" (the server
// default), "USDC", "USDC-SOL", "USDT0-XLAYER" or "RLUSD".
func (c *Client) BuyPlan(ctx context.Context, plan string, opts PayOptions) (*PlanResult, error) {
	if plan == "" {
		return nil, errEmptyArg
	}
	if c.apiKey == "" {
		// The server answers 401 before any 402. Fail here, before the network.
		return nil, &APIError{StatusCode: http.StatusUnauthorized, Message: "an X-API-Key is required to buy a plan"}
	}
	res, err := c.pay(ctx, pathX402Plan+pathSegment(plan), url.Values{}, opts)
	if err != nil {
		return nil, err
	}
	out := &PlanResult{PaidResult: *res}
	out.Plan, _ = res.Body["plan"].(string)
	out.PlanExpiresAt, _ = res.Body["plan_expires_at"].(string)
	return out, nil
}

// PayAlias pays a pay: alias with GET /api/v1/x402/pay/{alias}. amount is a
// decimal string in currency units. Omit it to pay the alias's declared
// price. The settle leg needs an API key.
func (c *Client) PayAlias(ctx context.Context, aliasURI, amount string, opts PayOptions) (*X402PaymentResult, error) {
	if aliasURI == "" {
		return nil, errEmptyArg
	}
	q := url.Values{}
	if amount != "" {
		q.Set("amount", amount)
	}
	res, err := c.pay(ctx, pathX402Pay+pathSegment(aliasURI), q, opts)
	if err != nil {
		return nil, err
	}
	return paymentResultFrom(res), nil
}

// AttestSettledPayment gets the attestation for an XRP payment the caller
// already settled on XRPL, like attest_settled_payment in Python. It sends
// ?amount=&currency=XRP and the tx-hash X-PAYMENT header.
func (c *Client) AttestSettledPayment(ctx context.Context, aliasURI, amountXRP, txHash, payer string, opts PayOptions) (*X402PaymentResult, error) {
	q := url.Values{"amount": {amountXRP}, "currency": {"XRP"}}
	res, err := c.SettleWithHeader(ctx, pathX402Pay+pathSegment(aliasURI), q, BuildXPaymentHeader(txHash, payer), opts)
	if err != nil {
		return nil, err
	}
	return paymentResultFrom(res), nil
}

func paymentResultFrom(res *PaidResult) *X402PaymentResult {
	att, _ := res.Body["attestation"].(map[string]any)
	if att == nil {
		att = map[string]any{}
	}
	out := &X402PaymentResult{PaidResult: *res, Attestation: att, Summary: SummarizeAttestation(att)}
	out.TxHash, _ = res.Proof["transaction"].(string)
	out.Payer, _ = res.Proof["payer"].(string)
	return out
}
