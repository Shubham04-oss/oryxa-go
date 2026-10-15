// Package manifest holds the connector manifest (connectors/<app>/manifest.json)
// and its validator. The format follows oryxa-platform's connector manifests,
// with the names and additions in docs/design/integrations/README.md §8.2.
package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Manifest describes one connector: how members sign in, which tools it
// offers and which streams it syncs.
type Manifest struct {
	ID               string          `json:"id"`
	Version          int             `json:"version"`
	DisplayName      string          `json:"display_name"`
	Description      string          `json:"description,omitempty"`
	ConnectionLevels []string        `json:"connection_levels"`
	Auth             Auth            `json:"auth"`
	Config           json.RawMessage `json:"config,omitempty"` // JSON Schema for per-connection settings
	BaseURL          string          `json:"base_url"`
	EgressAllowlist  []string        `json:"egress_allowlist"`
	Verify           *Verify         `json:"verify,omitempty"`
	RateLimit        *RateLimit      `json:"rate_limit,omitempty"`
	Tools            []Tool          `json:"tools"`
	Streams          []Stream        `json:"streams,omitempty"`
	Webhooks         *Webhooks       `json:"webhooks,omitempty"`
	ReauthOn         []ReauthSignal  `json:"reauth_on,omitempty"`
}

// Auth lists how a connection to the app can be made.
type Auth struct {
	CredentialModes []string   `json:"credential_modes"`
	OAuth2          *OAuth2    `json:"oauth2,omitempty"`
	UserKey         *UserKey   `json:"user_key,omitempty"`
	GitHubApp       *GitHubApp `json:"github_app,omitempty"`
}

// OAuth2 is the app's OAuth 2 authorization-code flow.
type OAuth2 struct {
	AuthorizationURL    string            `json:"authorization_url"`
	TokenURL            string            `json:"token_url"`
	RevokeURL           string            `json:"revoke_url,omitempty"`
	PKCE                bool              `json:"pkce"`
	ScopeSeparator      string            `json:"scope_separator,omitempty"`
	AuthorizationParams map[string]string `json:"authorization_params,omitempty"`
	TokenKinds          []TokenKind       `json:"token_kinds"`
	Refresh             *Refresh          `json:"refresh,omitempty"`
	Outputs             []Output          `json:"outputs,omitempty"`
}

// TokenKind is one token a sign-in yields (Slack: bot, user).
type TokenKind struct {
	Name          string   `json:"name"`
	Scopes        []string `json:"scopes"`
	ActsAs        string   `json:"acts_as,omitempty"`
	PersonalGrant bool     `json:"personal_grant,omitempty"`
	Optional      bool     `json:"optional,omitempty"`
	ScopeParam    string   `json:"scope_param,omitempty"`
	TokenPointer  string   `json:"token_pointer,omitempty"` // JSON pointer into the token response
}

// Refresh describes how the app's tokens are renewed.
type Refresh struct {
	Rotating     bool `json:"rotating"`
	AheadSeconds int  `json:"ahead_seconds,omitempty"`
	ReuseWindowS int  `json:"reuse_window_s,omitempty"`
}

// Output is a value read from the token response, or a follow-up request, that
// URLs and tools need later (Jira's cloud id).
type Output struct {
	Name     string        `json:"name"`
	Pointer  string        `json:"pointer"`
	Optional bool          `json:"optional,omitempty"`
	Request  string        `json:"request,omitempty"`
	Select   *OutputSelect `json:"select,omitempty"`
}

// OutputSelect picks one element of a list response.
type OutputSelect struct {
	Pointer string `json:"pointer"`
	Equals  string `json:"equals"`
}

// UserKey is a personal API key or token the member pastes in.
type UserKey struct {
	Fields        json.RawMessage `json:"fields"` // JSON Schema of the form
	BaseURL       string          `json:"base_url,omitempty"`
	Inject        Inject          `json:"inject"`
	PersonalGrant bool            `json:"personal_grant,omitempty"`
}

// Inject says how the proxy puts the key on a request.
type Inject struct {
	Header string `json:"header"`
	Value  string `json:"value"`
}

// GitHubApp holds what the GitHub App manifest flow needs.
type GitHubApp struct {
	ManifestTemplate string `json:"manifest_template"`
}

// Verify is the cheap call that tests a new connection.
type Verify struct {
	Path      string `json:"path"`
	TokenKind string `json:"token_kind,omitempty"`
}

