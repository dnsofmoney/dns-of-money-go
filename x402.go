package dnsofmoney

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// x402 header names.
const (
	HeaderPaymentRequired  = "PAYMENT-REQUIRED"
	HeaderPayment          = "X-PAYMENT"         // the retry header (x402 v1 name, read by the server)
	HeaderPaymentSignature = "PAYMENT-SIGNATURE" // the x402 v2 name, also read by the server
	HeaderPaymentResponse  = "PAYMENT-RESPONSE"
	HeaderXPaymentResponse = "X-PAYMENT-RESPONSE" // legacy mirror of PAYMENT-RESPONSE
	HeaderIdempotencyKey   = "Idempotency-Key"
	X402Version            = 2
	SchemeExact            = "exact"
)

// Resource is the resource field of a requirement. The 402 body carries a
// URL string. The PAYMENT-REQUIRED header envelope carries {"url": ...}.
type Resource struct {
	URL string
}

// UnmarshalJSON accepts a string or an object with a url key.
func (r *Resource) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		r.URL = s
		return nil
	}
	var o struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(b, &o); err != nil {
		return err
	}
	r.URL = o.URL
	return nil
}

// MarshalJSON writes the string form.
func (r Resource) MarshalJSON() ([]byte, error) { return json.Marshal(r.URL) }

// PaymentRequirements is one entry of the 402 accepts array: one way to pay.
type PaymentRequirements struct {
	Scheme            string         `json:"scheme"`
	Network           string         `json:"network"` // CAIP-2, for example "xrpl:0" or "eip155:8453"
	PayTo             string         `json:"payTo"`
	Asset             string         `json:"asset"`
	MaxAmountRequired string         `json:"maxAmountRequired,omitempty"` // atomic units (drops for XRP)
	Amount            string         `json:"amount,omitempty"`            // same value, x402 v2 name
	Resource          Resource       `json:"resource"`
	Description       string         `json:"description,omitempty"`
	MimeType          string         `json:"mimeType,omitempty"`
	MaxTimeoutSeconds int            `json:"maxTimeoutSeconds,omitempty"`
	OutputSchema      map[string]any `json:"outputSchema,omitempty"`
	InvoiceID         string         `json:"invoiceId,omitempty"` // deprecated top-level mirror of extra.invoiceId
	SourceTag         *int64         `json:"sourceTag,omitempty"` // deprecated top-level mirror of extra.sourceTag
	Extra             map[string]any `json:"extra,omitempty"`

	// Raw is the entry exactly as the server sent it.
	Raw json.RawMessage `json:"-"`
}

// UnmarshalJSON keeps the raw entry beside the decoded fields.
func (p *PaymentRequirements) UnmarshalJSON(b []byte) error {
	type plain PaymentRequirements
	var v plain
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*p = PaymentRequirements(v)
	p.Raw = append(json.RawMessage(nil), b...)
	return nil
}

// AtomicAmount returns Amount, or MaxAmountRequired when Amount is empty.
func (p PaymentRequirements) AtomicAmount() string {
	if p.Amount != "" {
		return p.Amount
	}
	return p.MaxAmountRequired
}

// InvoiceIDValue returns extra.invoiceId, or the top-level invoiceId on a
// server that predates extra. Python and TypeScript read it the same way.
func (p PaymentRequirements) InvoiceIDValue() string {
	if s, ok := p.Extra["invoiceId"].(string); ok && s != "" {
		return s
	}
	return p.InvoiceID
}

// SourceTagValue returns extra.sourceTag, or the top-level sourceTag.
func (p PaymentRequirements) SourceTagValue() (int64, bool) {
	if v, ok := p.Extra["sourceTag"]; ok && v != nil {
		switch n := v.(type) {
		case float64:
			return int64(n), true
		case json.Number:
			i, err := n.Int64()
			return i, err == nil
		}
	}
	if p.SourceTag != nil {
		return *p.SourceTag, true
	}
	return 0, false
}

