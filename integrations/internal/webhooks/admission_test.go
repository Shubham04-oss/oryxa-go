package webhooks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
)

type testRoutes struct{ route domain.WebhookRoute }

func (r testRoutes) WebhookRoute(_ context.Context, opaque string) (domain.WebhookRoute, error) {
	if opaque != r.route.OpaqueRoute {
		return domain.WebhookRoute{}, errors.New("not found")
	}
	return r.route, nil
}

type testSecrets struct {
	binding secrets.Binding
	opened  int
}

func (s *testSecrets) Open(_ context.Context, binding secrets.Binding, _ domain.SealedData) ([]byte, error) {
	s.binding = binding
	s.opened++
	return []byte("signing-secret"), nil
}

type testRunner struct {
	request RunnerRequest
	result  RunnerResult
	calls   int
}

func (r *testRunner) VerifyAndParse(_ context.Context, request RunnerRequest) (RunnerResult, error) {
	request.SigningSecret = append([]byte(nil), request.SigningSecret...)
	request.RawBody = append([]byte(nil), request.RawBody...)
	r.request = request
	r.calls++
	return r.result, nil
}

type claimRecord struct {
	token     string
	committed bool
}

type testClaims struct {
	values    map[string]claimRecord
	released  int
	commits   int
	commitErr error
}

func (c *testClaims) Acquire(_ context.Context, key string, _, _, _ time.Time) (string, bool, error) {
	if _, exists := c.values[key]; exists {
		return "", false, nil
	}
	token := fmt.Sprintf("claim-%d", len(c.values)+1)
	c.values[key] = claimRecord{token: token}
	return token, true, nil
}

func (c *testClaims) Commit(_ context.Context, key, token string) error {
	c.commits++
	record := c.values[key]
	if record.token != token {
		return errors.New("claim token mismatch")
	}
	if c.commitErr != nil {
		return c.commitErr
	}
	record.committed = true
	c.values[key] = record
	return nil
}

func (c *testClaims) Release(_ context.Context, key, token string) error {
	record := c.values[key]
	if record.token != token {
		return errors.New("claim token mismatch")
	}
	delete(c.values, key)
	c.released++
	return nil
}

type testQueue struct {
	envelopes []Envelope
	err       error
}

func (q *testQueue) EnqueueWebhook(_ context.Context, envelope Envelope) error {
	if q.err != nil {
		return q.err
	}
	q.envelopes = append(q.envelopes, envelope)
	return nil
}

