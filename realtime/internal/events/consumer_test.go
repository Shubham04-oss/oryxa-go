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

	agentseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	realtimev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/realtime/v1"

	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
	"github.com/Shubham04-oss/oryxa-go/realtime/internal/purge"
	"github.com/Shubham04-oss/oryxa-go/realtime/internal/requests"
)

// auth and agents sign the events these tests deliver, as their publishers
// would, and verifier is an enforcing consumer's: it trusts their keys and
// nothing else, each for its own source.
var (
	harnessOnce  sync.Once
	authSigner   *eventauth.Signer
	agentsSigner *eventauth.Signer
	verifier     *eventauth.Verifier
)

func harness() {
	harnessOnce.Do(func() {
		auth, authKey, err := eventauth.NewLocalSigner("auth", time.Now)
		if err != nil {
			panic(err)
		}
		agents, agentsKey, err := eventauth.NewLocalSigner("agents", time.Now)
		if err != nil {
			panic(err)
		}
		authSigner, agentsSigner = auth, agents
		verifier = eventauth.NewVerifier(map[string]*ecdsa.PublicKey{"auth": authKey, "agents": agentsKey}, nil, time.Now)
	})
}

// authHarness is auth's signer, and the verifier.
func authHarness() (*eventauth.Signer, *eventauth.Verifier) {
	harness()
	return authSigner, verifier
}

// agentsHarness is agents' signer, and the verifier.
func agentsHarness() (*eventauth.Signer, *eventauth.Verifier) {
	harness()
	return agentsSigner, verifier
}

// authEvent is an SQS message body as EventBridge delivers an auth event:
// the envelope, signed by signer over exactly what it holds.
func authEvent(signer *eventauth.Signer, detailType, eventID string, version int, data string) string {
	return sourcedEvent(signer, autheventsv1.Source, detailType, eventID, version, data)
}

// agentsEvent is authEvent for an event agents publishes.
func agentsEvent(signer *eventauth.Signer, detailType, eventID string, version int, data string) string {
	return sourcedEvent(signer, agentseventsv1.Source, detailType, eventID, version, data)
}

