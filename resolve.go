package dnsofmoney

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Resolution tiers. The server redacts the response by the caller's plan:
//
//	0  anonymous:   alias, status, resolution_id, message_id, resolved_at
//	1  starter:     + display_name, entity_type, preferred_rail, identity
//	2  developer+:  + endpoints and compliance (no routing_metadata)
//	3  system/agent: full response + agent_commerce
const (
	TierAnonymous = 0
	TierBasic     = 1
	TierEndpoints = 2
	TierFull      = 3
)

// Endpoint is one payment endpoint of a resolution.
type Endpoint struct {
	EndpointID        string         `json:"endpoint_id,omitempty"`
	RailType          string         `json:"rail_type,omitempty"`
	Rail              string         `json:"rail,omitempty"` // older servers
	Currency          string         `json:"currency,omitempty"`
	Address           string         `json:"address,omitempty"`
	Priority          int            `json:"priority,omitempty"`
	FeeEstimate       string         `json:"fee_estimate,omitempty"`
	SettlementLatency string         `json:"settlement_latency,omitempty"`
	RoutingMetadata   map[string]any `json:"routing_metadata,omitempty"` // tier 3 only
	IsActive          *bool          `json:"is_active,omitempty"`
}

// RailName returns RailType, or Rail on an older server.
func (e Endpoint) RailName() string {
	if e.RailType != "" {
		return e.RailType
	}
	return e.Rail
}

// Compliance is the screening signal attached to a resolution.
type Compliance struct {
	Screened         bool   `json:"screened"`
	Result           string `json:"result,omitempty"` // PASS, FAIL, REVIEW or PENDING
	Provider         string `json:"provider,omitempty"`
	Cached           bool   `json:"cached"`
	ScreenedAt       string `json:"screened_at,omitempty"`
	SanctionsChecked *bool  `json:"sanctions_checked,omitempty"` // older servers
	FATFRiskRating   string `json:"fatf_risk_rating,omitempty"`
}

// ISO20022Hint tells the caller how to build the ISO 20022 message.
type ISO20022Hint struct {
	MessageFamily      string `json:"message_family,omitempty"`
	ServiceLevel       string `json:"service_level,omitempty"`
	LocalInstrument    string `json:"local_instrument,omitempty"`
	SettlementMethod   string `json:"settlement_method,omitempty"`
	PurposeCode        string `json:"purpose_code,omitempty"`
	TravelRuleRequired bool   `json:"travel_rule_required"`
	TravelRuleProtocol string `json:"travel_rule_protocol,omitempty"`
}

// Identity is the on-chain identity (NFT) linked to an alias.
type Identity struct {
	NFTTokenID     string `json:"nft_token_id,omitempty"`
	ImageURI       string `json:"image_uri,omitempty"`
	MetadataURI    string `json:"metadata_uri,omitempty"`
	ImageURL       string `json:"image_url,omitempty"`
	NFTExplorerURL string `json:"nft_explorer_url,omitempty"`
	Generation     int    `json:"generation"`
	IdentityStatus string `json:"identity_status,omitempty"`
	Tier           string `json:"tier,omitempty"`
}

// AgentCommerce is the tier-3 agent block.
type AgentCommerce struct {
	AgentCapable bool   `json:"agent_capable"`
	AutoSettle   bool   `json:"auto_settle"`
	CallbackURL  string `json:"callback_url,omitempty"`
}

// ResolutionResponse is the data of a resolve call. Fields above the
// caller's tier are empty.
type ResolutionResponse struct {
	ResolutionID      string         `json:"resolution_id"`
	MessageID         string         `json:"message_id"`
	Alias             string         `json:"alias"`
	Status            string         `json:"status"` // resolved, not_found, ...
	ResolvedAt        string         `json:"resolved_at"`
	CallerTier        *int           `json:"caller_tier,omitempty"`
	EntityID          string         `json:"entity_id,omitempty"`
	DisplayName       string         `json:"display_name,omitempty"`
	EntityType        string         `json:"entity_type,omitempty"`
	PreferredRail     string         `json:"preferred_rail,omitempty"`
	PreferredEndpoint *Endpoint      `json:"preferred_endpoint,omitempty"`
	FallbackEndpoints []Endpoint     `json:"fallback_endpoints,omitempty"`
	LegacyEndpoints   []Endpoint     `json:"endpoints,omitempty"` // older servers
	ISO20022Hint      *ISO20022Hint  `json:"iso20022_hint,omitempty"`
	Compliance        *Compliance    `json:"compliance,omitempty"`
	CacheHit          bool           `json:"cache_hit"`
	ResolvedFrom      string         `json:"resolved_from,omitempty"`
	RailScore         map[string]any `json:"rail_score,omitempty"`
	Identity          *Identity      `json:"identity,omitempty"`
	AgentCommerce     *AgentCommerce `json:"agent_commerce,omitempty"`
	// XHandle is unverified social metadata. Do not treat it as identity.
	XHandle string `json:"x_handle,omitempty"`

	// Raw is the undecoded data object, for fields this struct does not name.
	Raw json.RawMessage `json:"-"`
	// Meta holds the rate-limit, quota and cache headers of the response.
	Meta ResponseMeta `json:"-"`
}

