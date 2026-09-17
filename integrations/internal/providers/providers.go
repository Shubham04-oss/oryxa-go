// Package providers describes how each provider delivers events to
// integrations: through one URL on the provider's app, or through a webhook
// integrations registers per subscription; how a delivery is verified; and
// what a verified one holds. A provider is one file returning a Provider,
// registered in Builtin.
package providers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

// ErrUnverified means a delivery's signature, or other proof that the
// provider sent it, is missing or wrong, or the delivery is too old.
var ErrUnverified = errors.New("delivery could not be verified")

type Delivery string

const (
	// DeliveryApp providers send every event for one of their apps to a URL
	// set on the app, signed with the app's secret, and integrations routes
	// each event to subscriptions by account, as Slack's Events API does.
	DeliveryApp Delivery = "app"
	// DeliveryWebhook providers deliver to a URL and secret integrations
	// registers for each subscription, as GitHub repository webhooks and
	// Microsoft Graph subscriptions do.
	DeliveryWebhook Delivery = "webhook"
)

// Provider is how one provider delivers events.
type Provider struct {
	Name     string
	Delivery Delivery
	// Target checks what a subscription watches and the events it asks for,
	// already bounded by domain.CheckTarget, and returns them as stored.
	Target func(target map[string]string, events []string) (map[string]string, []string, error)
	// AccountKey is what app deliveries for a connection's provider account
	// are routed by. DeliveryApp only.
	AccountKey func(account domain.ProviderAccount) (string, error)
	// Register creates what delivers a subscription's events to
	// request.CallbackURL. DeliveryWebhook only.
	Register func(ctx context.Context, client *http.Client, request RegisterRequest) (Registration, error)
	// Renew extends a registration before it expires. Nil when registrations
	// do not expire.
	Renew func(ctx context.Context, client *http.Client, request RenewRequest) (Registration, error)
	// Unregister removes a registration; one the provider no longer has is
	// not an error. DeliveryWebhook only.
	Unregister func(ctx context.Context, client *http.Client, request UnregisterRequest) error
	// Receive verifies a delivery with the secret and reads it. A delivery that
	// cannot be verified returns an error wrapping ErrUnverified, and nothing
	// in it may be used.
	Receive func(delivery Delivered, secret []byte, now time.Time) (Reception, error)

	// TargetFields describe what Target takes, for a caller offering
	// subscriptions to people.
	TargetFields []TargetField
	// Events describe what a subscription may ask for.
	Events EventsDescription
	// Actions are what a caller may do with a connection to this provider.
	Actions []Action
	// Skill is how the provider's tools and events are meant to be used, as
	// markdown, carried into an agent's prompt with its tools.
	Skill string
}

// Action finds one of the provider's actions.
func (provider Provider) Action(name string) (Action, bool) {
	for _, action := range provider.Actions {
		if action.Name == name {
			return action, true
		}
	}
	return Action{}, false
}

// Registry maps provider names to providers.
type Registry map[string]Provider

// Builtin returns the providers integrations delivers events for. Each call
// returns fresh values, so a caller may point one at a test server.
func Builtin() Registry {
	registry := Registry{}
	for _, provider := range []Provider{GitHub(), HubSpot(), Linear(), Microsoft(), Slack()} {
		registry[provider.Name] = provider
	}
	return registry
}

type RegisterRequest struct {
	Credential domain.Credential
	Target     map[string]string
	Events     []string
	// CallbackURL is the subscription's webhook URL, and Secret what the
	// provider signs or marks its deliveries with.
	CallbackURL string
	Secret      string
	Now         time.Time
}

// Registration is what a provider created for a subscription.
type Registration struct {
	// ID is the provider's ID for it, such as a GitHub hook ID.
	ID string
	// ExpiresAt is when the provider stops delivering unless it is renewed;
	// zero when it does not expire.
	ExpiresAt time.Time
	// Secret is the signing secret the provider issued for this registration,
	// where the provider chooses it rather than taking the one the request
	// carried: ClickUp answers a create with the webhook's own secret, and
	// Asana hands one over during the handshake and signs every later delivery
	// with it. The caller stores this in place of the secret it generated, so
	// Receive is given the value the provider actually signs with. Empty means
	// the request's Secret is what the provider signs with, as GitHub does.
	Secret string
}

type RenewRequest struct {
	Credential     domain.Credential
	RegistrationID string
	Target         map[string]string
	Events         []string
	Now            time.Time
}

type UnregisterRequest struct {
	Credential     domain.Credential
	RegistrationID string
	Target         map[string]string
}

// Delivered is a delivery as it arrived. Nothing in it is trusted until
// Receive verifies it.
type Delivered struct {
	Header http.Header
	Query  url.Values
	Body   []byte
	// Method and URI are the request line as the provider sent it, which a
	// signature that covers the request rather than only its body needs:
	// HubSpot signs method + URI + body + timestamp. URI is the path with its
	// query, still percent-encoded, so a provider's own encoding is preserved
	// rather than re-serialized from Query.
	Method string
	URI    string
}