// acceptedEntry is the accepts_entry shape the server builds, used to echo
// the chosen requirement in a payment header.
func (p PaymentRequirements) acceptedEntry() map[string]any {
	extra := p.Extra
	if extra == nil {
		extra = map[string]any{}
	}
	return map[string]any{
		"scheme":            p.Scheme,
		"network":           p.Network,
		"asset":             p.Asset,
		"amount":            p.AtomicAmount(),
		"payTo":             p.PayTo,
		"maxTimeoutSeconds": p.MaxTimeoutSeconds,
		"extra":             extra,
	}
}

// Challenge is a parsed 402 x402 challenge.
type Challenge struct {
	X402Version int                   `json:"x402Version"`
	Accepts     []PaymentRequirements `json:"accepts"`
	// Error is the server's reason when it rejected a payment. Empty on a
	// first challenge.
	Error      string         `json:"error,omitempty"`
	Extensions map[string]any `json:"extensions,omitempty"`
	// Header is the raw PAYMENT-REQUIRED header value. The server puts the
	// FIRST accept in it. A multi-rail client reads Accepts from the body.
	Header string `json:"-"`
}

// PaymentRequired is the decoded PAYMENT-REQUIRED header. For XRPL the
// server sends the bare requirement fields merged with the v2 envelope. For
// Algorand and EVM it sends the v2 envelope only.
type PaymentRequired struct {
	PaymentRequirements
	X402Version int                   `json:"x402Version"`
	Accepts     []PaymentRequirements `json:"accepts"`
	Error       *string               `json:"error"`
	Extensions  map[string]any        `json:"extensions,omitempty"`
}

// DecodePaymentRequired decodes the base64(JSON) PAYMENT-REQUIRED header into
// a generic map, like decode_payment_required in Python.
func DecodePaymentRequired(headerValue string) (map[string]any, error) {
	raw, err := decodeB64(headerValue)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("dnsofmoney: decode %s: %w", HeaderPaymentRequired, err)
	}
	return out, nil
}

