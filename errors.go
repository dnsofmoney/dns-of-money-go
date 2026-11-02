package dnsofmoney

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// Error codes the API returns. The x402 codes arrive in the body as
// {"detail": {"error": "<code>", ...}}. The quota and rate codes arrive as
// {"error": "<code>", ...}.
const (
	CodeQuotaExceeded     = "quota_exceeded"
	CodeRateLimitExceeded = "rate_limit_exceeded"
	CodeCapExceeded       = "CAP_EXCEEDED"

	CodeX402NotPayable                   = "X402_NOT_PAYABLE"
	CodeX402InvalidRequestKey            = "X402_INVALID_REQUEST_KEY"
	CodeX402AttestationUnsignable        = "X402_ATTESTATION_UNSIGNABLE"
	CodeX402SettlementAccountUnavailable = "X402_SETTLEMENT_ACCOUNT_UNAVAILABLE"
	CodeX402ScreenUnpriced               = "X402_SCREEN_UNPRICED"
	CodeX402ResolveUnpriced              = "X402_RESOLVE_UNPRICED"
	CodeX402ResolveNotResolved           = "X402_RESOLVE_NOT_RESOLVED"
	CodeX402PlanNotForSale               = "X402_PLAN_NOT_FOR_SALE"
	CodeX402PlanAlreadyHigher            = "X402_PLAN_ALREADY_HIGHER"
	CodeX402PlanCurrency                 = "X402_PLAN_CURRENCY"
	CodeX402PlanPriceInvalid             = "X402_PLAN_PRICE_INVALID"
	CodeX402PlanGrantFailed              = "X402_PLAN_GRANT_FAILED"
)

// Sentinel errors for errors.Is. An *APIError matches the sentinel for its
// status and code.
var (
	ErrAuthentication  = errors.New("dnsofmoney: invalid or missing API key")
	ErrAliasNotFound   = errors.New("dnsofmoney: not found")
	ErrAliasTaken      = errors.New("dnsofmoney: alias already taken")
	ErrCapReached      = errors.New("dnsofmoney: founding tier cap reached")
	ErrRateLimited     = errors.New("dnsofmoney: rate limit exceeded")
	ErrQuotaExceeded   = errors.New("dnsofmoney: monthly quota exceeded")
	ErrPaymentRequired = errors.New("dnsofmoney: payment required")
)

// maxBodySnippet caps the body text kept on an error, as the reference SDKs do.
const maxBodySnippet = 300

// APIError is an HTTP failure from the API, with the response context.
type APIError struct {
	StatusCode int
	// Code is the machine-readable code: error_code, detail.error or error,
	// in that order. Empty when the body carries none.
	Code string
	// Message is detail (when it is a string), detail.message or message.
	Message string
	// Detail is the decoded "detail" object of a FastAPI error, if any.
	Detail map[string]any
	// Body is the first 300 bytes of the response body.
	Body string
	Meta ResponseMeta
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("dnsofmoney: HTTP %d", e.StatusCode)
	if e.Code != "" {
		msg += " " + e.Code
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// Is maps the error onto the package sentinels.
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrAuthentication:
		return e.StatusCode == http.StatusUnauthorized
	case ErrAliasNotFound:
		return e.StatusCode == http.StatusNotFound
	case ErrCapReached:
		return e.StatusCode == http.StatusConflict && e.Code == CodeCapExceeded
	case ErrAliasTaken:
		return e.StatusCode == http.StatusConflict && e.Code != CodeCapExceeded && e.Code != CodeX402PlanAlreadyHigher
	case ErrRateLimited:
		return e.StatusCode == http.StatusTooManyRequests
	case ErrQuotaExceeded:
		return e.StatusCode == http.StatusPaymentRequired && e.Code == CodeQuotaExceeded
	case ErrPaymentRequired:
		return e.StatusCode == http.StatusPaymentRequired && e.Code != CodeQuotaExceeded
	}
	return false
}

// RateLimitError is a 429 from the per-minute limiter.
type RateLimitError struct {
	*APIError
	// RetryAfter comes from the Retry-After header. Zero when absent.
	RetryAfter time.Duration
	Limit      int
	ResetAt    string // ISO 8601, from the body
}

func (e *RateLimitError) Unwrap() error { return e.APIError }

