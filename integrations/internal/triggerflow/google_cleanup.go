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
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers/google"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
)

const maximumGoogleCleanupAttempts = 8

type GoogleCleanupJobs interface {
	ClaimGoogleCleanup(context.Context, int, string, time.Time, time.Time) ([]domain.GoogleCleanupJob, error)
	CompleteGoogleCleanup(context.Context, domain.GoogleCleanupJob, time.Time) error
	RetryGoogleCleanup(context.Context, domain.GoogleCleanupJob, time.Time, string) error
}

type GoogleCleanupWorker struct {
	Jobs    GoogleCleanupJobs
	Routes  microsoftGraphCleanupRoutes
	State   microsoftGraphCleanupState
	Secrets microsoftGraphCleanupSecrets
	Google  GoogleWatches
	Census  GoogleGmailCensus

	Now         func() time.Time
	Random      io.Reader
	BatchSize   int
	Lease       time.Duration
	RetryAfter  time.Duration
	Interval    time.Duration
	ReportError func(error)
}

func (w GoogleCleanupWorker) Run(ctx context.Context) {
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

func (w GoogleCleanupWorker) RunOnce(ctx context.Context) error {
	if w.Jobs == nil || w.State == nil || w.Secrets == nil || w.Google == nil {
		return errors.New("Google cleanup worker is not configured")
	}
	leaseToken, err := w.newLeaseToken()
	if err != nil {
		return err
	}
	now := w.now()
	jobs, err := w.Jobs.ClaimGoogleCleanup(ctx, w.batchSize(), leaseToken, now, now.Add(w.lease()))
	if err != nil {
		return fmt.Errorf("claim Google cleanup work: %w", err)
	}
	for _, job := range jobs {
		if err := w.cleanup(ctx, job); err != nil {
			availableAt, code := w.now().Add(w.retryAfter()), "cleanup_failed"
			var delayed cleanupRetry
			if errors.As(err, &delayed) {
				availableAt, code = delayed.AvailableAt, delayed.Code
			}
			if retryErr := w.Jobs.RetryGoogleCleanup(ctx, job, availableAt, code); retryErr != nil {
				return fmt.Errorf("reschedule Google cleanup: %w", retryErr)
			}
			continue
		}
		if err := w.Jobs.CompleteGoogleCleanup(ctx, job, w.now()); err != nil {
			return fmt.Errorf("complete Google cleanup: %w", err)
		}
	}
	return nil
}

func (w GoogleCleanupWorker) cleanup(ctx context.Context, job domain.GoogleCleanupJob) error {
	if job.OrganizationID == "" || job.TriggerID == "" || job.ConnectionID == "" || job.ProfileID == "" ||
		(job.TriggerName != "new-email" && job.TriggerName != "calendar-event-changed" &&
			job.TriggerName != "drive-file-changed") ||
		job.Connection.ID != job.ConnectionID || job.Connection.OrganizationID != job.OrganizationID ||
		job.Connection.ProfileID != job.ProfileID || job.Connection.Provider != "google" {
		return errors.New("invalid Google cleanup job")
	}
	if (job.TriggerName == "calendar-event-changed" || job.TriggerName == "drive-file-changed") && w.Routes != nil {
		route, err := w.Routes.ActiveWebhookRouteForConnection(ctx, job.OrganizationID, job.ProfileID, job.TriggerName, job.ConnectionID)
		if err == nil {
			if err := w.Routes.DeactivateWebhookRoute(ctx, route.OpaqueRoute); err != nil {
				return fmt.Errorf("deactivate Google Calendar route: %w", err)
			}
		} else if !errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("load Google Calendar route: %w", err)
		}
	}

	state, _, err := w.State.LoadTriggerState(ctx, job.OrganizationID, job.TriggerID)
	if err != nil {
		return fmt.Errorf("load Google watch state: %w", err)
	}
	if len(state) == 0 {
		return nil
	}
	watch, err := googleWatchState(state)
	if err != nil {
		return fmt.Errorf("decode Google watch state: %w", err)
	}
	if job.Attempt >= maximumGoogleCleanupAttempts {
		if err := w.State.DeleteTriggerState(ctx, job.OrganizationID, job.TriggerID); err != nil {
			return fmt.Errorf("delete exhausted Google watch state: %w", err)
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
	if watch.Kind == google.WatchKindGmail {
		if w.Census != nil {
			count, err := w.Census.ActiveGoogleGmailTriggerCount(ctx, google.NormalizeMailboxEmail(job.Connection.Identity.AccountID))
			if err != nil {
				return fmt.Errorf("count remaining Gmail watches: %w", err)
			}
			if count > 0 {
				if err := w.State.DeleteTriggerState(ctx, job.OrganizationID, job.TriggerID); err != nil {
					return fmt.Errorf("delete Gmail watch state: %w", err)
				}
				return nil
			}
		}
		if err := w.Google.StopGmail(ctx, accessToken); err != nil {
			if google.IsAuthorizationRejection(err) {
				return cleanupRetry{Cause: err, AvailableAt: w.now().Add(w.retryAfter()), Code: "google_auth_rejected"}
			}
			return fmt.Errorf("stop Gmail watch: %w", err)
		}
	} else {
		if err := w.Google.StopChannel(ctx, accessToken, watch); err != nil {
			if google.IsAuthorizationRejection(err) {
				return cleanupRetry{Cause: err, AvailableAt: w.now().Add(w.retryAfter()), Code: "google_auth_rejected"}
			}
			return fmt.Errorf("stop Google Calendar channel: %w", err)
		}
	}
	if err := w.State.DeleteTriggerState(ctx, job.OrganizationID, job.TriggerID); err != nil {
		return fmt.Errorf("delete Google watch state: %w", err)
	}
	return nil
}

func (w GoogleCleanupWorker) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}

func (w GoogleCleanupWorker) batchSize() int {
	if w.BatchSize > 0 && w.BatchSize <= 100 {
		return w.BatchSize
	}
	return 25
}

func (w GoogleCleanupWorker) lease() time.Duration {
	if w.Lease > 0 {
		return w.Lease
	}
	return 2 * time.Minute
}

func (w GoogleCleanupWorker) retryAfter() time.Duration {
	if w.RetryAfter > 0 {
		return w.RetryAfter
	}
	return time.Minute
}

func (w GoogleCleanupWorker) interval() time.Duration {
	if w.Interval > 0 {
		return w.Interval
	}
	return time.Minute
}

func (w GoogleCleanupWorker) newLeaseToken() (string, error) {
	source := w.Random
	if source == nil {
		source = rand.Reader
	}
	bytes := make([]byte, 24)
	if _, err := io.ReadFull(source, bytes); err != nil {
		return "", fmt.Errorf("generate Google cleanup lease: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
