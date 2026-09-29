package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
)

// ingress takes every app's deliveries, at POST /v1/apps/hooks/{endpointID}.
// With an alias it is instead a surface's static URL, which names its
// endpoint by toolkit and not by id.
type ingress struct {
	service *Service
	alias   *alias
}

// ServeHTTP runs the plan's twelve steps in order. A delivery that fails
// verification is answered 401 and nothing in it is used or logged. Anything
// that went wrong on Oryxa's side is answered 5xx, so the app delivers again.
func (h ingress) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	s := h.service
	ctx := request.Context()
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "deliveries are POSTed")
		return
	}
	now := s.now().UTC()
	if h.alias != nil {
		// A surface's app waits a few seconds for its answer and delivers
		// again when it does not get one. Everything from here on that
		// touches the database does so inside a deadline of its own, which
		// leaves room to answer, and nothing on the way calls anything else.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.surfaceDeadline)
		defer cancel()
	}

	// 1. The endpoint, by the id in the path, or a surface's by its toolkit.
	// An unknown one is refused before its body is read.
	endpoint, ok := h.lookup(ctx, writer, request, now)
	if !ok {
		return
	}
	log := s.logger.With("endpoint_id", endpoint.ID, "toolkit", endpoint.Toolkit)

	// 2. The endpoint's own rate.
	if !s.limiter.allow(endpoint.ID) {
		log.WarnContext(ctx, "webhook endpoint rate limited")
		writer.Header().Set("Retry-After", "1")
		writeError(writer, http.StatusTooManyRequests, "rate_limited", "too many requests; retry")
		return
	}

	// 3. The body, at most MaxBodyBytes.
	body, err := io.ReadAll(io.LimitReader(request.Body, MaxBodyBytes+1))
	switch {
	case err != nil:
		writeError(writer, http.StatusBadRequest, "invalid_request", "the delivery could not be read")
		return
	case len(body) > MaxBodyBytes:
		writeError(writer, http.StatusRequestEntityTooLarge, "invalid_request", "the delivery is too large")
		return
	}
	d := newDelivery(request.Header, body)

	// 4. The unsigned handshakes, each in its narrow window: Microsoft
	// Graph's validation, and the verification token Notion sends once.
	if s.unsignedHandshake(ctx, writer, request, endpoint, d, now, log) {
		return
	}

	// 5. The signature or token.
	if err := s.verify(ctx, endpoint, d, now); err != nil {
		refused(ctx, writer, log, err)
		return
	}

	// 6. The timestamp, against the scheme's tolerance.
	if err := checkTolerance(endpoint.Scheme.Verify, d, now); err != nil {
		refused(ctx, writer, log, err)
		return
	}

	// 7. The signed handshakes: Slack's url_verification, GitHub's ping, and
	// the sync message that opens a Google channel.
	if s.signedHandshake(ctx, writer, endpoint, d, now, log) {
		return
	}

	// 8. The delivery's id, and whether it was handled already.
	key := "hook:" + endpoint.ID + ":" + deliveryID(endpoint.Scheme, d)
	handled, err := s.store.HookDeliveryHandled(ctx, key)
	if err != nil {
		failed(ctx, writer, log, err)
		return
	}
	if handled {
		log.InfoContext(ctx, "webhook delivery already handled")
		writer.WriteHeader(http.StatusOK)
		return
	}

	// 9. A surface's delivery goes to the surface, which records it and its
	// dedupe mark together; there is nothing to parse or route.
	if h.alias != nil {
		h.deliver(ctx, writer, endpoint, d, key, now, log)
		return
	}

	// 9. Otherwise, the integration's own reading of it.
	events, triggers, parsing, err := s.parse(ctx, endpoint, d)
	if err != nil {
		log.WarnContext(ctx, "an integration could not parse a verified delivery", "error", truncate(err.Error(), maxLoggedError))
		writeError(writer, http.StatusBadRequest, "invalid_request", "the delivery could not be parsed")
		return
	}
	if !parsing {
		log.InfoContext(ctx, "no integration parses this toolkit's deliveries yet; acknowledged and dropped")
	}

	// 10. Each event to the member whose connection it belongs to.
	routed, err := s.route(ctx, endpoint, triggers, events, now, log)
	if err != nil {
		failed(ctx, writer, log, err)
		return
	}

	// 11. The events and the delivery's mark, in one transaction.
	recorded, err := s.store.RecordHookDelivery(ctx, Delivery{Key: key, ReceivedAt: now, Events: routed})
	if err != nil {
		failed(ctx, writer, log, err)
		return
	}
	if recorded {
		log.InfoContext(ctx, "webhook delivery handled", "events", len(events), "published", len(routed))
	} else {
		log.InfoContext(ctx, "webhook delivery already handled")
	}

	// 12. Done.
	writer.WriteHeader(http.StatusOK)
}

