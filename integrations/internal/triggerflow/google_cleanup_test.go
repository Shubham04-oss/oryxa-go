package triggerflow

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers/google"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
)

type googleCleanupJobs struct {
	jobs      []domain.GoogleCleanupJob
	completed []domain.GoogleCleanupJob
	retried   []domain.GoogleCleanupJob
	retryCode []string
}

func (s *googleCleanupJobs) ClaimGoogleCleanup(_ context.Context, _ int, lease string, _, _ time.Time) ([]domain.GoogleCleanupJob, error) {
	result := make([]domain.GoogleCleanupJob, len(s.jobs))
	copy(result, s.jobs)
	for index := range result {
		result[index].LeaseToken = lease
		if result[index].Attempt == 0 {
			result[index].Attempt = 1
		}
	}
	return result, nil
}
func (s *googleCleanupJobs) CompleteGoogleCleanup(_ context.Context, job domain.GoogleCleanupJob, _ time.Time) error {
	s.completed = append(s.completed, job)
	return nil
}
func (s *googleCleanupJobs) RetryGoogleCleanup(_ context.Context, job domain.GoogleCleanupJob, _ time.Time, code string) error {
	s.retried = append(s.retried, job)
	s.retryCode = append(s.retryCode, code)
	return nil
}

func googleCalendarCleanupJob(now time.Time) domain.GoogleCleanupJob {
	connection := testGoogleConnection()
	connection.Status = domain.ConnectionStatusDisconnected
	return domain.GoogleCleanupJob{OrganizationID: "org-1", TriggerID: "trg-1", ConnectionID: "con-1", ProfileID: "app-1",
		TriggerName: "calendar-event-changed", CreatedAt: now, Connection: connection}
}

func googleGmailCleanupJob(now time.Time) domain.GoogleCleanupJob {
	job := googleCalendarCleanupJob(now)
	job.TriggerName = "new-email"
	return job
}

func googleCalendarCleanupState(now time.Time) *graphState {
	return &graphState{values: map[string]any{
		"\x00google_watch_kind": google.WatchKindCalendar, "\x00google_channel_id": "c-channel",
		"\x00google_resource_id": "resource-1", "\x00google_calendar_id": "primary",
		"\x00google_watch_expires_at": now.Add(time.Hour).Format(time.RFC3339Nano),
	}}
}

func googleGmailCleanupState(now time.Time) *graphState {
	return &graphState{values: map[string]any{
		"\x00google_watch_kind": google.WatchKindGmail, "\x00google_history_id": "12345",
		"\x00google_watch_expires_at": now.Add(time.Hour).Format(time.RFC3339Nano),
	}}
}

func TestGoogleCleanupWorkerStopsCalendarChannelThenDeletesState(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	routes := &graphRoutes{route: domain.WebhookRoute{OpaqueRoute: "opaque", OrganizationID: "org-1", ProfileID: "app-1", ConnectionID: "con-1", TriggerName: "calendar-event-changed", Mode: domain.WebhookRoutePerConnection, Active: true}}
	state := googleCalendarCleanupState(now)
	jobs := &googleCleanupJobs{jobs: []domain.GoogleCleanupJob{googleCalendarCleanupJob(now)}}
	watches := &googleWatches{}
	secretBox := &cleanupSecrets{value: []byte(`{"access_token":"cleanup-token","refresh_token":{"ignored":true}}`)}
	worker := GoogleCleanupWorker{Jobs: jobs, Routes: routes, State: state, Secrets: secretBox, Google: watches,
		Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{1}, 24))}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !routes.deactivated || watches.stopped.ChannelID != "c-channel" || !state.deleted ||
		len(jobs.completed) != 1 || len(jobs.retried) != 0 {
		t.Fatalf("route/stop/state/jobs = %v/%+v/%v/%d/%d", routes.deactivated, watches.stopped, state.deleted, len(jobs.completed), len(jobs.retried))
	}
	if secretBox.binding != (secrets.Binding{OrganizationID: "org-1", ResourceType: "connection", ResourceID: "con-1"}) {
		t.Fatalf("credential binding = %+v", secretBox.binding)
	}
}

func TestGoogleCleanupWorkerSkipsGmailStopWhenOtherWatchesRemain(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	state := googleGmailCleanupState(now)
	jobs := &googleCleanupJobs{jobs: []domain.GoogleCleanupJob{googleGmailCleanupJob(now)}}
	watches := &googleWatches{}
	worker := GoogleCleanupWorker{Jobs: jobs, State: state, Secrets: &cleanupSecrets{value: []byte(`{"access_token":"cleanup-token"}`)},
		Google: watches, Census: gmailCensus{count: 1}, Now: func() time.Time { return now },
		Random: bytes.NewReader(bytes.Repeat([]byte{2}, 24))}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if watches.gmailStopped || !state.deleted || len(jobs.completed) != 1 {
		t.Fatalf("shared gmail cleanup = stopped:%v deleted:%v complete:%d", watches.gmailStopped, state.deleted, len(jobs.completed))
	}
}