// QuotaExceededError is a 402 from the monthly API-key quota. It is not an
// x402 challenge: the body names the routes that get the caller past it.
type QuotaExceededError struct {
	*APIError
	Plan    string
	Limit   int64
	Used    int64
	ResetAt string // ISO 8601
	// PayPerCall is the route template to pay per call, for example
	// "/api/v1/x402/resolve/{alias_name}".
	PayPerCall string
	// Upgrade is the route that sells a higher plan, for example
	// "/api/v1/x402/plan/business".
	Upgrade string
}

func (e *QuotaExceededError) Unwrap() error { return e.APIError }

// PaymentRequiredError is an x402 402 challenge that the call did not pay.
// Challenge.Error carries the server's reason when it rejected a payment.
type PaymentRequiredError struct {
	*APIError
	Challenge *Challenge
}

func (e *PaymentRequiredError) Unwrap() error { return e.APIError }

// X402PayError is a client-side failure of the x402 pay path: a bad
// challenge, no Signer, a Signer failure or no matching accept.
type X402PayError struct {
	Msg string
	Err error
}

func (e *X402PayError) Error() string {
	if e.Err != nil {
		return "dnsofmoney: x402: " + e.Msg + ": " + e.Err.Error()
	}
	return "dnsofmoney: x402: " + e.Msg
}

func (e *X402PayError) Unwrap() error { return e.Err }

// errorBody is the union of the error shapes the API returns.
type errorBody struct {
	ErrorCode  string          `json:"error_code"`
	Message    string          `json:"message"`
	Error      json.RawMessage `json:"error"`
	Detail     json.RawMessage `json:"detail"`
	Limit      json.Number     `json:"limit"`
	Used       json.Number     `json:"used"`
	ResetAt    string          `json:"reset_at"`
	Plan       string          `json:"plan"`
	PayPerCall string          `json:"pay_per_call"`
	Upgrade    string          `json:"upgrade"`
	Accepts    json.RawMessage `json:"accepts"`
}

// newAPIError builds the typed error for a non-success response.
func newAPIError(status int, header http.Header, body []byte) error {
	base := &APIError{StatusCode: status, Meta: parseMeta(header)}
	if len(body) > maxBodySnippet {
		base.Body = string(body[:maxBodySnippet])
	} else {
		base.Body = string(body)
	}

	var eb errorBody
	_ = json.Unmarshal(body, &eb)
	base.Code = eb.ErrorCode
	base.Message = eb.Message
	var errStr string
	if len(eb.Error) > 0 {
		_ = json.Unmarshal(eb.Error, &errStr)
	}
	if len(eb.Detail) > 0 {
		var s string
		if json.Unmarshal(eb.Detail, &s) == nil {
			if base.Message == "" {
				base.Message = s
			}
		} else {
			var d map[string]any
			if json.Unmarshal(eb.Detail, &d) == nil {
				base.Detail = d
				if c, ok := d["error"].(string); ok && base.Code == "" {
					base.Code = c
				}
				if m, ok := d["message"].(string); ok && base.Message == "" {
					base.Message = m
				}
			}
		}
	}

	switch status {
	case http.StatusTooManyRequests:
		if base.Code == "" {
			base.Code = errStr
		}
		e := &RateLimitError{APIError: base, ResetAt: eb.ResetAt}
		if n, err := eb.Limit.Int64(); err == nil {
			e.Limit = int(n)
		}
		if s, err := strconv.Atoi(header.Get("Retry-After")); err == nil && s > 0 {
			e.RetryAfter = time.Duration(s) * time.Second
		}
		return e
	case http.StatusPaymentRequired:
		if errStr == CodeQuotaExceeded {
			base.Code = CodeQuotaExceeded
			e := &QuotaExceededError{
				APIError:   base,
				Plan:       eb.Plan,
				ResetAt:    eb.ResetAt,
				PayPerCall: eb.PayPerCall,
				Upgrade:    eb.Upgrade,
			}
			e.Limit, _ = eb.Limit.Int64()
			e.Used, _ = eb.Used.Int64()
			return e
		}
		ch, err := parseChallenge(header, body)
		if err != nil {
			return base
		}
		if base.Message == "" {
			base.Message = ch.Error
		}
		return &PaymentRequiredError{APIError: base, Challenge: ch}
	}
	if base.Code == "" {
		base.Code = errStr
	}
	return base
}
