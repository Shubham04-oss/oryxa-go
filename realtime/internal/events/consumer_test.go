package events

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"

	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
	"github.com/Shubham04-oss/oryxa-go/realtime/internal/purge"
)

// auth signs the events these tests deliver, as auth's publisher would, and
// verifier is an enforcing consumer's: it trusts auth's key and nothing else.
var (
	authOnce     sync.Once
	authSigner   *eventauth.Signer
	authVerifier *eventauth.Verifier
)

func authHarness() (*eventauth.Signer, *eventauth.Verifier) {
	authOnce.Do(func() {
		signer, key, err := eventauth.NewLocalSigner("auth", time.Now)
		if err != nil {
			panic(err)
		}
		authSigner = signer
		authVerifier = eventauth.NewVerifier(map[string]*ecdsa.PublicKey{"auth": key}, nil, time.Now)
	})
	return authSigner, authVerifier
}

// authEvent is an SQS message body as EventBridge delivers an auth event:
// the envelope, signed by signer over exactly what it holds.
func authEvent(signer *eventauth.Signer, detailType, eventID string, version int, data string) string {
	occurredAt := "2026-09-24T12:00:00Z"
	signature, err := signer.Sign(autheventsv1.Source, detailType, eventID, version, occurredAt, []byte(data))
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf(`{"version":"0","id":"eb-%s","detail-type":%q,"source":"oryxa.auth","detail":{"event_id":%q,"version":%d,"occurred_at":%q,"data":%s,"sig":%q}}`,
		eventID, detailType, eventID, version, occurredAt, data, signature)
}

// When the events say the membership and the organization ended. Neither is
// the envelope's occurred_at, and both have fractions of a second, as auth's
// clock gives them, so a purge's removed_at shows where it came from.
const (
	removedAt = "2026-09-24T11:59:58.123456789Z"
	deletedAt = "2026-09-24T11:59:59.5Z"
)

func at(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func memberRemoved(eventID, organizationID, userID, reason string) string {
	signer, _ := authHarness()
	return authEvent(signer, autheventsv1.MemberRemovedDetailType, eventID, 1, fmt.Sprintf(
		`{"organization_id":%q,"organization_name":"Acme","user_id":%q,"email":"ben@example.com","previous_role":"member","reason":%q,"removed_by":"user-1","removed_at":%q}`,
		organizationID, userID, reason, removedAt))
}

func organizationDeleted(eventID, organizationID string) string {
	signer, _ := authHarness()
	return authEvent(signer, autheventsv1.OrganizationDeletedDetailType, eventID, 1, fmt.Sprintf(
		`{"organization_id":%q,"name":"Acme","slug":"acme","deleted_by":"user-1","deleted_at":%q}`, organizationID, deletedAt))
}

// fakePurger records every purge it is asked for, and answers with what
// fail holds for its event, or nil.
type fakePurger struct {
	mu       sync.Mutex
	requests []purge.Request
	fail     map[string]error
}

func (fake *fakePurger) Purge(_ context.Context, request purge.Request) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.requests = append(fake.requests, request)
	return fake.fail[request.EventID]
}

func (fake *fakePurger) asked() []purge.Request {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return slices.Clone(fake.requests)
}

// samePurges compares requests field by field, and their times as instants.
func samePurges(got, want []purge.Request) bool {
	return slices.EqualFunc(got, want, func(a, b purge.Request) bool {
		return a.EventID == b.EventID && a.OrganizationID == b.OrganizationID && a.Scope == b.Scope &&
			a.UserID == b.UserID && a.RemovedAt.Equal(b.RemovedAt)
	})
}

