package domain

import (
	"errors"
	"hash/fnv"
	"math"
	"time"
)

const (
	AdaptivePolicyVersion = "adaptive-ewma.v2"
	MaximumDeliveryDelay  = 90 * time.Minute
	fastHalfLife          = 15 * time.Minute
	slowHalfLife          = 90 * time.Minute
	minimumHistoryEvents  = 5
	minimumTargetSize     = 2
	priorityPressure      = 2.0
	priorityDeadlineLead  = 0.75
)

// FallbackPriority is what a trigger gets when its classifier could not be
// asked. It brings the trigger's soft deadline forward, but its pressure,
// 1 + priorityPressure×FallbackPriority, stays under minimumTargetSize, so one
// such trigger never makes a group due by itself and a classifier outage is
// not one agent turn per webhook.
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

func (scheduler AdaptiveScheduler) Valid() bool {
	return scheduler.rule.Size > 0 && scheduler.rule.MaxWait > 0 && scheduler.rule.MaxWait <= MaximumDeliveryDelay && scheduler.rule.Largest >= scheduler.rule.Size
}

// Observe advances the time-decayed profile and assigns this trigger its
// schedule. The returned deadlines depend only on this observation and are
// persisted with it; a later observation cannot postpone them.
func (scheduler AdaptiveScheduler) Observe(profile TrafficProfile, at time.Time, key string, priority float64) (TrafficProfile, Schedule, error) {
	if !scheduler.Valid() {
		return profile, Schedule{}, errors.New("adaptive scheduler is not configured")
	}
	if at.IsZero() || key == "" || priority < 0 || priority > 1 {
		return profile, Schedule{}, errors.New("adaptive observation needs a time, key, and priority between zero and one")
	}
	at = at.UTC()
	fastDecay := math.Ln2 / fastHalfLife.Hours()
	slowDecay := math.Ln2 / slowHalfLife.Hours()
	if profile.TotalEvents == 0 || profile.ObservedAt.IsZero() {
		// The configured cold target is an explicit prior of Size events per
		// cadence. It prevents the first few samples from producing a wildly
		// high or low target.
		baseRate := float64(scheduler.rule.Size) / scheduler.rule.MaxWait.Hours()
		profile.FastMass = baseRate / fastDecay
		profile.SlowMass = baseRate / slowDecay
		profile.ObservedAt = at
	} else {
		elapsed := at.Sub(profile.ObservedAt)
		if elapsed < 0 {
			elapsed = 0
		}
		profile.FastMass *= math.Exp(-fastDecay * elapsed.Hours())
		profile.SlowMass *= math.Exp(-slowDecay * elapsed.Hours())
	}
	profile.FastMass++
	profile.SlowMass++
	profile.ObservedAt = at
	profile.LastEventAt = at
	profile.TotalEvents++

	target := scheduler.rule.Size
	if profile.TotalEvents >= minimumHistoryEvents {
		fastRate := profile.FastMass * fastDecay
		slowRate := profile.SlowMass * slowDecay
		// Favor the current regime without discarding the stable baseline.
		effectiveRate := .75*fastRate + .25*slowRate
		target = int(math.Ceil(effectiveRate * scheduler.rule.MaxWait.Hours()))
	}
	target = max(minimumTargetSize, min(target, scheduler.rule.Largest))

	hardDue := cadenceBoundary(at, scheduler.rule.MaxWait, key)
	window := hardDue.Sub(at)
	lead := time.Duration(float64(window) * priorityDeadlineLead * priority)
	softDue := hardDue.Add(-lead)
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