func TestGoogleCleanupWorkerStopsGmailWhenMailboxIsOrphaned(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	state := googleGmailCleanupState(now)
	jobs := &googleCleanupJobs{jobs: []domain.GoogleCleanupJob{googleGmailCleanupJob(now)}}
	watches := &googleWatches{}
	worker := GoogleCleanupWorker{Jobs: jobs, State: state, Secrets: &cleanupSecrets{value: []byte(`{"access_token":"cleanup-token"}`)},
		Google: watches, Census: gmailCensus{}, Now: func() time.Time { return now },
		Random: bytes.NewReader(bytes.Repeat([]byte{3}, 24))}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !watches.gmailStopped || !state.deleted || len(jobs.completed) != 1 {
		t.Fatalf("orphan gmail cleanup = stopped:%v deleted:%v complete:%d", watches.gmailStopped, state.deleted, len(jobs.completed))
	}
}

func TestGoogleCleanupWorkerRetriesAuthThenStopsCallingGoogle(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	routes := &graphRoutes{route: domain.WebhookRoute{OpaqueRoute: "opaque", OrganizationID: "org-1", ProfileID: "app-1", ConnectionID: "con-1", TriggerName: "calendar-event-changed", Mode: domain.WebhookRoutePerConnection, Active: true}}
	state := googleCalendarCleanupState(now)
	jobs := &googleCleanupJobs{jobs: []domain.GoogleCleanupJob{googleCalendarCleanupJob(now)}}
	watches := &googleWatches{stopErr: google.RejectionError{StatusCode: 401}}
	worker := GoogleCleanupWorker{Jobs: jobs, Routes: routes, State: state, Secrets: &cleanupSecrets{value: []byte(`{"access_token":"expired-token"}`)},
		Google: watches, Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{4}, 24))}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !routes.deactivated || state.deleted || watches.stopped.ChannelID != "c-channel" || len(jobs.retried) != 1 ||
		jobs.retryCode[0] != "google_auth_rejected" {
		t.Fatalf("auth retry = route:%v state:%v stop:%q retry:%v", routes.deactivated, state.deleted, watches.stopped.ChannelID, jobs.retryCode)
	}

	job := googleCalendarCleanupJob(now)
	job.Attempt = maximumGoogleCleanupAttempts
	jobs = &googleCleanupJobs{jobs: []domain.GoogleCleanupJob{job}}
	watches = &googleWatches{}
	secretBox := &cleanupSecrets{value: []byte(`{"access_token":"should-not-be-opened"}`)}
	worker = GoogleCleanupWorker{Jobs: jobs, Routes: routes, State: googleCalendarCleanupState(now), Secrets: secretBox, Google: watches,
		Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{5}, 24))}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if watches.stopped.ChannelID != "" || secretBox.binding != (secrets.Binding{}) || len(jobs.completed) != 1 {
		t.Fatalf("bounded stop called Google: stop=%q binding=%+v complete=%d", watches.stopped.ChannelID, secretBox.binding, len(jobs.completed))
	}
}

func TestGoogleCleanupWorkerRejectsCrossTenantJob(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	job := googleCalendarCleanupJob(now)
	job.Connection.OrganizationID = "other-org"
	jobs := &googleCleanupJobs{jobs: []domain.GoogleCleanupJob{job}}
	worker := GoogleCleanupWorker{Jobs: jobs, Routes: &graphRoutes{}, State: googleCalendarCleanupState(now),
		Secrets: &cleanupSecrets{value: []byte(`{"access_token":"cleanup-token"}`)}, Google: &googleWatches{},
		Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{6}, 24))}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(jobs.retried) != 1 || len(jobs.completed) != 0 {
		t.Fatalf("cross-tenant cleanup jobs = retry:%d complete:%d", len(jobs.retried), len(jobs.completed))
	}
}

func TestGoogleCleanupWorkerRetriesTransientStopAfterClosingRoute(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	routes := &graphRoutes{route: domain.WebhookRoute{OpaqueRoute: "opaque", OrganizationID: "org-1", ProfileID: "app-1", ConnectionID: "con-1", TriggerName: "calendar-event-changed", Mode: domain.WebhookRoutePerConnection, Active: true}}
	state := googleCalendarCleanupState(now)
	jobs := &googleCleanupJobs{jobs: []domain.GoogleCleanupJob{googleCalendarCleanupJob(now)}}
	watches := &googleWatches{stopErr: errors.New("network unavailable")}
	worker := GoogleCleanupWorker{Jobs: jobs, Routes: routes, State: state, Secrets: &cleanupSecrets{value: []byte(`{"access_token":"cleanup-token"}`)},
		Google: watches, Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{7}, 24))}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !routes.deactivated || watches.stopped.ChannelID != "c-channel" || state.deleted || len(jobs.retried) != 1 {
		t.Fatalf("transient retry = route:%v stop:%q deleted:%v retried:%d", routes.deactivated, watches.stopped.ChannelID, state.deleted, len(jobs.retried))
	}
}
