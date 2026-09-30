package events

import (
	"context"
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

	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	agentseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"

	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
	"github.com/Shubham04-oss/oryxa-go/realtime/internal/purge"
	"github.com/Shubham04-oss/oryxa-go/realtime/internal/requests"
)

// When agents says a request moved, which is the request's updated_at, to
// the nanosecond. It is not the envelope's occurred_at.
const movedAt = "2026-09-24T11:59:56.123456789Z"

// aRequest is what agents says of a request between user-a, who asked, and
// user-b, whose agent was asked, at a revision and in a status.
func aRequest(status string, revision int64) agentseventsv1.InteractionChanged {
	return agentseventsv1.InteractionChanged{
		RequestID: "irq_1", OrganizationID: "org_1",
		RequesterUserID: "user-a", RequesterAgentID: "agent-a", TargetUserID: "user-b", TargetAgentID: "agent-b",
		Status: status, Revision: revision, ChangedAt: at(movedAt),
	}
}

// interactionEvent is an SQS message body as EventBridge delivers an event
// agents publishes about a request: the envelope, signed by agents.
func interactionEvent(detailType, eventID string, changed agentseventsv1.InteractionChanged) string {
	signer, _ := agentsHarness()
	data, err := json.Marshal(changed)
	if err != nil {
		panic(err)
	}
	return agentsEvent(signer, detailType, eventID, 1, string(data))
}

// The three events that move a request each say where it stands, and any of
// them is a note: who asked and whose agent was asked, where it was made,
// which revision it is and the state that revision is in, and when.
func TestConsumerNotesEachMoveOfARequest(t *testing.T) {
	for _, test := range []struct {
		name       string
		detailType string
		status     string
		origin     string
		state      requests.State
		wantOrigin string
	}{
		{"a request made", agentseventsv1.InteractionRequestedDetailType, "pending", "", requests.StateWaiting, "agent"},
		{"a request made from Slack", agentseventsv1.InteractionRequestedDetailType, "pending", agentseventsv1.InteractionOriginSlack, requests.StateWaiting, "slack"},
		{"a request an agent made", agentseventsv1.InteractionRequestedDetailType, "pending", agentseventsv1.InteractionOriginAgent, requests.StateWaiting, "agent"},
		{"an approval", agentseventsv1.InteractionDecidedDetailType, "approved", "", requests.StateApproved, "agent"},
		{"a rejection", agentseventsv1.InteractionDecidedDetailType, "rejected", "", requests.StateDeclined, "agent"},
		{"an answer", agentseventsv1.InteractionCompletedDetailType, "completed", "", requests.StateAnswered, "agent"},
		{"an answer that failed", agentseventsv1.InteractionCompletedDetailType, "failed", "", requests.StateFailed, "agent"},
		{"an answer held for review", agentseventsv1.InteractionCompletedDetailType, "review", "", requests.StateHeld, "agent"},
		{"an answer withheld", agentseventsv1.InteractionCompletedDetailType, "withheld", "", requests.StateWithheld, "agent"},
		// Oryxa closing a request a teamspace no longer holds is a rejection or a failure.
		{"a request closed as declined", agentseventsv1.InteractionDecidedDetailType, "rejected", agentseventsv1.InteractionOriginSlack, requests.StateDeclined, "slack"},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := aRequest(test.status, 3)
			changed.Origin = test.origin
			noter := &fakeNoter{}
			consumer := newNotingConsumer(t, noter)
			if err := consumer.Handle(context.Background(), interactionEvent(test.detailType, "evt_1", changed)); err != nil {
				t.Fatal(err)
			}
			want := []requests.Note{{
				OrganizationID: "org_1", RequestID: "irq_1", Revision: 3, AskerUserID: "user-a", OwnerUserID: "user-b",
				Origin: test.wantOrigin, State: test.state, ChangedAt: at(movedAt),
			}}
			if got := noter.noted(); !sameNotes(got, want) {
				t.Fatalf("notes = %+v, want %+v", got, want)
			}
		})
	}
}

