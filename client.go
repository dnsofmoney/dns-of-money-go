package dnsofmoney

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Version is the SDK version, sent in the User-Agent header.
const Version = "0.1.0"

const (
	// DefaultBaseURL is the production API.
	DefaultBaseURL = "https://api.dnsofmoney.com"
	// DefaultTimeout bounds one HTTP attempt when the Client builds its own http.Client.
	DefaultTimeout = 30 * time.Second
	// DefaultRetries is the number of retries of an idempotent GET (3 attempts in all).
	DefaultRetries = 2
	// DefaultBackoff is the first retry delay. It doubles on each retry, with no jitter.
	DefaultBackoff = 500 * time.Millisecond

	maxResponseBytes = 10 << 20
)

// Client calls the DNS of Money API. It is safe for concurrent use.
type Client struct {
	baseURL   string
	apiKey    string
	http      *http.Client
	retries   int
	backoff   time.Duration
	userAgent string
}

// Option configures a Client.
type Option func(*Client)

// WithAPIKey sets the X-API-Key header. Resolve works without a key at tier 0.
func WithAPIKey(key string) Option { return func(c *Client) { c.apiKey = key } }

// WithBaseURL overrides DefaultBaseURL. A trailing slash is removed.
func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") }
}

// WithHTTPClient sets the http.Client. Its Timeout bounds each attempt.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithRetries sets the retry count and first backoff of idempotent GETs.
// Retries happen only on a transport error or a 502, 503 or 504. A POST is
// never retried.
func WithRetries(retries int, backoff time.Duration) Option {
	return func(c *Client) {
		if retries < 0 {
			retries = 0
		}
		c.retries, c.backoff = retries, backoff
	}
}

// WithUserAgent replaces the User-Agent header.
func WithUserAgent(ua string) Option { return func(c *Client) { c.userAgent = ua } }

// NewClient returns a Client for DefaultBaseURL unless an Option overrides it.
func NewClient(opts ...Option) *Client {
	c := &Client{
		baseURL:   DefaultBaseURL,
		http:      &http.Client{Timeout: DefaultTimeout},
		retries:   DefaultRetries,
		backoff:   DefaultBackoff,
		userAgent: "dns-of-money-go/" + Version,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// BaseURL returns the base URL the Client calls.
func (c *Client) BaseURL() string { return c.baseURL }

// RateLimit is the per-minute limiter state from the X-RateLimit-* headers.
type RateLimit struct {
	Limit     int64
	Remaining int64
	Reset     time.Time // from a Unix-seconds value
}

// Quota is the monthly API-key quota from the X-Quota-* headers. The server
// sends them only on metered paths, for a keyed caller on a limited plan,
// when API_QUOTA_ENABLED is on.
type Quota struct {
	Limit     int64
	Remaining int64
	Reset     time.Time // first instant of the next UTC month
}

// ResponseMeta is the header data of one response.
type ResponseMeta struct {
	StatusCode   int
	RateLimit    *RateLimit // nil when the headers are absent
	Quota        *Quota     // nil when the headers are absent
	RequestID    string     // X-Request-Id
	ResolvedFrom string     // X-Resolved-From: origin, cache or negative_cache
}

func parseMeta(h http.Header) ResponseMeta {
	m := ResponseMeta{
		RequestID:    h.Get("X-Request-Id"),
		ResolvedFrom: h.Get("X-Resolved-From"),
	}
	if l, r, t, ok := parseTriple(h, "X-RateLimit-"); ok {
		m.RateLimit = &RateLimit{Limit: l, Remaining: r, Reset: t}
	}
	if l, r, t, ok := parseTriple(h, "X-Quota-"); ok {
		m.Quota = &Quota{Limit: l, Remaining: r, Reset: t}
	}
	return m
}

func parseTriple(h http.Header, prefix string) (limit, remaining int64, reset time.Time, ok bool) {
	ls := h.Get(prefix + "Limit")
	if ls == "" {
		return 0, 0, time.Time{}, false
	}
	limit, err := strconv.ParseInt(ls, 10, 64)
	if err != nil {
		return 0, 0, time.Time{}, false
	}
	remaining, _ = strconv.ParseInt(h.Get(prefix+"Remaining"), 10, 64)
	if s, err := strconv.ParseInt(h.Get(prefix+"Reset"), 10, 64); err == nil {
		reset = time.Unix(s, 0).UTC()
	}
	return limit, remaining, reset, true
}

// response is a fully read HTTP response.
type response struct {
	status int
	header http.Header
	body   []byte
}

func (r *response) meta() ResponseMeta {
	m := parseMeta(r.header)
	m.StatusCode = r.status
	return m
}

var retryableStatus = map[int]bool{502: true, 503: true, 504: true}

// get sends a GET with bounded retries. extra headers override the defaults.
func (c *Client) get(ctx context.Context, path string, q url.Values, extra http.Header) (*response, error) {
	u := c.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		c.setHeaders(req, extra)
		resp, err := c.do(req)
		retry := attempt < c.retries && ctx.Err() == nil &&
			(err != nil || retryableStatus[resp.status])
		if !retry {
			if err != nil {
				return nil, err
			}
			return resp.response, nil
		}
		if err := sleepCtx(ctx, c.backoff<<attempt); err != nil {
			return nil, err
		}
	}
}

// post sends a POST once. It is never retried, because a POST may move state.
func (c *Client) post(ctx context.Context, path string, body []byte, extra http.Header) (*response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setHeaders(req, extra)
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	if resp.method != http.MethodPost {
		// The server redirected and net/http resent the POST as a GET without its body.
		return nil, &APIError{
			StatusCode: resp.status,
			Message:    fmt.Sprintf("POST %s was redirected and resent as %s; the request body was lost", path, resp.method),
			Meta:       resp.meta(),
		}
	}
	return resp.response, nil
}

type sentResponse struct {
	*response
	method string
}

func (c *Client) setHeaders(req *http.Request, extra http.Header) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}
	for k, vs := range extra {
		req.Header.Del(k)
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
}

func (c *Client) do(req *http.Request) (*sentResponse, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, err
	}
	return &sentResponse{
		response: &response{status: resp.StatusCode, header: resp.Header, body: body},
		method:   resp.Request.Method,
	}, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// pathSegment escapes one path segment. A pay: alias keeps its colon.
func pathSegment(s string) string { return url.PathEscape(s) }

var errEmptyArg = errors.New("dnsofmoney: argument must not be empty")
