package domain

import "time"

const (
	// HealthInterval is how long a healthy connection waits for its next
	// check.
	HealthInterval = 24 * time.Hour
	// RecentUse is how recently issuing a token counts as proof a connection
	// works, so the heartbeat skips it.
	RecentUse = 12 * time.Hour
	// MinHealthBackoff and MaxHealthBackoff bound the wait before a degraded
	// connection is checked again.
	MinHealthBackoff = 5 * time.Minute
	MaxHealthBackoff = 6 * time.Hour
)

// HealthCheck is a connection's health as claimed for a check.
type HealthCheck struct {
	OrganizationID      string
	ConnectionID        string
	State               Health
	Reason              string
	ConsecutiveFailures int
	LastCheckedAt       *time.Time
}

// HealthResult is what a check records.
type HealthResult struct {
	State               Health
	Reason              string
	ConsecutiveFailures int
	// CheckedAt is nil when the connection was skipped rather than checked.
	CheckedAt   *time.Time
	Succeeded   bool
	NextCheckAt time.Time
}

// Changed reports whether the result moves the connection to another health
// state.
func (check HealthCheck) Changed(result HealthResult) bool {
	return result.State != check.State
}

// Succeeded records a passing check.
func (check HealthCheck) Succeeded(at, next time.Time) HealthResult {
	return HealthResult{State: HealthHealthy, CheckedAt: &at, Succeeded: true, NextCheckAt: next}
}

// Failed records a check the provider could not answer, retried with backoff.
func (check HealthCheck) Failed(reason string, at time.Time) HealthResult {
	failures := check.ConsecutiveFailures + 1
	return HealthResult{
		State: HealthDegraded, Reason: reason, ConsecutiveFailures: failures, CheckedAt: &at,
		NextCheckAt: at.Add(HealthBackoff(failures)),
	}
}

// Skipped keeps the connection's health as it is and checks it again at next.
func (check HealthCheck) Skipped(next time.Time) HealthResult {
	return HealthResult{State: check.State, Reason: check.Reason, ConsecutiveFailures: check.ConsecutiveFailures, NextCheckAt: next}
}

// HealthBackoff is the wait after a number of consecutive failures: 5 minutes,
// doubling up to 6 hours.
func HealthBackoff(failures int) time.Duration {
	wait := MinHealthBackoff
	for range max(failures-1, 0) {
		wait *= 2
		if wait >= MaxHealthBackoff {
			return MaxHealthBackoff
		}
	}
	return wait
}
