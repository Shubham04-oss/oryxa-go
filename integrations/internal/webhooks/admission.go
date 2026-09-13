package webhooks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
)

var (
	ErrRouteNotFound     = errors.New("webhook route not found")
	ErrPayloadTooLarge   = errors.New("webhook payload too large")
	ErrTimestampInvalid  = errors.New("webhook timestamp invalid")
	ErrSignatureInvalid  = errors.New("webhook signature invalid")
	ErrRoutingIdentifier = errors.New("webhook routing identifier missing")
	ErrQueueUnavailable  = errors.New("webhook queue unavailable")
)

type Routes interface {
	WebhookRoute(context.Context, string) (domain.WebhookRoute, error)
}

type SecretBox interface {
	Open(context.Context, secrets.Binding, domain.SealedData) ([]byte, error)
}

type Runner interface {
	VerifyAndParse(context.Context, RunnerRequest) (RunnerResult, error)
}

type Claims interface {
	Acquire(context.Context, string, time.Time, time.Time, time.Time) (token string, acquired bool, err error)
	Commit(context.Context, string, string) error
	Release(context.Context, string, string) error
}

type Queue interface {
	EnqueueWebhook(context.Context, Envelope) error
}

type Request struct {
	OpaqueRoute string
	Method      string
	Headers     map[string]string
	Query       map[string]string
	Body        any
	RawBody     []byte
}

type RunnerRequest struct {
	InvocationID  string
	Route         domain.WebhookRoute
	Method        string
	Headers       map[string]string
	Query         map[string]string
	Body          any
	RawBody       []byte
	SigningSecret []byte
}

type Reply struct {
	Status  int
	Headers map[string]string
	Body    any
}

type RunnerResult struct {
	Verified        bool
	Reply           *Reply
	Event           string
	IdentifierValue string
	ProviderEventID string
}

type Envelope struct {
	InvocationID    string            `json:"invocation_id"`
	DeliveryID      string            `json:"delivery_id"`
	OrganizationID  string            `json:"organization_id"`
	ProfileID       string            `json:"profile_id"`
	ConnectionID    string            `json:"connection_id,omitempty"`
	Provider        string            `json:"provider"`
	PieceName       string            `json:"piece_name"`
	PieceVersion    string            `json:"piece_version"`
	TriggerName     string            `json:"trigger_name"`
	Event           string            `json:"event,omitempty"`
	IdentifierValue string            `json:"identifier_value,omitempty"`
	Method          string            `json:"method"`
	Headers         map[string]string `json:"headers"`
	Query           map[string]string `json:"query"`
	Body            any               `json:"body"`
	RawBody         []byte            `json:"raw_body"`
	ReceivedAt      time.Time         `json:"received_at"`
}

type Decision struct {
	Status    int
	Headers   map[string]string
	Body      any
	Accepted  bool
	Duplicate bool
	Warning   error
}

type Service struct {
	Routes         Routes
	Secrets        SecretBox
	Runner         Runner
	Claims         Claims
	Queue          Queue
	Now            func() time.Time
	IDs            func(string) (string, error)
	DefaultMaxBody int64
	ClaimLease     time.Duration
	DedupeTTL      time.Duration
}