// Resolved reports whether Status is "resolved".
func (r *ResolutionResponse) Resolved() bool { return r.Status == "resolved" }

// Endpoints lists the payment endpoints in priority order: the preferred
// endpoint, then the fallbacks. On an older server it returns the flat
// "endpoints" array. The reference SDKs build the same list.
func (r *ResolutionResponse) Endpoints() []Endpoint {
	if r.PreferredEndpoint == nil {
		return r.LegacyEndpoints
	}
	out := make([]Endpoint, 0, 1+len(r.FallbackEndpoints))
	out = append(out, *r.PreferredEndpoint)
	return append(out, r.FallbackEndpoints...)
}

// ResolveRequest is the body of POST /resolve.
type ResolveRequest struct {
	AliasName     string `json:"alias_name"`
	Amount        string `json:"amount,omitempty"`   // decimal string
	Currency      string `json:"currency,omitempty"` // 3 letters; the server defaults to USD
	PreferredRail string `json:"preferred_rail,omitempty"`
}

// Resolve resolves a pay: alias with GET /resolve/{alias}. A key is optional.
// A keyed caller gets a higher tier. The response Meta exposes the
// X-RateLimit-* and X-Quota-* headers.
func (c *Client) Resolve(ctx context.Context, aliasURI string) (*ResolutionResponse, error) {
	if aliasURI == "" {
		return nil, errEmptyArg
	}
	resp, err := c.get(ctx, "/resolve/"+pathSegment(aliasURI), nil, nil)
	if err != nil {
		return nil, err
	}
	return parseResolveResponse(resp)
}

// ResolveWithOptions resolves with POST /resolve, which adds amount,
// currency and preferred rail to the resolution context. The POST is sent
// once and never retried.
func (c *Client) ResolveWithOptions(ctx context.Context, r ResolveRequest) (*ResolutionResponse, error) {
	if r.AliasName == "" {
		return nil, errEmptyArg
	}
	body, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	resp, err := c.post(ctx, "/resolve", body, nil)
	if err != nil {
		return nil, err
	}
	return parseResolveResponse(resp)
}

// apiEnvelope is the {success, data, error_code, message} wrapper.
type apiEnvelope struct {
	Success   *bool           `json:"success"`
	Data      json.RawMessage `json:"data"`
	ErrorCode string          `json:"error_code"`
	Message   string          `json:"message"`
}

func parseResolveResponse(resp *response) (*ResolutionResponse, error) {
	if resp.status != http.StatusOK {
		return nil, newAPIError(resp.status, resp.header, resp.body)
	}
	data, err := unwrapEnvelope(resp)
	if err != nil {
		return nil, err
	}
	out, err := decodeResolution(data)
	if err != nil {
		return nil, err
	}
	out.Meta = resp.meta()
	return out, nil
}

// unwrapEnvelope returns the data object. A body with no "success" key is
// the data itself, as in the reference SDKs.
func unwrapEnvelope(resp *response) (json.RawMessage, error) {
	var env apiEnvelope
	if err := json.Unmarshal(resp.body, &env); err != nil {
		return nil, fmt.Errorf("dnsofmoney: decode response: %w", err)
	}
	if env.Success == nil {
		return resp.body, nil
	}
	if !*env.Success {
		return nil, &APIError{StatusCode: resp.status, Code: env.ErrorCode, Message: env.Message, Body: string(resp.body), Meta: resp.meta()}
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return resp.body, nil
	}
	return env.Data, nil
}

func decodeResolution(data json.RawMessage) (*ResolutionResponse, error) {
	var out ResolutionResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("dnsofmoney: decode resolution: %w", err)
	}
	out.Raw = append(json.RawMessage(nil), data...)
	return &out, nil
}