// DecodePaymentRequiredHeader decodes the PAYMENT-REQUIRED header into types.
func DecodePaymentRequiredHeader(headerValue string) (*PaymentRequired, error) {
	raw, err := decodeB64(headerValue)
	if err != nil {
		return nil, err
	}
	var pr PaymentRequired
	if err := json.Unmarshal(raw, &pr.PaymentRequirements); err != nil {
		return nil, fmt.Errorf("dnsofmoney: decode %s: %w", HeaderPaymentRequired, err)
	}
	var env struct {
		X402Version int                   `json:"x402Version"`
		Accepts     []PaymentRequirements `json:"accepts"`
		Error       *string               `json:"error"`
		Extensions  map[string]any        `json:"extensions"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("dnsofmoney: decode %s: %w", HeaderPaymentRequired, err)
	}
	pr.X402Version, pr.Accepts, pr.Error, pr.Extensions = env.X402Version, env.Accepts, env.Error, env.Extensions
	return &pr, nil
}

func decodeB64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	b, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		return nil, fmt.Errorf("dnsofmoney: header is not base64: %w", err)
	}
	return b, nil
}

// ParseChallenge parses a 402 response into a Challenge. It reads accepts[]
// from the JSON body. When the body has none, it falls back to the
// PAYMENT-REQUIRED header, which carries one accept.
func ParseChallenge(resp *http.Response, body []byte) (*Challenge, error) {
	if resp.StatusCode != http.StatusPaymentRequired {
		return nil, &X402PayError{Msg: fmt.Sprintf("expected a 402 challenge, got %d", resp.StatusCode)}
	}
	return parseChallenge(resp.Header, body)
}

func parseChallenge(h http.Header, body []byte) (*Challenge, error) {
	ch := &Challenge{Header: h.Get(HeaderPaymentRequired)}
	if len(body) > 0 {
		if err := json.Unmarshal(body, ch); err != nil {
			return nil, &X402PayError{Msg: "decode 402 body", Err: err}
		}
	}
	if len(ch.Accepts) == 0 && ch.Header != "" {
		pr, err := DecodePaymentRequiredHeader(ch.Header)
		if err != nil {
			return nil, &X402PayError{Msg: "decode " + HeaderPaymentRequired, Err: err}
		}
		ch.Accepts = pr.Accepts
		if len(ch.Accepts) == 0 && pr.PayTo != "" {
			ch.Accepts = []PaymentRequirements{pr.PaymentRequirements}
		}
		if ch.X402Version == 0 {
			ch.X402Version = pr.X402Version
		}
	}
	if len(ch.Accepts) == 0 {
		return nil, &X402PayError{Msg: "402 response has no accepts and no " + HeaderPaymentRequired + " header"}
	}
	return ch, nil
}

// Selector chooses which accept to pay. It never sees keys.
type Selector func(accepts []PaymentRequirements) (PaymentRequirements, error)

// FirstAccept picks accepts[0], the rail the server lists first.
func FirstAccept(accepts []PaymentRequirements) (PaymentRequirements, error) {
	if len(accepts) == 0 {
		return PaymentRequirements{}, &X402PayError{Msg: "no accepts to choose from"}
	}
	return accepts[0], nil
}

// SelectNetwork picks the first accept whose network equals network, or
// starts with it when network ends in ":" (for example "eip155:" or "xrpl:").
// A non-empty asset must also match, without regard to case.
func SelectNetwork(network, asset string) Selector {
	return func(accepts []PaymentRequirements) (PaymentRequirements, error) {
		for _, a := range accepts {
			match := a.Network == network ||
				(strings.HasSuffix(network, ":") && strings.HasPrefix(a.Network, network))
			if match && (asset == "" || strings.EqualFold(a.Asset, asset)) {
				return a, nil
			}
		}
		return PaymentRequirements{}, &X402PayError{Msg: fmt.Sprintf("no accept for network %q asset %q", network, asset)}
	}
}

// Signer builds the payment header for one requirement. The caller
// implements it with its own wallet. The SDK never sees a key or a seed.
//
// SignPayment returns the header value: base64 of the x402 PaymentPayload
// JSON. On XRPL with a payment already on the ledger, use XRPLTxHashSigner.
type Signer interface {
	SignPayment(ctx context.Context, req PaymentRequirements, ch *Challenge) (string, error)
}

// SignerFunc adapts a function to Signer.
type SignerFunc func(ctx context.Context, req PaymentRequirements, ch *Challenge) (string, error)

// SignPayment calls f.
func (f SignerFunc) SignPayment(ctx context.Context, req PaymentRequirements, ch *Challenge) (string, error) {
	return f(ctx, req, ch)
}

// XRPLPayFunc pays req on XRPL with the caller's wallet and returns the
// validated transaction hash and the payer address. It should set the
// Payment's InvoiceID to InvoiceIDHash(req.InvoiceIDValue()) and its
// SourceTag to req.SourceTagValue() when present.
type XRPLPayFunc func(ctx context.Context, req PaymentRequirements) (txHash, payer string, err error)

// XRPLTxHashSigner returns a Signer for the XRPL read-only verifier: pay is
// called to settle on the ledger, and the header carries the tx hash. The
// header also echoes the chosen requirement in "accepted", so the server can
// tell the rail on a challenge that lists several.
func XRPLTxHashSigner(pay XRPLPayFunc) Signer {
	return SignerFunc(func(ctx context.Context, req PaymentRequirements, _ *Challenge) (string, error) {
		txHash, payer, err := pay(ctx, req)
		if err != nil {
			return "", err
		}
		return BuildXPaymentHeaderAccepted(req, txHash, payer)
	})
}

// BuildXPaymentHeader builds the X-PAYMENT header that proves a settled XRPL
// transaction: base64 of {"x402Version": 2, "payload": {"txHash", "payer"}}.
// The bytes are identical to build_x_payment_header in Python.
func BuildXPaymentHeader(txHash, payer string) string {
	var b strings.Builder
	b.WriteString(`{"x402Version": 2, "payload": {"txHash": `)
	pyEscapeString(&b, txHash)
	if payer != "" {
		b.WriteString(`, "payer": `)
		pyEscapeString(&b, payer)
	}
	b.WriteString(`}}`)
	return base64.StdEncoding.EncodeToString([]byte(b.String()))
}

// BuildXPaymentHeaderAccepted is BuildXPaymentHeader plus the "accepted"
// echo of req and its network. The server reads accepted.network to find
// the rail when the request does not pin ?currency=.
func BuildXPaymentHeaderAccepted(req PaymentRequirements, txHash, payer string) (string, error) {
	payload := map[string]any{"txHash": txHash}
	if payer != "" {
		payload["payer"] = payer
	}
	b, err := json.Marshal(map[string]any{
		"x402Version": X402Version,
		"scheme":      req.Scheme,
		"network":     req.Network,
		"accepted":    req.acceptedEntry(),
		"payload":     payload,
	})
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// InvoiceIDHash is the on-chain XRPL InvoiceID that binds a payment to a
// challenge: uppercase hex of SHA-256 over the invoiceId.
func InvoiceIDHash(invoiceID string) string {
	sum := sha256.Sum256([]byte(invoiceID))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// SettlementProof is the decoded PAYMENT-RESPONSE header.
type SettlementProof struct {
	Success      bool   `json:"success"`
	Transaction  string `json:"transaction,omitempty"`
	Network      string `json:"network"`
	Payer        string `json:"payer,omitempty"`
	ErrorReason  string `json:"errorReason,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
	Asset        string `json:"asset,omitempty"`
	AmountDrops  string `json:"amountDrops,omitempty"`
	RequestKey   string `json:"requestKey,omitempty"`
	// State is "settled" when this call moved funds, "replayed" when the
	// payment was already recorded.
	State string `json:"state,omitempty"`
}

// DecodePaymentResponse decodes a PAYMENT-RESPONSE header value.
func DecodePaymentResponse(headerValue string) (*SettlementProof, error) {
	raw, err := decodeB64(headerValue)
	if err != nil {
		return nil, err
	}
	var p SettlementProof
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("dnsofmoney: decode %s: %w", HeaderPaymentResponse, err)
	}
	return &p, nil
}