// A note is identities and a state, nothing of what was asked or answered:
// whatever else the event carries never leaves realtime.
func TestANoteCarriesIdentitiesAndAStateAlone(t *testing.T) {
	changed := aRequest("approved", 2)
	changed.Approval = agentseventsv1.InteractionApprovalOwner
	changed.AskID = "ask_1"
	changed.Origin = agentseventsv1.InteractionOriginSlack
	changed.Consent = agentseventsv1.InteractionConsentAccess
	changed.DecidedBy = &agentseventsv1.InteractionDecider{
		Kind: agentseventsv1.DecidedByMember, UserID: "user-b", Surface: agentseventsv1.DecidedViaSlack, Reason: "a reason",
	}
	noter := &fakeNoter{}
	if err := newNotingConsumer(t, noter).Handle(context.Background(),
		interactionEvent(agentseventsv1.InteractionDecidedDetailType, "evt_1", changed)); err != nil {
		t.Fatal(err)
	}
	got := noter.noted()
	if len(got) != 1 {
		t.Fatalf("notes = %+v, want one", got)
	}
	body, err := json.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(body, &keys); err != nil {
		t.Fatal(err)
	}
	var names []string
	for name := range keys {
		names = append(names, name)
	}
	slices.Sort(names)
	want := []string{"asker_user_id", "changed_at", "organization_id", "origin", "owner_user_id", "request_id", "revision", "state"}
	if !slices.Equal(names, want) {
		t.Fatalf("a note's keys = %v, want %v", names, want)
	}
	for _, unwanted := range []string{"agent-a", "agent-b", "ask_1", "access", "a reason", "member", "\"owner\""} {
		if strings.Contains(string(body), unwanted) {
			t.Errorf("a note carries %q: %s", unwanted, body)
		}
	}
}

// There is nothing to note for a request between one member's own agents (a
// conversation is between two people), for a status no note says, or for
// ids, or an origin, that a conversation's name or a note cannot hold. Each is
// logged, and done with: sending it again would say no more.
func TestConsumerNotesNothingWhereThereIsNothingToNote(t *testing.T) {
	for name, edit := range map[string]func(*agentseventsv1.InteractionChanged){
		"a request between one member's own agents": func(changed *agentseventsv1.InteractionChanged) { changed.TargetUserID = changed.RequesterUserID },
		"a request made in a teamspace, where a side conversation shows it": func(changed *agentseventsv1.InteractionChanged) {
			changed.TeamspaceID = "tsp_1"
		},
		"a status no note says":                func(changed *agentseventsv1.InteractionChanged) { changed.Status = "expired" },
		"a status this build has not heard of": func(changed *agentseventsv1.InteractionChanged) { changed.Status = "paused" },
		"an origin no note can carry":          func(changed *agentseventsv1.InteractionChanged) { changed.Origin = "Web App" },
		"an asker a name cannot hold":          func(changed *agentseventsv1.InteractionChanged) { changed.RequesterUserID = "user~a" },
		"an owner a name cannot hold":          func(changed *agentseventsv1.InteractionChanged) { changed.TargetUserID = "user b" },
		"an organization a name cannot hold":   func(changed *agentseventsv1.InteractionChanged) { changed.OrganizationID = "org.1" },
		"a request id a name cannot hold":      func(changed *agentseventsv1.InteractionChanged) { changed.RequestID = strings.Repeat("r", 129) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := aRequest("pending", 1)
			edit(&changed)
			noter := &fakeNoter{}
			var logs strings.Builder
			consumer := newNotingConsumerLogging(t, noter, &logs)
			if err := consumer.Handle(context.Background(), interactionEvent(agentseventsv1.InteractionRequestedDetailType, "evt_1", changed)); err != nil {
				t.Fatalf("Handle() = %v, want it done with", err)
			}
			if got := noter.noted(); len(got) != 0 {
				t.Fatalf("notes = %+v, want none", got)
			}
			skipped := logged(t, logs.String(), "realtime request not noted")
			if skipped["level"] != "WARN" || skipped["request_id"] != changed.RequestID {
				t.Fatalf("the skip was logged as %v, want a warning naming the request", skipped)
			}
		})
	}
}

