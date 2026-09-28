package signin

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func (h *harness) checkNow(connectionID string) Check {
	h.t.Helper()
	h.store.setNextCheck(connectionID, h.clock.Now())
	checks, err := h.service.CheckDue(context.Background(), 10)
	if err != nil {
		h.t.Fatalf("CheckDue() = %v", err)
	}
	if len(checks) != 1 || checks[0].ConnectionID != connectionID {
		h.t.Fatalf("checks = %+v, want %s alone", checks, connectionID)
	}
	return checks[0]
}

func TestCheckDuePassesAWorkingConnection(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.connect("con_1", "acme")
	credential, _ := h.store.stored("con_1")
	if day := credential.NextCheckAt.Sub(h.clock.Now()); day < 21*time.Hour || day > 27*time.Hour {
		t.Fatalf("a new credential's first check is in %v, want about a day", day)
	}
	if checks, _ := h.service.CheckDue(context.Background(), 10); len(checks) != 0 {
		t.Fatalf("checks = %+v, want none before they are due", checks)
	}
	check := h.checkNow("con_1")
	if check.Outcome != CheckPassed || check.OrganizationID != testOrganization || check.Toolkit != "acme" {
		t.Fatalf("check = %+v", check)
	}
	credential, _ = h.store.stored("con_1")
	if day := credential.NextCheckAt.Sub(h.clock.Now()); day < 21*time.Hour || day > 27*time.Hour || credential.CheckFailures != 0 {
		t.Fatalf("next check in %v with %d failures, want about a day and none", day, credential.CheckFailures)
	}
}

func TestCheckDueBacksOffWhileTheAppDoesNotAnswer(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.connect("con_1", "acme")
	h.app.set(func(a *stubApp) { a.meStatus = http.StatusServiceUnavailable })
	for failures, wait := range []time.Duration{0, 5 * time.Minute, 10 * time.Minute, 20 * time.Minute} {
		if failures == 0 {
			continue
		}
		if check := h.checkNow("con_1"); check.Outcome != CheckFailed || check.Reason != "" {
			t.Fatalf("check = %+v, want failed", check)
		}
		credential, _ := h.store.stored("con_1")
		if credential.CheckFailures != failures || !credential.NextCheckAt.Equal(h.clock.Now().Add(wait)) {
			t.Fatalf("after %d failures: %d recorded, next in %v; want a retry in %v", failures, credential.CheckFailures,
				credential.NextCheckAt.Sub(h.clock.Now()), wait)
		}
	}
	if got := backoff(20); got != 6*time.Hour {
		t.Fatalf("backoff(20) = %v, want the 6 hour cap", got)
	}
	// Answering again clears the failures.
	h.app.set(func(a *stubApp) { a.meStatus = 0 })
	if check := h.checkNow("con_1"); check.Outcome != CheckPassed {
		t.Fatalf("check = %+v", check)
	}
	if credential, _ := h.store.stored("con_1"); credential.CheckFailures != 0 {
		t.Fatalf("failures = %d, want none after a pass", credential.CheckFailures)
	}
}

func TestCheckDueRejectsWhatTheAppRefuses(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.connect("con_1", "acme")
	h.app.set(func(a *stubApp) { a.meStatus = http.StatusUnauthorized })
	if check := h.checkNow("con_1"); check.Outcome != CheckRejected || check.Reason != ExpiredCredentialRejected {
		t.Fatalf("a refused test call = %+v, want rejected credential_rejected", check)
	}

	h = newHarness(t)
	h.connect("con_1", "acme")
	h.app.set(func(a *stubApp) { a.refuseRefresh = true })
	h.untilDue("con_1")
	if check := h.checkNow("con_1"); check.Outcome != CheckRejected || check.Reason != ExpiredRefreshRejected {
		t.Fatalf("a refused refresh = %+v, want rejected refresh_rejected", check)
	}
}

func TestCheckDueKeepsAnIdleGrantAlive(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.connect("con_1", "acme")
	h.untilDue("con_1")
	h.clock.Advance(time.Hour)
	if check := h.checkNow("con_1"); check.Outcome != CheckPassed {
		t.Fatalf("check = %+v", check)
	}
	if _, refreshes, _ := h.app.counts(); refreshes != 1 {
		t.Fatalf("refreshes = %d, want the idle token renewed by its check", refreshes)
	}
}

func TestCheckDueSkipsWhatWasUsedAndWhatIsNotActive(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.connect("con_used", "acme")
	h.connect("con_expired", "acme")
	h.store.setNextCheck("con_used", h.clock.Now())
	h.store.setNextCheck("con_expired", h.clock.Now())
	h.store.setStatus("con_expired", "expired")
	session, err := h.service.Session(context.Background(), "con_used")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := get(t, session, h.app.base+"/api/data"); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(11 * time.Hour)
	if checks, _ := h.service.CheckDue(context.Background(), 10); len(checks) != 0 {
		t.Fatalf("checks = %+v, want none: one was used within 12 hours, the other is not active", checks)
	}
	h.clock.Advance(2 * time.Hour)
	if checks, _ := h.service.CheckDue(context.Background(), 10); len(checks) != 1 || checks[0].ConnectionID != "con_used" {
		t.Fatalf("checks = %+v, want the used one once 12 hours have passed", checks)
	}
	// A claimed check is not claimed again while it runs elsewhere.
	h.store.setNextCheck("con_used", h.clock.Now())
	claimed, err := h.store.ClaimCredentialChecks(context.Background(), h.clock.Now(), h.clock.Now().Add(claimFor), 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	if again, _ := h.service.CheckDue(context.Background(), 10); len(again) != 0 {
		t.Fatalf("a claimed check was checked again: %+v", again)
	}
}
