package domain

import (
	"errors"
	"hash/fnv"
	"math"
	"time"
)

const (
	AdaptivePolicyVersion = "adaptive-ewma.v3"
	MaximumDeliveryDelay  = 90 * time.Minute
	fastHalfLife          = 15 * time.Minute
	slowHalfLife          = 90 * time.Minute
	minimumHistoryEvents  = 5
	// minimumTargetSize is as low as quiet traffic can bring a member's
	// target, unless the configured cold target is lower still: an operator
	// who asked for batches of one gets them.
	minimumTargetSize    = 2
	priorityPressure     = 2.0
	priorityDeadlineLead = 0.75
	// backlogAge is how long before its recording a trigger must have been
	// received to count as part of a backlog, well past ordinary delivery.
	backlogAge = 5 * time.Minute
	// catchUpWindow is how long a backlogged trigger waits past its
	// recording, so a member's backlog goes out in batches rather than a
	// trigger or two per sweep.
	catchUpWindow = time.Minute
)

// FallbackPriority is what a trigger gets when its classifier could not be
// asked. It brings the trigger's soft deadline forward, but its pressure,
// 1 + priorityPressure×FallbackPriority, stays under minimumTargetSize. That
// is the smallest target whenever the cold target is at least two, so one
// such trigger then never makes a group due by itself and a classifier outage
// is not one agent turn per webhook. A cold target of one asks for a turn per
// webhook anyway.
const FallbackPriority = 0.4

// AdaptiveScheduler combines two exponentially decayed arrival-rate views.
// The fast view reacts to regime changes and bursts; the slow view prevents
// one short interval from completely rewriting a member's normal behavior.
// Every observation also receives a fixed cadence deadline, so estimation
// errors cannot strand a partial batch.
type AdaptiveScheduler struct {
	rule Rule
}

func NewAdaptiveScheduler(rule Rule) (AdaptiveScheduler, error) {
	if rule.Size < 1 || rule.MaxWait <= 0 || rule.MaxWait > MaximumDeliveryDelay || rule.Largest < rule.Size {
		return AdaptiveScheduler{}, errors.New("adaptive batching needs a positive cold target and a cadence no longer than 90 minutes, with largest at least the target")
	}
	return AdaptiveScheduler{rule: rule}, nil
}

// WithMaxWait keeps the deployment's batch sizes while using an
// organization's cadence for newly recorded digest triggers.
func (scheduler AdaptiveScheduler) WithMaxWait(maxWait time.Duration) (AdaptiveScheduler, error) {
	rule := scheduler.rule
	rule.MaxWait = maxWait
	return NewAdaptiveScheduler(rule)
}

func (scheduler AdaptiveScheduler) Valid() bool {
	return scheduler.rule.Size > 0 && scheduler.rule.MaxWait > 0 && scheduler.rule.MaxWait <= MaximumDeliveryDelay && scheduler.rule.Largest >= scheduler.rule.Size
}

// CountFrom is the time a trigger's deadlines count from: when apps received
// it, since the promise is about how long what a member's app reported
// waits, not about when a replica happened to record it. A receipt ahead of
// the recording clock counts as the recording time, so a skewed clock cannot
// postpone a deadline. A receipt more than MaximumDeliveryDelay before
// recording counts as exactly that long before: every cadence boundary after
// it has passed either way, and whatever a broken clock said, the cadence
// arithmetic stays in range and the traffic estimate's observation recent.
func CountFrom(receivedAt, recordedAt time.Time) time.Time {
	receivedAt, recordedAt = receivedAt.UTC(), recordedAt.UTC()
	if receivedAt.After(recordedAt) {
		return recordedAt
	}
	if earliest := recordedAt.Add(-MaximumDeliveryDelay); receivedAt.Before(earliest) {
		return earliest
	}
	return receivedAt
}