// RateLimit tells the shared limiter how the app limits calls.
type RateLimit struct {
	Scope          string           `json:"scope,omitempty"` // app | connection | user
	PerHour        int              `json:"per_hour,omitempty"`
	MaxConcurrency int              `json:"max_concurrency,omitempty"`
	Headers        RateLimitHeaders `json:"headers"`
}

// RateLimitHeaders names the response headers that carry the app's own limit
// signals; empty names are not used.
type RateLimitHeaders struct {
	Remaining  string `json:"remaining,omitempty"`
	Reset      string `json:"reset,omitempty"`
	RetryAfter string `json:"retry_after,omitempty"`
}

// Tool is one action agents can run, as <app>.<action>.
type Tool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Effect       string          `json:"effect"` // read | write | external | delete
	InputSchema  json.RawMessage `json:"input_schema"`
	OutputSchema json.RawMessage `json:"output_schema"`
	Scopes       []string        `json:"scopes,omitempty"`
	TokenKind    string          `json:"token_kind,omitempty"`
}

// Stream is one kind of record the connector syncs.
type Stream struct {
	Name      string    `json:"name"`
	Modes     []string  `json:"modes"` // full | incremental | webhook
	Schedule  string    `json:"schedule,omitempty"`
	Partition string    `json:"partition,omitempty"`
	Deletes   string    `json:"deletes,omitempty"` // feed | field | webhook | reconcile
	Lookback  string    `json:"lookback,omitempty"`
	Document  *Document `json:"document,omitempty"`
	ACL       string    `json:"acl,omitempty"` // where the ACL comes from
}

// Document maps a record to the fields knowledge indexes, as JSON pointers.
type Document struct {
	Title     string `json:"title"`
	Body      string `json:"body,omitempty"`
	URL       string `json:"url,omitempty"`
	Author    string `json:"author,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// Webhooks says how the app's deliveries are verified and routed.
type Webhooks struct {
	Verification     Verification            `json:"verification"`
	Routing          string                  `json:"routing"` // org_app: the URL names the org's app
	RouteBy          []string                `json:"route_by,omitempty"`
	RouteHeader      string                  `json:"route_header,omitempty"`
	DeliveryIDHeader string                  `json:"delivery_id_header,omitempty"`
	ReplayWindowS    int                     `json:"replay_window_s,omitempty"`
	Subscription     *Subscription           `json:"subscription,omitempty"`
	Events           map[string]WebhookEvent `json:"events,omitempty"`
	FallbackPollS    int                     `json:"fallback_poll_s,omitempty"`
}

// Verification names the scheme that proves a delivery came from the app.
type Verification struct {
	Scheme          string `json:"scheme"`
	Header          string `json:"header,omitempty"`
	SignatureHeader string `json:"signature_header,omitempty"`
	TimestampHeader string `json:"timestamp_header,omitempty"`
	Basestring      string `json:"basestring,omitempty"`
	Prefix          string `json:"prefix,omitempty"`
}

// Subscription is a webhook subscription the app expires and we renew.
type Subscription struct {
	MaxTTLS int     `json:"max_ttl_s"`
	RenewAt float64 `json:"renew_at"` // fraction of the lifetime, 0 < x < 1
}

// WebhookEvent maps one app event to a stream re-fetch, a trigger, or both.
type WebhookEvent struct {
	Stream   string   `json:"stream,omitempty"`
	ID       string   `json:"id,omitempty"` // JSON pointer to the object id in the payload
	DeleteOn []string `json:"delete_on,omitempty"`
	Trigger  string   `json:"trigger,omitempty"`
}

// ReauthSignal is what the app answers when a token is dead: an HTTP status
// (401) or an error code (invalid_auth).
type ReauthSignal struct {
	Status int
	Code   string
}

// UnmarshalJSON accepts a number or a string.
func (r *ReauthSignal) UnmarshalJSON(b []byte) error {
	var status int
	if err := json.Unmarshal(b, &status); err == nil {
		*r = ReauthSignal{Status: status}
		return nil
	}
	var code string
	if err := json.Unmarshal(b, &code); err != nil {
		return errors.New("reauth_on entries are HTTP statuses or error codes")
	}
	*r = ReauthSignal{Code: code}
	return nil
}

// MarshalJSON writes the number or the string back.
func (r ReauthSignal) MarshalJSON() ([]byte, error) {
	if r.Code != "" {
		return json.Marshal(r.Code)
	}
	return json.Marshal(r.Status)
}

// Parse decodes a manifest, refusing unknown fields, and validates it. The
// error lists every problem found.
func Parse(data []byte) (*Manifest, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("manifest: trailing data after the manifest")
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}
