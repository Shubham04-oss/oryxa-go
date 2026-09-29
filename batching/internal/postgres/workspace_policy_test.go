package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/batching/internal/domain"
	workspacepolicy "github.com/Shubham04-oss/oryxa-go/internal/workspace/policy"
)

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