// Observe advances the time-decayed profile and assigns this trigger its
// schedule, counted from CountFrom(receivedAt, recordedAt) but never set
// before recordedAt: a trigger whose cadence passed on its way here is due
// as it is recorded, not before — or, when it is part of a backlog, a short
// catch-up window after, so the backlog goes out together. The returned deadlines depend only on this
// observation and are persisted with it; a later observation cannot
// postpone them.
func (scheduler AdaptiveScheduler) Observe(profile TrafficProfile, receivedAt, recordedAt time.Time, key string, priority float64) (TrafficProfile, Schedule, error) {
	if !scheduler.Valid() {
		return profile, Schedule{}, errors.New("adaptive scheduler is not configured")
	}
	if receivedAt.IsZero() || recordedAt.IsZero() || key == "" || priority < 0 || priority > 1 {
		return profile, Schedule{}, errors.New("adaptive observation needs its times, a key, and a priority between zero and one")
	}
	at := CountFrom(receivedAt, recordedAt)
	fastDecay := math.Ln2 / fastHalfLife.Hours()
	slowDecay := math.Ln2 / slowHalfLife.Hours()
	switch {
	case profile.TotalEvents == 0 || profile.ObservedAt.IsZero():
		// The configured cold target is an explicit prior of Size events per
		// cadence. It prevents the first few samples from producing a wildly
		// high or low target.
		baseRate := float64(scheduler.rule.Size) / scheduler.rule.MaxWait.Hours()
		profile.FastMass = baseRate/fastDecay + 1
		profile.SlowMass = baseRate/slowDecay + 1
		profile.ObservedAt, profile.LastEventAt = at, at
	case at.Before(profile.ObservedAt):
		// The bus orders nothing, so an event can be older than one already
		// observed. It adds its weight as of when it happened, and the
		// profile's clock stays put: turning it back would decay the time in
		// between twice. Past the first event, the order events arrive in
		// does not change the estimate.
		age := profile.ObservedAt.Sub(at).Hours()
		profile.FastMass += math.Exp(-fastDecay * age)
		profile.SlowMass += math.Exp(-slowDecay * age)
	default:
		elapsed := at.Sub(profile.ObservedAt).Hours()
		profile.FastMass = profile.FastMass*math.Exp(-fastDecay*elapsed) + 1
		profile.SlowMass = profile.SlowMass*math.Exp(-slowDecay*elapsed) + 1
		profile.ObservedAt = at
	}
	if at.After(profile.LastEventAt) {
		profile.LastEventAt = at
	}
	profile.TotalEvents++

	target := scheduler.rule.Size
	if profile.TotalEvents >= minimumHistoryEvents {
		fastRate := profile.FastMass * fastDecay
		slowRate := profile.SlowMass * slowDecay
		// Favor the current regime without discarding the stable baseline.
		effectiveRate := .75*fastRate + .25*slowRate
		target = int(math.Ceil(effectiveRate * scheduler.rule.MaxWait.Hours()))
	}
	// The floor never exceeds the configured sizes, so the target stays
	// within [floor, Largest] for any rule the scheduler accepts.
	floor := min(minimumTargetSize, scheduler.rule.Size)
	target = max(floor, min(target, scheduler.rule.Largest))

	hardDue := cadenceBoundary(at, scheduler.rule.MaxWait, key)
	window := hardDue.Sub(at)
	lead := time.Duration(float64(window) * priorityDeadlineLead * priority)
	softDue := hardDue.Add(-lead)
	// Raising both to the recording keeps the soft deadline at or before
	// the hard one.
	recorded := recordedAt.UTC()
	floorAt := recorded
	if recorded.Sub(receivedAt.UTC()) > backlogAge {
		// Arriving this late, it is part of a backlog — the consumer was
		// down, or the queue behind — and the rest of the member's backlog
		// is arriving with it. Due the moment each is recorded, the sweep
		// would release it a few triggers at a time, one agent turn each;
		// a short window lets what arrives together go together. It only
		// ever delays what is already late, and by no more than the window.
		floorAt = recorded.Add(catchUpWindow)
	}
	if hardDue.Before(floorAt) {
		hardDue = floorAt
	}
	if softDue.Before(floorAt) {
		softDue = floorAt
	}
	return profile, Schedule{
		TargetSize: target, Pressure: 1 + priorityPressure*priority,
		SoftDueAt: softDue, HardDueAt: hardDue, PolicyVersion: AdaptivePolicyVersion,
	}, nil
}

// cadenceBoundary gives every member a stable offset inside the cadence. It
// preserves the periodic flush guarantee without making all members due on
// the same second.
func cadenceBoundary(at time.Time, cadence time.Duration, key string) time.Time {
	value := fnv.New64a()
	_, _ = value.Write([]byte(key))
	offset := time.Duration(value.Sum64() % uint64(cadence))
	nanos := at.UnixNano()
	shifted := nanos - int64(offset)
	period := int64(cadence)
	boundary := (floorDiv(shifted, period) + 1) * period
	return time.Unix(0, boundary+int64(offset)).UTC()
}

func floorDiv(value, divisor int64) int64 {
	quotient := value / divisor
	if value < 0 && value%divisor != 0 {
		quotient--
	}
	return quotient
}
