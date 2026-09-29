package postgres

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/batching/internal/domain"
	workspacepolicy "github.com/Shubham04-oss/oryxa-go/internal/workspace/policy"
	"github.com/jackc/pgx/v5/pgxpool"
)

type poolPolicy struct {
	pool    *pgxpool.Pool
	entered chan struct{}
	start   chan struct{}
}

func (provider poolPolicy) Get(ctx context.Context, _ string) (workspacepolicy.Settings, error) {
	provider.entered <- struct{}{}
	select {
	case <-provider.start:
	case <-ctx.Done():
		return workspacepolicy.Settings{}, ctx.Err()
	}
	var one int
	err := provider.pool.QueryRow(ctx, "SELECT 1").Scan(&one)
	return workspacepolicy.Settings{WakeUpWithin: time.Minute}, err
}

func TestConcurrentDigestPolicyReadsDoNotStarveSmallPool(t *testing.T) {
	databaseURL := os.Getenv("BATCHING_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("BATCHING_TEST_DATABASE_URL is not set")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("pool_max_conns", "2")
	parsed.RawQuery = query.Encode()
	t.Setenv("BATCHING_TEST_DATABASE_URL", parsed.String())
	store, organizationID := testStore(t, domain.Rule{Size: 5, MaxWait: time.Hour, Largest: 20})
	provider := poolPolicy{pool: store.Pool(), entered: make(chan struct{}, 2), start: make(chan struct{})}
	store.WithPolicy(provider)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 2)
	for i := range 2 {
		go func() {
			_, err := store.Record(ctx, testTrigger(organizationID, fmt.Sprintf("member_%d", i), fmt.Sprintf("policy_%d", i), domain.LaneDigest))
			done <- err
		}()
	}
	for range 2 {
		select {
		case <-provider.entered:
		case <-ctx.Done():
			t.Fatal("policy reads did not start")
		}
	}
	close(provider.start)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("policy lookup held a transaction connection: %v", err)
		}
	}
}

type fakeCadencePolicy struct {
	wait time.Duration
	err  error
}

func (policy fakeCadencePolicy) Get(context.Context, string) (workspacepolicy.Settings, error) {
	return workspacepolicy.Settings{WakeUpWithin: policy.wait}, policy.err
}

func TestWorkspaceWakeUpCadenceUsesAppliedPolicy(t *testing.T) {
	defaultScheduler, err := domain.NewAdaptiveScheduler(domain.Rule{Size: 5, MaxWait: time.Hour, Largest: 20})
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{scheduler: defaultScheduler}
	base := time.Date(2026, time.September, 29, 12, 0, 0, 0, time.UTC)
	deadline := func(scheduler domain.AdaptiveScheduler) time.Time {
		_, scheduled, err := scheduler.Observe(domain.TrafficProfile{}, base, base, "org_1/member_1", 0)
		if err != nil {
			t.Fatal(err)
		}
		return scheduled.HardDueAt
	}
	defaultDue := deadline(defaultScheduler)
	wantScheduler, err := domain.NewAdaptiveScheduler(domain.Rule{Size: 5, MaxWait: 10 * time.Minute, Largest: 20})
	if err != nil {
		t.Fatal(err)
	}
	store.WithPolicy(fakeCadencePolicy{wait: 10 * time.Minute})
	chosen, err := store.schedulerFor(context.Background(), "org_1")
	if err != nil {
		t.Fatal(err)
	}
	if !deadline(chosen).Equal(deadline(wantScheduler)) || deadline(chosen).After(base.Add(10*time.Minute)) {
		t.Fatalf("workspace deadline = %s, expected = %s", deadline(chosen), deadline(wantScheduler))
	}
	store.WithPolicy(fakeCadencePolicy{})
	chosen, err = store.schedulerFor(context.Background(), "org_1")
	if err != nil || !deadline(chosen).Equal(defaultDue) {
		t.Fatalf("omitted cadence deadline = %s, default = %s, error = %v", deadline(chosen), defaultDue, err)
	}
	store.WithPolicy(fakeCadencePolicy{err: errors.New("database down")})
	if _, err := store.schedulerFor(context.Background(), "org_1"); err == nil {
		t.Fatal("batching proceeded without its workspace policy")
	}
}
