package triggerflow

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers/microsoftgraph"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
)

type cleanupJobs struct {
	jobs      []domain.MicrosoftGraphCleanupJob
	completed []domain.MicrosoftGraphCleanupJob
	retried   []domain.MicrosoftGraphCleanupJob
	retryAt   []time.Time
	retryCode []string
}

func (s *cleanupJobs) ClaimMicrosoftGraphCleanup(_ context.Context, _ int, lease string, _, _ time.Time) ([]domain.MicrosoftGraphCleanupJob, error) {
	result := make([]domain.MicrosoftGraphCleanupJob, len(s.jobs))
	copy(result, s.jobs)
	for index := range result {
		result[index].LeaseToken = lease
	}
	return result, nil
}
func (s *cleanupJobs) CompleteMicrosoftGraphCleanup(_ context.Context, job domain.MicrosoftGraphCleanupJob, _ time.Time) error {
	s.completed = append(s.completed, job)
	return nil
}
func (s *cleanupJobs) RetryMicrosoftGraphCleanup(_ context.Context, job domain.MicrosoftGraphCleanupJob, availableAt time.Time, code string) error {
	s.retried = append(s.retried, job)
	s.retryAt = append(s.retryAt, availableAt)
	s.retryCode = append(s.retryCode, code)
	return nil
}

type cleanupSecrets struct {
	binding secrets.Binding
	value   []byte
}

func (s *cleanupSecrets) Open(_ context.Context, binding secrets.Binding, _ domain.SealedData) ([]byte, error) {
	s.binding = binding
	return append([]byte(nil), s.value...), nil
}

func cleanupJob(now time.Time) domain.MicrosoftGraphCleanupJob {
	connection := testMicrosoftConnection()
	connection.Status = domain.ConnectionStatusDisconnected
	return domain.MicrosoftGraphCleanupJob{OrganizationID: "org-1", TriggerID: "trg-1", ConnectionID: "con-1", ProfileID: "app-1", TriggerName: "new-email", CreatedAt: now, Connection: connection}
}

func cleanupState(now time.Time) *graphState {
	return &graphState{values: map[string]any{
		"\x00microsoft_graph_subscription_id":         "sub-1",
		"\x00microsoft_graph_subscription_expires_at": now.Add(time.Hour).Format(time.RFC3339Nano),
	}}
}

func TestMicrosoftGraphCleanupWorkerDeactivatesThenRemovesAndDeletesState(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	routes := &graphRoutes{route: domain.WebhookRoute{OpaqueRoute: "opaque", OrganizationID: "org-1", ProfileID: "app-1", ConnectionID: "con-1", TriggerName: "new-email", Mode: domain.WebhookRoutePerConnection, Active: true}}
	state := cleanupState(now)
	jobs := &cleanupJobs{jobs: []domain.MicrosoftGraphCleanupJob{cleanupJob(now)}}
	graph := &graphSubscriptions{}
	// refresh_token has an intentionally incompatible shape. Cleanup must read
	// only access_token and never decode or retain the full grant.
	secretBox := &cleanupSecrets{value: []byte(`{"access_token":"cleanup-token","refresh_token":{"ignored":true}}`)}
	worker := MicrosoftGraphCleanupWorker{Jobs: jobs, Routes: routes, State: state, Secrets: secretBox, Graph: graph,
		Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{1}, 24))}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !routes.deactivated || graph.removeID != "sub-1" || !state.deleted || len(jobs.completed) != 1 || len(jobs.retried) != 0 {
		t.Fatalf("route/remove/state/jobs = %v/%q/%v/%d/%d", routes.deactivated, graph.removeID, state.deleted, len(jobs.completed), len(jobs.retried))
	}
	if secretBox.binding != (secrets.Binding{OrganizationID: "org-1", ResourceType: "connection", ResourceID: "con-1"}) {
		t.Fatalf("credential binding = %+v", secretBox.binding)
	}
}

func TestMicrosoftGraphCleanupWorkerRetriesGraphFailureAfterClosingRoute(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	routes := &graphRoutes{route: domain.WebhookRoute{OpaqueRoute: "opaque", OrganizationID: "org-1", ProfileID: "app-1", ConnectionID: "con-1", TriggerName: "new-email", Mode: domain.WebhookRoutePerConnection, Active: true}}
	state := cleanupState(now)
	jobs := &cleanupJobs{jobs: []domain.MicrosoftGraphCleanupJob{cleanupJob(now)}}
	graph := &graphSubscriptions{removeErr: errors.New("network unavailable")}
	worker := MicrosoftGraphCleanupWorker{Jobs: jobs, Routes: routes, State: state, Secrets: &cleanupSecrets{value: []byte(`{"access_token":"cleanup-token"}`)}, Graph: graph,
		Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{2}, 24))}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !routes.deactivated || graph.removeID != "sub-1" || state.deleted || len(jobs.retried) != 1 || len(jobs.completed) != 0 {
		t.Fatalf("route/remove/state/jobs = %v/%q/%v/%d/%d", routes.deactivated, graph.removeID, state.deleted, len(jobs.retried), len(jobs.completed))
	}
	// A later job lease sees no active route, retries the idempotent Graph
	// delete, and finishes without reopening ingress.
	graph.removeErr = nil
	worker.Random = bytes.NewReader(bytes.Repeat([]byte{4}, 24))
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !state.deleted || len(jobs.completed) != 1 || graph.removeID != "sub-1" {
		t.Fatalf("retry completion = state:%v complete:%d remove:%q", state.deleted, len(jobs.completed), graph.removeID)
	}
}