// deliver hands a verified, unhandled delivery to its surface, and answers
// 200 once the surface has recorded it.
func (h ingress) deliver(ctx context.Context, writer http.ResponseWriter, endpoint Endpoint, d *delivery, key string, now time.Time, log *slog.Logger) {
	surface, ok := h.service.surface(endpoint.Toolkit)
	if !ok {
		writeError(writer, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	// The surface never sees the delivery's own credentials, as an
	// integration does not.
	header := d.header.Clone()
	header.Del("Authorization")
	header.Del("Cookie")
	if err := surface.Deliver(ctx, h.alias.kind, key, header, bytes.Clone(d.body), now); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			err = fmt.Errorf("%w: the delivery could not be recorded in time: %v", errUnavailable, err)
		}
		failed(ctx, writer, log, err)
		return
	}
	writer.WriteHeader(http.StatusOK)
}

// lookup finds the endpoint a request is for. A surface's alias asks for its
// toolkit's deployment endpoint, making it the first time; anything else is
// found by the id in the path. A malformed id is refused without asking the
// store, and a lapsed endpoint answers as an unknown one.
func (h ingress) lookup(ctx context.Context, writer http.ResponseWriter, request *http.Request, now time.Time) (Endpoint, bool) {
	s := h.service
	if h.alias != nil {
		if h.alias.toolkit == "" {
			writeError(writer, http.StatusNotFound, "not_found", "resource not found")
			return Endpoint{}, false
		}
		endpoint, _, err := s.DeploymentEndpoint(ctx, h.alias.toolkit)
		if err != nil {
			failed(ctx, writer, s.logger, err)
			return Endpoint{}, false
		}
		return endpoint, true
	}
	id := request.PathValue("endpointID")
	if id == "" {
		id, _ = strings.CutPrefix(request.URL.Path, PathPrefix)
	}
	if !idPattern.MatchString(id) {
		writeError(writer, http.StatusNotFound, "not_found", "resource not found")
		return Endpoint{}, false
	}
	endpoint, err := s.store.HookEndpoint(ctx, id)
	switch {
	case errors.Is(err, domain.ErrNotFound) || (err == nil && endpoint.expired(now)):
		writeError(writer, http.StatusNotFound, "not_found", "resource not found")
		return Endpoint{}, false
	case err != nil:
		failed(ctx, writer, s.logger, err)
		return Endpoint{}, false
	}
	// A surface is reached at its own URLs, which say what kind of delivery
	// each is. Its endpoint's id is no address.
	if _, isSurface := s.surface(endpoint.Toolkit); isSurface {
		writeError(writer, http.StatusNotFound, "not_found", "resource not found")
		return Endpoint{}, false
	}
	return endpoint, true
}

// verify checks a delivery's signature or token by its endpoint's scheme. A
// delivery that offers no signature or token at all is refused before any
// secret is opened.
func (s *Service) verify(ctx context.Context, endpoint Endpoint, d *delivery, now time.Time) error {
	scheme := endpoint.Scheme
	switch scheme.Verify.Type {
	case integration.VerifyHMACSHA256:
		check, err := prepareHMAC(scheme, d)
		if err != nil {
			return err
		}
		keys, err := s.keys(ctx, endpoint, now)
		if err != nil {
			return err
		}
		return check.match(keys)
	case integration.VerifyStaticToken:
		tokens, err := presentedTokens(scheme.Verify, d)
		if err != nil {
			return err
		}
		keys, err := s.keys(ctx, endpoint, now)
		if err != nil {
			return err
		}
		return matchTokens(tokens, keys)
	case integration.VerifyJWTPush:
		return s.verifyJWT(ctx, endpoint, d)
	}
	return refuse("the scheme is not one hooks knows")
}

