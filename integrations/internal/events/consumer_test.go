package events

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	teamsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"

	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
)

// fakeApp records what the consumer handed it, and keeps handled events in
// memory as the table does.
type fakeApp struct {
	mu       sync.Mutex
	removed  []authv1.MemberRemoved
	deleted  []authv1.OrganizationDeleted
	handled  map[string]string
	fail     error
	markFail error
}

func (f *fakeApp) MemberRemoved(_ context.Context, event authv1.MemberRemoved) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.removed = append(f.removed, event)
	return nil
}

func (f *fakeApp) OrganizationDeleted(_ context.Context, event authv1.OrganizationDeleted) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.deleted = append(f.deleted, event)
	return nil
}

func (f *fakeApp) AlreadyHandled(_ context.Context, eventID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.handled[eventID]
	return ok, nil
}

func (f *fakeApp) MarkHandled(_ context.Context, eventID, detailType string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.markFail != nil {
		return f.markFail
	}
	if f.handled == nil {
		f.handled = map[string]string{}
	}
	f.handled[eventID] = detailType
	return nil
}

func (f *fakeApp) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.removed) + len(f.deleted)
}

// auth and teams sign the events these tests deliver, as their publishers
// would, and verifier is an enforcing consumer's: it trusts their keys and
// nothing else, each for its own source.
var (
	harnessOnce sync.Once
	authSigner  *eventauth.Signer
	teamsSigner *eventauth.Signer
	verifier    *eventauth.Verifier
)

func harness() {
	harnessOnce.Do(func() {
		auth, authKey, err := eventauth.NewLocalSigner("auth", time.Now)
		if err != nil {
			panic(err)
		}
		teams, teamsKey, err := eventauth.NewLocalSigner("teams", time.Now)
		if err != nil {
			panic(err)
		}
		authSigner, teamsSigner = auth, teams
		verifier = eventauth.NewVerifier(map[string]*ecdsa.PublicKey{"auth": authKey, "teams": teamsKey}, nil, time.Now)
	})
}

// sourcedEvent is an SQS message body as EventBridge delivers an event
// source published: the envelope, signed by signer over exactly what it holds.
func sourcedEvent(signer *eventauth.Signer, source, detailType, eventID string, version int, data string) string {
	occurredAt := "2026-10-22T12:00:00Z"
	signature, err := signer.Sign(source, detailType, eventID, version, occurredAt, []byte(data))
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf(`{"version":"0","id":"eb-%s","detail-type":%q,"source":%q,"detail":{"event_id":%q,"version":%d,"occurred_at":%q,"data":%s,"sig":%q}}`,
		eventID, detailType, source, eventID, version, occurredAt, data, signature)
}

const (
	memberData = `{"organization_id":"org_1","organization_name":"Acme","user_id":"user-2","email":"ben@example.com",` +
		`"previous_role":"member","reason":"left","removed_by":"user-2","removed_at":"2026-10-22T11:59:58.5Z"}`
	orgData = `{"organization_id":"org_1","name":"Acme","slug":"acme","deleted_by":"user-1","deleted_at":"2026-10-22T11:59:59Z"}`
)

func memberRemoved(eventID string) string {
	harness()
	return sourcedEvent(authSigner, authv1.Source, authv1.MemberRemovedDetailType, eventID, 1, memberData)
}

func organizationDeleted(eventID string) string {
	harness()
	return sourcedEvent(authSigner, authv1.Source, authv1.OrganizationDeletedDetailType, eventID, 1, orgData)
}

func newConsumer(t *testing.T, app *fakeApp) *Consumer {
	t.Helper()
	harness()
	consumer, err := NewConsumer(app, verifier, nil)
	if err != nil {
		t.Fatal(err)
	}
	return consumer
}

// Every detail type integrations subscribes to must have a handler, or
// provisioning would route events the consumer then refuses.
func TestEverySubscriptionHasAHandler(t *testing.T) {
	consumer := newConsumer(t, &fakeApp{})
	for source, detailTypes := range Subscriptions() {
		for _, detailType := range detailTypes {
			if subscribed, ok := consumer.handlers[detailType]; !ok || subscribed.source != source {
				t.Errorf("%s from %s has no handler", detailType, source)
			}
		}
	}
	if len(consumer.handlers) != 2 {
		t.Errorf("handlers = %d, want one per subscription", len(consumer.handlers))
	}
}

func TestConsumerRefusesToRunWithoutAVerifier(t *testing.T) {
	if _, err := NewConsumer(&fakeApp{}, nil, nil); err == nil {
		t.Fatal("a consumer without a verifier was built")
	}
}

func TestConsumerHandsDeparturesOver(t *testing.T) {
	app := &fakeApp{}
	consumer := newConsumer(t, app)
	ctx := context.Background()
	if err := consumer.Handle(ctx, memberRemoved("evt_1")); err != nil {
		t.Fatal(err)
	}
	if err := consumer.Handle(ctx, organizationDeleted("evt_2")); err != nil {
		t.Fatal(err)
	}
	removedAt, _ := time.Parse(time.RFC3339Nano, "2026-10-22T11:59:58.5Z")
	if len(app.removed) != 1 || app.removed[0].OrganizationID != "org_1" || app.removed[0].UserID != "user-2" ||
		!app.removed[0].RemovedAt.Equal(removedAt) {
		t.Fatalf("member removals = %+v", app.removed)
	}
	if len(app.deleted) != 1 || app.deleted[0].OrganizationID != "org_1" {
		t.Fatalf("organization deletions = %+v", app.deleted)
	}
	if app.handled["evt_1"] != authv1.MemberRemovedDetailType || app.handled["evt_2"] != authv1.OrganizationDeletedDetailType {
		t.Fatalf("handled = %v", app.handled)
	}
}

