package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// TestClaimHealthChecksFiltersAndConcurrency checks that ClaimHealthChecks
// skips a check that is not due, one whose connection is not active, and one
// whose credentials were deleted, and that concurrent claims with different
// tokens never both return the same due row.
func TestClaimHealthChecksFiltersAndConcurrency(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	org := "org_claim_health"
	app := createApp(t, store, org, "app_google", domain.CredentialOAuth2, now)

	due := createConnection(t, store, app, "con_due", "user-1", "account-1", now)
	notDue := createConnection(t, store, app, "con_not_due", "user-1", "account-2", now)

	needsReconnect := createConnection(t, store, app, "con_needs_reconnect", "user-1", "account-3", now)
	if _, err := store.MarkNeedsReconnect(ctx, org, needsReconnect.ID, needsReconnect.Version, domain.ReconnectUnauthorized, now,
		func(domain.Connection) (domain.Event, error) {
			return testEvent("connections.connection.needs-reconnect.v1", now), nil
		}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReportConnection(ctx, org, needsReconnect.ID, now); err != nil {
		t.Fatal(err)
	}

	deletedCredentials := createConnection(t, store, app, "con_deleted_credentials", "user-1", "account-4", now)
	disconnect(t, store, org, deletedCredentials.ID, domain.DisconnectRequested, now)
	claimedForDeletion, err := store.ClaimCredentialDeletions(ctx, now, 10, "claim-del", now.Add(time.Minute))
	if err != nil || len(claimedForDeletion) != 1 {
		t.Fatalf("ClaimCredentialDeletions() = %+v, %v", claimedForDeletion, err)
	}
	if err := store.DeleteCredentials(ctx, org, deletedCredentials.ID, "claim-del", now,
		testEvent("connections.connection.credentials-deleted.v1", now)); err != nil {
		t.Fatal(err)
	}

	// Bring "due"'s check due right now; "notDue"'s stays 24h out, from the
	// fixture's CreateConnection call.
	if err := store.ReportConnection(ctx, org, due.ID, now); err != nil {
		t.Fatal(err)
	}

	const attempts = 5
	results := make([][]domain.HealthCheck, attempts)
	errs := make([]error, attempts)
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			checks, err := store.ClaimHealthChecks(ctx, now, 10, fmt.Sprintf("claim-%d", i), now.Add(time.Minute))
			results[i], errs[i] = checks, err
		}(i)
	}
	wg.Wait()

	seen := map[string]int{}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("ClaimHealthChecks() goroutine %d error = %v", i, err)
		}
		for _, check := range results[i] {
			seen[check.ConnectionID]++
		}
	}
	if seen[due.ID] != 1 {
		t.Fatalf("connection %s claimed %d times, want exactly 1: %v", due.ID, seen[due.ID], seen)
	}
	if seen[notDue.ID] != 0 {
		t.Fatalf("not-due connection was claimed: %v", seen)
	}
	if seen[needsReconnect.ID] != 0 {
		t.Fatalf("needs_reconnect connection was claimed: %v", seen)
	}
	if seen[deletedCredentials.ID] != 0 {
		t.Fatalf("credentials-deleted connection was claimed: %v", seen)
	}
	if total := len(seen); total != 1 {
		t.Fatalf("expected only one connection claimed across all goroutines, saw %d: %v", total, seen)
	}

	// The claimed row is now locked: an immediate re-claim finds nothing.
	again, err := store.ClaimHealthChecks(ctx, now, 10, "claim-again", now.Add(time.Minute))
	if err != nil || len(again) != 0 {
		t.Fatalf("re-claim before lock expiry = %+v, %v, want empty", again, err)
	}
}

func TestRecordHealthCheck(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	org := "org_record_health"
	app := createApp(t, store, org, "app_google", domain.CredentialOAuth2, now)
	connection := createConnection(t, store, app, "con_a", "user-1", "account-1", now)
	if err := store.ReportConnection(ctx, org, connection.ID, now); err != nil {
		t.Fatal(err)
	}

	checks, err := store.ClaimHealthChecks(ctx, now, 10, "token-1", now.Add(time.Minute))
	if err != nil || len(checks) != 1 {
		t.Fatalf("ClaimHealthChecks() = %+v, %v", checks, err)
	}
	check := checks[0]
	if check.State != domain.HealthUnknown {
		t.Fatalf("claimed check state = %s, want unknown", check.State)
	}

	// A token that was never granted the claim fails.
	if err := store.RecordHealthCheck(ctx, check, "wrong-token", check.Succeeded(now, now.Add(domain.HealthInterval)), nil); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("RecordHealthCheck(stale token) error = %v, want ErrConflict", err)
	}

	event := testEvent("connections.connection.health-changed.v1", now)
	if err := store.RecordHealthCheck(ctx, check, "token-1", check.Succeeded(now, now.Add(domain.HealthInterval)), &event); err != nil {
		t.Fatal(err)
	}

	var state, reason string
	var nextCheckAt, lastCheckedAt, lastSuccessAt time.Time
	var claimToken *string
	if err := store.pool.QueryRow(ctx, `
		SELECT state, reason, next_check_at, last_checked_at, last_success_at, claim_token
		FROM connections_health WHERE organization_id = $1 AND connection_id = $2`,
		org, connection.ID).Scan(&state, &reason, &nextCheckAt, &lastCheckedAt, &lastSuccessAt, &claimToken); err != nil {
		t.Fatal(err)
	}
	if state != string(domain.HealthHealthy) || !nextCheckAt.Equal(now.Add(domain.HealthInterval)) ||
		!lastCheckedAt.Equal(now) || !lastSuccessAt.Equal(now) || claimToken != nil {
		t.Fatalf("health row after success: state=%s next=%v checked=%v success=%v claim=%v",
			state, nextCheckAt, lastCheckedAt, lastSuccessAt, claimToken)
	}

	// Recording again under the same, now-cleared token fails: the claim is
	// gone.
	if err := store.RecordHealthCheck(ctx, check, "token-1", check.Succeeded(now, now.Add(domain.HealthInterval)), nil); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("RecordHealthCheck() after the claim was cleared error = %v, want ErrConflict", err)
	}

	// Bring it due again and fail it: consecutive_failures increments and
	// last_success_at is left as it was.
	if err := store.ReportConnection(ctx, org, connection.ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	checks, err = store.ClaimHealthChecks(ctx, now.Add(time.Hour), 10, "token-2", now.Add(time.Hour+time.Minute))
	if err != nil || len(checks) != 1 {
		t.Fatalf("second ClaimHealthChecks() = %+v, %v", checks, err)
	}
	check = checks[0]
	if check.State != domain.HealthHealthy || check.ConsecutiveFailures != 0 {
		t.Fatalf("claimed check before failure = %+v, want healthy/0", check)
	}
	failResult := check.Failed("timeout", now.Add(time.Hour))
	if err := store.RecordHealthCheck(ctx, check, "token-2", failResult, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(ctx, `
		SELECT state, reason, consecutive_failures, last_success_at
		FROM connections_health WHERE organization_id = $1 AND connection_id = $2`,
		org, connection.ID).Scan(&state, &reason, new(int), &lastSuccessAt); err != nil {
		t.Fatal(err)
	}
	if state != string(domain.HealthDegraded) || reason != "timeout" || !lastSuccessAt.Equal(now) {
		t.Fatalf("health row after failure: state=%s reason=%s lastSuccess=%v, want degraded/timeout/%v", state, reason, lastSuccessAt, now)
	}
}
