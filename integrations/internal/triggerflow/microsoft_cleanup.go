package triggerflow

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers/microsoftgraph"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
)

const (
	maximumGraphSubscriptionLease = 72 * time.Hour
	graphLeaseSafetyBuffer        = 5 * time.Minute
)

// MicrosoftGraphCleanupJobs is the PostgreSQL-backed worker queue. Its claim
// token prevents a stale worker from completing or rescheduling another
// worker's leased job.
type MicrosoftGraphCleanupJobs interface {
	ClaimMicrosoftGraphCleanup(context.Context, int, string, time.Time, time.Time) ([]domain.MicrosoftGraphCleanupJob, error)
	CompleteMicrosoftGraphCleanup(context.Context, domain.MicrosoftGraphCleanupJob, time.Time) error
	RetryMicrosoftGraphCleanup(context.Context, domain.MicrosoftGraphCleanupJob, time.Time, string) error
}

type microsoftGraphCleanupRoutes interface {
	ActiveWebhookRouteForConnection(context.Context, string, string, string, string) (domain.WebhookRoute, error)
	DeactivateWebhookRoute(context.Context, string) error
}

type microsoftGraphCleanupState interface {
	LoadTriggerState(context.Context, string, string) (map[string]any, int64, error)
	DeleteTriggerState(context.Context, string, string) error
}

type microsoftGraphCleanupSecrets interface {
	Open(context.Context, secrets.Binding, domain.SealedData) ([]byte, error)
}

// MicrosoftGraphCleanupWorker is deliberately separate from Service: it is a
// privileged scheduler entry point, not an end-user authorized trigger API.
// It consumes work persisted atomically with disconnect/profile disable.
type MicrosoftGraphCleanupWorker struct {
	Jobs    MicrosoftGraphCleanupJobs
	Routes  microsoftGraphCleanupRoutes
	State   microsoftGraphCleanupState
	Secrets microsoftGraphCleanupSecrets
	Graph   MicrosoftGraphSubscriptions

	Now         func() time.Time
	Random      io.Reader
	BatchSize   int
	Lease       time.Duration
	RetryAfter  time.Duration
	Interval    time.Duration
	ReportError func(error)
}

