package triggerflow

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers/github"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
)

const maximumGitHubCleanupAttempts = 8

type GitHubCleanupJobs interface {
	ClaimGitHubCleanup(context.Context, int, string, time.Time, time.Time) ([]domain.GitHubCleanupJob, error)
	CompleteGitHubCleanup(context.Context, domain.GitHubCleanupJob, time.Time) error
	RetryGitHubCleanup(context.Context, domain.GitHubCleanupJob, time.Time, string) error
}

type GitHubCleanupWorker struct {
	Jobs    GitHubCleanupJobs
	Routes  microsoftGraphCleanupRoutes
	State   microsoftGraphCleanupState
	Secrets microsoftGraphCleanupSecrets
	GitHub  GitHubHooks

	Now         func() time.Time
	Random      io.Reader
	BatchSize   int
	Lease       time.Duration
	RetryAfter  time.Duration
	Interval    time.Duration
	ReportError func(error)
}

func (w GitHubCleanupWorker) Run(ctx context.Context) {
	for {
		if err := w.RunOnce(ctx); err != nil && w.ReportError != nil {
			w.ReportError(err)
		}
		timer := time.NewTimer(w.interval())
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func (w GitHubCleanupWorker) RunOnce(ctx context.Context) error {
	if w.Jobs == nil || w.Routes == nil || w.State == nil || w.Secrets == nil || w.GitHub == nil {
		return errors.New("GitHub cleanup worker is not configured")
	}
	leaseToken, err := w.newLeaseToken()
	if err != nil {
		return err
	}
	now := w.now()
	jobs, err := w.Jobs.ClaimGitHubCleanup(ctx, w.batchSize(), leaseToken, now, now.Add(w.lease()))
	if err != nil {
		return fmt.Errorf("claim GitHub cleanup work: %w", err)
	}
	for _, job := range jobs {
		if err := w.cleanup(ctx, job); err != nil {
			availableAt, code := w.now().Add(w.retryAfter()), "cleanup_failed"
			var delayed cleanupRetry
			if errors.As(err, &delayed) {
				availableAt, code = delayed.AvailableAt, delayed.Code
			}
			if retryErr := w.Jobs.RetryGitHubCleanup(ctx, job, availableAt, code); retryErr != nil {
				return fmt.Errorf("reschedule GitHub cleanup: %w", retryErr)
			}
			continue
		}
		if err := w.Jobs.CompleteGitHubCleanup(ctx, job, w.now()); err != nil {
			return fmt.Errorf("complete GitHub cleanup: %w", err)
		}
	}
	return nil
}

func (w GitHubCleanupWorker) cleanup(ctx context.Context, job domain.GitHubCleanupJob) error {
	if job.OrganizationID == "" || job.TriggerID == "" || job.ConnectionID == "" || job.ProfileID == "" ||
		(job.TriggerName != "new-issue" && job.TriggerName != "new-pull-request") ||
		job.Connection.ID != job.ConnectionID || job.Connection.OrganizationID != job.OrganizationID ||
		job.Connection.ProfileID != job.ProfileID || job.Connection.Provider != "github" {
		return errors.New("invalid GitHub cleanup job")
	}
	route, err := w.Routes.ActiveWebhookRouteForConnection(ctx, job.OrganizationID, job.ProfileID, job.TriggerName, job.ConnectionID)
	if err == nil {
		if err := w.Routes.DeactivateWebhookRoute(ctx, route.OpaqueRoute); err != nil {
			return fmt.Errorf("deactivate GitHub route: %w", err)
		}
	} else if !errors.Is(err, domain.ErrNotFound) {
		return fmt.Errorf("load GitHub route: %w", err)
	}

	state, _, err := w.State.LoadTriggerState(ctx, job.OrganizationID, job.TriggerID)
	if err != nil {
		return fmt.Errorf("load GitHub webhook state: %w", err)
	}
	if len(state) == 0 {
		return nil
	}
	hook, err := githubHookState(state)
	if err != nil {
		return fmt.Errorf("decode GitHub webhook state: %w", err)
	}
	if job.Attempt >= maximumGitHubCleanupAttempts {
		if err := w.State.DeleteTriggerState(ctx, job.OrganizationID, job.TriggerID); err != nil {
			return fmt.Errorf("delete exhausted GitHub webhook state: %w", err)
		}
		return nil
	}
	plaintext, err := w.Secrets.Open(ctx, secrets.Binding{
		OrganizationID: job.OrganizationID, ResourceType: "connection", ResourceID: job.ConnectionID,
	}, job.Connection.EncryptedCredentials)
	if err != nil {
		return fmt.Errorf("open cleanup connection grant: %w", err)
	}
	defer wipe(plaintext)
	accessToken, err := cleanupAccessToken(plaintext)
	if err != nil {
		return errors.New("invalid cleanup connection grant")
	}
	if err := w.GitHub.Remove(ctx, accessToken, hook); err != nil {
		if github.IsAuthorizationRejection(err) {
			return cleanupRetry{Cause: err, AvailableAt: w.now().Add(w.retryAfter()), Code: "github_auth_rejected"}
		}
		return fmt.Errorf("remove GitHub webhook: %w", err)
	}
	if err := w.State.DeleteTriggerState(ctx, job.OrganizationID, job.TriggerID); err != nil {
		return fmt.Errorf("delete GitHub webhook state: %w", err)
	}
	return nil
}

func (w GitHubCleanupWorker) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}

func (w GitHubCleanupWorker) batchSize() int {
	if w.BatchSize > 0 && w.BatchSize <= 100 {
		return w.BatchSize
	}
	return 25
}

func (w GitHubCleanupWorker) lease() time.Duration {
	if w.Lease > 0 {
		return w.Lease
	}
	return 2 * time.Minute
}

func (w GitHubCleanupWorker) retryAfter() time.Duration {
	if w.RetryAfter > 0 {
		return w.RetryAfter
	}
	return time.Minute
}

func (w GitHubCleanupWorker) interval() time.Duration {
	if w.Interval > 0 {
		return w.Interval
	}
	return time.Minute
}

func (w GitHubCleanupWorker) newLeaseToken() (string, error) {
	source := w.Random
	if source == nil {
		source = rand.Reader
	}
	bytes := make([]byte, 24)
	if _, err := io.ReadFull(source, bytes); err != nil {
		return "", fmt.Errorf("generate GitHub cleanup lease: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