// Bounds on what a handshake echoes back to the app.
const (
	maxValidationToken   = 1024
	maxChallenge         = 1024
	minVerificationToken = 8
	maxVerificationToken = 512
)

// unsignedHandshake answers the two requests an app sends before the
// endpoint can verify anything, each only in its narrow window. It says
// whether it answered.
func (s *Service) unsignedHandshake(ctx context.Context, writer http.ResponseWriter, request *http.Request, endpoint Endpoint, d *delivery, now time.Time, log *slog.Logger) bool {
	switch endpoint.Scheme.Handshake {
	case integration.HandshakeGraphValidation:
		query := request.URL.Query()
		if !query.Has("validationToken") {
			return false
		}
		// Graph echoes a token to prove the URL is ours, unsigned. Only a
		// new endpoint that has never validated answers it, and only once.
		token := query.Get("validationToken")
		if !printable(token, 1, maxValidationToken) || endpoint.ValidatedAt != nil ||
			!endpoint.CreatedAt.After(now.Add(-GraphValidationWindow)) {
			refused(ctx, writer, log, refuse("graph validation outside its window"))
			return true
		}
		validated, err := s.store.MarkHookValidated(ctx, endpoint.ID, now.Add(-GraphValidationWindow), now)
		if err != nil {
			failed(ctx, writer, log, err)
			return true
		}
		if !validated {
			refused(ctx, writer, log, refuse("graph validation outside its window"))
			return true
		}
		log.InfoContext(ctx, "webhook handshake answered", "handshake", integration.HandshakeGraphValidation)
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, token)
		return true

	case integration.HandshakeNotionVerification:
		// Notion sends the subscription's signing secret once, unsigned. It
		// is taken only while the endpoint has none; after that, every
		// delivery must be signed with it.
		if endpoint.Secret.Current != nil {
			return false
		}
		token, ok := verificationToken(d)
		if !ok {
			return false
		}
		sealed, err := s.seal(ctx, endpoint.ID, token)
		if err != nil {
			failed(ctx, writer, log, err)
			return true
		}
		captured, err := s.store.CaptureHookSecret(ctx, endpoint.ID, sealed, now)
		if err != nil {
			failed(ctx, writer, log, err)
			return true
		}
		if !captured {
			refused(ctx, writer, log, refuse("a verification token arrived for an endpoint that has one"))
			return true
		}
		log.InfoContext(ctx, "webhook verification token captured, for an admin to reveal once")
		writer.WriteHeader(http.StatusOK)
		return true
	}
	return false
}

// verificationToken reads Notion's verification request: a JSON object whose
// only member is verification_token.
func verificationToken(d *delivery) (string, bool) {
	document, _ := d.json()
	object, ok := document.(map[string]any)
	if !ok || len(object) != 1 {
		return "", false
	}
	token, ok := object["verification_token"].(string)
	return token, ok && printable(token, minVerificationToken, maxVerificationToken) && !strings.Contains(token, " ")
}

// signedHandshake answers a verified request that proves the endpoint rather
// than delivering an event. It says whether it answered.
func (s *Service) signedHandshake(ctx context.Context, writer http.ResponseWriter, endpoint Endpoint, d *delivery, now time.Time, log *slog.Logger) bool {
	handshake := endpoint.Scheme.Handshake
	switch handshake {
	case integration.HandshakeSlackURLVerification:
		document, _ := d.json()
		object, _ := document.(map[string]any)
		if object == nil || object["type"] != "url_verification" {
			return false
		}
		challenge, ok := object["challenge"].(string)
		if !ok || !printable(challenge, 1, maxChallenge) {
			writeError(writer, http.StatusBadRequest, "invalid_request", "the challenge is missing")
			return true
		}
		s.validated(ctx, endpoint, now, log)
		writeJSON(writer, http.StatusOK, map[string]string{"challenge": challenge})
		return true
	case integration.HandshakeGitHubPing:
		if d.header.Get("X-GitHub-Event") != "ping" {
			return false
		}
	case integration.HandshakeGoogleChannelSync:
		if d.header.Get("X-Goog-Resource-State") != "sync" {
			return false
		}
	default:
		return false
	}
	s.validated(ctx, endpoint, now, log)
	writer.WriteHeader(http.StatusOK)
	return true
}

