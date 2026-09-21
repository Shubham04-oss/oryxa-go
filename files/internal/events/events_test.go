package events

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	eventbridgetypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	fileseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/files/v1"
	teamseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"

	"github.com/Shubham04-oss/oryxa-go/files/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
)

type fakeEventBridge struct {
	input  *eventbridge.PutEventsInput
	output *eventbridge.PutEventsOutput
}

func (fake *fakeEventBridge) PutEvents(_ context.Context, input *eventbridge.PutEventsInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error) {
	fake.input = input
	return fake.output, nil
}

// publisherHarness builds a signed publisher over a fake bus, for the tests
// below. A nil signer is the one thing the constructor refuses, which its
// own test covers.
func publisherHarness(t *testing.T, output *eventbridge.PutEventsOutput) *Publisher {
	t.Helper()
	signer, _, err := eventauth.NewLocalSigner("files", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := NewPublisher(&fakeEventBridge{output: output}, "oryxa", signer)
	if err != nil {
		t.Fatal(err)
	}
	return publisher
}

func TestPublisherWrapsEventsInTheEnvelope(t *testing.T) {
	client := &fakeEventBridge{output: &eventbridge.PutEventsOutput{}}
	signer, _, err := eventauth.NewLocalSigner("files", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := NewPublisher(client, "oryxa", signer)
	if err != nil {
		t.Fatal(err)
	}
	occurredAt := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)

	if err := publisher.PublishEvent(context.Background(), domain.Event{
		ID:         "evt_1",
		DetailType: "files.object.created.v1",
		OccurredAt: occurredAt,
		Data:       json.RawMessage(`{"file_id":"file_1"}`),
	}); err != nil {
		t.Fatalf("PublishEvent() error = %v", err)
	}
	entry := client.input.Entries[0]
	if aws.ToString(entry.Source) != fileseventsv1.Source || aws.ToString(entry.EventBusName) != "oryxa" {
		t.Fatalf("entry = %+v", entry)
	}
	var detail struct {
		EventID   string `json:"event_id"`
		Signature string `json:"sig"`
	}
	if err := json.Unmarshal([]byte(aws.ToString(entry.Detail)), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Signature == "" {
		t.Fatal("published envelope carries no signature")
	}
}

func TestPublisherRefusesToBuildWithoutASigningIdentity(t *testing.T) {
	if _, err := NewPublisher(&fakeEventBridge{output: &eventbridge.PutEventsOutput{}}, "default", nil); err == nil {
		t.Fatal("a publisher without a signer must not build")
	}
}

func TestPublisherReportsRejectedEntries(t *testing.T) {
	publisher := publisherHarness(t, &eventbridge.PutEventsOutput{
		FailedEntryCount: 1,
		Entries:          []eventbridgetypes.PutEventsResultEntry{{ErrorCode: aws.String("InternalFailure"), ErrorMessage: aws.String("rejected")}},
	})
	if err := publisher.PublishEvent(context.Background(), domain.Event{ID: "evt_1", DetailType: "files.x.v1", OccurredAt: time.Now(), Data: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("PublishEvent() error = nil for a rejected entry")
	}
}

type fakeApplication struct {
	created []autheventsv1.OrganizationCreated
	deleted []autheventsv1.OrganizationDeleted
	removed []autheventsv1.MemberRemoved
	teams   []teamseventsv1.TeamDeleted
	err     error
}

func (fake *fakeApplication) OrganizationCreated(_ context.Context, data autheventsv1.OrganizationCreated) error {
	fake.created = append(fake.created, data)
	return fake.err
}
func (fake *fakeApplication) OrganizationUpdated(context.Context, autheventsv1.OrganizationUpdated) error {
	return fake.err
}
func (fake *fakeApplication) OrganizationDeleted(_ context.Context, data autheventsv1.OrganizationDeleted) error {
	fake.deleted = append(fake.deleted, data)
	return fake.err
}
func (fake *fakeApplication) MemberRemoved(_ context.Context, data autheventsv1.MemberRemoved) error {
	fake.removed = append(fake.removed, data)
	return fake.err
}
func (fake *fakeApplication) TeamDeleted(_ context.Context, data teamseventsv1.TeamDeleted) error {
	fake.teams = append(fake.teams, data)
	return fake.err
}

// consumedHarness holds one signer per source files listens to, and a
// verifier that trusts them, so each fixture is signed the way the named
// service would have signed it.
var (
	harnessOnce sync.Once
	authSigner  *eventauth.Signer
	teamSigner  *eventauth.Signer
	verifier    *eventauth.Verifier
)

func consumedHarness() (*eventauth.Signer, *eventauth.Signer, *eventauth.Verifier) {
	harnessOnce.Do(func() {
		keys := map[string]*ecdsa.PublicKey{}
		for service := range map[string]bool{"auth": true, "teams": true} {
			signer, key, err := eventauth.NewLocalSigner(service, time.Now)
			if err != nil {
				panic(err)
			}
			keys[service] = key
			if service == "auth" {
				authSigner = signer
			} else {
				teamSigner = signer
			}
		}
		verifier = eventauth.NewVerifier(keys, nil, time.Now)
	})
	return authSigner, teamSigner, verifier
}

func signedEvent(signer *eventauth.Signer, source, eventID, detailType string, data string) string {
	signature, err := signer.Sign(source, detailType, eventID, 1, "2026-09-14T12:00:00Z", []byte(data))
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf(`{"version":"0","id":"eb-1","detail-type":%q,"source":%q,"detail":{"event_id":%q,"version":1,"occurred_at":"2026-09-14T12:00:00Z","data":%s,"sig":%q}}`,
		detailType, source, eventID, data, signature)
}

func authEvent(detailType string, data string) string {
	signer, _, _ := consumedHarness()
	return signedEvent(signer, "oryxa.auth", "evt_auth_1", detailType, data)
}

func teamsEvent(detailType string, data string) string {
	_, teamSigner, _ := consumedHarness()
	return signedEvent(teamSigner, "oryxa.teams", "evt_teams_1", detailType, data)
}

func consumerFor(t *testing.T, app application) *Consumer {
	t.Helper()
	_, _, verifier := consumedHarness()
	consumer, err := NewConsumer(app, verifier, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return consumer
}

func TestConsumerDispatchesOrganizationEvents(t *testing.T) {
	app := &fakeApplication{}
	consumer := consumerFor(t, app)
	must(t, consumer.Handle(context.Background(), authEvent(autheventsv1.OrganizationCreatedDetailType, `{"organization_id":"org_1","name":"Acme","slug":"acme","created_by":"user-1","created_at":"2026-09-14T12:00:00Z"}`)))
	must(t, consumer.Handle(context.Background(), authEvent(autheventsv1.OrganizationDeletedDetailType, `{"organization_id":"org_1","name":"Acme","deleted_at":"2026-09-14T14:00:00Z"}`)))
	must(t, consumer.Handle(context.Background(), authEvent(autheventsv1.MemberRemovedDetailType, `{"organization_id":"org_1","user_id":"user-2","email":"ben@example.com","reason":"left","removed_at":"2026-09-14T15:00:00Z"}`)))
	must(t, consumer.Handle(context.Background(), teamsEvent(teamseventsv1.TeamDeletedDetailType, `{"team_id":"team_eng","organization_id":"org_1","name":"Engineering","deleted_by":"user-1","deleted_at":"2026-09-14T16:00:00Z"}`)))
	if len(app.created) != 1 || app.created[0].Name != "Acme" || len(app.deleted) != 1 || len(app.removed) != 1 || len(app.teams) != 1 || app.teams[0].TeamID != "team_eng" {
		t.Fatalf("dispatched created=%+v deleted=%+v removed=%+v teams=%+v", app.created, app.deleted, app.removed, app.teams)
	}
}

func TestConsumerRejectsWhatItCannotHandle(t *testing.T) {
	consumer := consumerFor(t, &fakeApplication{err: errors.New("database down")})
	if err := consumer.Handle(context.Background(), "{"); err == nil {
		t.Fatal("Handle() error = nil")
	}
}

func TestConsumerRefusesAnUnsignedEventHoweverWellFormed(t *testing.T) {
	consumer := consumerFor(t, &fakeApplication{})
	body := `{"version":"0","id":"eb-1","detail-type":` + `"` + autheventsv1.OrganizationDeletedDetailType + `","source":"oryxa.auth","detail":{"event_id":"evt_forged","version":1,"occurred_at":"2026-09-14T12:00:00Z","data":{"organization_id":"org_1"}}}`
	if err := consumer.Handle(context.Background(), body); err == nil {
		t.Fatal("an unsigned event was dispatched")
	}
	if len(consumer.handlers) == 0 {
		t.Fatal("consumer lost its handlers")
	}
}

func TestConsumerRefusesASignatureMadeByTheWrongService(t *testing.T) {
	// teams' key signing an auth event: a real signature, but not the
	// service the event claims.
	_, teamSigner, verifier := consumedHarness()
	consumer, err := NewConsumer(&fakeApplication{}, verifier, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	body := signedEvent(teamSigner, "oryxa.auth", "evt_wrong", autheventsv1.OrganizationDeletedDetailType, `{"organization_id":"org_1"}`)
	if err := consumer.Handle(context.Background(), body); err == nil {
		t.Fatal("an event signed by the wrong service was dispatched")
	}
}

type fakeQueue struct {
	batches [][]sqstypes.Message
	deleted []sqstypes.DeleteMessageBatchRequestEntry
	cancel  context.CancelFunc
}

func (fake *fakeQueue) ReceiveMessage(ctx context.Context, _ *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	if len(fake.batches) == 0 {
		fake.cancel()
		return nil, ctx.Err()
	}
	batch := fake.batches[0]
	fake.batches = fake.batches[1:]
	return &sqs.ReceiveMessageOutput{Messages: batch}, nil
}
func (fake *fakeQueue) DeleteMessageBatch(_ context.Context, input *sqs.DeleteMessageBatchInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error) {
	fake.deleted = append(fake.deleted, input.Entries...)
	return &sqs.DeleteMessageBatchOutput{}, nil
}

func TestPollDeletesOnlyHandledMessages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := &fakeQueue{cancel: cancel, batches: [][]sqstypes.Message{{
		{MessageId: aws.String("handled"), ReceiptHandle: aws.String("receipt-handled"), Body: aws.String(authEvent(autheventsv1.OrganizationCreatedDetailType, `{"organization_id":"org_1","name":"Acme"}`))},
		{MessageId: aws.String("failed"), ReceiptHandle: aws.String("receipt-failed"), Body: aws.String("{")},
	}}}
	consumerFor(t, &fakeApplication{}).Poll(ctx, queue, "http://localhost:4566/000000000000/files-events")
	if len(queue.deleted) != 1 || aws.ToString(queue.deleted[0].ReceiptHandle) != "receipt-handled" {
		t.Fatalf("deleted = %+v, want only receipt-handled", queue.deleted)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
