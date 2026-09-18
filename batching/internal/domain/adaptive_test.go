package domain

import (
	"testing"
	"time"
)

var adaptiveTestTime = time.Date(2026, time.September, 19, 12, 0, 0, 0, time.UTC)

func testScheduler(t *testing.T) AdaptiveScheduler {
	t.Helper()
	scheduler, err := NewAdaptiveScheduler(Rule{Size: 5, MaxWait: time.Hour, Largest: 20})
	if err != nil {
		t.Fatal(err)
	}
	return scheduler
}

func TestAdaptiveSchedulerStartsFromTheConfiguredPriorAndFixesADeadline(t *testing.T) {
	_, schedule, err := testScheduler(t).Observe(TrafficProfile{}, adaptiveTestTime, "org_1/user_1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if schedule.TargetSize != 5 || schedule.PolicyVersion != AdaptivePolicyVersion {
		t.Fatalf("schedule = %+v", schedule)
	}
	if !schedule.HardDueAt.After(adaptiveTestTime) || schedule.HardDueAt.Sub(adaptiveTestTime) > time.Hour || schedule.SoftDueAt != schedule.HardDueAt {
		t.Fatalf("deadlines = %s / %s", schedule.SoftDueAt, schedule.HardDueAt)
	}
}

func TestAdaptiveSchedulerRejectsDeliveryCadencesOverNinetyMinutes(t *testing.T) {
	if _, err := NewAdaptiveScheduler(Rule{Size: 5, MaxWait: MaximumDeliveryDelay + time.Second, Largest: 20}); err == nil {
		t.Fatal("NewAdaptiveScheduler() error = nil")
	}
}

func TestCurrentTrafficCanRaiseThenSilenceCanLowerTheTarget(t *testing.T) {
	scheduler := testScheduler(t)
	profile := TrafficProfile{}
	var schedule Schedule
	var err error
	for index := range 30 {
		profile, schedule, err = scheduler.Observe(profile, adaptiveTestTime.Add(time.Duration(index)*time.Minute), "org_1/user_1", 0)
		if err != nil {
			t.Fatal(err)
		}
	}
	if schedule.TargetSize <= 5 {
		t.Fatalf("burst target = %d", schedule.TargetSize)
	}
	_, afterSilence, err := scheduler.Observe(profile, adaptiveTestTime.Add(3*time.Hour), "org_1/user_1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if afterSilence.TargetSize >= schedule.TargetSize {
		t.Fatalf("target did not fall after silence: before %d, after %d", schedule.TargetSize, afterSilence.TargetSize)
	}
}

func TestPriorityAddsPressureAndOnlyBringsTheSoftDeadlineForward(t *testing.T) {
	scheduler := testScheduler(t)
	_, ordinary, err := scheduler.Observe(TrafficProfile{}, adaptiveTestTime, "org_1/user_1", 0)
	if err != nil {
		t.Fatal(err)
	}
	_, important, err := scheduler.Observe(TrafficProfile{}, adaptiveTestTime, "org_1/user_1", .8)
	if err != nil {
		t.Fatal(err)
	}
	if important.HardDueAt != ordinary.HardDueAt || !important.SoftDueAt.Before(ordinary.SoftDueAt) || important.Pressure <= ordinary.Pressure {
		t.Fatalf("ordinary = %+v, important = %+v", ordinary, important)
	}
}

func TestCadenceOffsetIsStablePerMember(t *testing.T) {
	first := cadenceBoundary(adaptiveTestTime, time.Hour, "org_1/user_1")
	second := cadenceBoundary(first.Add(-time.Minute), time.Hour, "org_1/user_1")
	if first != second {
		t.Fatalf("boundary moved within one member epoch: %s != %s", first, second)
	}
	if other := cadenceBoundary(adaptiveTestTime, time.Hour, "org_1/user_2"); other == first {
		t.Fatalf("two member offsets unexpectedly matched: %s", first)
	}
}
