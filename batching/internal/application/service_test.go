package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"
	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	batchingv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/batching/v1"

	"github.com/Shubham04-oss/oryxa-go/batching/internal/domain"
)

var testClock = time.Date(2026, time.September, 19, 12, 0, 0, 0, time.UTC)

type fakeStore struct {
	recorded  []domain.Trigger
	screened  domain.Admission
	admission domain.Admission
	batches   []domain.Batch
	completed []domain.Event
	forgot    []string
	discarded []string
	unclaimed []string
	claimErr  error
	// swept is the cutoff each sweep asked tombstones to be deleted before.
	swept []time.Time
}

func (store *fakeStore) Screen(context.Context, domain.Trigger) (domain.Admission, error) {
	return store.screened, nil
}
func (store *fakeStore) Record(_ context.Context, trigger domain.Trigger) (domain.Admission, error) {
	if store.admission != domain.Admitted {
		return store.admission, nil
	}
	store.recorded = append(store.recorded, trigger)
	return domain.Admitted, nil
}
func (store *fakeStore) ClaimDue(context.Context, domain.Rule, time.Duration, int) ([]domain.Batch, error) {
	return store.batches, store.claimErr
}
func (store *fakeStore) Unclaim(_ context.Context, _ string, ids []string) error {
	store.unclaimed = append(store.unclaimed, ids...)
	return nil
}
func (store *fakeStore) Complete(_ context.Context, _ domain.Batch, event domain.Event) error {
	store.completed = append(store.completed, event)
	return nil
}
func (store *fakeStore) Forget(_ context.Context, organizationID, memberID string) error {
	store.forgot = append(store.forgot, organizationID+"/"+memberID)
	return nil
}
func (store *fakeStore) Discard(_ context.Context, organizationID, connectionID, triggerID string) error {
	store.discarded = append(store.discarded, organizationID+"/"+connectionID+"/"+triggerID)
	return nil
}
func (*fakeStore) DeleteBatched(context.Context, time.Time, int) (int, error) { return 0, nil }
func (store *fakeStore) DeleteForgotten(_ context.Context, before time.Time) (int, error) {
	store.swept = append(store.swept, before)
	return 0, nil
}

func TestTriggerReceivedUsesTheClassifierAndPreservesThePayload(t *testing.T) {
	store := &fakeStore{}
	service := New(store, ClassifierFunc(func(context.Context, appsv1.TriggerReceived) (domain.Classification, error) {
		return domain.Classification{Lane: domain.LaneImmediate, Priority: .98, Classifier: "jev-test", Reason: "direct mention"}, nil
	}), Config{}, nil)
	trigger := appsv1.TriggerReceived{
		TriggerID: "apt_1", ConnectionID: "apc_1", OrganizationID: "org_1", MemberID: "user_1",
		Toolkit: "slack", Trigger: "message", Data: json.RawMessage(`{"text":"@Asha help"}`), ReceivedAt: testClock,
	}
	if err := service.TriggerReceived(context.Background(), "evt_apps_1", trigger); err != nil {
		t.Fatal(err)
	}
	got := store.recorded[0]
	if got.ID != "evt_apps_1" || got.Classification.Lane != domain.LaneImmediate || got.Classification.Priority != .98 ||
		string(got.Data) != string(trigger.Data) {
		t.Fatalf("recorded = %+v", got)
	}
}

func TestResilientClassifierAcceleratesInsteadOfLosingAnEvent(t *testing.T) {
	classifier := NewResilientClassifier(ClassifierFunc(func(context.Context, appsv1.TriggerReceived) (domain.Classification, error) {
		return domain.Classification{}, errors.New("provider unavailable")
	}), nil)
	classification, err := classifier.Classify(context.Background(), appsv1.TriggerReceived{Toolkit: "slack", Trigger: "message"})
	if err != nil {
		t.Fatal(err)
	}
	if classification.Lane != domain.LaneDigest || classification.Priority != domain.FallbackPriority || classification.Classifier != "jev-fallback.v1" {
		t.Fatalf("classification = %+v", classification)
	}
}

