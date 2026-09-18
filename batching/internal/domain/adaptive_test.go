package domain

import (
	"math"
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

// observe records an event received and recorded at the same moment.
func observe(scheduler AdaptiveScheduler, profile TrafficProfile, at time.Time, priority float64) (TrafficProfile, Schedule, error) {
	return scheduler.Observe(profile, at, at, "org_1/user_1", priority)
}

func TestAdaptiveSchedulerStartsFromTheConfiguredPriorAndFixesADeadline(t *testing.T) {
	_, schedule, err := testScheduler(t).Observe(TrafficProfile{}, adaptiveTestTime, adaptiveTestTime, "org_1/user_1", 0)
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
		profile, schedule, err = observe(scheduler, profile, adaptiveTestTime.Add(time.Duration(index)*time.Minute), 0)
		if err != nil {
			t.Fatal(err)
		}
	}
	if schedule.TargetSize <= 5 {
		t.Fatalf("burst target = %d", schedule.TargetSize)
	}
	_, afterSilence, err := scheduler.Observe(profile, adaptiveTestTime.Add(3*time.Hour), adaptiveTestTime.Add(3*time.Hour), "org_1/user_1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if afterSilence.TargetSize >= schedule.TargetSize {
		t.Fatalf("target did not fall after silence: before %d, after %d", schedule.TargetSize, afterSilence.TargetSize)
	}
}

func TestPriorityAddsPressureAndOnlyBringsTheSoftDeadlineForward(t *testing.T) {
	scheduler := testScheduler(t)
	_, ordinary, err := scheduler.Observe(TrafficProfile{}, adaptiveTestTime, adaptiveTestTime, "org_1/user_1", 0)
	if err != nil {
		t.Fatal(err)
	}
	_, important, err := scheduler.Observe(TrafficProfile{}, adaptiveTestTime, adaptiveTestTime, "org_1/user_1", .8)
	if err != nil {
		t.Fatal(err)
	}
	if important.HardDueAt != ordinary.HardDueAt || !important.SoftDueAt.Before(ordinary.SoftDueAt) || important.Pressure <= ordinary.Pressure {
		t.Fatalf("ordinary = %+v, important = %+v", ordinary, important)
	}
}

// TestAFallbackTriggerNeverMakesAGroupDueByItself keeps a classifier outage
// from becoming one agent turn per webhook: the fallback still brings the
// soft deadline forward, but alone it never reaches any target.
func TestAFallbackTriggerNeverMakesAGroupDueByItself(t *testing.T) {
	if pressure := 1 + priorityPressure*FallbackPriority; pressure >= minimumTargetSize {
		t.Fatalf("fallback pressure = %v, want it under the smallest target %d", pressure, minimumTargetSize)
	}
	scheduler := testScheduler(t)
	_, ordinary, err := scheduler.Observe(TrafficProfile{}, adaptiveTestTime, adaptiveTestTime, "org_1/user_1", 0)
	if err != nil {
		t.Fatal(err)
	}
	_, fallback, err := scheduler.Observe(TrafficProfile{}, adaptiveTestTime, adaptiveTestTime, "org_1/user_1", FallbackPriority)
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Pressure >= float64(fallback.TargetSize) || !fallback.SoftDueAt.Before(ordinary.SoftDueAt) {
		t.Fatalf("fallback = %+v, ordinary = %+v", fallback, ordinary)
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

// TestDeadlinesCountFromWhenAppsReceivedTheTrigger: a trigger that took a
// while to reach batching has waited that long already, so its deadlines
// count from its receipt, not from whenever a replica recorded it.
func TestDeadlinesCountFromWhenAppsReceivedTheTrigger(t *testing.T) {
	scheduler := testScheduler(t)
	received, recorded := adaptiveTestTime, adaptiveTestTime.Add(40*time.Minute)
	want := cadenceBoundary(received, time.Hour, "org_1/user_1")
	if cadenceBoundary(recorded, time.Hour, "org_1/user_1").Equal(want) {
		t.Fatal("receipt and recording fall in one cadence, so this test cannot tell them apart")
	}
	_, schedule, err := scheduler.Observe(TrafficProfile{}, received, recorded, "org_1/user_1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !schedule.HardDueAt.Equal(want) {
		t.Fatalf("hard deadline = %s, want the cadence boundary after receipt, %s", schedule.HardDueAt, want)
	}
}

func TestAReceiptTimeOutsideReasonCountsFromTheNearestSaneOne(t *testing.T) {
	scheduler := testScheduler(t)
	recorded := adaptiveTestTime
	// A receipt from the future, a skewed clock's, cannot postpone anything.
	_, ahead, err := scheduler.Observe(TrafficProfile{}, recorded.Add(time.Hour), recorded, "org_1/user_1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := cadenceBoundary(recorded, time.Hour, "org_1/user_1"); !ahead.HardDueAt.Equal(want) {
		t.Fatalf("a future receipt's hard deadline = %s, want %s", ahead.HardDueAt, want)
	}
	// A receipt far in the past is due as soon as it is recorded, with its
	// deadlines no further back than one longest cadence.
	for _, received := range []time.Time{recorded.Add(-72 * time.Hour), time.Date(1000, time.January, 1, 0, 0, 0, 0, time.UTC)} {
		_, old, err := scheduler.Observe(TrafficProfile{}, received, recorded, "org_1/user_1", 0)
		if err != nil {
			t.Fatal(err)
		}
		if old.HardDueAt.After(recorded) || old.HardDueAt.Before(recorded.Add(-MaximumDeliveryDelay)) || old.SoftDueAt.After(old.HardDueAt) {
			t.Fatalf("received %s: deadlines = %s / %s, want both due by %s and none before %s",
				received, old.SoftDueAt, old.HardDueAt, recorded, recorded.Add(-MaximumDeliveryDelay))
		}
	}
}

// TestTheOrderEventsArriveInDoesNotChangeTheEstimate: the bus orders
// nothing, and a late event must neither turn the profile's clock back nor
// count differently from one that arrived on time.
func TestTheOrderEventsArriveInDoesNotChangeTheEstimate(t *testing.T) {
	scheduler := testScheduler(t)
	warm, _, err := observe(scheduler, TrafficProfile{}, adaptiveTestTime, 0)
	if err != nil {
		t.Fatal(err)
	}
	first, second := adaptiveTestTime.Add(5*time.Minute), adaptiveTestTime.Add(20*time.Minute)
	inOrder, _, _ := observe(scheduler, warm, first, 0)
	inOrder, _, _ = observe(scheduler, inOrder, second, 0)
	reversed, _, _ := observe(scheduler, warm, second, 0)
	reversed, _, err = observe(scheduler, reversed, first, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reversed.ObservedAt.Equal(second) || !reversed.LastEventAt.Equal(second) {
		t.Fatalf("the profile's clock went back to %s / %s", reversed.ObservedAt, reversed.LastEventAt)
	}
	if math.Abs(inOrder.FastMass-reversed.FastMass) > 1e-9 || math.Abs(inOrder.SlowMass-reversed.SlowMass) > 1e-9 ||
		inOrder.TotalEvents != reversed.TotalEvents {
		t.Fatalf("in order %+v, reversed %+v", inOrder, reversed)
	}
}