// Reception is what a verified delivery holds.
type Reception struct {
	// Reply, when set, is the answer the provider needs, such as a handshake
	// echoing a challenge, or the status it expects. Otherwise the delivery
	// is answered 200 OK. Events and Lifecycle are handled either way.
	Reply *Reply
	// AccountKeys are the accounts an app delivery is for, such as the Slack
	// workspaces whose installation it reached. DeliveryApp only.
	AccountKeys []string
	Events      []Event
	// Lifecycle is what the provider said about the registration itself.
	Lifecycle Lifecycle
}

type Reply struct {
	Status      int
	ContentType string
	Body        []byte
}

// Event is one thing that happened, as a provider reported it.
type Event struct {
	// Name is the provider's name for it, without the provider's own name:
	// issues.opened for GitHub.
	Name string
	// DeliveryID is the provider's ID for the delivery, when it gives one.
	DeliveryID string
	// Key tells a repeated delivery of the event from a new one: the delivery
	// ID when the provider gives one, otherwise a digest of the event.
	Key string
	// Type is what a subscription's events match for app deliveries, such as
	// Slack's event type. Empty means Name.
	Type string
	// RegistrationID names the registration a delivery says it came through,
	// when it says, so one meant for another registration is ignored.
	RegistrationID string
	Resource       integrationsv1.Resource
	Actor          *integrationsv1.Actor
	// OccurredAt is when it happened, when the provider says; zero means when
	// it was received.
	OccurredAt time.Time
	// Payload is what the provider sent about the event.
	Payload json.RawMessage
}

// Lifecycle is news about a registration rather than an event.
type Lifecycle string

const (
	LifecycleNone Lifecycle = ""
	// LifecycleRenew asks for the registration to be renewed now, as Graph's
	// reauthorizationRequired does.
	LifecycleRenew Lifecycle = "renew"
	// LifecycleRemoved says the provider removed the registration.
	LifecycleRemoved Lifecycle = "removed"
)

// Authorize puts a credential on a request the way connections said to send
// it: a header, or a query parameter.
func Authorize(request *http.Request, credential domain.Credential) {
	if credential.In == "query" {
		query := request.URL.Query()
		query.Set(credential.Name, credential.Prefix+credential.AccessToken)
		request.URL.RawQuery = query.Encode()
		return
	}
	name := credential.Name
	if name == "" {
		name = "Authorization"
	}
	request.Header.Set(name, credential.Prefix+credential.AccessToken)
}

// NewHTTPClient is the client providers are called with: bounded, and never
// following a redirect with a credential on it.
func NewHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// maxResponseBytes bounds what integrations reads of a provider's answer.
const maxResponseBytes = 1 << 20

// sendJSON sends body as JSON with a credential and decodes a JSON answer into
// target when target is not nil. Errors wrap StatusError's domain errors and
// never carry what the provider answered.
func sendJSON(ctx context.Context, client *http.Client, method, endpoint string, credential domain.Credential, body, target any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode provider request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("%w: provider request could not be built", domain.ErrProviderUnavailable)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	Authorize(request, credential)
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: provider request failed", domain.ErrProviderUnavailable)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(payload) > maxResponseBytes {
		return fmt.Errorf("%w: provider response could not be read", domain.ErrProviderUnavailable)
	}
	if err := StatusError(response.StatusCode); err != nil {
		return err
	}
	if target != nil && len(payload) > 0 && json.Unmarshal(payload, target) != nil {
		return fmt.Errorf("%w: provider response is not valid", domain.ErrProviderUnavailable)
	}
	return nil
}

// StatusError is what a provider's status means: nil for success; the
// credential refused, the operation forbidden, or nothing found; the provider
// unavailable, for a time-out, throttling or its own failure; otherwise a
// request it would not take.
func StatusError(status int) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusUnauthorized:
		return fmt.Errorf("%w: provider returned %d", domain.ErrCredentialRejected, status)
	case status == http.StatusForbidden:
		return fmt.Errorf("%w: provider returned %d", domain.ErrForbidden, status)
	case status == http.StatusNotFound || status == http.StatusGone:
		return fmt.Errorf("%w: provider returned %d", domain.ErrNotFound, status)
	case status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500:
		return fmt.Errorf("%w: provider returned %d", domain.ErrProviderUnavailable, status)
	default:
		return fmt.Errorf("%w: provider returned %d", domain.ErrInvalid, status)
	}
}

// ValidHMACSHA256 reports whether signature is the hex HMAC-SHA256 of message
// under secret, compared in constant time.
func ValidHMACSHA256(secret, message []byte, signature string) bool {
	expected, err := hex.DecodeString(signature)
	if err != nil || len(expected) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(message)
	return hmac.Equal(mac.Sum(nil), expected)
}

// withoutFields is a JSON object without the named top-level fields, for a
// payload that carries a secret. What is not an object is left out entirely.
func withoutFields(raw []byte, names ...string) json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	for _, name := range names {
		delete(fields, name)
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil
	}
	return encoded
}

// Digest is a key for a delivery the provider gives no ID for.
func Digest(parts ...[]byte) string {
	hash := sha256.New()
	for _, part := range parts {
		var length [8]byte
		for index := range length {
			length[index] = byte(uint64(len(part)) >> (8 * index))
		}
		hash.Write(length[:])
		hash.Write(part)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}