// AttestationSummary is the attestation verdicts at a glance.
type AttestationSummary struct {
	Verdict      string // CLEAR, REVIEW, BLOCKED or UNKNOWN
	PayeeVerdict string
	PayerVerdict string
	TxID         string
	Signed       bool
}

// SummarizeAttestation reads a signed VC (credentialSubject) or the flat
// unsigned form, like summarize_attestation in Python.
func SummarizeAttestation(attestation map[string]any) AttestationSummary {
	subject := attestation
	if cs, ok := attestation["credentialSubject"].(map[string]any); ok {
		subject = cs
	}
	screen, _ := subject["screen"].(map[string]any)
	settlement, _ := subject["settlement"].(map[string]any)
	payee, _ := screen["payee"].(map[string]any)
	payer, _ := screen["payer"].(map[string]any)
	str := func(m map[string]any, k string) string { s, _ := m[k].(string); return s }
	return AttestationSummary{
		Verdict:      str(screen, "verdict"),
		PayeeVerdict: str(payee, "verdict"),
		PayerVerdict: str(payer, "verdict"),
		TxID:         str(settlement, "txid"),
		Signed:       truthy(attestation["proof"]),
	}
}

// truthy is Python's bool() for a decoded JSON value.
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	case map[string]any:
		return len(t) > 0
	case []any:
		return len(t) > 0
	}
	return true
}