// Run keeps cleanup progressing in the always-on broker. RunOnce remains the
// bounded entry point for a future EventBridge Scheduler target.
func (w MicrosoftGraphCleanupWorker) Run(ctx context.Context) {
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

// RunOnce is safe for an EventBridge Scheduler/Lambda invocation. Individual
// provider failures are durably retried and do not expose Graph or OAuth
// details to the scheduler response.
func (w MicrosoftGraphCleanupWorker) RunOnce(ctx context.Context) error {
	if w.Jobs == nil || w.Routes == nil || w.State == nil || w.Secrets == nil || w.Graph == nil {
		return errors.New("Microsoft Graph cleanup worker is not configured")
	}
	leaseToken, err := w.newLeaseToken()
	if err != nil {
		return err
	}
	now := w.now()
	jobs, err := w.Jobs.ClaimMicrosoftGraphCleanup(ctx, w.batchSize(), leaseToken, now, now.Add(w.lease()))
	if err != nil {
		return fmt.Errorf("claim Microsoft Graph cleanup work: %w", err)
	}
	for _, job := range jobs {
		if err := w.cleanup(ctx, job); err != nil {
			// The error itself can include provider details, so persist only a
			// stable code. The durable job remains the sole retry signal.
			availableAt, code := w.now().Add(w.retryAfter()), "cleanup_failed"
			var delayed cleanupRetry
			if errors.As(err, &delayed) {
				availableAt, code = delayed.AvailableAt, delayed.Code
			}
			if retryErr := w.Jobs.RetryMicrosoftGraphCleanup(ctx, job, availableAt, code); retryErr != nil {
				return fmt.Errorf("reschedule Microsoft Graph cleanup: %w", retryErr)
			}
			continue
		}
		if err := w.Jobs.CompleteMicrosoftGraphCleanup(ctx, job, w.now()); err != nil {
			return fmt.Errorf("complete Microsoft Graph cleanup: %w", err)
		}
	}
	return nil
}

func (w MicrosoftGraphCleanupWorker) cleanup(ctx context.Context, job domain.MicrosoftGraphCleanupJob) error {
	if job.OrganizationID == "" || job.TriggerID == "" || job.ConnectionID == "" || job.ProfileID == "" ||
		(job.TriggerName != "new-email" && job.TriggerName != "calendar-event-changed") ||
		job.Connection.ID != job.ConnectionID || job.Connection.OrganizationID != job.OrganizationID ||
		job.Connection.ProfileID != job.ProfileID || job.Connection.Provider != "microsoft" {
		return errors.New("invalid Microsoft Graph cleanup job")
	}
	// Close ingress first. It remains closed even when Graph is unavailable,
	// avoiding a disabled connection receiving more notifications during retry.
	route, err := w.Routes.ActiveWebhookRouteForConnection(ctx, job.OrganizationID, job.ProfileID, job.TriggerName, job.ConnectionID)
	if err == nil {
		if err := w.Routes.DeactivateWebhookRoute(ctx, route.OpaqueRoute); err != nil {
			return fmt.Errorf("deactivate Microsoft Graph route: %w", err)
		}
	} else if !errors.Is(err, domain.ErrNotFound) {
		return fmt.Errorf("load Microsoft Graph route: %w", err)
	}

	state, _, err := w.State.LoadTriggerState(ctx, job.OrganizationID, job.TriggerID)
	if err != nil {
		return fmt.Errorf("load Microsoft Graph subscription state: %w", err)
	}
	// A previous attempt may have deleted state just before losing its lease.
	// Graph Remove is idempotent, so state absence is the durable completion
	// boundary for that case.
	if len(state) == 0 {
		return nil
	}
	subscription, err := microsoftSubscriptionState(state)
	if err != nil {
		return fmt.Errorf("decode Microsoft Graph subscription state: %w", err)
	}
	// A renewal PATCH can succeed remotely and fail before its local state is
	// persisted. Therefore stored expiry alone never proves remote deletion.
	// Once a maximum Graph lease from the durable disable boundary has passed,
	// the remote subscription can no longer exist and local cleanup is safe.
	if !w.maximumRemoteLeaseExpiry(job, subscription).After(w.now()) {
		if err := w.State.DeleteTriggerState(ctx, job.OrganizationID, job.TriggerID); err != nil {
			return fmt.Errorf("delete elapsed Microsoft Graph subscription state: %w", err)
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
	// The Graph client treats a 404 delete as success, making retries and the
	// local-state-delete/complete gap harmless.
	if err := w.Graph.Remove(ctx, accessToken, subscription.ID); err != nil {
		if microsoftgraph.IsAuthorizationRejection(err) {
			return cleanupRetry{Cause: err, AvailableAt: w.maximumRemoteLeaseExpiry(job, subscription), Code: "graph_auth_rejected"}
		}
		return fmt.Errorf("remove Microsoft Graph subscription: %w", err)
	}
	if err := w.State.DeleteTriggerState(ctx, job.OrganizationID, job.TriggerID); err != nil {
		return fmt.Errorf("delete Microsoft Graph subscription state: %w", err)
	}
	return nil
}

// cleanupRetry keeps a provider failure out of the durable error field while
// allowing RunOnce to choose a lease-aware retry time.
type cleanupRetry struct {
	Cause       error
	AvailableAt time.Time
	Code        string
}

func (e cleanupRetry) Error() string { return e.Code }
func (e cleanupRetry) Unwrap() error { return e.Cause }

func cleanupAccessToken(value []byte) (string, error) {
	var grant struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(value, &grant); err != nil || grant.AccessToken == "" {
		return "", errors.New("access token is missing")
	}
	return grant.AccessToken, nil
}

func (w MicrosoftGraphCleanupWorker) maximumRemoteLeaseExpiry(job domain.MicrosoftGraphCleanupJob, subscription microsoftgraph.SubscriptionState) time.Time {
	// created_at is persisted with every queue item. A zero value only appears
	// in malformed/legacy in-memory input, where we choose a conservative future
	// boundary rather than trusting the stale subscription expiry.
	boundary := w.now()
	if !job.CreatedAt.IsZero() {
		boundary = job.CreatedAt.UTC()
	}
	maximum := boundary.Add(maximumGraphSubscriptionLease + graphLeaseSafetyBuffer)
	if subscription.ExpiresAt.After(maximum) {
		return subscription.ExpiresAt.UTC()
	}
	return maximum
}

func (w MicrosoftGraphCleanupWorker) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}

func (w MicrosoftGraphCleanupWorker) batchSize() int {
	if w.BatchSize > 0 && w.BatchSize <= 100 {
		return w.BatchSize
	}
	return 25
}

func (w MicrosoftGraphCleanupWorker) lease() time.Duration {
	if w.Lease > 0 {
		return w.Lease
	}
	return 2 * time.Minute
}

func (w MicrosoftGraphCleanupWorker) retryAfter() time.Duration {
	if w.RetryAfter > 0 {
		return w.RetryAfter
	}
	return time.Minute
}

func (w MicrosoftGraphCleanupWorker) interval() time.Duration {
	if w.Interval > 0 {
		return w.Interval
	}
	return time.Minute
}

func (w MicrosoftGraphCleanupWorker) newLeaseToken() (string, error) {
	source := w.Random
	if source == nil {
		source = rand.Reader
	}
	bytes := make([]byte, 24)
	if _, err := io.ReadFull(source, bytes); err != nil {
		return "", fmt.Errorf("generate Microsoft Graph cleanup lease: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