// sourcedEvent is an SQS message body as EventBridge delivers an event
// source published: the envelope, signed by signer over exactly what it
// holds.
func sourcedEvent(signer *eventauth.Signer, source, detailType, eventID string, version int, data string) string {
	occurredAt := "2026-09-24T12:00:00Z"
	signature, err := signer.Sign(source, detailType, eventID, version, occurredAt, []byte(data))
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf(`{"version":"0","id":"eb-%s","detail-type":%q,"source":%q,"detail":{"event_id":%q,"version":%d,"occurred_at":%q,"data":%s,"sig":%q}}`,
		eventID, detailType, source, eventID, version, occurredAt, data, signature)
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

// When agents says a person left a teamspace, which is Teams' removed_at, to
// the nanosecond, and when agents deleted one, its changed_at. Neither is the
// envelope's occurred_at.
const (
	leftAt    = "2026-09-24T11:59:57.987654321Z"
	changedAt = "2026-09-24T11:59:59.25Z"
)

func teamspaceMemberRemoved(eventID, organizationID, teamspaceID, userID string) string {
	signer, _ := agentsHarness()
	return agentsEvent(signer, agentseventsv1.TeamspaceMemberRemovedDetailType, eventID, 1, fmt.Sprintf(
		`{"organization_id":%q,"teamspace_id":%q,"team_id":"team_1","user_id":%q,"removed_at":%q}`,
		organizationID, teamspaceID, userID, leftAt))
}

func teamspaceDeleted(eventID, organizationID, teamspaceID string) string {
	signer, _ := agentsHarness()
	return agentsEvent(signer, agentseventsv1.TeamspaceDeletedDetailType, eventID, 1, fmt.Sprintf(
		`{"teamspace_id":%q,"organization_id":%q,"team_id":"team_1","organization_agent_id":"oag_1","name":"Design",`+
			`"status":"active","revision":4,"contributor_user_ids":["user-2"],"changed_at":%q}`,
		teamspaceID, organizationID, changedAt))
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

// fakeNoter records every note it is asked for, and answers with what fail
// holds for its request and revision (key "request:revision"), or nil.
type fakeNoter struct {
	mu    sync.Mutex
	notes []requests.Note
	fail  map[string]error
}

func (fake *fakeNoter) Note(_ context.Context, note requests.Note) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.notes = append(fake.notes, note)
	return fake.fail[fmt.Sprintf("%s:%d", note.RequestID, note.Revision)]
}

func (fake *fakeNoter) noted() []requests.Note {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return slices.Clone(fake.notes)
}

// samePurges compares requests field by field, and their times as instants.
func samePurges(got, want []purge.Request) bool {
	return slices.EqualFunc(got, want, func(a, b purge.Request) bool {
		return a.EventID == b.EventID && a.OrganizationID == b.OrganizationID && a.Scope == b.Scope &&
			a.UserID == b.UserID && a.TeamspaceID == b.TeamspaceID && a.RemovedAt.Equal(b.RemovedAt)
	})
}

// fakeRecorder keeps every completed purge it is given, answering fail
// instead while it is set.
type fakeRecorder struct {
	mu        sync.Mutex
	completed []realtimev1.PurgeCompleted
	fail      error
}

func (fake *fakeRecorder) RecordPurgeCompleted(ctx context.Context, completed realtimev1.PurgeCompleted) (bool, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.fail != nil {
		return false, fake.fail
	}
	// As a database would, it refuses a context already done.
	if err := ctx.Err(); err != nil {
		return false, err
	}
	fake.completed = append(fake.completed, completed)
	return true, nil
}

func (fake *fakeRecorder) recorded() []realtimev1.PurgeCompleted {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return slices.Clone(fake.completed)
}

func (fake *fakeRecorder) failWith(err error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.fail = err
}

func newConsumer(t *testing.T, purger Purger) *Consumer {
	t.Helper()
	return newRecordingConsumer(t, purger, &fakeRecorder{})
}

func newRecordingConsumer(t *testing.T, purger Purger, recorder Recorder) *Consumer {
	t.Helper()
	_, verifier := authHarness()
	consumer, err := NewConsumer(purger, &fakeNoter{}, recorder, verifier, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return consumer
}

// Realtime hears the ends of memberships and teamspaces, which it purges for,
// and the moves of a request between two members, which it has the Worker
// note. The lease and secret events of a Slack ask change no request's state,
// so they are not its business.
func TestSubscriptionsAreTheEndsOfMembershipsAndTeamspacesAndTheMovesOfRequests(t *testing.T) {
	want := map[string][]string{
		"oryxa.auth": {"auth.member.removed.v1", "auth.organization.deleted.v1"},
		"oryxa.agents": {
			"agents.interaction.completed.v1", "agents.interaction.decided.v1", "agents.interaction.requested.v1",
			"agents.teamspace.deleted.v1", "agents.teamspace.member.removed.v1",
		},
	}
	got := Subscriptions()
	if len(got) != len(want) {
		t.Fatalf("Subscriptions() = %v, want %v", got, want)
	}
	for source, detailTypes := range want {
		if sorted := slices.Sorted(slices.Values(got[source])); !slices.Equal(sorted, detailTypes) {
			t.Fatalf("Subscriptions()[%s] = %v, want %v", source, sorted, detailTypes)
		}
	}
}

// A member of a deleted organization is purged too, although the
// organization's own purge takes everything it kept: that purge reaches only
// the conversations the Worker's registry knows of.
func TestConsumerPurgesAMemberWhoseMembershipEnded(t *testing.T) {
	for _, reason := range []string{
		autheventsv1.MemberRemovedReasonLeft, autheventsv1.MemberRemovedReasonRemoved, autheventsv1.MemberRemovedReasonOrganizationDeleted,
	} {
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

// A person who left a teamspace's team loses their side conversations in it,
// as a member who left the organization loses theirs: up to when Teams says
// they left.
func TestConsumerPurgesTheSideConversationsOfAMemberWhoLeftATeamspace(t *testing.T) {
	purger := &fakePurger{}
	if err := newConsumer(t, purger).Handle(context.Background(), teamspaceMemberRemoved("evt_3", "org_1", "tsp_1", "user-2")); err != nil {
		t.Fatal(err)
	}
	want := []purge.Request{{
		EventID: "evt_3", OrganizationID: "org_1", Scope: purge.ScopeTeamspaceMember, UserID: "user-2", TeamspaceID: "tsp_1",
		RemovedAt: at(leftAt),
	}}
	if got := purger.asked(); !samePurges(got, want) {
		t.Fatalf("purges = %+v, want %+v", got, want)
	}
}

// A deleted teamspace loses every side conversation in it, as a deleted
// organization loses everything it kept, removed when agents deleted it.
func TestConsumerPurgesADeletedTeamspace(t *testing.T) {
	purger := &fakePurger{}
	if err := newConsumer(t, purger).Handle(context.Background(), teamspaceDeleted("evt_4", "org_1", "tsp_1")); err != nil {
		t.Fatal(err)
	}
	want := []purge.Request{{EventID: "evt_4", OrganizationID: "org_1", Scope: purge.ScopeTeamspace, TeamspaceID: "tsp_1", RemovedAt: at(changedAt)}}
	if got := purger.asked(); !samePurges(got, want) {
		t.Fatalf("purges = %+v, want %+v", got, want)
	}
}

func TestConsumerRefusesWhatItCannotHandle(t *testing.T) {
	signer, _ := authHarness()
	agents, _ := agentsHarness()
	stranger, _, err := eventauth.NewLocalSigner("auth", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	orgData := `{"organization_id":"org_1","name":"Acme","deleted_at":"2026-09-24T12:00:00Z"}`
	spaceData := `{"teamspace_id":"tsp_1","organization_id":"org_1","changed_at":"2026-09-24T12:00:00Z"}`
	leftData := `{"organization_id":"org_1","teamspace_id":"tsp_1","team_id":"team_1","user_id":"user-2","removed_at":"2026-09-24T12:00:00Z"}`
	tests := map[string]string{
		"not json":          "{",
		"unsubscribed type": authEvent(signer, autheventsv1.MemberJoinedDetailType, "evt_1", 1, `{"organization_id":"org_1","user_id":"user-2"}`),
		"another source": `{"id":"eb-1","detail-type":"auth.organization.deleted.v1","source":"oryxa.billing",` +
			`"detail":{"event_id":"evt_1","version":1,"data":{"organization_id":"org_1"}}}`,
		"future version":      authEvent(signer, autheventsv1.OrganizationDeletedDetailType, "evt_1", 2, orgData),
		"no event id":         authEvent(signer, autheventsv1.OrganizationDeletedDetailType, "", 1, orgData),
		"bad data":            authEvent(signer, autheventsv1.OrganizationDeletedDetailType, "evt_1", 1, `{"organization_id":42}`),
		"no organization":     authEvent(signer, autheventsv1.OrganizationDeletedDetailType, "evt_1", 1, `{"name":"Acme","deleted_at":"2026-09-24T12:00:00Z"}`),
		"no deleted_at":       authEvent(signer, autheventsv1.OrganizationDeletedDetailType, "evt_1", 1, `{"organization_id":"org_1","name":"Acme"}`),
		"member without user": authEvent(signer, autheventsv1.MemberRemovedDetailType, "evt_1", 1, `{"organization_id":"org_1","reason":"left","removed_at":"2026-09-24T12:00:00Z"}`),
		"member without org":  authEvent(signer, autheventsv1.MemberRemovedDetailType, "evt_1", 1, `{"user_id":"user-2","reason":"left","removed_at":"2026-09-24T12:00:00Z"}`),
		"member without time": authEvent(signer, autheventsv1.MemberRemovedDetailType, "evt_1", 1, `{"organization_id":"org_1","user_id":"user-2","reason":"left"}`),
		"deleted organization's member without time": authEvent(signer, autheventsv1.MemberRemovedDetailType, "evt_1", 1,
			`{"organization_id":"org_1","user_id":"user-2","reason":"organization_deleted"}`),
		"signed by someone else": authEvent(stranger, autheventsv1.OrganizationDeletedDetailType, "evt_1", 1, orgData),
		"unsigned": `{"id":"eb-1","detail-type":"auth.organization.deleted.v1","source":"oryxa.auth",` +
			`"detail":{"event_id":"evt_1","version":1,"occurred_at":"2026-09-24T12:00:00Z","data":{"organization_id":"org_1"}}}`,
		"unsubscribed agents type": agentsEvent(agents, agentseventsv1.TeamspaceCreatedDetailType, "evt_1", 1, spaceData),
		// Each source's own detail types only.
		"agents' type from auth":  authEvent(signer, agentseventsv1.TeamspaceDeletedDetailType, "evt_1", 1, spaceData),
		"auth's type from agents": agentsEvent(agents, autheventsv1.OrganizationDeletedDetailType, "evt_1", 1, orgData),
		// Auth's key cannot speak for agents.
		"agents' signed by auth": sourcedEvent(signer, agentseventsv1.Source, agentseventsv1.TeamspaceDeletedDetailType, "evt_1", 1, spaceData),
		"bad teamspace data":     agentsEvent(agents, agentseventsv1.TeamspaceDeletedDetailType, "evt_1", 1, `{"teamspace_id":42}`),
		"teamspace without id": agentsEvent(agents, agentseventsv1.TeamspaceDeletedDetailType, "evt_1", 1,
			`{"organization_id":"org_1","changed_at":"2026-09-24T12:00:00Z"}`),
		"teamspace without org": agentsEvent(agents, agentseventsv1.TeamspaceDeletedDetailType, "evt_1", 1,
			`{"teamspace_id":"tsp_1","changed_at":"2026-09-24T12:00:00Z"}`),
		"teamspace without time": agentsEvent(agents, agentseventsv1.TeamspaceDeletedDetailType, "evt_1", 1,
			`{"teamspace_id":"tsp_1","organization_id":"org_1"}`),
		"teamspace member without teamspace": agentsEvent(agents, agentseventsv1.TeamspaceMemberRemovedDetailType, "evt_1", 1,
			`{"organization_id":"org_1","team_id":"team_1","user_id":"user-2","removed_at":"2026-09-24T12:00:00Z"}`),
		"teamspace member without user": agentsEvent(agents, agentseventsv1.TeamspaceMemberRemovedDetailType, "evt_1", 1,
			`{"organization_id":"org_1","teamspace_id":"tsp_1","team_id":"team_1","removed_at":"2026-09-24T12:00:00Z"}`),
		"teamspace member without org": agentsEvent(agents, agentseventsv1.TeamspaceMemberRemovedDetailType, "evt_1", 1,
			`{"teamspace_id":"tsp_1","team_id":"team_1","user_id":"user-2","removed_at":"2026-09-24T12:00:00Z"}`),
		"teamspace member without time": agentsEvent(agents, agentseventsv1.TeamspaceMemberRemovedDetailType, "evt_1", 1,
			`{"organization_id":"org_1","teamspace_id":"tsp_1","team_id":"team_1","user_id":"user-2"}`),
		"a future version of a teamspace member": agentsEvent(agents, agentseventsv1.TeamspaceMemberRemovedDetailType, "evt_1", 2, leftData),
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
		consumer, err := NewConsumer(purger, &fakeNoter{}, &fakeRecorder{}, registry.Verifier(), slog.New(slog.DiscardHandler))
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
	purger.fail["evt_space_refused"] = fmt.Errorf("%w: it answered 400 Bad Request", purge.ErrPermanent)
	spaceRefused := consumer.Handle(context.Background(), teamspaceDeleted("evt_space_refused", "org_1", "tsp_1"))
	if !errors.Is(spaceRefused, purge.ErrPermanent) || !strings.Contains(spaceRefused.Error(), "organization org_1, teamspace tsp_1") {
		t.Fatalf("a teamspace's refused for good = %v, want ErrPermanent naming the organization and the teamspace", spaceRefused)
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

// fakeQueue hands out its batches in turn, then cancels Poll's context. Like
// SQS, it refuses a call whose context is done.
type fakeQueue struct {
	batches [][]sqstypes.Message
	deleted []string
	cancel  context.CancelFunc
	// deleteErr fails every deletion; failDeleting names the receipts a
	// deletion reports it could not delete.
	deleteErr    error
	failDeleting map[string]bool
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

func (fake *fakeQueue) DeleteMessageBatch(ctx context.Context, input *sqs.DeleteMessageBatchInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if fake.deleteErr != nil {
		return nil, fake.deleteErr
	}
	output := &sqs.DeleteMessageBatchOutput{}
	for _, entry := range input.Entries {
		receipt := aws.ToString(entry.ReceiptHandle)
		if fake.failDeleting[receipt] {
			output.Failed = append(output.Failed, sqstypes.BatchResultErrorEntry{
				Id: entry.Id, Code: aws.String("ReceiptHandleIsInvalid"), Message: aws.String("the receipt handle has expired"), SenderFault: true,
			})
			continue
		}
		fake.deleted = append(fake.deleted, receipt)
		output.Successful = append(output.Successful, sqstypes.DeleteMessageBatchResultEntry{Id: entry.Id})
	}
	return output, nil
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
		case "org_refused":
			// The Worker refusing the body itself, as it does one it cannot
			// read as a purge.
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(writer, `{"error":{"code":"invalid_request","message":"A purge needs event_id, organization_id, a scope that agrees with user_id, and removed_at."}}`)
		case "org_misrouted":
			// What a Worker without the route answers.
			writer.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(writer, "Not found")
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
			message("refused", organizationDeleted("evt_refused", "org_refused")),
			message("worker-down", organizationDeleted("evt_down", "org_down")),
		},
		{
			message("misrouted", organizationDeleted("evt_misrouted", "org_misrouted")),
			message("undecodable", "{"),
			// Delivered again after its deletion failed: done already.
			message("member-left-again", memberRemoved("evt_left", "org_1", "user-2", "left")),
		},
	}}
	var logs strings.Builder
	_, verifier := authHarness()
	consumer, err := NewConsumer(client, &fakeNoter{}, &fakeRecorder{}, verifier, slog.New(slog.NewJSONHandler(&logs, nil)))
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
		`{"event_id":"evt_member_of_deleted","organization_id":"org_1","scope":"member","user_id":"user-3","removed_at":"2026-09-24T11:59:58.123456789Z"}`,
		`{"event_id":"evt_refused","organization_id":"org_refused","scope":"organization","user_id":"","removed_at":"2026-09-24T11:59:59.5Z"}`,
		`{"event_id":"evt_down","organization_id":"org_down","scope":"organization","user_id":"","removed_at":"2026-09-24T11:59:59.5Z"}`,
		`{"event_id":"evt_misrouted","organization_id":"org_misrouted","scope":"organization","user_id":"","removed_at":"2026-09-24T11:59:59.5Z"}`,
	}
	if !slices.Equal(bodies, wantBodies) {
		t.Fatalf("the Worker was sent %v, want %v", bodies, wantBodies)
	}
	if !strings.Contains(logs.String(), "realtime purge dropped") || !strings.Contains(logs.String(), "invalid_request") {
		t.Fatalf("the refusal was not logged with the Worker's answer:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "the queue will deliver it again") {
		t.Fatalf("the failure worth retrying was not logged:\n%s", logs.String())
	}
}

// A teamspace's purges reach the Worker through the real client as the
// contract has them: teamspace_id after user_id, which is empty for a deleted
// teamspace, and removed_at as agents said it, to the nanosecond. Each is
// recorded, with its teamspace, and deleted.
func TestPollSendsTheWorkerATeamspacesPurges(t *testing.T) {
	const secret = "a-secret-the-worker-holds-too-0123456789"
	var (
		mu     sync.Mutex
		bodies []string
	)
	worker := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		var stamp int64
		if _, err := fmt.Sscan(request.Header.Get(purge.TimestampHeader), &stamp); err != nil ||
			request.Header.Get(purge.SignatureHeader) != purge.Sign(secret, stamp, body) {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer worker.Close()
	client, err := purge.New(purge.Config{WorkerURL: worker.URL, Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := &fakeQueue{cancel: cancel, batches: [][]sqstypes.Message{{
		message("left-space", teamspaceMemberRemoved("evt_left_space", "org_1", "tsp_1", "user-2")),
		message("space-deleted", teamspaceDeleted("evt_space_deleted", "org_1", "tsp_1")),
	}}}
	recorder := &fakeRecorder{}
	newRecordingConsumer(t, client, recorder).Poll(ctx, queue, "http://localhost:4566/000000000000/realtime-events")

	if want := []string{"left-space", "space-deleted"}; !slices.Equal(queue.deleted, want) {
		t.Fatalf("deleted = %v, want %v", queue.deleted, want)
	}
	wantBodies := []string{
		`{"event_id":"evt_left_space","organization_id":"org_1","scope":"teamspace_member","user_id":"user-2","teamspace_id":"tsp_1","removed_at":"2026-09-24T11:59:57.987654321Z"}`,
		`{"event_id":"evt_space_deleted","organization_id":"org_1","scope":"teamspace","user_id":"","teamspace_id":"tsp_1","removed_at":"2026-09-24T11:59:59.25Z"}`,
	}
	if !slices.Equal(bodies, wantBodies) {
		t.Fatalf("the Worker was sent %v, want %v", bodies, wantBodies)
	}
	completed := recorder.recorded()
	if len(completed) != 2 ||
		completed[0].Scope != realtimev1.PurgeScopeTeamspaceMember || completed[0].UserID != "user-2" || completed[0].TeamspaceID != "tsp_1" ||
		completed[1].Scope != realtimev1.PurgeScopeTeamspace || completed[1].UserID != "" || completed[1].TeamspaceID != "tsp_1" {
		t.Fatalf("recorded %+v, want both purges with their teamspace", completed)
	}
}

// purgerFunc is a Purger made of a function.
type purgerFunc func(context.Context, purge.Request) error

func (purgeWith purgerFunc) Purge(ctx context.Context, request purge.Request) error {
	return purgeWith(ctx, request)
}

// A purge the Worker has done is recorded and deleted even when the process
// is told to stop while it was being sent; left, it would come back to the
// next process and be sent again. What was not yet handled goes back to the
// queue.
func TestPollDeletesWhatItPurgedAsItStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopping := purgerFunc(func(context.Context, purge.Request) error {
		cancel()
		return nil
	})
	queue := &fakeQueue{cancel: cancel, batches: [][]sqstypes.Message{{
		message("purged", memberRemoved("evt_1", "org_1", "user-2", autheventsv1.MemberRemovedReasonLeft)),
		message("not-yet", memberRemoved("evt_2", "org_1", "user-3", autheventsv1.MemberRemovedReasonLeft)),
	}}}
	recorder := &fakeRecorder{}
	newRecordingConsumer(t, stopping, recorder).Poll(ctx, queue, "http://localhost:4566/000000000000/realtime-events")
	if !slices.Equal(queue.deleted, []string{"purged"}) {
		t.Fatalf("deleted = %v, want the purged message alone", queue.deleted)
	}
	if completed := recorder.recorded(); len(completed) != 1 || completed[0].SourceEventID != "evt_1" {
		t.Fatalf("recorded %+v, want the purge done as the process stopped", completed)
	}
}

// A deletion that fails leaves the message to come back, which the dedupe
// and the Worker's idempotence make harmless, and is said.
func TestPollSaysWhenADeletionFails(t *testing.T) {
	for name, queue := range map[string]*fakeQueue{
		"the whole batch": {deleteErr: errors.New("the queue could not be reached")},
		"one message":     {failDeleting: map[string]bool{"purged": true}},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			queue.cancel = cancel
			queue.batches = [][]sqstypes.Message{{message("purged", memberRemoved("evt_1", "org_1", "user-2", autheventsv1.MemberRemovedReasonLeft))}}
			var logs strings.Builder
			_, verifier := authHarness()
			consumer, err := NewConsumer(&fakePurger{}, &fakeNoter{}, &fakeRecorder{}, verifier, slog.New(slog.NewJSONHandler(&logs, nil)))
			if err != nil {
				t.Fatal(err)
			}
			consumer.Poll(ctx, queue, "http://localhost:4566/000000000000/realtime-events")
			if len(queue.deleted) != 0 || !strings.Contains(logs.String(), `"msg":"delete handled message`) {
				t.Fatalf("deleted = %v; want nothing deleted, and the failure logged:\n%s", queue.deleted, logs.String())
			}
		})
	}
}

// A purge dropped for good is logged whole, as the Worker was sent it, so it
// can be sent again by hand once whatever refused it is put right.
func TestPollLogsADroppedPurgeWhole(t *testing.T) {
	for name, test := range map[string]struct {
		body string
		want map[string]string
	}{
		"a member's": {memberRemoved("evt_refused", "org_1", "user-2", autheventsv1.MemberRemovedReasonRemoved), map[string]string{
			"sqs_message_id": "refused", "event_id": "evt_refused", "scope": "member",
			"organization_id": "org_1", "user_id": "user-2", "teamspace_id": "", "removed_at": removedAt,
		}},
		"a teamspace member's": {teamspaceMemberRemoved("evt_refused", "org_1", "tsp_1", "user-2"), map[string]string{
			"sqs_message_id": "refused", "event_id": "evt_refused", "scope": "teamspace_member",
			"organization_id": "org_1", "user_id": "user-2", "teamspace_id": "tsp_1", "removed_at": leftAt,
		}},
	} {
		t.Run(name, func(t *testing.T) {
			purger := &fakePurger{fail: map[string]error{"evt_refused": fmt.Errorf("%w: it answered 400 Bad Request", purge.ErrPermanent)}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			queue := &fakeQueue{cancel: cancel, batches: [][]sqstypes.Message{{message("refused", test.body)}}}
			var logs strings.Builder
			_, verifier := authHarness()
			consumer, err := NewConsumer(purger, &fakeNoter{}, &fakeRecorder{}, verifier, slog.New(slog.NewJSONHandler(&logs, nil)))
			if err != nil {
				t.Fatal(err)
			}
			consumer.Poll(ctx, queue, "http://localhost:4566/000000000000/realtime-events")

			if !slices.Equal(queue.deleted, []string{"refused"}) {
				t.Fatalf("deleted = %v, want the refused purge dropped", queue.deleted)
			}
			dropped := logged(t, logs.String(), "realtime purge dropped")
			for key, want := range test.want {
				if got := dropped[key]; got != want {
					t.Errorf("the dropped purge's %s = %v, want %q", key, got, want)
				}
			}
		})
	}
}

// A removal still to come did not happen: purged up to it, a member would
// lose what they wrote after being invited back. Within a few minutes it is
// only two clocks disagreeing.
func TestConsumerDropsARemovalStillToCome(t *testing.T) {
	for name, event := range map[string]struct {
		body string
		at   time.Time
	}{
		"a member's":           {memberRemoved("evt_member", "org_1", "user-2", autheventsv1.MemberRemovedReasonRemoved), at(removedAt)},
		"an organization's":    {organizationDeleted("evt_organization", "org_1"), at(deletedAt)},
		"a teamspace member's": {teamspaceMemberRemoved("evt_teamspace_member", "org_1", "tsp_1", "user-2"), at(leftAt)},
		"a teamspace's":        {teamspaceDeleted("evt_teamspace", "org_1", "tsp_1"), at(changedAt)},
	} {
		t.Run(name, func(t *testing.T) {
			purger := &fakePurger{}
			consumer := newConsumer(t, purger)
			result, err := consumer.handle(context.Background(), event.body, event.at.Add(-maxClockSkew-time.Second))
			request := result.purge
			if !errors.Is(err, errFutureRemoval) || !request.RemovedAt.Equal(event.at) || len(purger.asked()) != 0 {
				t.Fatalf("handle() = %+v, %v; purges %+v; want the purge named, refused and not sent", request, err, purger.asked())
			}
			if _, err := consumer.handle(context.Background(), event.body, event.at.Add(-maxClockSkew+time.Second)); err != nil || len(purger.asked()) != 1 {
				t.Fatalf("within the skew: handle() = %v, purges %+v; want it purged", err, purger.asked())
			}
		})
	}
}

func TestPollDropsARemovalStillToComeLoudly(t *testing.T) {
	signer, _ := authHarness()
	future := authEvent(signer, autheventsv1.MemberRemovedDetailType, "evt_future", 1,
		`{"organization_id":"org_1","user_id":"user-2","reason":"removed","removed_at":"2099-01-01T00:00:00Z"}`)
	purger := &fakePurger{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := &fakeQueue{cancel: cancel, batches: [][]sqstypes.Message{{message("future", future)}}}
	var logs strings.Builder
	_, verifier := authHarness()
	consumer, err := NewConsumer(purger, &fakeNoter{}, &fakeRecorder{}, verifier, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	consumer.Poll(ctx, queue, "http://localhost:4566/000000000000/realtime-events")

	if !slices.Equal(queue.deleted, []string{"future"}) || len(purger.asked()) != 0 {
		t.Fatalf("deleted = %v, purges %+v; want it dropped unsent", queue.deleted, purger.asked())
	}
	dropped := logged(t, logs.String(), "realtime purge dropped")
	for key, want := range map[string]string{
		"event_id": "evt_future", "scope": "member", "organization_id": "org_1", "user_id": "user-2", "removed_at": "2099-01-01T00:00:00Z",
	} {
		if got := dropped[key]; got != want {
			t.Errorf("the dropped purge's %s = %v, want %q", key, got, want)
		}
	}
	if !strings.Contains(fmt.Sprint(dropped["error"]), "still to come") {
		t.Errorf("the drop's error = %v, want it to say why", dropped["error"])
	}
}

// logged is the JSON log record whose message starts with prefix.
func logged(t *testing.T, logs, prefix string) map[string]any {
	t.Helper()
	for line := range strings.Lines(logs) {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) == nil && strings.HasPrefix(fmt.Sprint(record["msg"]), prefix) {
			return record
		}
	}
	t.Fatalf("nothing logged starts with %q:\n%s", prefix, logs)
	return nil
}

func TestNewConsumerRefusesToRunBlind(t *testing.T) {
	_, verifier := authHarness()
	if _, err := NewConsumer(nil, &fakeNoter{}, &fakeRecorder{}, verifier, nil); err == nil {
		t.Fatal("NewConsumer() without a purger = nil error")
	}
	if _, err := NewConsumer(&fakePurger{}, nil, &fakeRecorder{}, verifier, nil); err == nil {
		t.Fatal("NewConsumer() without a noter = nil error")
	}
	if _, err := NewConsumer(&fakePurger{}, &fakeNoter{}, nil, verifier, nil); err == nil {
		t.Fatal("NewConsumer() without a recorder = nil error")
	}
	if _, err := NewConsumer(&fakePurger{}, &fakeNoter{}, &fakeRecorder{}, nil, nil); err == nil {
		t.Fatal("NewConsumer() without a verifier = nil error")
	}
}

// Every purge the Worker has done is recorded, once, as its completion: what
// it took, up to when, and auth's event that asked for it.
func TestConsumerRecordsEachPurgeTheWorkerHasDone(t *testing.T) {
	purger := &fakePurger{fail: map[string]error{"evt_down": errors.New("the Worker answered 503 Service Unavailable, not 204")}}
	recorder := &fakeRecorder{}
	consumer := newRecordingConsumer(t, purger, recorder)
	ctx := context.Background()
	before := time.Now()
	for _, body := range []string{
		memberRemoved("evt_left", "org_1", "user-2", autheventsv1.MemberRemovedReasonLeft),
		organizationDeleted("evt_deleted", "org_1"),
		// Delivered again: purged and recorded already.
		memberRemoved("evt_left", "org_1", "user-2", autheventsv1.MemberRemovedReasonLeft),
		teamspaceMemberRemoved("evt_left_space", "org_1", "tsp_1", "user-2"),
		teamspaceDeleted("evt_space_deleted", "org_1", "tsp_1"),
	} {
		if err := consumer.Handle(ctx, body); err != nil {
			t.Fatal(err)
		}
	}
	// A purge the Worker did not do is not recorded.
	if err := consumer.Handle(ctx, organizationDeleted("evt_down", "org_2")); err == nil {
		t.Fatal("Handle() = nil for a purge the Worker did not do")
	}

	completed := recorder.recorded()
	if len(completed) != 4 {
		t.Fatalf("recorded %+v, want the four purges done, once each", completed)
	}
	want := []realtimev1.PurgeCompleted{
		{OrganizationID: "org_1", Scope: realtimev1.PurgeScopeMember, UserID: "user-2", RemovedAt: at(removedAt), SourceEventID: "evt_left"},
		{OrganizationID: "org_1", Scope: realtimev1.PurgeScopeOrganization, RemovedAt: at(deletedAt), SourceEventID: "evt_deleted"},
		{
			OrganizationID: "org_1", Scope: realtimev1.PurgeScopeTeamspaceMember, UserID: "user-2", TeamspaceID: "tsp_1",
			RemovedAt: at(leftAt), SourceEventID: "evt_left_space",
		},
		{
			OrganizationID: "org_1", Scope: realtimev1.PurgeScopeTeamspace, TeamspaceID: "tsp_1",
			RemovedAt: at(changedAt), SourceEventID: "evt_space_deleted",
		},
	}
	for index, got := range completed {
		expected := want[index]
		if got.OrganizationID != expected.OrganizationID || got.Scope != expected.Scope || got.UserID != expected.UserID ||
			got.TeamspaceID != expected.TeamspaceID ||
			!got.RemovedAt.Equal(expected.RemovedAt) || got.SourceEventID != expected.SourceEventID ||
			got.CompletedAt.Before(before) || got.CompletedAt.After(time.Now()) || got.CompletedAt.Location() != time.UTC {
			t.Errorf("recorded %+v, want %+v, completed while it was handled, in UTC", got, expected)
		}
	}
}

// A purge done whose completion could not be recorded is still done. Its
// message comes back, the Worker purges again, idempotently, and it is
// recorded then.
func TestConsumerPurgesAgainWhenItCouldNotRecordThePurge(t *testing.T) {
	purger := &fakePurger{}
	recorder := &fakeRecorder{}
	recorder.failWith(errors.New("the database is unreachable"))
	consumer := newRecordingConsumer(t, purger, recorder)
	ctx := context.Background()
	body := memberRemoved("evt_left", "org_1", "user-2", autheventsv1.MemberRemovedReasonLeft)

	err := consumer.Handle(ctx, body)
	if !errors.Is(err, errUnrecorded) || errors.Is(err, purge.ErrPermanent) || !strings.Contains(err.Error(), "the database is unreachable") {
		t.Fatalf("Handle() = %v, want the purge done but unrecorded, worth delivering again", err)
	}
	recorder.failWith(nil)
	if err := consumer.Handle(ctx, body); err != nil {
		t.Fatal(err)
	}
	if err := consumer.Handle(ctx, body); err != nil {
		t.Fatal(err)
	}
	if purges, completed := len(purger.asked()), len(recorder.recorded()); purges != 2 || completed != 1 {
		t.Fatalf("purges = %d, recorded = %d; want the Worker asked again until the purge was recorded, then not", purges, completed)
	}
}

// Poll keeps such a message for the queue to deliver again, and says the
// purge itself was done.
func TestPollKeepsAPurgeItCouldNotRecord(t *testing.T) {
	recorder := &fakeRecorder{}
	recorder.failWith(errors.New("the database is unreachable"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := &fakeQueue{cancel: cancel, batches: [][]sqstypes.Message{
		{message("unrecorded", memberRemoved("evt_left", "org_1", "user-2", autheventsv1.MemberRemovedReasonLeft))},
	}}
	var logs strings.Builder
	_, verifier := authHarness()
	consumer, err := NewConsumer(&fakePurger{}, &fakeNoter{}, recorder, verifier, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	consumer.Poll(ctx, queue, "http://localhost:4566/000000000000/realtime-events")
	if len(queue.deleted) != 0 {
		t.Fatalf("deleted = %v, want the message kept", queue.deleted)
	}
	record := logged(t, logs.String(), "realtime purge done but not recorded")
	if record["event_id"] != "evt_left" || record["organization_id"] != "org_1" || !strings.Contains(fmt.Sprint(record["error"]), "unreachable") {
		t.Fatalf("logged %v", record)
	}
}