// An interaction event that cannot be read stays on the queue, and ends in the
// dead-letter queue, as one of any consumer's does: nothing about it can be
// put right by sending it again, but an operator can see it.
func TestConsumerRefusesAnInteractionEventItCannotRead(t *testing.T) {
	signer, _ := agentsHarness()
	stranger, _, err := eventauth.NewLocalSigner("agents", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	valid := func() agentseventsv1.InteractionChanged { return aRequest("pending", 1) }
	dataOf := func(changed agentseventsv1.InteractionChanged) string {
		data, err := json.Marshal(changed)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	without := func(edit func(*agentseventsv1.InteractionChanged)) string {
		changed := valid()
		edit(&changed)
		return interactionEvent(agentseventsv1.InteractionRequestedDetailType, "evt_1", changed)
	}
	tests := map[string]string{
		"bad data":         agentsEvent(signer, agentseventsv1.InteractionRequestedDetailType, "evt_1", 1, `{"revision":"one"}`),
		"not an object":    agentsEvent(signer, agentseventsv1.InteractionRequestedDetailType, "evt_1", 1, `[]`),
		"no organization":  without(func(changed *agentseventsv1.InteractionChanged) { changed.OrganizationID = "" }),
		"no request":       without(func(changed *agentseventsv1.InteractionChanged) { changed.RequestID = "" }),
		"no requester":     without(func(changed *agentseventsv1.InteractionChanged) { changed.RequesterUserID = "" }),
		"no target":        without(func(changed *agentseventsv1.InteractionChanged) { changed.TargetUserID = "" }),
		"no revision":      without(func(changed *agentseventsv1.InteractionChanged) { changed.Revision = 0 }),
		"no time":          without(func(changed *agentseventsv1.InteractionChanged) { changed.ChangedAt = time.Time{} }),
		"a future version": agentsEvent(signer, agentseventsv1.InteractionRequestedDetailType, "evt_1", 2, dataOf(valid())),
		// Only what changes a request's state.
		"a lease revoked": agentsEvent(signer, agentseventsv1.InteractionLeaseRevokedDetailType, "evt_1", 1,
			`{"lease_id":"lease_1","organization_id":"org_1","owner_user_id":"user-b","asker_user_id":"user-a","channel_id":"C1","thread_ts":"1.2","expires_at":"2026-09-24T12:00:00Z","revoked_at":"2026-09-24T11:00:00Z"}`),
		"a secret viewed": agentsEvent(signer, agentseventsv1.InteractionSecretViewedDetailType, "evt_1", 1,
			`{"request_id":"irq_1","organization_id":"org_1","owner_user_id":"user-b","asker_user_id":"user-a","viewed_at":"2026-09-24T11:00:00Z"}`),
		// Agents' events only from agents, signed by agents.
		"a request from auth":    authEvent(mustAuthSigner(), agentseventsv1.InteractionRequestedDetailType, "evt_1", 1, dataOf(valid())),
		"signed by someone else": sourcedEvent(stranger, agentseventsv1.Source, agentseventsv1.InteractionRequestedDetailType, "evt_1", 1, dataOf(valid())),
		"signed by auth":         sourcedEvent(mustAuthSigner(), agentseventsv1.Source, agentseventsv1.InteractionRequestedDetailType, "evt_1", 1, dataOf(valid())),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			noter := &fakeNoter{}
			err := newNotingConsumer(t, noter).Handle(context.Background(), body)
			if err == nil || errors.Is(err, requests.ErrPermanent) || errors.Is(err, purge.ErrPermanent) {
				t.Fatalf("Handle() = %v, want an error that leaves the message for the dead-letter queue", err)
			}
			if got := noter.noted(); len(got) != 0 {
				t.Fatalf("notes = %+v, want none", got)
			}
		})
	}
}

func mustAuthSigner() *eventauth.Signer {
	signer, _ := authHarness()
	return signer
}

// A request's revision is noted once: SQS delivers at least once, and a
// message whose deletion failed comes back. A later revision is another note,
// and a note the Worker did not take is not remembered, so its redelivery
// sends it again.
func TestConsumerNotesARequestsRevisionOnce(t *testing.T) {
	noter := &fakeNoter{fail: map[string]error{"irq_1:3": errors.New("the Worker answered 502 Bad Gateway, not 204")}}
	consumer := newNotingConsumer(t, noter)
	ctx := context.Background()
	moved := func(eventID string, revision int64, status string) string {
		return interactionEvent(agentseventsv1.InteractionDecidedDetailType, eventID, aRequest(status, revision))
	}
	for range 3 {
		if err := consumer.Handle(ctx, moved("evt_1", 2, "approved")); err != nil {
			t.Fatal(err)
		}
	}
	// The same revision under another event id, as agents may publish it
	// again, is still the same note.
	if err := consumer.Handle(ctx, moved("evt_1_again", 2, "approved")); err != nil {
		t.Fatal(err)
	}
	if got := len(noter.noted()); got != 1 {
		t.Fatalf("notes for one revision delivered four times = %d, want 1", got)
	}
	if err := consumer.Handle(ctx, moved("evt_2", 3, "completed")); err == nil {
		t.Fatal("Handle() = nil for a note the Worker did not take")
	}
	delete(noter.fail, "irq_1:3")
	if err := consumer.Handle(ctx, moved("evt_2", 3, "completed")); err != nil {
		t.Fatal(err)
	}
	if got := len(noter.noted()); got != 3 {
		t.Fatalf("notes = %d, want the failed one sent again", got)
	}
	// Another request with the same revision is its own.
	other := aRequest("approved", 2)
	other.RequestID = "irq_2"
	if err := consumer.Handle(ctx, interactionEvent(agentseventsv1.InteractionDecidedDetailType, "evt_3", other)); err != nil {
		t.Fatal(err)
	}
	if got := len(noter.noted()); got != 4 {
		t.Fatalf("notes = %d, want another request's revision noted too", got)
	}
}