// validated records an endpoint's first handshake. Failing to record it does
// not fail the handshake, which has already been proven.
func (s *Service) validated(ctx context.Context, endpoint Endpoint, now time.Time, log *slog.Logger) {
	log.InfoContext(ctx, "webhook handshake answered", "handshake", endpoint.Scheme.Handshake)
	if endpoint.ValidatedAt != nil {
		return
	}
	if _, err := s.store.MarkHookValidated(ctx, endpoint.ID, time.Time{}, now); err != nil {
		log.ErrorContext(ctx, "record a webhook endpoint's handshake", "error", err)
	}
}

// deliveryID is what a delivery is deduped on, as its scheme says where to
// find it. A delivery without one is known by its body's hash: it has been
// verified, and an identical body is the same delivery.
func deliveryID(scheme integration.Webhook, d *delivery) string {
	if !scheme.DeliveryID.Hash {
		if id, ok := d.one(scheme.DeliveryID); ok {
			return keyPart(id)
		}
	}
	return d.bodyHash()
}

// parse hands a verified delivery to its toolkit's integration, and answers
// the events it found and the triggers its manifest pushes. It answers false
// for a toolkit whose integration cannot parse deliveries yet: the delivery
// is acknowledged and recorded, and nothing is published. The integration
// never sees the delivery's Authorization or Cookie header.
func (s *Service) parse(ctx context.Context, endpoint Endpoint, d *delivery) ([]integration.Event, map[string]bool, bool, error) {
	registered, ok := s.integrations.Lookup(endpoint.Toolkit)
	if !ok {
		return nil, nil, false, nil
	}
	pusher, ok := registered.(integration.Pusher)
	if !ok {
		return nil, nil, false, nil
	}
	triggers := map[string]bool{}
	for _, trigger := range registered.Manifest().Triggers {
		if trigger.Kind == integration.TriggerPush {
			triggers[trigger.Slug] = true
		}
	}
	header := d.header.Clone()
	header.Del("Authorization")
	header.Del("Cookie")
	events, err := pusher.Parse(ctx, header, bytes.Clone(d.body))
	return events, triggers, true, err
}

// route makes each event an apps.trigger.received.v1 for each subscription it
// reaches. A connection endpoint's events reach its own connection only.
// Every other endpoint's reach the connection whose own account ref the
// event names, so an event about something several members can see reaches
// only the member it was delivered for. Whatever Subscriptions answers is
// checked against that, and a subscription outside it is dropped.
func (s *Service) route(ctx context.Context, endpoint Endpoint, triggers map[string]bool, events []integration.Event, now time.Time, log *slog.Logger) ([]DeliveredEvent, error) {
	var routed []DeliveredEvent
	for _, event := range events {
		if !triggers[event.Trigger] {
			log.WarnContext(ctx, "an integration parsed an event for a trigger its manifest does not push", "trigger", event.Trigger)
			continue
		}
		if !json.Valid(event.Data) {
			log.WarnContext(ctx, "an integration parsed an event whose data is not JSON", "trigger", event.Trigger)
			continue
		}
		subscriptions, err := s.subscriptionsFor(ctx, endpoint, event)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, subscription := range subscriptions {
			if !reaches(endpoint, event, subscription) {
				log.ErrorContext(ctx, "a subscription outside a delivery's reach was offered for it, and dropped",
					"trigger", event.Trigger, "trigger_id", subscription.TriggerID, "connection_id", subscription.ConnectionID)
				continue
			}
			if seen[subscription.TriggerID] {
				continue
			}
			seen[subscription.TriggerID] = true
			received := appsv1.TriggerReceived{
				TriggerID: subscription.TriggerID, ConnectionID: subscription.ConnectionID,
				OrganizationID: subscription.OrganizationID, MemberID: subscription.MemberID,
				Toolkit: subscription.Toolkit, Trigger: subscription.Trigger, ReceivedAt: now,
			}
			if len(event.Data) > appsv1.MaxTriggerDataBytes {
				received.DataOmitted = true
			} else {
				received.Data = event.Data
			}
			outboxEvent, err := s.event(appsv1.TriggerReceivedDetailType, received, now)
			if err != nil {
				return nil, err
			}
			routed = append(routed, DeliveredEvent{
				Key: eventKey(subscription.TriggerID, event.ID), TriggerID: subscription.TriggerID, Event: outboxEvent,
			})
		}
	}
	return routed, nil
}

