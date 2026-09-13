package triggerflow

import (
	"context"
	"sync"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

const (
	defaultRenewalInterval  = time.Hour
	defaultRenewalBatchSize = 100
	maximumRenewalBatchSize = 500
)

// MicrosoftGraphTriggerReferences returns only the durable identifiers needed
// by the scheduler. It must provide a stable, keyset-paginated ordering by
// organization ID then trigger ID.
type MicrosoftGraphTriggerReferences interface {
	ActiveMicrosoftGraphTriggers(context.Context, string, string, int) ([]domain.Trigger, error)
}

// Renewer intentionally matches Service.Renew so the worker has no access to
// credentials, subscriptions, or any end-user authority.
type Renewer interface {
	Renew(context.Context, string, string) error
}

// RenewalLease serializes the complete keyset scan across broker replicas.
// The callback form makes the lease lifetime explicit and keeps tests free of
// PostgreSQL while allowing the production implementation to hold one session
// advisory lock for every Graph PATCH in a scan.
type RenewalLease interface {
	WithMicrosoftGraphRenewalLease(context.Context, func(context.Context) error) (acquired bool, err error)
}

// RenewalWorker renews active Microsoft Graph subscriptions. It runs a scan
// immediately, then on Interval. Per-trigger failures are isolated so one
// revoked grant or expired subscription never prevents other tenants from
// being renewed.
type RenewalWorker struct {
	References MicrosoftGraphTriggerReferences
	Renewer    Renewer
	Lease      RenewalLease
	Interval   time.Duration
	BatchSize  int
	// ReportError is optional. It receives only operation errors; callers that
	// log errors remain responsible for their own safe logging policy.
	ReportError func(error)

	mu      sync.Mutex
	running bool
}

// Run blocks until ctx is canceled. It always performs an immediate scan so a
// newly started broker does not wait an interval before protecting existing
// subscriptions.
func (w *RenewalWorker) Run(ctx context.Context) {
	w.RunOnce(ctx)
	interval := w.interval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.RunOnce(ctx)
		}
	}
}

// RunOnce is safe to call from another scheduler. A scan already in progress
// wins; the overlapping invocation returns without duplicating Graph renewals.
func (w *RenewalWorker) RunOnce(ctx context.Context) {
	if ctx.Err() != nil || w.References == nil || w.Renewer == nil || !w.acquire() {
		return
	}
	defer w.release()
	if w.Lease == nil {
		if err := w.scan(ctx); err != nil {
			w.report(err)
		}
		return
	}
	acquired, err := w.Lease.WithMicrosoftGraphRenewalLease(ctx, w.scan)
	if err != nil {
		w.report(err)
	}
	if !acquired {
		return
	}
}

func (w *RenewalWorker) scan(ctx context.Context) error {
	organizationID, triggerID := "", ""
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		triggers, err := w.References.ActiveMicrosoftGraphTriggers(ctx, organizationID, triggerID, w.batchSize())
		if err != nil {
			return err
		}
		if len(triggers) == 0 {
			return nil
		}
		for _, trigger := range triggers {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if trigger.OrganizationID == "" || trigger.ID == "" {
				// The database query is trusted, but avoid turning malformed data
				// into an unscoped renewal call.
				continue
			}
			if err := w.Renewer.Renew(ctx, trigger.OrganizationID, trigger.ID); err != nil {
				w.report(err)
			}
		}
		last := triggers[len(triggers)-1]
		if last.OrganizationID == "" || last.ID == "" || len(triggers) < w.batchSize() {
			return nil
		}
		organizationID, triggerID = last.OrganizationID, last.ID
	}
}

func (w *RenewalWorker) acquire() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.running {
		return false
	}
	w.running = true
	return true
}

func (w *RenewalWorker) release() {
	w.mu.Lock()
	w.running = false
	w.mu.Unlock()
}

func (w *RenewalWorker) interval() time.Duration {
	if w.Interval > 0 {
		return w.Interval
	}
	return defaultRenewalInterval
}

func (w *RenewalWorker) batchSize() int {
	if w.BatchSize <= 0 {
		return defaultRenewalBatchSize
	}
	if w.BatchSize > maximumRenewalBatchSize {
		return maximumRenewalBatchSize
	}
	return w.BatchSize
}

func (w *RenewalWorker) report(err error) {
	if err != nil && w.ReportError != nil {
		w.ReportError(err)
	}
}