func TestAdmitPreservesRawPayloadAndQueuesVerifiedDelivery(t *testing.T) {
	t.Parallel()
	service, secretBox, runner, claims, queue, now := newWebhookService()
	raw := []byte(`{"event_id":"Ev123","team_id":"T123"}`)
	decision, err := service.Admit(context.Background(), Request{
		OpaqueRoute: "opaque-route", Method: "POST",
		Headers: map[string]string{"X-Slack-Request-Timestamp": fmt.Sprint(now.Unix()), "X-Slack-Signature": "v0=test"},
		Query:   map[string]string{"source": "provider"}, Body: map[string]any{"team_id": "T123"}, RawBody: raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Status != 202 || !decision.Accepted || decision.Duplicate {
		t.Fatalf("unexpected decision: %+v", decision)
	}
	if got := string(runner.request.RawBody); got != string(raw) {
		t.Fatalf("runner raw body = %q", got)
	}
	if runner.request.Headers["x-slack-signature"] != "v0=test" {
		t.Fatal("headers were not normalized for the piece")
	}
	if string(runner.request.SigningSecret) != "signing-secret" {
		t.Fatal("runner did not receive the one scoped signing secret")
	}
	if secretBox.binding != (secrets.Binding{OrganizationID: "org-1", ResourceType: "webhook-route", ResourceID: "opaque-route"}) {
		t.Fatalf("unexpected secret binding: %+v", secretBox.binding)
	}
	if len(queue.envelopes) != 1 || string(queue.envelopes[0].RawBody) != string(raw) {
		t.Fatal("verified payload was not queued exactly once")
	}
	if queue.envelopes[0].DeliveryID == "" || strings.Contains(queue.envelopes[0].DeliveryID, "Ev123") {
		t.Fatalf("delivery ID is missing or exposes provider identity: %q", queue.envelopes[0].DeliveryID)
	}
	if claims.commits != 1 {
		t.Fatalf("claim commits = %d, want 1", claims.commits)
	}
}

func TestAdmitRejectsStaleTimestampBeforeOpeningSecretOrCallingRunner(t *testing.T) {
	t.Parallel()
	service, secretBox, runner, _, queue, now := newWebhookService()
	decision, err := service.Admit(context.Background(), Request{
		OpaqueRoute: "opaque-route", Method: "POST", RawBody: []byte("{}"),
		Headers: map[string]string{"x-slack-request-timestamp": fmt.Sprint(now.Add(-6 * time.Minute).Unix())},
	})
	if !errors.Is(err, ErrTimestampInvalid) || decision.Status != 401 {
		t.Fatalf("decision/error = %+v/%v", decision, err)
	}
	if secretBox.opened != 0 || runner.calls != 0 || len(queue.envelopes) != 0 {
		t.Fatal("stale request reached credential, runner, or queue")
	}
}

func TestAdmitRejectsInvalidSignatureWithoutClaimingOrQueueing(t *testing.T) {
	t.Parallel()
	service, _, runner, claims, queue, now := newWebhookService()
	runner.result.Verified = false
	decision, err := service.Admit(context.Background(), validWebhookRequest(now))
	if !errors.Is(err, ErrSignatureInvalid) || decision.Status != 401 {
		t.Fatalf("decision/error = %+v/%v", decision, err)
	}
	if len(claims.values) != 0 || len(queue.envelopes) != 0 {
		t.Fatal("invalid signature was claimed or queued")
	}
}

func TestAdmitReturnsSynchronousChallengeWithoutQueueing(t *testing.T) {
	t.Parallel()
	service, _, runner, claims, queue, now := newWebhookService()
	runner.result.Reply = &Reply{Status: 200, Headers: map[string]string{"content-type": "text/plain"}, Body: "challenge"}
	decision, err := service.Admit(context.Background(), validWebhookRequest(now))
	if err != nil {
		t.Fatal(err)
	}
	if decision.Status != 200 || decision.Body != "challenge" || decision.Headers["content-type"] != "text/plain" {
		t.Fatalf("unexpected challenge decision: %+v", decision)
	}
	if len(claims.values) != 0 || len(queue.envelopes) != 0 {
		t.Fatal("provider challenge was claimed or queued")
	}
}

func TestAdmitAcknowledgesReplayWithoutQueueingAgain(t *testing.T) {
	t.Parallel()
	service, _, _, _, queue, now := newWebhookService()
	request := validWebhookRequest(now)
	if _, err := service.Admit(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	decision, err := service.Admit(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Duplicate || decision.Status != 202 {
		t.Fatalf("unexpected duplicate decision: %+v", decision)
	}
	if len(queue.envelopes) != 1 {
		t.Fatalf("queue count = %d, want 1", len(queue.envelopes))
	}
}

func TestQueueFailureReleasesClaimSoProviderRetryCanRecover(t *testing.T) {
	t.Parallel()
	service, _, _, claims, queue, now := newWebhookService()
	request := validWebhookRequest(now)
	queue.err = errors.New("SQS unavailable")
	decision, err := service.Admit(context.Background(), request)
	if !errors.Is(err, ErrQueueUnavailable) || decision.Status != 503 {
		t.Fatalf("decision/error = %+v/%v", decision, err)
	}
	if claims.released != 1 || len(claims.values) != 0 {
		t.Fatal("failed enqueue did not release its claim")
	}
	queue.err = nil
	decision, err = service.Admit(context.Background(), request)
	if err != nil || decision.Status != 202 || len(queue.envelopes) != 1 {
		t.Fatalf("retry did not recover: decision=%+v error=%v queued=%d", decision, err, len(queue.envelopes))
	}
}

func TestCommitFailureIsAcknowledgedAfterDurableEnqueue(t *testing.T) {
	t.Parallel()
	service, _, _, claims, queue, now := newWebhookService()
	claims.commitErr = errors.New("conditional write timeout")
	decision, err := service.Admit(context.Background(), validWebhookRequest(now))
	if err != nil {
		t.Fatal(err)
	}
	if decision.Status != 202 || decision.Warning == nil || len(queue.envelopes) != 1 {
		t.Fatalf("unexpected post-enqueue decision: %+v queued=%d", decision, len(queue.envelopes))
	}
}

func TestInactiveRouteAcknowledgesLateDeliveryWithoutTouchingRuntime(t *testing.T) {
	t.Parallel()
	service, secretBox, runner, claims, queue, now := newWebhookService()
	routes := service.Routes.(testRoutes)
	routes.route.Active = false
	service.Routes = routes
	decision, err := service.Admit(context.Background(), validWebhookRequest(now))
	if err != nil || decision.Status != 202 {
		t.Fatalf("decision/error = %+v/%v", decision, err)
	}
	if secretBox.opened != 0 || runner.calls != 0 || len(claims.values) != 0 || len(queue.envelopes) != 0 {
		t.Fatal("inactive route performed runtime work")
	}
}

func newWebhookService() (Service, *testSecrets, *testRunner, *testClaims, *testQueue, time.Time) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	route := domain.WebhookRoute{
		OpaqueRoute: "opaque-route", OrganizationID: "org-1", ProfileID: "app-1",
		Provider: "slack", PieceName: "@activepieces/piece-slack", PieceVersion: "0.17.10",
		TriggerName: "new-message", Mode: domain.WebhookRouteAppLevel,
		TimestampHeader: "x-slack-request-timestamp", TimestampWindow: 5 * time.Minute,
		MaxBodyBytes: 1024, Active: true,
		SigningSecret: domain.SealedData{Version: 1, Ciphertext: []byte("ciphertext")},
	}
	secretBox := &testSecrets{}
	runner := &testRunner{result: RunnerResult{
		Verified: true, Event: "message", IdentifierValue: "T123", ProviderEventID: "Ev123",
	}}
	claims := &testClaims{values: make(map[string]claimRecord)}
	queue := &testQueue{}
	return Service{
		Routes: testRoutes{route: route}, Secrets: secretBox, Runner: runner, Claims: claims, Queue: queue,
		Now: func() time.Time { return now }, IDs: func(prefix string) (string, error) { return prefix + "-1", nil },
	}, secretBox, runner, claims, queue, now
}

func validWebhookRequest(now time.Time) Request {
	return Request{
		OpaqueRoute: "opaque-route", Method: "POST",
		Headers: map[string]string{"x-slack-request-timestamp": fmt.Sprint(now.Unix()), "x-slack-signature": "v0=test"},
		Body:    map[string]any{"team_id": "T123"}, RawBody: []byte(`{"team_id":"T123"}`),
	}
}