func TestReadyEmitsOneBatchEventWithOriginalTriggers(t *testing.T) {
	store := &fakeStore{}
	service := New(store, nil, Config{}, nil)
	service.now = func() time.Time { return testClock.Add(time.Minute) }
	batch := domain.Batch{ID: "bat_1", OrganizationID: "org_1", MemberID: "user_1", Lane: domain.LaneDigest,
		Triggers: []domain.Trigger{{
			ID: "evt_1", TriggerID: "apt_1", ConnectionID: "apc_1", Toolkit: "github", Trigger: "issue",
			Data: json.RawMessage(`{"title":"broken"}`), ReceivedAt: testClock,
			Classification: domain.Classification{Lane: domain.LaneDigest, Classifier: "default-digest.v1"},
		}}}
	if err := service.ready(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	if len(store.completed) != 1 || store.completed[0].DetailType != batchingv1.TriggerBatchReadyDetailType {
		t.Fatalf("completed = %+v", store.completed)
	}
	var ready batchingv1.TriggerBatchReady
	if err := json.Unmarshal(store.completed[0].Data, &ready); err != nil {
		t.Fatal(err)
	}
	if ready.BatchID != "bat_1" || ready.EventCount != 1 || ready.Triggers[0].EventID != "evt_1" ||
		string(ready.Triggers[0].Data) != `{"title":"broken"}` {
		t.Fatalf("ready = %+v", ready)
	}
}

func TestBatchDetailSplitsWithoutTruncatingAnEvent(t *testing.T) {
	large := json.RawMessage(`{"text":"` + strings.Repeat("x", 100<<10) + `"}`)
	classification := domain.Classification{Lane: domain.LaneDigest, Classifier: "default-digest.v1"}
	schedule := domain.Schedule{TargetSize: 2, Pressure: 1, SoftDueAt: testClock.Add(time.Hour), HardDueAt: testClock.Add(time.Hour), PolicyVersion: domain.AdaptivePolicyVersion}
	batch := domain.Batch{ID: "bat_1", OrganizationID: "org_1", MemberID: "user_1", Lane: domain.LaneDigest,
		Triggers: []domain.Trigger{
			{ID: "evt_1", Data: large, ReceivedAt: testClock, Classification: classification, Schedule: schedule},
			{ID: "evt_2", Data: large, ReceivedAt: testClock.Add(time.Second), Classification: classification, Schedule: schedule},
		}}
	detail, kept, _, err := batchDetail(batch, testClock)
	if err != nil {
		t.Fatal(err)
	}
	if kept != 1 || len(detail) > batchingv1.MaxBatchDataBytes || !strings.Contains(string(detail), "evt_1") || strings.Contains(string(detail), "evt_2") {
		t.Fatalf("kept = %d, bytes = %d", kept, len(detail))
	}
	var ready batchingv1.TriggerBatchReady
	if err := json.Unmarshal(detail, &ready); err != nil {
		t.Fatal(err)
	}
	if ready.EventCount != 1 || ready.Pressure != 1 || ready.TargetSize != 2 {
		t.Fatalf("ready = %+v", ready)
	}
}

// TestATriggerTooLargeForAnyBatchGoesWithoutItsData: apps checks a payload's
// raw bytes, and encoding grows every <, > and & to six, so a payload apps
// carried can be too large for any batch here. Refused, it would be claimed
// again for ever and hold every trigger behind it; it goes without its data
// instead, the way apps sends a payload it cannot carry.
func TestATriggerTooLargeForAnyBatchGoesWithoutItsData(t *testing.T) {
	markup := json.RawMessage(`{"html":"` + strings.Repeat("<b></b>", 9<<10) + `"}`)
	if len(markup) > 128<<10 {
		t.Fatalf("the payload is %d bytes, over what apps itself carries", len(markup))
	}
	classification := domain.Classification{Lane: domain.LaneDigest, Classifier: "default-digest.v1"}
	schedule := domain.Schedule{TargetSize: 2, Pressure: 1, SoftDueAt: testClock.Add(time.Hour), HardDueAt: testClock.Add(time.Hour), PolicyVersion: domain.AdaptivePolicyVersion}
	batch := domain.Batch{ID: "bat_1", OrganizationID: "org_1", MemberID: "user_1", Lane: domain.LaneDigest,
		Triggers: []domain.Trigger{
			{ID: "evt_markup", Data: markup, ReceivedAt: testClock, Classification: classification, Schedule: schedule},
			{ID: "evt_small", Data: json.RawMessage(`{"title":"hello"}`), ReceivedAt: testClock.Add(time.Second), Classification: classification, Schedule: schedule},
		}}
	detail, kept, omitted, err := batchDetail(batch, testClock)
	if err != nil {
		t.Fatalf("batchDetail() error = %v, want the trigger sent without its data", err)
	}
	if kept != 2 || len(omitted) != 1 || omitted[0] != "evt_markup" || len(detail) > batchingv1.MaxBatchDataBytes {
		t.Fatalf("kept = %d, omitted = %v, bytes = %d", kept, omitted, len(detail))
	}
	var ready batchingv1.TriggerBatchReady
	if err := json.Unmarshal(detail, &ready); err != nil {
		t.Fatal(err)
	}
	if !ready.Triggers[0].DataOmitted || len(ready.Triggers[0].Data) != 0 {
		t.Fatalf("the oversized trigger = %+v, want it without its data", ready.Triggers[0])
	}
	if ready.Triggers[1].DataOmitted || string(ready.Triggers[1].Data) != `{"title":"hello"}` {
		t.Fatalf("the trigger behind it = %+v, want it whole", ready.Triggers[1])
	}

	// And the whole claim completes, rather than being claimed again.
	store := &fakeStore{}
	service := New(store, nil, Config{}, nil)
	service.now = func() time.Time { return testClock }
	if err := service.ready(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	if len(store.completed) != 1 {
		t.Fatalf("completed = %d batches, want 1", len(store.completed))
	}
}

func TestFlushCompletesClaimsReturnedAlongsideALaterClaimError(t *testing.T) {
	store := &fakeStore{
		claimErr: errors.New("claim another lane"),
		batches: []domain.Batch{{
			ID: "bat_1", OrganizationID: "org_1", MemberID: "user_1", Lane: domain.LaneDigest,
			Triggers: []domain.Trigger{{
				ID: "evt_1", ReceivedAt: testClock,
				Classification: domain.Classification{Lane: domain.LaneDigest, Classifier: "default-digest.v1"},
			}},
		}},
	}
	service := New(store, nil, Config{}, nil)
	service.flush(context.Background())
	if len(store.completed) != 1 {
		t.Fatalf("completed = %+v", store.completed)
	}
}

func TestRemovalEventsForgetTheRightScope(t *testing.T) {
	store := &fakeStore{}
	service := New(store, nil, Config{}, nil)
	if err := service.MemberRemoved(context.Background(), authv1.MemberRemoved{OrganizationID: "org_1", UserID: "user_1"}); err != nil {
		t.Fatal(err)
	}
	if err := service.OrganizationDeleted(context.Background(), authv1.OrganizationDeleted{OrganizationID: "org_2"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(store.forgot, ",") != "org_1/user_1,org_2/" {
		t.Fatalf("forgot = %v", store.forgot)
	}
}

// TestARefusedTriggerIsAcknowledged: a trigger from a member removed after
// apps received it is kept nowhere, and delivering it again would change
// nothing, so it is not an error that keeps it on the queue.
func TestARefusedTriggerIsAcknowledged(t *testing.T) {
	store := &fakeStore{admission: domain.Forgotten}
	service := New(store, nil, Config{}, nil)
	err := service.TriggerReceived(context.Background(), "evt_apps_1", appsv1.TriggerReceived{
		OrganizationID: "org_1", MemberID: "user_gone", ReceivedAt: testClock,
	})
	if err != nil {
		t.Fatalf("TriggerReceived() error = %v, want the refusal acknowledged", err)
	}
	if len(store.recorded) != 0 {
		t.Fatalf("recorded = %+v", store.recorded)
	}
}

func TestTheSweepForgetsRemovalsOnlyAfterAMonth(t *testing.T) {
	store := &fakeStore{}
	service := New(store, nil, Config{}, nil)
	service.now = func() time.Time { return testClock }
	service.sweep(context.Background())
	if len(store.swept) != 1 || !store.swept[0].Equal(testClock.Add(-30*24*time.Hour)) {
		t.Fatalf("swept removals before %v, want %v", store.swept, testClock.Add(-30*24*time.Hour))
	}
}

// TestAppRemovalsDiscardTheirWaitingTriggers: a disconnected app, or a
// removed trigger, should not still wake the member's agent for what it
// reported before it went.
func TestAppRemovalsDiscardTheirWaitingTriggers(t *testing.T) {
	store := &fakeStore{}
	service := New(store, nil, Config{}, nil)
	ctx := context.Background()
	if err := service.ConnectionRemoved(ctx, appsv1.ConnectionRemoved{
		ConnectionID: "apc_1", OrganizationID: "org_1", MemberID: "user_1", Reason: appsv1.RemovedByMember,
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.TriggerUnsubscribed(ctx, appsv1.TriggerUnsubscribed{
		TriggerID: "apt_1", ConnectionID: "apc_2", OrganizationID: "org_1", MemberID: "user_1",
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(store.discarded, ",") != "org_1/apc_1/,org_1//apt_1" {
		t.Fatalf("discarded = %v", store.discarded)
	}
	if err := service.ConnectionRemoved(ctx, appsv1.ConnectionRemoved{OrganizationID: "org_1"}); err == nil {
		t.Fatal("ConnectionRemoved() without a connection error = nil")
	}
	if err := service.TriggerUnsubscribed(ctx, appsv1.TriggerUnsubscribed{TriggerID: "apt_1"}); err == nil {
		t.Fatal("TriggerUnsubscribed() without an organization error = nil")
	}
}

// TestATriggerIsScreenedBeforeItIsClassified: a redelivery, or a trigger
// from someone since removed, must not wait on the classifier, nor send its
// payload to the classifier's provider a second time.
func TestATriggerIsScreenedBeforeItIsClassified(t *testing.T) {
	for _, screened := range []domain.Admission{domain.AlreadyRecorded, domain.Forgotten} {
		store := &fakeStore{screened: screened}
		classified := 0
		service := New(store, ClassifierFunc(func(context.Context, appsv1.TriggerReceived) (domain.Classification, error) {
			classified++
			return domain.Classification{Lane: domain.LaneDigest, Classifier: "counting"}, nil
		}), Config{}, nil)
		err := service.TriggerReceived(context.Background(), "evt_apps_1", appsv1.TriggerReceived{
			OrganizationID: "org_1", MemberID: "user_1", ReceivedAt: testClock,
		})
		if err != nil || classified != 0 || len(store.recorded) != 0 {
			t.Fatalf("screened %d: error = %v, classified %d times, recorded %d", screened, err, classified, len(store.recorded))
		}
	}
}