// subscriptionsFor asks for the subscriptions an event may reach: by the
// endpoint's connection, or by the event's account ref. An event that names
// no account on an endpoint shared by many accounts reaches nobody.
func (s *Service) subscriptionsFor(ctx context.Context, endpoint Endpoint, event integration.Event) ([]Subscription, error) {
	if endpoint.Scope == integration.HookScopeConnection {
		return s.subscriptions.ForEndpoint(ctx, endpoint, event.Trigger)
	}
	if event.AccountRef == "" {
		return nil, nil
	}
	organizationID := ""
	if endpoint.Scope == integration.HookScopeOrganization {
		organizationID = endpoint.OrganizationID
	}
	return s.subscriptions.ForAccount(ctx, endpoint.Toolkit, organizationID, event.AccountRef, event.Trigger)
}

// reaches says whether a delivery may reach a subscription: the same toolkit
// and trigger, and either the endpoint's own connection, or a connection
// whose own account ref is the one the event names, inside the endpoint's
// organization when it has one.
func reaches(endpoint Endpoint, event integration.Event, subscription Subscription) bool {
	switch {
	case subscription.TriggerID == "" || subscription.ConnectionID == "" ||
		subscription.OrganizationID == "" || subscription.MemberID == "":
		return false
	case subscription.Toolkit != endpoint.Toolkit || subscription.Trigger != event.Trigger:
		return false
	}
	switch endpoint.Scope {
	case integration.HookScopeConnection:
		return subscription.ConnectionID == endpoint.ConnectionID && subscription.OrganizationID == endpoint.OrganizationID
	case integration.HookScopeOrganization:
		return subscription.OrganizationID == endpoint.OrganizationID &&
			event.AccountRef != "" && subscription.AccountRef == event.AccountRef
	case integration.HookScopeDeployment:
		return event.AccountRef != "" && subscription.AccountRef == event.AccountRef
	}
	return false
}

// eventKey dedupes an event for its trigger across deliveries, as an app
// that batches may send the same event twice in different batches. An event
// without an id has none.
func eventKey(triggerID, eventID string) string {
	if eventID == "" {
		return ""
	}
	return "trigger:" + triggerID + ":" + keyPart(eventID)
}

// event packages what happened for the outbox, in the envelope apps uses.
func (s *Service) event(detailType string, data any, now time.Time) (domain.Event, error) {
	id, err := newID(s.random, "evt_")
	if err != nil {
		return domain.Event{}, err
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return domain.Event{}, err
	}
	return domain.Event{ID: id, DetailType: detailType, OccurredAt: now, Data: encoded}, nil
}

// refused answers a delivery that could not be verified, 401, logging only
// why, never what it carried. A delivery that could not be checked because
// something Oryxa relies on failed is answered 503 instead, so the app
// retries it.
func refused(ctx context.Context, writer http.ResponseWriter, log *slog.Logger, err error) {
	if errors.Is(err, ErrUnverified) {
		log.WarnContext(ctx, "webhook delivery refused", "reason", err.Error())
		writeError(writer, http.StatusUnauthorized, "unauthorized", "the delivery could not be verified")
		return
	}
	failed(ctx, writer, log, err)
}

// failed answers a delivery Oryxa could not handle, so the app delivers it
// again.
func failed(ctx context.Context, writer http.ResponseWriter, log *slog.Logger, err error) {
	if errors.Is(err, domain.ErrUnavailable) {
		log.ErrorContext(ctx, "webhook delivery could not be checked", "error", err)
		writeError(writer, http.StatusServiceUnavailable, "unavailable", "a service apps depends on did not answer; retry")
		return
	}
	log.ErrorContext(ctx, "webhook delivery failed", "error", err)
	writeError(writer, http.StatusInternalServerError, "internal", "internal server error")
}

// maxLoggedError bounds how much of an integration's error is logged.
const maxLoggedError = 300

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

// printable says whether value is between minimum and maximum bytes of
// printable ASCII, as a value echoed back to an app must be.
func printable(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < ' ' || value[i] > '~' {
			return false
		}
	}
	return true
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]map[string]string{"error": {"code": code, "message": message}})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
