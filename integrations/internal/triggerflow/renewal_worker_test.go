package triggerflow

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

type renewalPageStore struct {
	mu       sync.Mutex
	pages    map[string][]domain.Trigger
	calls    [][3]any
	block    chan struct{}
	entered  chan struct{}
	queryErr error
}

func (s *renewalPageStore) ActiveMicrosoftGraphTriggers(ctx context.Context, organizationID, triggerID string, limit int) ([]domain.Trigger, error) {
	s.mu.Lock()
	s.calls = append(s.calls, [3]any{organizationID, triggerID, limit})
	block, entered, err := s.block, s.entered, s.queryErr
	page := append([]domain.Trigger(nil), s.pages[organizationID+"/"+triggerID]...)
	s.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return page, err
}

type renewalRecorder struct {
	mu      sync.Mutex
	calls   []string
	failFor map[string]error
	block   chan struct{}
	entered chan struct{}
}

type sharedRenewalLease struct {
	mu           sync.Mutex
	held         bool
	acquisitions int
	releases     int
}

func (l *sharedRenewalLease) WithMicrosoftGraphRenewalLease(ctx context.Context, callback func(context.Context) error) (bool, error) {
	l.mu.Lock()
	if l.held {
		l.mu.Unlock()
		return false, nil
	}
	l.held = true
	l.acquisitions++
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.held = false
		l.releases++
		l.mu.Unlock()
	}()
	return true, callback(ctx)
}

func (r *renewalRecorder) Renew(ctx context.Context, organizationID, triggerID string) error {
	r.mu.Lock()
	r.calls = append(r.calls, organizationID+"/"+triggerID)
	block, entered, err := r.block, r.entered, r.failFor[organizationID+"/"+triggerID]
	r.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func TestRenewalWorkerPagesStablyCapsBatchesAndIsolatesFailures(t *testing.T) {
	t.Parallel()
	store := &renewalPageStore{pages: map[string][]domain.Trigger{
		"/":           {{OrganizationID: "org-a", ID: "trg-1"}, {OrganizationID: "org-a", ID: "trg-2"}},
		"org-a/trg-2": {{OrganizationID: "org-b", ID: "trg-1"}},
	}}
	renewer := &renewalRecorder{failFor: map[string]error{"org-a/trg-1": errors.New("expected failure")}}
	var reported []error
	worker := RenewalWorker{References: store, Renewer: renewer, BatchSize: 2,
		ReportError: func(err error) { reported = append(reported, err) }}
	worker.RunOnce(context.Background())
	if !slices.Equal(renewer.calls, []string{"org-a/trg-1", "org-a/trg-2", "org-b/trg-1"}) {
		t.Fatalf("renewed = %#v", renewer.calls)
	}
	if len(reported) != 1 || reported[0].Error() != "expected failure" {
		t.Fatalf("reported = %#v", reported)
	}
	if len(store.calls) != 2 || store.calls[0][2] != 2 || store.calls[1][0] != "org-a" || store.calls[1][1] != "trg-2" {
		t.Fatalf("queries = %#v", store.calls)
	}
}

func TestRenewalWorkerDoesNotOverlapScans(t *testing.T) {
	t.Parallel()
	block := make(chan struct{})
	store := &renewalPageStore{pages: map[string][]domain.Trigger{"/": nil}, block: block, entered: make(chan struct{}, 1)}
	worker := RenewalWorker{References: store, Renewer: &renewalRecorder{}}
	done := make(chan struct{})
	go func() { worker.RunOnce(context.Background()); close(done) }()
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("first scan did not start")
	}
	worker.RunOnce(context.Background())
	store.mu.Lock()
	calls := len(store.calls)
	store.mu.Unlock()
	if calls != 1 {
		t.Fatalf("overlapping scans = %d", calls)
	}
	close(block)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("first scan did not finish")
	}
}

func TestRenewalWorkerLeaseExcludesConcurrentReplicas(t *testing.T) {
	t.Parallel()
	lease := &sharedRenewalLease{}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	store := &renewalPageStore{pages: map[string][]domain.Trigger{
		"/": {{OrganizationID: "org-a", ID: "trg-1"}},
	}}
	renewer := &renewalRecorder{block: release, entered: started}
	first := &RenewalWorker{References: store, Renewer: renewer, Lease: lease}
	second := &RenewalWorker{References: store, Renewer: renewer, Lease: lease}
	done := make(chan struct{})
	go func() { first.RunOnce(context.Background()); close(done) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first replica did not start renewal")
	}
	second.RunOnce(context.Background())
	renewer.mu.Lock()
	calls := len(renewer.calls)
	renewer.mu.Unlock()
	if calls != 1 {
		t.Fatalf("duplicate Graph PATCH across replicas = %d", calls)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("first replica did not release lease")
	}
	lease.mu.Lock()
	acquisitions, releases, held := lease.acquisitions, lease.releases, lease.held
	lease.mu.Unlock()
	if acquisitions != 1 || releases != 1 || held {
		t.Fatalf("lease state = acquired:%d released:%d held:%v", acquisitions, releases, held)
	}
}

func TestRenewalWorkerRunsImmediatelyAndStopsOnCancellation(t *testing.T) {
	t.Parallel()
	renewStarted := make(chan struct{}, 1)
	store := &renewalPageStore{pages: map[string][]domain.Trigger{"/": {{OrganizationID: "org-a", ID: "trg-1"}}}}
	renewer := &renewalRecorder{block: make(chan struct{}), entered: renewStarted}
	worker := RenewalWorker{References: store, Renewer: renewer, Interval: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { worker.Run(ctx); close(done) }()
	select {
	case <-renewStarted:
	case <-time.After(time.Second):
		t.Fatal("immediate scan did not renew")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
}

func TestRenewalWorkerDefaults(t *testing.T) {
	t.Parallel()
	worker := RenewalWorker{}
	capped := RenewalWorker{BatchSize: maximumRenewalBatchSize + 1}
	if worker.interval() != defaultRenewalInterval || worker.batchSize() != defaultRenewalBatchSize || capped.batchSize() != maximumRenewalBatchSize {
		t.Fatalf("defaults = %s/%d", worker.interval(), worker.batchSize())
	}
}