func newConsumer(t *testing.T, purger Purger) *Consumer {
	t.Helper()
	_, verifier := authHarness()
	consumer, err := NewConsumer(purger, verifier, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return consumer
}

func TestAuthSubscriptionsAreTheEndsOfMemberships(t *testing.T) {
	got := slices.Sorted(slices.Values(AuthSubscriptions()))
	if want := []string{"auth.member.removed.v1", "auth.organization.deleted.v1"}; !slices.Equal(got, want) {
		t.Fatalf("AuthSubscriptions() = %v, want %v", got, want)
	}
}

func TestConsumerPurgesAMemberWhoseMembershipEnded(t *testing.T) {
	for _, reason := range []string{autheventsv1.MemberRemovedReasonLeft, autheventsv1.MemberRemovedReasonRemoved} {
		t.Run(reason, func(t *testing.T) {
			purger := &fakePurger{}
			if err := newConsumer(t, purger).Handle(context.Background(), memberRemoved("evt_1", "org_1", "user-2", reason)); err != nil {
				t.Fatal(err)
			}
			want := []purge.Request{{EventID: "evt_1", OrganizationID: "org_1", Scope: purge.ScopeMember, UserID: "user-2", RemovedAt: at(removedAt)}}
			if got := purger.asked(); !samePurges(got, want) {
				t.Fatalf("purges = %+v, want %+v", got, want)
			}
		})
	}
}

func TestConsumerPurgesADeletedOrganization(t *testing.T) {
	purger := &fakePurger{}
	if err := newConsumer(t, purger).Handle(context.Background(), organizationDeleted("evt_2", "org_1")); err != nil {
		t.Fatal(err)
	}
	want := []purge.Request{{EventID: "evt_2", OrganizationID: "org_1", Scope: purge.ScopeOrganization, RemovedAt: at(deletedAt)}}
	if got := purger.asked(); !samePurges(got, want) {
		t.Fatalf("purges = %+v, want %+v", got, want)
	}
}

// Deleting an organization removes every member too, each with its own
// event; the organization's purge takes everything, so theirs are not sent.
func TestConsumerLeavesAMemberOfADeletedOrganizationToTheOrganizationsPurge(t *testing.T) {
	purger := &fakePurger{}
	consumer := newConsumer(t, purger)
	if err := consumer.Handle(context.Background(), memberRemoved("evt_3", "org_1", "user-2", autheventsv1.MemberRemovedReasonOrganizationDeleted)); err != nil {
		t.Fatal(err)
	}
	// Needing no purge, it needs no time either.
	signer, _ := authHarness()
	untimed := authEvent(signer, autheventsv1.MemberRemovedDetailType, "evt_4", 1, `{"organization_id":"org_1","user_id":"user-3","reason":"organization_deleted"}`)
	if err := consumer.Handle(context.Background(), untimed); err != nil {
		t.Fatal(err)
	}
	if got := purger.asked(); len(got) != 0 {
		t.Fatalf("purges = %+v, want none", got)
	}
}

func TestConsumerRefusesWhatItCannotHandle(t *testing.T) {
	signer, _ := authHarness()
	stranger, _, err := eventauth.NewLocalSigner("auth", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	orgData := `{"organization_id":"org_1","name":"Acme","deleted_at":"2026-09-24T12:00:00Z"}`
	tests := map[string]string{
		"not json":          "{",
		"unsubscribed type": authEvent(signer, autheventsv1.MemberJoinedDetailType, "evt_1", 1, `{"organization_id":"org_1","user_id":"user-2"}`),
		"another source": `{"id":"eb-1","detail-type":"auth.organization.deleted.v1","source":"oryxa.billing",` +
			`"detail":{"event_id":"evt_1","version":1,"data":{"organization_id":"org_1"}}}`,
		"future version":         authEvent(signer, autheventsv1.OrganizationDeletedDetailType, "evt_1", 2, orgData),
		"no event id":            authEvent(signer, autheventsv1.OrganizationDeletedDetailType, "", 1, orgData),
		"bad data":               authEvent(signer, autheventsv1.OrganizationDeletedDetailType, "evt_1", 1, `{"organization_id":42}`),
		"no organization":        authEvent(signer, autheventsv1.OrganizationDeletedDetailType, "evt_1", 1, `{"name":"Acme","deleted_at":"2026-09-24T12:00:00Z"}`),
		"no deleted_at":          authEvent(signer, autheventsv1.OrganizationDeletedDetailType, "evt_1", 1, `{"organization_id":"org_1","name":"Acme"}`),
		"member without user":    authEvent(signer, autheventsv1.MemberRemovedDetailType, "evt_1", 1, `{"organization_id":"org_1","reason":"left","removed_at":"2026-09-24T12:00:00Z"}`),
		"member without org":     authEvent(signer, autheventsv1.MemberRemovedDetailType, "evt_1", 1, `{"user_id":"user-2","reason":"left","removed_at":"2026-09-24T12:00:00Z"}`),
		"member without time":    authEvent(signer, autheventsv1.MemberRemovedDetailType, "evt_1", 1, `{"organization_id":"org_1","user_id":"user-2","reason":"left"}`),
		"signed by someone else": authEvent(stranger, autheventsv1.OrganizationDeletedDetailType, "evt_1", 1, orgData),
		"unsigned": `{"id":"eb-1","detail-type":"auth.organization.deleted.v1","source":"oryxa.auth",` +
			`"detail":{"event_id":"evt_1","version":1,"occurred_at":"2026-09-24T12:00:00Z","data":{"organization_id":"org_1"}}}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			purger := &fakePurger{}
			err := newConsumer(t, purger).Handle(context.Background(), body)
			if err == nil || errors.Is(err, purge.ErrPermanent) {
				t.Fatalf("Handle() = %v, want an error that leaves the message for the dead-letter queue", err)
			}
			if got := purger.asked(); len(got) != 0 {
				t.Fatalf("purges = %+v, want none", got)
			}
		})
	}
}

// Report-only is the host's default: a signature that does not check out is
// reported and the event handled anyway, as every consumer does.
func TestConsumerFollowsTheRegistrysSignaturePolicy(t *testing.T) {
	stranger, _, err := eventauth.NewLocalSigner("auth", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	orgData := `{"organization_id":"org_1","deleted_at":"2026-09-24T12:00:00Z"}`
	forged := authEvent(stranger, autheventsv1.OrganizationDeletedDetailType, "evt_forged", 1, orgData)

	for _, reporting := range []bool{true, false} {
		registry := eventauth.NewRegistry(nil, time.Now)
		var reported []string
		if reporting {
			registry.ReportOnly(func(_, _, eventID string, _ error) { reported = append(reported, eventID) })
		}
		signer, err := registry.Signer("auth")
		if err != nil {
			t.Fatal(err)
		}
		purger := &fakePurger{}
		consumer, err := NewConsumer(purger, registry.Verifier(), slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatal(err)
		}
		if err := consumer.Handle(context.Background(), authEvent(signer, autheventsv1.OrganizationDeletedDetailType, "evt_signed", 1, orgData)); err != nil {
			t.Fatalf("reporting=%v: a correctly signed event = %v", reporting, err)
		}
		err = consumer.Handle(context.Background(), forged)
		switch {
		case reporting && (err != nil || !slices.Equal(reported, []string{"evt_forged"}) || len(purger.asked()) != 2):
			t.Fatalf("reporting: forged event = %v, reported %v, purges %+v; want it reported and purged", err, reported, purger.asked())
		case !reporting && (!errors.Is(err, eventauth.ErrUnverified) || len(purger.asked()) != 1):
			t.Fatalf("enforcing: forged event = %v, purges %+v; want it refused and not purged", err, purger.asked())
		}
	}
}

func TestConsumerSaysWhetherTheWorkerRefusedForGood(t *testing.T) {
	purger := &fakePurger{fail: map[string]error{
		"evt_refused": fmt.Errorf("%w: it answered 400 Bad Request", purge.ErrPermanent),
		"evt_down":    errors.New("the Worker answered 503 Service Unavailable, not 204"),
	}}
	consumer := newConsumer(t, purger)
	refused := consumer.Handle(context.Background(), organizationDeleted("evt_refused", "org_1"))
	if !errors.Is(refused, purge.ErrPermanent) || !strings.Contains(refused.Error(), "evt_refused") ||
		!strings.Contains(refused.Error(), "organization org_1") || !strings.Contains(refused.Error(), "400") {
		t.Fatalf("refused for good = %v, want ErrPermanent naming the event, organization and answer", refused)
	}
	down := consumer.Handle(context.Background(), organizationDeleted("evt_down", "org_1"))
	if down == nil || errors.Is(down, purge.ErrPermanent) {
		t.Fatalf("Worker down = %v, want an error worth retrying", down)
	}
}

func TestConsumerAsksTheWorkerOncePerEvent(t *testing.T) {
	purger := &fakePurger{fail: map[string]error{"evt_flaky": errors.New("the Worker answered 502 Bad Gateway, not 204")}}
	consumer := newConsumer(t, purger)
	ctx := context.Background()
	for range 3 {
		if err := consumer.Handle(ctx, memberRemoved("evt_1", "org_1", "user-2", "left")); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(purger.asked()); got != 1 {
		t.Fatalf("purges for one event delivered three times = %d, want 1", got)
	}
	// A purge that failed is not remembered: its redelivery asks again.
	if err := consumer.Handle(ctx, organizationDeleted("evt_flaky", "org_1")); err == nil {
		t.Fatal("Handle() = nil for a failed purge")
	}
	delete(purger.fail, "evt_flaky")
	if err := consumer.Handle(ctx, organizationDeleted("evt_flaky", "org_1")); err != nil {
		t.Fatal(err)
	}
	if got := len(purger.asked()); got != 3 {
		t.Fatalf("purges = %d, want the failed one asked for again", got)
	}
}

func TestRecentForgetsTheOldestFirst(t *testing.T) {
	seen := newRecent(2)
	seen.add("a")
	seen.add("b")
	seen.add("b")
	if !seen.has("a") || !seen.has("b") {
		t.Fatal("recent forgot an id while it had room")
	}
	seen.add("c")
	if seen.has("a") || !seen.has("b") || !seen.has("c") {
		t.Fatalf("after a third id: a=%v b=%v c=%v, want a forgotten", seen.has("a"), seen.has("b"), seen.has("c"))
	}
}

type fakeQueue struct {
	batches [][]sqstypes.Message
	deleted []string
	cancel  context.CancelFunc
}

func (fake *fakeQueue) ReceiveMessage(ctx context.Context, input *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	if input.MaxNumberOfMessages != ReceiveBatch {
		return nil, fmt.Errorf("asked for %d messages", input.MaxNumberOfMessages)
	}
	if len(fake.batches) == 0 {
		fake.cancel()
		return nil, ctx.Err()
	}
	batch := fake.batches[0]
	fake.batches = fake.batches[1:]
	return &sqs.ReceiveMessageOutput{Messages: batch}, nil
}

func (fake *fakeQueue) DeleteMessageBatch(_ context.Context, input *sqs.DeleteMessageBatchInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error) {
	for _, entry := range input.Entries {
		fake.deleted = append(fake.deleted, aws.ToString(entry.ReceiptHandle))
	}
	return &sqs.DeleteMessageBatchOutput{}, nil
}

func message(receipt, body string) sqstypes.Message {
	return sqstypes.Message{MessageId: aws.String(receipt), ReceiptHandle: aws.String(receipt), Body: aws.String(body)}
}

// Poll against an httptest Worker, through the real client: what the Worker
// did, and what it refused for good, is deleted; what it could not do yet,
// and what nobody could handle, stays for the queue to deliver again.
func TestPollDeletesWhatIsDoneWithAndKeepsWhatMaySucceedLater(t *testing.T) {
	const secret = "a-secret-the-worker-holds-too-0123456789"
	var (
		mu     sync.Mutex
		bodies []string
	)
	worker := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		timestamp := request.Header.Get(purge.TimestampHeader)
		var stamp int64
		if _, err := fmt.Sscan(timestamp, &stamp); err != nil ||
			request.Header.Get(purge.SignatureHeader) != purge.Sign(secret, stamp, body) {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		var purged purge.Request
		_ = json.Unmarshal(body, &purged)
		switch purged.OrganizationID {
		case "org_gone":
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(writer, `{"error":"unknown organization"}`)
		case "org_down":
			writer.WriteHeader(http.StatusServiceUnavailable)
		default:
			writer.WriteHeader(http.StatusNoContent)
		}
	}))
	defer worker.Close()
	client, err := purge.New(purge.Config{WorkerURL: worker.URL, Secret: secret})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := &fakeQueue{cancel: cancel, batches: [][]sqstypes.Message{
		{
			message("member-left", memberRemoved("evt_left", "org_1", "user-2", "left")),
			message("org-deleted", organizationDeleted("evt_deleted", "org_1")),
			message("member-of-deleted-org", memberRemoved("evt_member_of_deleted", "org_1", "user-3", "organization_deleted")),
			message("refused", organizationDeleted("evt_refused", "org_gone")),
			message("worker-down", organizationDeleted("evt_down", "org_down")),
		},
		{
			message("undecodable", "{"),
			// Delivered again after its deletion failed: done already.
			message("member-left-again", memberRemoved("evt_left", "org_1", "user-2", "left")),
		},
	}}
	var logs strings.Builder
	_, verifier := authHarness()
	consumer, err := NewConsumer(client, verifier, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	consumer.Poll(ctx, queue, "http://localhost:4566/000000000000/realtime-events")

	if want := []string{"member-left", "org-deleted", "member-of-deleted-org", "refused", "member-left-again"}; !slices.Equal(queue.deleted, want) {
		t.Fatalf("deleted = %v, want %v", queue.deleted, want)
	}
	wantBodies := []string{
		`{"event_id":"evt_left","organization_id":"org_1","scope":"member","user_id":"user-2","removed_at":"2026-09-24T11:59:58.123456789Z"}`,
		`{"event_id":"evt_deleted","organization_id":"org_1","scope":"organization","user_id":"","removed_at":"2026-09-24T11:59:59.5Z"}`,
		`{"event_id":"evt_refused","organization_id":"org_gone","scope":"organization","user_id":"","removed_at":"2026-09-24T11:59:59.5Z"}`,
		`{"event_id":"evt_down","organization_id":"org_down","scope":"organization","user_id":"","removed_at":"2026-09-24T11:59:59.5Z"}`,
	}
	if !slices.Equal(bodies, wantBodies) {
		t.Fatalf("the Worker was sent %v, want %v", bodies, wantBodies)
	}
	if !strings.Contains(logs.String(), "realtime purge dropped") || !strings.Contains(logs.String(), "unknown organization") {
		t.Fatalf("the refusal was not logged with the Worker's answer:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "the queue will deliver it again") {
		t.Fatalf("the failure worth retrying was not logged:\n%s", logs.String())
	}
}

func TestNewConsumerRefusesToRunBlind(t *testing.T) {
	_, verifier := authHarness()
	if _, err := NewConsumer(nil, verifier, nil); err == nil {
		t.Fatal("NewConsumer() without a purger = nil error")
	}
	if _, err := NewConsumer(&fakePurger{}, nil, nil); err == nil {
		t.Fatal("NewConsumer() without a verifier = nil error")
	}
}