func TestConsumerSaysWhetherTheWorkerRefusedANoteForGood(t *testing.T) {
	noter := &fakeNoter{fail: map[string]error{
		"irq_1:1": fmt.Errorf("%w: it answered 400 Bad Request", requests.ErrPermanent),
		"irq_1:2": errors.New("the Worker answered 503 Service Unavailable, not 204"),
	}}
	consumer := newNotingConsumer(t, noter)
	refused := consumer.Handle(context.Background(), interactionEvent(agentseventsv1.InteractionRequestedDetailType, "evt_1", aRequest("pending", 1)))
	if !errors.Is(refused, requests.ErrPermanent) || !strings.Contains(refused.Error(), "evt_1") ||
		!strings.Contains(refused.Error(), "organization org_1, request irq_1") || !strings.Contains(refused.Error(), "400") {
		t.Fatalf("refused for good = %v, want ErrPermanent naming the event, organization, request and answer", refused)
	}
	down := consumer.Handle(context.Background(), interactionEvent(agentseventsv1.InteractionDecidedDetailType, "evt_2", aRequest("approved", 2)))
	if down == nil || errors.Is(down, requests.ErrPermanent) || !strings.Contains(down.Error(), "request irq_1") {
		t.Fatalf("Worker down = %v, want an error worth retrying that names the request", down)
	}
}

