package inbound

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/surfaces"
)

// handled is one event a service was handed.
type handled struct{ detailType, eventID, data string }

// fakeHandler is the router, or the surfaces service, as the consumer sees it.
type fakeHandler struct {
	mu     sync.Mutex
	events []handled
	err    error
}

func (handler *fakeHandler) HandleEvent(_ context.Context, detailType, eventID string, data []byte) error {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	handler.events = append(handler.events, handled{detailType, eventID, string(data)})
	return handler.err
}

func (handler *fakeHandler) got() []handled {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	return slices.Clone(handler.events)
}

// slackChange is the ledger's projection of a request asked in Slack.
func slackChange(status string, revision int64) agentsv1.InteractionChanged {
	return agentsv1.InteractionChanged{
		RequestID: "irq_1", OrganizationID: "org_1", RequesterUserID: "user_a", RequesterAgentID: "agt_a",
		TargetUserID: "user_b", TargetAgentID: "agt_b", Status: status, Revision: revision,
		ChangedAt: time.Unix(100, 0).UTC(), Origin: agentsv1.InteractionOriginSlack, AskID: "iak_1",
	}
}

// Every agents request event reaches the service that says what became of a
// Slack request, before the delivery recorder, and for every status: an answer
// held for review wakes nobody in the recorder and still has its status said in
// the thread.
func TestTheLedgersRequestEventsReachTheSurfacesFirstAndForEveryStatus(t *testing.T) {
	recorder := &fakeInteractionRecorder{}
	consumer, err := NewConsumer(&fakeRecorder{}, recorder, nil, testVerifier(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	service := &fakeHandler{}
	consumer.WithSurfaces(service)
	ctx := context.Background()
	for _, event := range []struct {
		detailType string
		status     string
		revision   int64
	}{
		{agentsv1.InteractionRequestedDetailType, "pending", 1},
		{agentsv1.InteractionDecidedDetailType, "approved", 2},
		{agentsv1.InteractionCompletedDetailType, "review", 3},
		{agentsv1.InteractionCompletedDetailType, "completed", 4},
	} {
		body := delivery(t, agentsv1.Source, event.detailType, "evt_"+event.status, slackChange(event.status, event.revision))
		if err := consumer.Handle(ctx, body); err != nil {
			t.Fatalf("%s: %v", event.status, err)
		}
	}
	got := service.got()
	if len(got) != 4 || got[2].detailType != agentsv1.InteractionCompletedDetailType || got[2].eventID != "evt_review" || !strings.Contains(got[2].data, `"status":"review"`) {
		t.Fatalf("surfaces heard %+v, want all four events, the held answer's among them", got)
	}
	// The recorder still sees what it always did: nobody is woken for a held answer.
	if len(recorder.deliveries) != 3 {
		t.Fatalf("the recorder kept %d deliveries, want the three it records for a request, an approval and a completion", len(recorder.deliveries))
	}
}

// A service that cannot take an event fails it, so that the queue delivers it
// again: the event is not lost, and the recorder is not run on an event whose
// Slack side did not happen.
func TestASurfacesFailureFailsTheEvent(t *testing.T) {
	recorder := &fakeInteractionRecorder{}
	consumer, err := NewConsumer(&fakeRecorder{}, recorder, nil, testVerifier(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	consumer.WithSurfaces(&fakeHandler{err: errors.New("database is away")})
	body := delivery(t, agentsv1.Source, agentsv1.InteractionRequestedDetailType, "evt_1", slackChange("pending", 1))
	if err := consumer.Handle(context.Background(), body); err == nil || !strings.Contains(err.Error(), "database is away") {
		t.Fatalf("Handle() = %v, want the service's failure", err)
	}
	if len(recorder.deliveries) != 0 {
		t.Fatalf("the recorder ran for an event that failed: %+v", recorder.deliveries)
	}
}

// With the loop off, nothing is handed to any service and every event is what
// it was before there was one.
func TestWithoutTheSlackLoopTheRequestEventsAreAsTheyWere(t *testing.T) {
	recorder := &fakeInteractionRecorder{}
	consumer, err := NewConsumer(&fakeRecorder{}, recorder, nil, testVerifier(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	body := delivery(t, agentsv1.Source, agentsv1.InteractionRequestedDetailType, "evt_1", slackChange("pending", 1))
	if err := consumer.Handle(context.Background(), body); err != nil || len(recorder.deliveries) != 1 {
		t.Fatalf("Handle() = %v with %d deliveries, want the request recorded", err, len(recorder.deliveries))
	}
}

// The events apps publishes for a delivery from Slack are the router's, and
// nobody else's: a message and a click each become one job.
func TestASlackDeliveryIsTheRoutersJob(t *testing.T) {
	consumer, err := NewConsumer(&fakeRecorder{}, nil, nil, testVerifier(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	router := &fakeHandler{}
	consumer.WithSlackRouter(router)
	ctx := context.Background()
	message := appsv1.SlackMessageReceived{InboundID: "asi_1", OrganizationID: "org_1", TeamID: "T1", ReceivedAt: time.Unix(100, 0).UTC()}
	action := appsv1.SlackActionReceived{InboundID: "asi_2", OrganizationID: "org_1", TeamID: "T1", ReceivedAt: time.Unix(101, 0).UTC()}
	if err := consumer.Handle(ctx, delivery(t, appsv1.Source, appsv1.SlackMessageReceivedDetailType, "evt_m", message)); err != nil {
		t.Fatal(err)
	}
	if err := consumer.Handle(ctx, delivery(t, appsv1.Source, appsv1.SlackActionReceivedDetailType, "evt_a", action)); err != nil {
		t.Fatal(err)
	}
	got := router.got()
	if len(got) != 2 || got[0].detailType != appsv1.SlackMessageReceivedDetailType || got[0].eventID != "evt_m" ||
		got[1].detailType != appsv1.SlackActionReceivedDetailType || !strings.Contains(got[1].data, `"inbound_id":"asi_2"`) {
		t.Fatalf("the router heard %+v", got)
	}
	// Whatever the router cannot take is the event's failure.
	failing, err := NewConsumer(&fakeRecorder{}, nil, nil, testVerifier(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	failing.WithSlackRouter(&fakeHandler{err: errors.New("no")})
	if err := failing.Handle(ctx, delivery(t, appsv1.Source, appsv1.SlackMessageReceivedDetailType, "evt_m", message)); err == nil {
		t.Fatal("a delivery the router could not take was acknowledged")
	}
}

// A process with the loop off is not woken by these events, and does not fail
// them either: the rule that delivers them is provisioned apart from any
// process's switches, and a failure would send each to the dead-letter queue.
func TestASlackDeliveryIsAcknowledgedAndDroppedWithTheLoopOff(t *testing.T) {
	consumer, err := NewConsumer(&fakeRecorder{}, nil, nil, testVerifier(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	message := appsv1.SlackMessageReceived{InboundID: "asi_1", OrganizationID: "org_1", TeamID: "T1", ReceivedAt: time.Unix(100, 0).UTC()}
	if err := consumer.Handle(ctx, delivery(t, appsv1.Source, appsv1.SlackMessageReceivedDetailType, "evt_m", message)); err != nil {
		t.Fatalf("a message with the loop off = %v, want it acknowledged", err)
	}
	ended := appsv1.SlackInstallChanged{OrganizationID: "org_1", TeamID: "T1", State: appsv1.SlackInstallEnded, Reason: appsv1.SlackAppUninstalled, ChangedAt: time.Unix(100, 0).UTC()}
	if err := consumer.Handle(ctx, delivery(t, appsv1.Source, appsv1.SlackInstallChangedDetailType, "evt_i", ended)); err != nil {
		t.Fatalf("an install ending with the loop off = %v, want it acknowledged", err)
	}
}

// A workspace's install ending has everything the loop holds for it removed,
// each service's part running whatever became of the others'; one coming, or
// staying, active removes nothing.
func TestAnEndedSlackInstallHasTheWorkspaceForgotten(t *testing.T) {
	consumer, err := NewConsumer(&fakeRecorder{}, nil, nil, testVerifier(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var calls []string
	forget := func(name string, err error) WorkspaceForgetter {
		return func(_ context.Context, organizationID, teamID string) error {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, name+":"+organizationID+":"+teamID)
			return err
		}
	}
	consumer.WithWorkspaceForgetters(forget("router", nil), forget("surfaces", nil))
	ctx := context.Background()

	active := appsv1.SlackInstallChanged{OrganizationID: "org_1", TeamID: "T1", State: appsv1.SlackInstallActive, Reason: appsv1.SlackInstalled, ChangedAt: time.Unix(100, 0).UTC()}
	if err := consumer.Handle(ctx, delivery(t, appsv1.Source, appsv1.SlackInstallChangedDetailType, "evt_active", active)); err != nil || len(calls) != 0 {
		t.Fatalf("an install becoming active: err=%v calls=%v, want nothing forgotten", err, calls)
	}
	ended := active
	ended.State, ended.Reason = appsv1.SlackInstallEnded, appsv1.SlackTokensRevoked
	if err := consumer.Handle(ctx, delivery(t, appsv1.Source, appsv1.SlackInstallChangedDetailType, "evt_ended", ended)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(calls, []string{"router:org_1:T1", "surfaces:org_1:T1"}) {
		t.Fatalf("forgotten = %v, want the router's and the surfaces' parts for org_1's T1", calls)
	}

	// One that fails leaves the other done, and the event failed, to come back.
	calls = nil
	failing, err := NewConsumer(&fakeRecorder{}, nil, nil, testVerifier(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	failing.WithWorkspaceForgetters(forget("router", errors.New("router down")), forget("surfaces", nil))
	if err := failing.Handle(ctx, delivery(t, appsv1.Source, appsv1.SlackInstallChangedDetailType, "evt_ended", ended)); err == nil || !strings.Contains(err.Error(), "router down") {
		t.Fatalf("Handle() = %v, want the router's failure", err)
	}
	if !slices.Equal(calls, []string{"router:org_1:T1", "surfaces:org_1:T1"}) {
		t.Fatalf("calls = %v, want the surfaces' part run beside the router's failure", calls)
	}

	// An event that names no workspace cannot be acted on, and is the queue's to keep.
	nowhere := ended
	nowhere.TeamID = ""
	if err := consumer.Handle(ctx, delivery(t, appsv1.Source, appsv1.SlackInstallChangedDetailType, "evt_nowhere", nowhere)); err == nil {
		t.Fatal("an install change that names no workspace was accepted")
	}
}

// What each service subscribes to is in what agents' rule matches, whichever
// service takes it: the rule is built from Subscriptions alone.
func TestEveryEventTheSlackServicesTakeIsSubscribedTo(t *testing.T) {
	subscriptions := Subscriptions()
	for _, services := range []map[string][]string{slackrouter.Subscriptions(), surfaces.Subscriptions()} {
		for source, detailTypes := range services {
			for _, detailType := range detailTypes {
				if !slices.Contains(subscriptions[source], detailType) {
					t.Errorf("%s from %s is taken by a service and not subscribed to: %v", detailType, source, subscriptions[source])
				}
			}
		}
	}
	// And the delivery recorder's own types stay what they were.
	for _, detailType := range []string{agentsv1.InteractionRequestedDetailType, agentsv1.InteractionDecidedDetailType, agentsv1.InteractionCompletedDetailType} {
		if !slices.Contains(subscriptions[agentsv1.Source], detailType) {
			t.Errorf("%s is no longer subscribed to", detailType)
		}
	}
}
