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
	batches   []domain.Batch
	completed []domain.Event
	forgot    []string
	unclaimed []string
	claimErr  error
}

func (store *fakeStore) Record(_ context.Context, trigger domain.Trigger) (bool, error) {
	store.recorded = append(store.recorded, trigger)
	return true, nil
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
func (*fakeStore) DeleteBatched(context.Context, time.Time, int) (int, error) { return 0, nil }

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
	if classification.Lane != domain.LaneDigest || classification.Priority != 1 || classification.Classifier != "jev-fallback.v1" {
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
	detail, kept, err := batchDetail(batch, testClock)
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