// Poll against an httptest Worker, through the real client, as a purge's is:
// what the Worker noted, and what it refused for good, is deleted, with the
// refusal logged whole; what it could not do yet, and what nobody could read,
// stays for the queue to deliver again.
func TestPollDeletesANotedRequestAndKeepsWhatMaySucceedLater(t *testing.T) {
	const secret = "a-secret-the-worker-holds-too-0123456789"
	var (
		mu     sync.Mutex
		bodies []string
	)
	worker := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		var stamp int64
		if request.URL.Path != requests.Path {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if _, err := fmt.Sscan(request.Header.Get(requests.TimestampHeader), &stamp); err != nil ||
			request.Header.Get(requests.SignatureHeader) != requests.Sign(secret, stamp, body) {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		var noted requests.Note
		_ = json.Unmarshal(body, &noted)
		switch noted.OrganizationID {
		case "org_refused":
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(writer, `{"error":{"code":"invalid_request","message":"A request note needs its ids."}}`)
		case "org_down":
			writer.WriteHeader(http.StatusServiceUnavailable)
		default:
			writer.WriteHeader(http.StatusNoContent)
		}
	}))
	defer worker.Close()
	noter, err := requests.New(requests.Config{WorkerURL: worker.URL, Secret: secret})
	if err != nil {
		t.Fatal(err)
	}

	in := func(organization, request string, revision int64, status string) agentseventsv1.InteractionChanged {
		changed := aRequest(status, revision)
		changed.OrganizationID, changed.RequestID = organization, request
		return changed
	}
	slack := in("org_1", "irq_2", 1, "pending")
	slack.Origin = agentseventsv1.InteractionOriginSlack
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := &fakeQueue{cancel: cancel, batches: [][]sqstypes.Message{
		{
			message("made", interactionEvent(agentseventsv1.InteractionRequestedDetailType, "evt_made", in("org_1", "irq_1", 1, "pending"))),
			message("made-in-slack", interactionEvent(agentseventsv1.InteractionRequestedDetailType, "evt_slack", slack)),
			message("answered", interactionEvent(agentseventsv1.InteractionCompletedDetailType, "evt_answered", in("org_1", "irq_1", 3, "completed"))),
			message("refused", interactionEvent(agentseventsv1.InteractionDecidedDetailType, "evt_refused", in("org_refused", "irq_3", 2, "approved"))),
			message("worker-down", interactionEvent(agentseventsv1.InteractionDecidedDetailType, "evt_down", in("org_down", "irq_4", 2, "rejected"))),
		},
		{
			message("unreadable", interactionEvent(agentseventsv1.InteractionRequestedDetailType, "evt_unreadable", agentseventsv1.InteractionChanged{RequestID: "irq_5"})),
			// A purge on the same queue is handled as ever.
			message("member-left", memberRemoved("evt_left", "org_1", "user-2", "left")),
			// Delivered again after its deletion failed: noted already.
			message("made-again", interactionEvent(agentseventsv1.InteractionRequestedDetailType, "evt_made", in("org_1", "irq_1", 1, "pending"))),
		},
	}}
	var logs strings.Builder
	_, verifier := authHarness()
	consumer, err := NewConsumer(&fakePurger{}, noter, &fakeRecorder{}, verifier, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	consumer.Poll(ctx, queue, "http://localhost:4566/000000000000/realtime-events")

	if want := []string{"made", "made-in-slack", "answered", "refused", "member-left", "made-again"}; !slices.Equal(queue.deleted, want) {
		t.Fatalf("deleted = %v, want %v", queue.deleted, want)
	}
	// The purge went to the purge route; the Worker above would have answered
	// 404 to a note sent there, and took nothing else.
	wantBodies := []string{
		`{"organization_id":"org_1","request_id":"irq_1","revision":1,"asker_user_id":"user-a","owner_user_id":"user-b","origin":"agent","state":"waiting","changed_at":"2026-09-24T11:59:56.123456789Z"}`,
		`{"organization_id":"org_1","request_id":"irq_2","revision":1,"asker_user_id":"user-a","owner_user_id":"user-b","origin":"slack","state":"waiting","changed_at":"2026-09-24T11:59:56.123456789Z"}`,
		`{"organization_id":"org_1","request_id":"irq_1","revision":3,"asker_user_id":"user-a","owner_user_id":"user-b","origin":"agent","state":"answered","changed_at":"2026-09-24T11:59:56.123456789Z"}`,
		`{"organization_id":"org_refused","request_id":"irq_3","revision":2,"asker_user_id":"user-a","owner_user_id":"user-b","origin":"agent","state":"approved","changed_at":"2026-09-24T11:59:56.123456789Z"}`,
		`{"organization_id":"org_down","request_id":"irq_4","revision":2,"asker_user_id":"user-a","owner_user_id":"user-b","origin":"agent","state":"declined","changed_at":"2026-09-24T11:59:56.123456789Z"}`,
	}
	if !slices.Equal(bodies, wantBodies) {
		t.Fatalf("the Worker was sent %v, want %v", bodies, wantBodies)
	}
	dropped := logged(t, logs.String(), "realtime request note dropped")
	for key, want := range map[string]string{
		"request_id": "irq_3", "organization_id": "org_refused", "asker_user_id": "user-a", "owner_user_id": "user-b",
		"origin": "agent", "state": "approved", "changed_at": "2026-09-24T11:59:56.123456789Z",
	} {
		if got := dropped[key]; got != want {
			t.Errorf("the dropped note's %s = %v, want %q", key, got, want)
		}
	}
	if !strings.Contains(fmt.Sprint(dropped["error"]), "invalid_request") {
		t.Errorf("the drop's error = %v, want the Worker's answer", dropped["error"])
	}
	if failed := logged(t, logs.String(), "realtime request note failed; the queue will deliver it again"); !strings.Contains(fmt.Sprint(failed["error"]), "503") {
		t.Errorf("the failure worth retrying = %v, want the Worker's 503", failed)
	}
	if !strings.Contains(logs.String(), "realtime request noted") || !strings.Contains(logs.String(), "realtime request already noted") {
		t.Fatalf("what was noted, and what was noted already, were not logged:\n%s", logs.String())
	}
}

// newNotingConsumer is a consumer noting through noter, and purging through
// a purger that is never asked.
func newNotingConsumer(t *testing.T, noter Noter) *Consumer {
	t.Helper()
	return newNotingConsumerLogging(t, noter, io.Discard)
}

func newNotingConsumerLogging(t *testing.T, noter Noter, logs io.Writer) *Consumer {
	t.Helper()
	_, verifier := agentsHarness()
	consumer, err := NewConsumer(&fakePurger{}, noter, &fakeRecorder{}, verifier, slog.New(slog.NewJSONHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return consumer
}

// sameNotes compares notes field by field, and their times as instants.
func sameNotes(got, want []requests.Note) bool {
	return slices.EqualFunc(got, want, func(a, b requests.Note) bool {
		return a.OrganizationID == b.OrganizationID && a.RequestID == b.RequestID && a.Revision == b.Revision &&
			a.AskerUserID == b.AskerUserID && a.OwnerUserID == b.OwnerUserID && a.Origin == b.Origin &&
			a.State == b.State && a.ChangedAt.Equal(b.ChangedAt)
	})
}
