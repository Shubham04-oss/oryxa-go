package triggerflow

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers/github"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
)

type githubCleanupJobs struct {
	jobs      []domain.GitHubCleanupJob
	completed []domain.GitHubCleanupJob
	retried   []domain.GitHubCleanupJob
	retryCode []string
}

func (s *githubCleanupJobs) ClaimGitHubCleanup(_ context.Context, _ int, lease string, _, _ time.Time) ([]domain.GitHubCleanupJob, error) {
	result := make([]domain.GitHubCleanupJob, len(s.jobs))
	copy(result, s.jobs)
	for index := range result {
		result[index].LeaseToken = lease
		if result[index].Attempt == 0 {
			result[index].Attempt = 1
		}
	}
	return result, nil
}
func (s *githubCleanupJobs) CompleteGitHubCleanup(_ context.Context, job domain.GitHubCleanupJob, _ time.Time) error {
	s.completed = append(s.completed, job)
	return nil
}
func (s *githubCleanupJobs) RetryGitHubCleanup(_ context.Context, job domain.GitHubCleanupJob, _ time.Time, code string) error {
	s.retried = append(s.retried, job)
	s.retryCode = append(s.retryCode, code)
	return nil
}

func githubCleanupJob(now time.Time) domain.GitHubCleanupJob {
	connection := testGitHubConnection()
	connection.Status = domain.ConnectionStatusDisconnected
	return domain.GitHubCleanupJob{OrganizationID: "org-1", TriggerID: "trg-1", ConnectionID: "con-1", ProfileID: "app-1",
		TriggerName: "new-issue", CreatedAt: now, Connection: connection}
}

func githubCleanupState() *graphState {
	return &graphState{values: map[string]any{
		"\x00github_hook_id": "12345", "\x00github_hook_scope": "repository",
		"\x00github_hook_owner": "oryxa", "\x00github_hook_repo": "api",
	}}
}

func TestGitHubCleanupWorkerDeactivatesThenRemovesAndDeletesState(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	routes := &graphRoutes{route: domain.WebhookRoute{OpaqueRoute: "opaque", OrganizationID: "org-1", ProfileID: "app-1", ConnectionID: "con-1", TriggerName: "new-issue", Mode: domain.WebhookRoutePerConnection, Active: true}}
	state := githubCleanupState()
	jobs := &githubCleanupJobs{jobs: []domain.GitHubCleanupJob{githubCleanupJob(now)}}
	hooks := &githubHooks{}
	secretBox := &cleanupSecrets{value: []byte(`{"access_token":"cleanup-token","refresh_token":{"ignored":true}}`)}
	worker := GitHubCleanupWorker{Jobs: jobs, Routes: routes, State: state, Secrets: secretBox, GitHub: hooks,
		Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{1}, 24))}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !routes.deactivated || hooks.removed.ID != "12345" || hooks.removed.Owner != "oryxa" || !state.deleted ||
		len(jobs.completed) != 1 || len(jobs.retried) != 0 {
		t.Fatalf("route/remove/state/jobs = %v/%+v/%v/%d/%d", routes.deactivated, hooks.removed, state.deleted, len(jobs.completed), len(jobs.retried))
	}
	if secretBox.binding != (secrets.Binding{OrganizationID: "org-1", ResourceType: "connection", ResourceID: "con-1"}) {
		t.Fatalf("credential binding = %+v", secretBox.binding)
	}
}

func TestGitHubCleanupWorkerRetriesThenStopsCallingGitHub(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	routes := &graphRoutes{route: domain.WebhookRoute{OpaqueRoute: "opaque", OrganizationID: "org-1", ProfileID: "app-1", ConnectionID: "con-1", TriggerName: "new-issue", Mode: domain.WebhookRoutePerConnection, Active: true}}
	state := githubCleanupState()
	jobs := &githubCleanupJobs{jobs: []domain.GitHubCleanupJob{githubCleanupJob(now)}}
	hooks := &githubHooks{removeErr: github.RejectionError{StatusCode: 401}}
	worker := GitHubCleanupWorker{Jobs: jobs, Routes: routes, State: state, Secrets: &cleanupSecrets{value: []byte(`{"access_token":"expired-token"}`)}, GitHub: hooks,
		Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{2}, 24))}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !routes.deactivated || state.deleted || hooks.removed.ID != "12345" || len(jobs.retried) != 1 || jobs.retryCode[0] != "github_auth_rejected" {
		t.Fatalf("auth retry = route:%v state:%v remove:%q retry:%v", routes.deactivated, state.deleted, hooks.removed.ID, jobs.retryCode)
	}

	job := githubCleanupJob(now)
	job.Attempt = maximumGitHubCleanupAttempts
	jobs = &githubCleanupJobs{jobs: []domain.GitHubCleanupJob{job}}
	hooks = &githubHooks{}
	secretBox := &cleanupSecrets{value: []byte(`{"access_token":"should-not-be-opened"}`)}
	worker = GitHubCleanupWorker{Jobs: jobs, Routes: routes, State: githubCleanupState(), Secrets: secretBox, GitHub: hooks,
		Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{3}, 24))}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hooks.removed.ID != "" || secretBox.binding != (secrets.Binding{}) || len(jobs.completed) != 1 {
		t.Fatalf("bounded stop called GitHub: remove=%q binding=%+v complete=%d", hooks.removed.ID, secretBox.binding, len(jobs.completed))
	}
}

func TestGitHubCleanupWorkerRejectsCrossTenantJob(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	job := githubCleanupJob(now)
	job.Connection.OrganizationID = "other-org"
	jobs := &githubCleanupJobs{jobs: []domain.GitHubCleanupJob{job}}
	worker := GitHubCleanupWorker{Jobs: jobs, Routes: &graphRoutes{}, State: githubCleanupState(), Secrets: &cleanupSecrets{value: []byte(`{"access_token":"cleanup-token"}`)}, GitHub: &githubHooks{},
		Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{4}, 24))}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(jobs.retried) != 1 || len(jobs.completed) != 0 {
		t.Fatalf("cross-tenant cleanup jobs = retry:%d complete:%d", len(jobs.retried), len(jobs.completed))
	}
}

func TestGitHubCleanupWorkerRetriesTransientDeleteAfterClosingRoute(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	routes := &graphRoutes{route: domain.WebhookRoute{OpaqueRoute: "opaque", OrganizationID: "org-1", ProfileID: "app-1", ConnectionID: "con-1", TriggerName: "new-issue", Mode: domain.WebhookRoutePerConnection, Active: true}}
	state := githubCleanupState()
	jobs := &githubCleanupJobs{jobs: []domain.GitHubCleanupJob{githubCleanupJob(now)}}
	hooks := &githubHooks{removeErr: errors.New("network unavailable")}
	worker := GitHubCleanupWorker{Jobs: jobs, Routes: routes, State: state, Secrets: &cleanupSecrets{value: []byte(`{"access_token":"cleanup-token"}`)}, GitHub: hooks,
		Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{5}, 24))}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !routes.deactivated || hooks.removed.ID != "12345" || state.deleted || len(jobs.retried) != 1 {
		t.Fatalf("transient retry = route:%v remove:%q deleted:%v retried:%d", routes.deactivated, hooks.removed.ID, state.deleted, len(jobs.retried))
	}
}