func (s Service) Admit(ctx context.Context, request Request) (Decision, error) {
	route, err := s.Routes.WebhookRoute(ctx, request.OpaqueRoute)
	if err != nil {
		return Decision{Status: 404}, fmt.Errorf("%w: %v", ErrRouteNotFound, err)
	}
	if route.OpaqueRoute != request.OpaqueRoute {
		return Decision{Status: 404}, ErrRouteNotFound
	}
	if !route.Active {
		return Decision{Status: 202, Accepted: true}, nil
	}
	maxBody := route.MaxBodyBytes
	if maxBody == 0 {
		maxBody = s.DefaultMaxBody
	}
	if maxBody == 0 {
		maxBody = 256 * 1024
	}
	if int64(len(request.RawBody)) > maxBody {
		return Decision{Status: 413}, ErrPayloadTooLarge
	}
	headers := normalizedHeaders(request.Headers)
	if err := verifyTimestamp(headers, route.TimestampHeader, route.TimestampWindow, s.now()); err != nil {
		return Decision{Status: 401}, err
	}
	signingSecret, err := s.Secrets.Open(ctx, webhookBinding(route), route.SigningSecret)
	if err != nil {
		return Decision{Status: 503}, fmt.Errorf("open webhook signing secret: %w", err)
	}
	defer wipe(signingSecret)
	invocationID, err := s.newID("whi")
	if err != nil {
		return Decision{Status: 503}, err
	}
	result, err := s.Runner.VerifyAndParse(ctx, RunnerRequest{
		InvocationID:  invocationID,
		Route:         route,
		Method:        request.Method,
		Headers:       cloneMap(headers),
		Query:         cloneMap(request.Query),
		Body:          request.Body,
		RawBody:       append([]byte(nil), request.RawBody...),
		SigningSecret: signingSecret,
	})
	if err != nil {
		return Decision{Status: 503}, fmt.Errorf("verify webhook with piece runner: %w", err)
	}
	if !result.Verified {
		return Decision{Status: 401}, ErrSignatureInvalid
	}
	if result.Reply != nil {
		status := result.Reply.Status
		if status == 0 {
			status = 200
		}
		return Decision{Status: status, Headers: cloneMap(result.Reply.Headers), Body: result.Reply.Body, Accepted: true}, nil
	}
	if route.Mode == domain.WebhookRouteAppLevel && result.IdentifierValue == "" {
		return Decision{Status: 422}, ErrRoutingIdentifier
	}
	claimKey := dedupeKey(route, result, request.RawBody)
	lease := s.ClaimLease
	if lease == 0 {
		lease = 30 * time.Second
	}
	ttl := s.DedupeTTL
	if ttl == 0 {
		ttl = 24 * time.Hour
	}
	now := s.now()
	claimToken, acquired, err := s.Claims.Acquire(ctx, claimKey, now, now.Add(lease), now.Add(ttl))
	if err != nil {
		return Decision{Status: 503}, fmt.Errorf("claim webhook delivery: %w", err)
	}
	if !acquired {
		return Decision{Status: 202, Accepted: true, Duplicate: true}, nil
	}
	envelope := Envelope{
		InvocationID:    invocationID,
		DeliveryID:      claimKey,
		OrganizationID:  route.OrganizationID,
		ProfileID:       route.ProfileID,
		ConnectionID:    route.ConnectionID,
		Provider:        route.Provider,
		PieceName:       route.PieceName,
		PieceVersion:    route.PieceVersion,
		TriggerName:     route.TriggerName,
		Event:           result.Event,
		IdentifierValue: result.IdentifierValue,
		Method:          request.Method,
		Headers:         headers,
		Query:           cloneMap(request.Query),
		Body:            request.Body,
		RawBody:         append([]byte(nil), request.RawBody...),
		ReceivedAt:      now,
	}
	if err := s.Queue.EnqueueWebhook(ctx, envelope); err != nil {
		releaseErr := s.Claims.Release(ctx, claimKey, claimToken)
		return Decision{Status: 503}, errors.Join(fmt.Errorf("%w: %v", ErrQueueUnavailable, err), releaseErr)
	}
	decision := Decision{Status: 202, Accepted: true}
	if err := s.Claims.Commit(ctx, claimKey, claimToken); err != nil {
		// The payload is already durably queued. A provider retry is more likely
		// to duplicate work than recover anything, so acknowledge and surface a
		// warning for telemetry/reconciliation.
		decision.Warning = fmt.Errorf("commit webhook dedupe claim after enqueue: %w", err)
	}
	return decision, nil
}

func verifyTimestamp(headers map[string]string, headerName string, window time.Duration, now time.Time) error {
	if headerName == "" {
		return nil
	}
	value := headers[strings.ToLower(headerName)]
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return ErrTimestampInvalid
	}
	if window == 0 {
		window = 5 * time.Minute
	}
	delta := now.Sub(time.Unix(seconds, 0))
	if delta < 0 {
		delta = -delta
	}
	if delta > window {
		return ErrTimestampInvalid
	}
	return nil
}

func dedupeKey(route domain.WebhookRoute, result RunnerResult, rawBody []byte) string {
	material := route.OpaqueRoute + "\x00" + result.IdentifierValue + "\x00"
	if result.ProviderEventID != "" {
		material += "event:" + result.ProviderEventID
	} else {
		digest := sha256.Sum256(rawBody)
		material += "body:" + hex.EncodeToString(digest[:])
	}
	digest := sha256.Sum256([]byte(material))
	return "webhook:" + hex.EncodeToString(digest[:])
}

func webhookBinding(route domain.WebhookRoute) secrets.Binding {
	return secrets.Binding{
		OrganizationID: route.OrganizationID,
		ResourceType:   "webhook-route",
		ResourceID:     route.OpaqueRoute,
	}
}

func normalizedHeaders(headers map[string]string) map[string]string {
	result := make(map[string]string, len(headers))
	for key, value := range headers {
		result[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	return result
}

func cloneMap(values map[string]string) map[string]string {
	if values == nil {
		return map[string]string{}
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s Service) newID(prefix string) (string, error) {
	if s.IDs == nil {
		return "", fmt.Errorf("ID generator is required")
	}
	return s.IDs(prefix)
}

func wipe(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
