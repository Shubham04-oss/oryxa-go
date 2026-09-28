package integration

import "time"

// Webhook is how an app's deliveries are verified before anything in them is
// used. The core does all of it, in hooks; an integration only parses what
// has passed.
type Webhook struct {
	// Scope says whose endpoint and secret a delivery arrives on.
	Scope  string `json:"scope"`
	Verify Verify `json:"verify"`
	// DeliveryID is what deliveries are deduped on, for 7 days.
	DeliveryID Locator `json:"delivery_id"`
	Handshake  string  `json:"handshake,omitempty"`
}

// Webhook scopes: whose endpoint and secret a delivery arrives on.
const (
	// HookScopeConnection is one endpoint per subscription, whose secret the
	// core generates and hands to Pusher.Subscribe: GitHub's hook secret,
	// Google's channel token, Graph's clientState.
	HookScopeConnection = "connection"
	// HookScopeOrganization is an endpoint whose secret an organization admin
	// enters, such as their own Slack app's signing secret.
	HookScopeOrganization = "organization"
	// HookScopeDeployment is one endpoint for the whole deployment, whose
	// secret lives in Secrets Manager: the app delivers every account's
	// events to it, and they are routed by account_ref.
	HookScopeDeployment = "deployment"
)

// Verification schemes. The core adds a scheme here, once, with its
// conformance tests in hooks.
const (
	// VerifyHMACSHA256 checks an HMAC-SHA256 of Signed, keyed with the
	// endpoint's secret, against Signature.
	VerifyHMACSHA256 = "hmac_sha256"
	// VerifyStaticToken checks Token against the endpoint's secret.
	VerifyStaticToken = "static_token"
	// VerifyJWTPush checks the Bearer JWT in the Authorization header with
	// go-oidc: signed by Issuer, for Audience, and, when Email is set, for
	// that verified account.
	VerifyJWTPush = "jwt_push"
)

// Handshakes: requests an app sends to prove an endpoint, which the core
// answers itself and never passes to Parse.
const (
	// HandshakeSlackURLVerification answers Slack's signed url_verification
	// with its challenge.
	HandshakeSlackURLVerification = "slack_url_verification"
	// HandshakeGitHubPing acknowledges the signed ping GitHub sends when a
	// hook is created.
	HandshakeGitHubPing = "github_ping"
	// HandshakeGraphValidation echoes Microsoft Graph's unsigned
	// validationToken, only for an endpoint created in the last ten minutes
	// that has not validated yet.
	HandshakeGraphValidation = "graph_validation"
	// HandshakeGoogleChannelSync acknowledges the sync message (message
	// number 1) that opens every Google push channel.
	HandshakeGoogleChannelSync = "google_channel_sync"
	// HandshakeNotionVerification takes Notion's one unsigned
	// {"verification_token": "…"} POST, sent when a subscription is created
	// in the integration's settings. The token is the subscription's signing
	// secret: the core accepts it only while the endpoint has no secret yet,
	// keeps it sealed for an admin to paste into Notion's Verify form, and
	// never logs it.
	HandshakeNotionVerification = "notion_verification"
)

// Signature encodings.
const (
	EncodingHex    = "hex"
	EncodingBase64 = "base64"
)

// Timestamp units.
const (
	UnitSeconds      = "s"
	UnitMilliseconds = "ms"
)

// DefaultToleranceSeconds is how far a delivery's timestamp may be from now
// when the manifest reads a timestamp and sets no tolerance.
const DefaultToleranceSeconds = 300

// Verify is one verification scheme and what it reads.
type Verify struct {
	Type      string   `json:"type"`
	Signature *Locator `json:"signature,omitempty"`
	Encoding  string   `json:"encoding,omitempty"`
	// Signed is what the signature covers, built from {body}, {timestamp}
	// and {id} (the delivery id): "{body}", "v0:{timestamp}:{body}",
	// "{id}.{timestamp}.{body}".
	Signed    string   `json:"signed,omitempty"`
	Timestamp *Locator `json:"timestamp,omitempty"`
	// Tolerance defaults to DefaultToleranceSeconds when a timestamp is set.
	Tolerance int `json:"tolerance_seconds,omitempty"`
	// Token is the static_token, checked against the sealed secret.
	Token *Locator `json:"token,omitempty"`
	// Issuer, Audience and Email are what a jwt_push token must say.
	// Audience may use {endpoint_url}; Email may use {config:name}, a value
	// from the deployment's configuration.
	Issuer   string `json:"issuer,omitempty"`
	Audience string `json:"audience,omitempty"`
	Email    string `json:"email,omitempty"`
}

// EffectiveTolerance is how far a delivery's timestamp may be from now. It is
// zero, and nothing is checked, when the scheme reads no timestamp.
func (v Verify) EffectiveTolerance() time.Duration {
	switch {
	case v.Timestamp == nil:
		return 0
	case v.Tolerance > 0:
		return time.Duration(v.Tolerance) * time.Second
	default:
		return DefaultToleranceSeconds * time.Second
	}
}

// Locator is where in a delivery a value is: exactly one of a header, a JSON
// pointer into the body, or, for a delivery id, a hash of the body.
type Locator struct {
	Header string `json:"header,omitempty"`
	// JSON is an RFC 6901 pointer. In a token locator, a "*" token means
	// every element of an array, and every one must match.
	JSON string `json:"json,omitempty"`
	// Hash is the SHA-256 of the body, for payloads without an id.
	Hash bool `json:"hash,omitempty"`
	// Prefix comes before the value, and is not part of it: "sha256=".
	Prefix string `json:"prefix,omitempty"`
	// Split separates several signatures, as during a secret's rotation.
	Split string `json:"split,omitempty"`
	// Unit is a timestamp's: s or ms.
	Unit string `json:"unit,omitempty"`
}
