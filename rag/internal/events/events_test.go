package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	eventbridgetypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"crypto/ecdsa"
	"sync"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	fileseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/files/v1"
	knowledgeeventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"
	rageventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/rag/v1"
	teamseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"

	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// signingHarness holds one signer for each source rag listens to and a
// verifier that trusts them, so a delivery is signed the way the service it
// names would have signed it.
var (
	harnessOnce    sync.Once
	harnessErr     error
	sourceSigners  = map[string]*eventauth.Signer{}
	harnessVerif   *eventauth.Verifier
	harnessSources = []string{"auth", "files", "knowledge", "teams"}
)

func signingHarness() (*eventauth.Verifier, error) {
	harnessOnce.Do(func() {
		keys := map[string]*ecdsa.PublicKey{}
		for _, service := range harnessSources {
			signer, key, err := eventauth.NewLocalSigner(service, time.Now)
			if err != nil {
				harnessErr = err
				return
			}
			sourceSigners["oryxa."+service] = signer
			keys[service] = key
		}
		harnessVerif = eventauth.NewVerifier(keys, nil, time.Now)
	})
	return harnessVerif, harnessErr
}

// consumerFor builds the consumer the way mount does, with a verifier that
// trusts the harness's signers.
func consumerFor(app application) (*Consumer, error) {
	verifier, err := signingHarness()
	if err != nil {
		return nil, err
	}
	return NewConsumer(app, verifier, slog.New(slog.DiscardHandler))
}