// The bus delivers at least once: a second delivery of an event, or a replay
// of it, finds it handled and does nothing.
func TestDuplicateAndReplayedEventsAreNoOps(t *testing.T) {
	app := &fakeApp{}
	consumer := newConsumer(t, app)
	ctx := context.Background()
	first := memberRemoved("evt_1")
	for range 3 {
		if err := consumer.Handle(ctx, first); err != nil {
			t.Fatal(err)
		}
	}
	// A replay is signed afresh, but has the same event id.
	if err := consumer.Handle(ctx, memberRemoved("evt_1")); err != nil {
		t.Fatal(err)
	}
	if got := app.calls(); got != 1 {
		t.Fatalf("handler ran %d times, want once", got)
	}
}

// A handler that fails records nothing, so the redelivery runs it again.
func TestAFailedHandlerIsNotRecorded(t *testing.T) {
	app := &fakeApp{fail: errors.New("database down")}
	consumer := newConsumer(t, app)
	ctx := context.Background()
	if err := consumer.Handle(ctx, memberRemoved("evt_1")); err == nil {
		t.Fatal("a failed handler was reported handled")
	}
	if len(app.handled) != 0 {
		t.Fatalf("handled = %v, want nothing recorded", app.handled)
	}
	app.fail = nil
	if err := consumer.Handle(ctx, memberRemoved("evt_1")); err != nil || app.calls() != 1 {
		t.Fatalf("redelivery = %v, calls %d", err, app.calls())
	}
}

func TestConsumerRefusesWhatItCannotHandle(t *testing.T) {
	harness()
	stranger, _, err := eventauth.NewLocalSigner("auth", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	teamsMember := `{"team_id":"team_1","organization_id":"org_1","user_id":"user-2","reason":"left","removed_at":"2026-10-22T12:00:00Z"}`
	tests := map[string]string{
		"not json":          "{",
		"unsubscribed type": sourcedEvent(authSigner, authv1.Source, authv1.MemberJoinedDetailType, "evt_1", 1, `{"organization_id":"org_1","user_id":"user-2"}`),
		// Leaving a team is not leaving the organization.
		"a team membership": sourcedEvent(teamsSigner, teamsv1.Source, teamsv1.MemberRemovedDetailType, "evt_1", 1, teamsMember),
		"another source": `{"id":"eb-1","detail-type":"auth.member.removed.v1","source":"oryxa.billing",` +
			`"detail":{"event_id":"evt_1","version":1,"data":` + memberData + `}}`,
		// teams' key cannot speak for auth, nor auth's type come from teams.
		"auth's type signed by teams": sourcedEvent(teamsSigner, authv1.Source, authv1.MemberRemovedDetailType, "evt_1", 1, memberData),
		"auth's type from teams":      sourcedEvent(teamsSigner, teamsv1.Source, authv1.MemberRemovedDetailType, "evt_1", 1, memberData),
		"future version":              sourcedEvent(authSigner, authv1.Source, authv1.MemberRemovedDetailType, "evt_1", 2, memberData),
		"no event id":                 sourcedEvent(authSigner, authv1.Source, authv1.MemberRemovedDetailType, "", 1, memberData),
		"bad data":                    sourcedEvent(authSigner, authv1.Source, authv1.OrganizationDeletedDetailType, "evt_1", 1, `{"organization_id":42}`),
		"signed by someone else":      sourcedEvent(stranger, authv1.Source, authv1.MemberRemovedDetailType, "evt_1", 1, memberData),
		"unsigned": `{"id":"eb-1","detail-type":"auth.member.removed.v1","source":"oryxa.auth",` +
			`"detail":{"event_id":"evt_1","version":1,"occurred_at":"2026-10-22T12:00:00Z","data":` + memberData + `}}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			app := &fakeApp{}
			if err := newConsumer(t, app).Handle(context.Background(), body); err == nil {
				t.Fatal("Handle() = nil, want an error that leaves the message for the dead-letter queue")
			}
			if app.calls() != 0 || len(app.handled) != 0 {
				t.Fatalf("the event was acted on: %+v", app)
			}
		})
	}
}

// Report-only is the host's default until signatures are enforced: a
// signature that does not check out is reported and the event handled
// anyway, as every consumer does. Enforcing, it is refused.
func TestConsumerFollowsTheRegistrysSignaturePolicy(t *testing.T) {
	stranger, _, err := eventauth.NewLocalSigner("auth", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	forged := sourcedEvent(stranger, authv1.Source, authv1.MemberRemovedDetailType, "evt_forged", 1, memberData)
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
		app := &fakeApp{}
		consumer, err := NewConsumer(app, registry.Verifier(eventauth.MaxAge(SignatureMaxAge)), nil)
		if err != nil {
			t.Fatal(err)
		}
		signed := sourcedEvent(signer, authv1.Source, authv1.MemberRemovedDetailType, "evt_signed", 1, memberData)
		if err := consumer.Handle(context.Background(), signed); err != nil {
			t.Fatalf("reporting=%v: a correctly signed event = %v", reporting, err)
		}
		err = consumer.Handle(context.Background(), forged)
		switch {
		case reporting && (err != nil || !slices.Equal(reported, []string{"evt_forged"}) || app.calls() != 2):
			t.Fatalf("reporting: forged event = %v, reported %v, calls %d; want it reported and handled", err, reported, app.calls())
		case !reporting && (!errors.Is(err, eventauth.ErrUnverified) || app.calls() != 1):
			t.Fatalf("enforcing: forged event = %v, calls %d; want it refused", err, app.calls())
		}
	}
}