func TestMicrosoftGraphCleanupWorkerRejectsCrossTenantJob(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	job := cleanupJob(now)
	job.Connection.OrganizationID = "other-org"
	jobs := &cleanupJobs{jobs: []domain.MicrosoftGraphCleanupJob{job}}
	worker := MicrosoftGraphCleanupWorker{Jobs: jobs, Routes: &graphRoutes{}, State: cleanupState(now), Secrets: &cleanupSecrets{value: []byte(`{"access_token":"cleanup-token"}`)}, Graph: &graphSubscriptions{},
		Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{3}, 24))}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(jobs.retried) != 1 || len(jobs.completed) != 0 {
		t.Fatalf("cross-tenant cleanup jobs = retry:%d complete:%d", len(jobs.retried), len(jobs.completed))
	}
}

func TestMicrosoftGraphCleanupWorkerDoesNotInferRemoteDeletionFromStoredExpiry(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	routes := &graphRoutes{route: domain.WebhookRoute{OpaqueRoute: "opaque", OrganizationID: "org-1", ProfileID: "app-1", ConnectionID: "con-1", TriggerName: "new-email", Mode: domain.WebhookRoutePerConnection, Active: true}}
	state := &graphState{values: map[string]any{
		"\x00microsoft_graph_subscription_id":         "sub-expired",
		"\x00microsoft_graph_subscription_expires_at": now.Add(-time.Minute).Format(time.RFC3339Nano),
	}}
	jobs := &cleanupJobs{jobs: []domain.MicrosoftGraphCleanupJob{cleanupJob(now)}}
	graph := &graphSubscriptions{}
	secretBox := &cleanupSecrets{value: []byte(`{"access_token":"expired"}`)}
	worker := MicrosoftGraphCleanupWorker{Jobs: jobs, Routes: routes, State: state, Secrets: secretBox, Graph: graph,
		Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{5}, 24))}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !routes.deactivated || !state.deleted || graph.removeID != "sub-expired" || secretBox.binding == (secrets.Binding{}) || len(jobs.completed) != 1 {
		t.Fatalf("stored-expiry cleanup = route:%v state:%v remove:%q binding:%+v complete:%d", routes.deactivated, state.deleted, graph.removeID, secretBox.binding, len(jobs.completed))
	}
}

func TestMicrosoftGraphCleanupWorkerDelaysAuthRejectionUntilMaximumRemoteLease(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	routes := &graphRoutes{route: domain.WebhookRoute{OpaqueRoute: "opaque", OrganizationID: "org-1", ProfileID: "app-1", ConnectionID: "con-1", TriggerName: "new-email", Mode: domain.WebhookRoutePerConnection, Active: true}}
	state := cleanupState(now)
	job := cleanupJob(now)
	jobs := &cleanupJobs{jobs: []domain.MicrosoftGraphCleanupJob{job}}
	graph := &graphSubscriptions{removeErr: microsoftgraph.RejectionError{Code: "InvalidAuthenticationToken", StatusCode: 401}}
	worker := MicrosoftGraphCleanupWorker{Jobs: jobs, Routes: routes, State: state, Secrets: &cleanupSecrets{value: []byte(`{"access_token":"expired-token"}`)}, Graph: graph,
		Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{6}, 24))}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantRetry := now.Add(maximumGraphSubscriptionLease + graphLeaseSafetyBuffer)
	if !routes.deactivated || state.deleted || len(jobs.retried) != 1 || len(jobs.retryAt) != 1 || !jobs.retryAt[0].Equal(wantRetry) || jobs.retryCode[0] != "graph_auth_rejected" {
		t.Fatalf("auth retry = route:%v state:%v retry:%v code:%v", routes.deactivated, state.deleted, jobs.retryAt, jobs.retryCode)
	}
}

func TestMicrosoftGraphCleanupWorkerCompletesAfterMaximumRemoteLease(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 6, 0, 0, time.UTC)
	routes := &graphRoutes{route: domain.WebhookRoute{OpaqueRoute: "opaque", OrganizationID: "org-1", ProfileID: "app-1", ConnectionID: "con-1", TriggerName: "new-email", Mode: domain.WebhookRoutePerConnection, Active: true}}
	state := cleanupState(now.Add(-96 * time.Hour))
	job := cleanupJob(now.Add(-maximumGraphSubscriptionLease - graphLeaseSafetyBuffer - time.Second))
	jobs := &cleanupJobs{jobs: []domain.MicrosoftGraphCleanupJob{job}}
	graph := &graphSubscriptions{}
	secretBox := &cleanupSecrets{value: []byte(`{"access_token":"should-not-be-opened"}`)}
	worker := MicrosoftGraphCleanupWorker{Jobs: jobs, Routes: routes, State: state, Secrets: secretBox, Graph: graph,
		Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{7}, 24))}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !routes.deactivated || !state.deleted || graph.removeID != "" || secretBox.binding != (secrets.Binding{}) || len(jobs.completed) != 1 {
		t.Fatalf("elapsed lease cleanup = route:%v state:%v remove:%q binding:%+v complete:%d", routes.deactivated, state.deleted, graph.removeID, secretBox.binding, len(jobs.completed))
	}
}