// ragPublisher builds a publisher with rag's own signing identity.
func ragPublisher(t *testing.T, client putEventsAPI, bus string) *Publisher {
	t.Helper()
	signer, _, err := eventauth.NewLocalSigner("rag", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := NewPublisher(client, bus, signer)
	if err != nil {
		t.Fatal(err)
	}
	return publisher
}

// fakeApplication records what the consumer dispatched to it, one slice per
// method, so a test can check both that a handler ran and what it was given.
type fakeApplication struct {
	created   []autheventsv1.OrganizationCreated
	updated   []autheventsv1.OrganizationUpdated
	deleted   []autheventsv1.OrganizationDeleted
	removed   []autheventsv1.MemberRemoved
	teams     []teamseventsv1.TeamDeleted
	objects   []fileseventsv1.ObjectDeleted
	published []knowledgeeventsv1.DocumentPublished
	forgotten []knowledgeeventsv1.DocumentRemoved
	err       error
}

func (fake *fakeApplication) OrganizationCreated(_ context.Context, data autheventsv1.OrganizationCreated) error {
	fake.created = append(fake.created, data)
	return fake.err
}

func (fake *fakeApplication) OrganizationUpdated(_ context.Context, data autheventsv1.OrganizationUpdated) error {
	fake.updated = append(fake.updated, data)
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

func (fake *fakeApplication) ObjectDeleted(_ context.Context, data fileseventsv1.ObjectDeleted) error {
	fake.objects = append(fake.objects, data)
	return fake.err
}

func (fake *fakeApplication) DocumentPublished(_ context.Context, data knowledgeeventsv1.DocumentPublished) error {
	fake.published = append(fake.published, data)
	return fake.err
}

func (fake *fakeApplication) DocumentRemoved(_ context.Context, data knowledgeeventsv1.DocumentRemoved) error {
	fake.forgotten = append(fake.forgotten, data)
	return fake.err
}

// delivery is one message body exactly as EventBridge, via SQS, would
// present it: the outer envelope naming the source and detail type, and the
// inner one signed the way the named service would have signed it. A source
// no harness signer exists for stays unsigned, which is one more thing the
// consumer must refuse.
func delivery(source, detailType string, version int, data string) string {
	occurredAt := "2026-09-14T12:00:00Z"
	detail := fmt.Sprintf(`"event_id":"evt-1","version":%d,"occurred_at":%q,"data":%s`, version, occurredAt, data)
	if signer, ok := sourceSigners[source]; ok {
		signature, err := signer.Sign(source, detailType, "evt-1", version, occurredAt, []byte(data))
		if err != nil {
			panic(err)
		}
		detail += fmt.Sprintf(`,"sig":%q`, signature)
	}
	return fmt.Sprintf(`{"version":"0","id":"eb-1","detail-type":%q,"source":%q,"detail":{%s}}`,
		detailType, source, detail)
}

func TestEverySubscribedEventHasAHandler(t *testing.T) {
	// The provisioner builds the EventBridge rule from these subscription
	// lists, so a detail type named there but not handled would mean the
	// queue receives something nothing acts on, and one handled but never
	// named would mean an event that never arrives. Neither shows up at
	// runtime as anything but silence.
	consumer, err := consumerFor(&fakeApplication{})
	if err != nil {
		t.Fatal(err)
	}
	var subscribed []string
	subscribed = append(subscribed, AuthSubscriptions()...)
	subscribed = append(subscribed, TeamSubscriptions()...)
	subscribed = append(subscribed, FileSubscriptions()...)
	subscribed = append(subscribed, KnowledgeSubscriptions()...)
	for _, detailType := range subscribed {
		if _, ok := consumer.handlers[detailType]; !ok {
			t.Fatalf("%s is subscribed but nothing handles it", detailType)
		}
	}
	for detailType := range consumer.handlers {
		if !slices.Contains(subscribed, detailType) {
			t.Fatalf("%s is handled but never subscribed, so it will never arrive", detailType)
		}
	}
}

func TestSubscriptionsNameTheSourceEachEventComesFrom(t *testing.T) {
	consumer, err := consumerFor(&fakeApplication{})
	if err != nil {
		t.Fatal(err)
	}
	checkSource := func(detailTypes []string, source string) {
		t.Helper()
		for _, detailType := range detailTypes {
			if got := consumer.handlers[detailType].source; got != source {
				t.Fatalf("%s is subscribed from %s but handled as %s", detailType, source, got)
			}
		}
	}
	checkSource(AuthSubscriptions(), autheventsv1.Source)
	checkSource(TeamSubscriptions(), teamseventsv1.Source)
	checkSource(FileSubscriptions(), fileseventsv1.Source)
	checkSource(KnowledgeSubscriptions(), knowledgeeventsv1.Source)
}

func TestHandleDispatchesEachSubscribedEventToItsOwnMethod(t *testing.T) {
	app := &fakeApplication{}
	consumer, err := consumerFor(app)
	if err != nil {
		t.Fatal(err)
	}
	must(t, consumer.Handle(context.Background(), delivery(autheventsv1.Source, autheventsv1.OrganizationCreatedDetailType, 1,
		`{"organization_id":"org_1","name":"Acme","created_by":"user-1","created_at":"2026-09-14T12:00:00Z"}`)))
	must(t, consumer.Handle(context.Background(), delivery(autheventsv1.Source, autheventsv1.OrganizationUpdatedDetailType, 1,
		`{"organization_id":"org_1","name":"Acme Inc","updated_at":"2026-09-14T12:00:00Z"}`)))
	must(t, consumer.Handle(context.Background(), delivery(autheventsv1.Source, autheventsv1.OrganizationDeletedDetailType, 1,
		`{"organization_id":"org_1","name":"Acme","deleted_at":"2026-09-14T14:00:00Z"}`)))
	must(t, consumer.Handle(context.Background(), delivery(autheventsv1.Source, autheventsv1.MemberRemovedDetailType, 1,
		`{"organization_id":"org_1","user_id":"user-2","email":"ben@example.com","reason":"left","removed_at":"2026-09-14T15:00:00Z"}`)))
	must(t, consumer.Handle(context.Background(), delivery(teamseventsv1.Source, teamseventsv1.TeamDeletedDetailType, 1,
		`{"team_id":"team_eng","organization_id":"org_1","name":"Engineering","deleted_by":"user-1","deleted_at":"2026-09-14T16:00:00Z"}`)))
	must(t, consumer.Handle(context.Background(), delivery(fileseventsv1.Source, fileseventsv1.ObjectDeletedDetailType, 1,
		`{"file_id":"file_1","organization_id":"org_1","reason":"deleted","deleted_at":"2026-09-14T17:00:00Z"}`)))
	must(t, consumer.Handle(context.Background(), delivery(knowledgeeventsv1.Source, knowledgeeventsv1.DocumentPublishedDetailType, 1,
		`{"document_id":"doc_1","organization_id":"org_1","source_id":"src_1","provider":"github","scope":"member","member_id":"user-1","access":"members","external_id":"42","kind":"issue","mime":"text/plain","content_hash":"abc","body":"hello","metadata":{"knowledge_category":"work_material"},"extracted_at":"2026-09-14T18:00:00Z"}`)))
	must(t, consumer.Handle(context.Background(), delivery(knowledgeeventsv1.Source, knowledgeeventsv1.DocumentRemovedDetailType, 1,
		`{"document_id":"doc_1","organization_id":"org_1","source_id":"src_1","provider":"github","external_id":"42","reason":"deleted","removed_at":"2026-09-14T19:00:00Z"}`)))

	if len(app.created) != 1 || app.created[0].Name != "Acme" {
		t.Fatalf("OrganizationCreated = %+v", app.created)
	}
	if len(app.updated) != 1 || app.updated[0].Name != "Acme Inc" {
		t.Fatalf("OrganizationUpdated = %+v", app.updated)
	}
	if len(app.deleted) != 1 || app.deleted[0].OrganizationID != "org_1" {
		t.Fatalf("OrganizationDeleted = %+v", app.deleted)
	}
	if len(app.removed) != 1 || app.removed[0].UserID != "user-2" {
		t.Fatalf("MemberRemoved = %+v", app.removed)
	}
	if len(app.teams) != 1 || app.teams[0].TeamID != "team_eng" {
		t.Fatalf("TeamDeleted = %+v", app.teams)
	}
	if len(app.objects) != 1 || app.objects[0].FileID != "file_1" {
		t.Fatalf("ObjectDeleted = %+v", app.objects)
	}
	if len(app.published) != 1 || app.published[0].DocumentID != "doc_1" || app.published[0].MemberID != "user-1" ||
		app.published[0].Access != "members" || app.published[0].Metadata["knowledge_category"] != "work_material" {
		t.Fatalf("DocumentPublished = %+v", app.published)
	}
	if len(app.forgotten) != 1 || app.forgotten[0].DocumentID != "doc_1" {
		t.Fatalf("DocumentRemoved = %+v", app.forgotten)
	}
}

func TestHandleRefusesAnUnknownDetailType(t *testing.T) {
	consumer, err := consumerFor(&fakeApplication{})
	if err != nil {
		t.Fatal(err)
	}
	body := delivery(autheventsv1.Source, "auth.something.nobody-handles.v1", 1, `{}`)
	if err := consumer.Handle(context.Background(), body); err == nil {
		t.Fatal("Handle() error = nil, want a refusal")
	}
}

func TestHandleRefusesADetailTypeFromTheWrongSource(t *testing.T) {
	// A detail type nobody else may claim: its name alone is not enough to
	// say who actually sent it, so a message that only got the source wrong
	// must not reach the handler meant for the real thing.
	consumer, err := consumerFor(&fakeApplication{})
	if err != nil {
		t.Fatal(err)
	}
	body := delivery("oryxa.someone-else", fileseventsv1.ObjectDeletedDetailType, 1, `{"file_id":"file_1"}`)
	if err := consumer.Handle(context.Background(), body); err == nil {
		t.Fatal("Handle() error = nil, want a refusal")
	}
}

func TestHandleRefusesAMalformedBody(t *testing.T) {
	consumer, err := consumerFor(&fakeApplication{})
	if err != nil {
		t.Fatal(err)
	}
	if err := consumer.Handle(context.Background(), "{"); err == nil {
		t.Fatal("Handle() error = nil, want a refusal")
	}
}

func TestHandleRefusesMalformedDetailData(t *testing.T) {
	consumer, err := consumerFor(&fakeApplication{})
	if err != nil {
		t.Fatal(err)
	}
	body := delivery(fileseventsv1.Source, fileseventsv1.ObjectDeletedDetailType, 1, `"not an object"`)
	if err := consumer.Handle(context.Background(), body); err == nil {
		t.Fatal("Handle() error = nil, want a refusal")
	}
}

func TestHandleRefusesAnUnsupportedEnvelope(t *testing.T) {
	tests := map[string]string{
		"wrong version": `{"id":"eb-1","source":"` + fileseventsv1.Source + `","detail-type":"` + fileseventsv1.ObjectDeletedDetailType +
			`","detail":{"event_id":"evt-1","version":2,"data":{}}}`,
		"no event id": `{"id":"eb-1","source":"` + fileseventsv1.Source + `","detail-type":"` + fileseventsv1.ObjectDeletedDetailType +
			`","detail":{"event_id":"","version":1,"data":{}}}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			consumer, err := consumerFor(&fakeApplication{})
			if err != nil {
				t.Fatal(err)
			}
			if err := consumer.Handle(context.Background(), body); err == nil {
				t.Fatal("Handle() error = nil, want a refusal")
			}
		})
	}
}

func TestHandleLeavesAFailedEventForRedelivery(t *testing.T) {
	// Handle's error is what tells Poll not to delete the SQS message, so a
	// handler failure here must reach the caller rather than being swallowed.
	consumer, err := consumerFor(&fakeApplication{err: errors.New("the store was unreachable")})
	if err != nil {
		t.Fatal(err)
	}
	body := delivery(fileseventsv1.Source, fileseventsv1.ObjectDeletedDetailType, 1, `{"file_id":"file_1"}`)
	if err := consumer.Handle(context.Background(), body); err == nil {
		t.Fatal("Handle() error = nil, want the handler's failure to reach the caller")
	}
}

// TestHandleRefusesAnUnsignedEventHoweverWellFormed is the attack the
// signatures exist for: a perfectly-shaped message that no service signed.
func TestHandleRefusesAnUnsignedEventHoweverWellFormed(t *testing.T) {
	consumer, err := consumerFor(&fakeApplication{})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"version":"0","id":"eb-1","detail-type":` + strconv.Quote(fileseventsv1.ObjectDeletedDetailType) +
		`,"source":` + strconv.Quote(fileseventsv1.Source) +
		`,"detail":{"event_id":"evt-forged","version":1,"occurred_at":"2026-09-14T12:00:00Z","data":{"file_id":"file_1"}}}`
	if err := consumer.Handle(context.Background(), body); err == nil {
		t.Fatal("an unsigned event was dispatched")
	}
}

// TestHandleRefusesASignatureMadeByTheWrongService: a real signature, but
// from a key the claimed source does not resolve to. files' key signs an
// event that claims to come from auth.
func TestHandleRefusesASignatureMadeByTheWrongService(t *testing.T) {
	consumer, err := consumerFor(&fakeApplication{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signingHarness(); err != nil {
		t.Fatal(err)
	}
	signer := sourceSigners[fileseventsv1.Source]
	signature, err := signer.Sign(autheventsv1.Source, autheventsv1.MemberRemovedDetailType, "evt-wrong", 1,
		"2026-09-14T12:00:00Z", []byte(`{"organization_id":"org_1","user_id":"user-2"}`))
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"version":"0","id":"eb-1","detail-type":%q,"source":%q,"detail":{"event_id":"evt-wrong","version":1,"occurred_at":"2026-09-14T12:00:00Z","data":{"organization_id":"org_1","user_id":"user-2"},"sig":%q}}`,
		autheventsv1.MemberRemovedDetailType, autheventsv1.Source, signature)
	if err := consumer.Handle(context.Background(), body); err == nil {
		t.Fatal("an event signed by the wrong service was dispatched")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
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
		{MessageId: aws.String("handled"), ReceiptHandle: aws.String("receipt-handled"),
			Body: aws.String(delivery(fileseventsv1.Source, fileseventsv1.ObjectDeletedDetailType, 1, `{"file_id":"file_1"}`))},
		{MessageId: aws.String("failed"), ReceiptHandle: aws.String("receipt-failed"), Body: aws.String("{")},
	}}}
	consumer, err := consumerFor(&fakeApplication{})
	if err != nil {
		t.Fatal(err)
	}
	consumer.Poll(ctx, queue, "http://localhost:4566/000000000000/rag-events")
	if len(queue.deleted) != 1 || aws.ToString(queue.deleted[0].ReceiptHandle) != "receipt-handled" {
		t.Fatalf("deleted = %+v, want only the handled message's receipt", queue.deleted)
	}
}

type fakeEventBridge struct {
	input  *eventbridge.PutEventsInput
	output *eventbridge.PutEventsOutput
}

func (fake *fakeEventBridge) PutEvents(_ context.Context, input *eventbridge.PutEventsInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error) {
	fake.input = input
	return fake.output, nil
}

func TestPublisherWrapsEventsInTheEnvelope(t *testing.T) {
	client := &fakeEventBridge{output: &eventbridge.PutEventsOutput{}}
	occurredAt := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)

	err := ragPublisher(t, client, "oryxa").PublishEvent(context.Background(), domain.Event{
		ID:         "evt_1",
		DetailType: rageventsv1.DocumentIndexedDetailType,
		OccurredAt: occurredAt,
		Data:       json.RawMessage(`{"document_id":"doc_1"}`),
	})
	if err != nil {
		t.Fatalf("PublishEvent() error = %v", err)
	}
	if len(client.input.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(client.input.Entries))
	}
	entry := client.input.Entries[0]
	if aws.ToString(entry.Source) != rageventsv1.Source {
		t.Fatalf("source = %q, want %q", aws.ToString(entry.Source), rageventsv1.Source)
	}
	if aws.ToString(entry.EventBusName) != "oryxa" || aws.ToString(entry.DetailType) != rageventsv1.DocumentIndexedDetailType {
		t.Fatalf("entry = %+v", entry)
	}
	var detail struct {
		EventID    string          `json:"event_id"`
		Version    int             `json:"version"`
		OccurredAt string          `json:"occurred_at"`
		Data       json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(aws.ToString(entry.Detail)), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.EventID != "evt_1" || detail.Version != 1 || string(detail.Data) != `{"document_id":"doc_1"}` {
		t.Fatalf("detail = %+v", detail)
	}
}

func TestPublisherRejectsAnIncompleteEvent(t *testing.T) {
	client := &fakeEventBridge{output: &eventbridge.PutEventsOutput{}}
	err := ragPublisher(t, client, "oryxa").PublishEvent(context.Background(), domain.Event{DetailType: rageventsv1.CorpusCreatedDetailType})
	if err == nil {
		t.Fatal("PublishEvent() error = nil for an event with no id or occurred_at")
	}
}

func TestPublisherReportsRejectedEntries(t *testing.T) {
	client := &fakeEventBridge{output: &eventbridge.PutEventsOutput{
		FailedEntryCount: 1,
		Entries:          []eventbridgetypes.PutEventsResultEntry{{ErrorCode: aws.String("InternalFailure"), ErrorMessage: aws.String("rejected")}},
	}}
	err := ragPublisher(t, client, "default").PublishEvent(context.Background(), domain.Event{
		ID: "evt_1", DetailType: rageventsv1.CorpusCreatedDetailType, OccurredAt: time.Now(), Data: json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatal("PublishEvent() error = nil for a rejected entry")
	}
}
