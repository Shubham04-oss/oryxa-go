package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

// jsonEqual compares two JSON values for what they mean rather than their
// exact bytes: jsonb re-serializes on the way back out of Postgres (adding a
// space after each ':', for one), so a round trip never guarantees the same
// text back, only the same value.
func jsonEqual(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		t.Fatalf("unmarshal %s: %v", a, err)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}
	return reflect.DeepEqual(av, bv)
}

// newAction is a valid action ready for StartAction, with organization scope
// and every optional field populated so read-back tests have something to
// check; callers override whatever a case needs.
func newAction(organizationID, id, connectionID, appID, name, idempotencyKey, requestedBy string, at time.Time) domain.Action {
	return domain.Action{
		ID:              id,
		OrganizationID:  organizationID,
		ConnectionID:    connectionID,
		AppID:           appID,
		Provider:        "github",
		Name:            name,
		Scope:           "organization",
		ProviderAccount: domain.ProviderAccount{ID: "acct_" + id, Name: "Account " + id},
		RequestedBy:     requestedBy,
		Status:          domain.ActionRunning,
		StartedAt:       at,
		IdempotencyKey:  idempotencyKey,
	}
}

func assertActionRoundTrip(t *testing.T, got, want domain.Action) {
	t.Helper()
	if got.ID != want.ID || got.OrganizationID != want.OrganizationID || got.ConnectionID != want.ConnectionID ||
		got.AppID != want.AppID || got.Provider != want.Provider || got.Name != want.Name {
		t.Fatalf("Action() identity = %+v, want %+v", got, want)
	}
	if got.Scope != want.Scope || got.TeamID != want.TeamID || got.MemberID != want.MemberID {
		t.Fatalf("Action() placement = scope=%s team=%s member=%s, want scope=%s team=%s member=%s",
			got.Scope, got.TeamID, got.MemberID, want.Scope, want.TeamID, want.MemberID)
	}
	if got.ProviderAccount != want.ProviderAccount {
		t.Fatalf("Action().ProviderAccount = %+v, want %+v", got.ProviderAccount, want.ProviderAccount)
	}
	if got.RequestedBy != want.RequestedBy {
		t.Fatalf("Action().RequestedBy = %q, want %q", got.RequestedBy, want.RequestedBy)
	}
	if got.IdempotencyKey != want.IdempotencyKey {
		t.Fatalf("Action().IdempotencyKey = %q, want %q", got.IdempotencyKey, want.IdempotencyKey)
	}
	if got.Status != want.Status {
		t.Fatalf("Action().Status = %s, want %s", got.Status, want.Status)
	}
	if !got.StartedAt.Equal(want.StartedAt) {
		t.Fatalf("Action().StartedAt = %v, want %v", got.StartedAt, want.StartedAt)
	}
}

func TestStartAction(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	org := "org_start"

	t.Run("stores and reads back every field", func(t *testing.T) {
		action := newAction(org, "act_full", "con_1", "app_1", "github_create_issue", "idem-"+randomHex(8), "user_1", now)

		started, ok, err := store.StartAction(ctx, action)
		if err != nil {
			t.Fatalf("StartAction() error = %v", err)
		}
		if !ok {
			t.Fatalf("StartAction() ok = false, want true for a new action")
		}
		assertActionRoundTrip(t, started, action)
		if started.Status != domain.ActionRunning {
			t.Fatalf("StartAction().Status = %s, want %s", started.Status, domain.ActionRunning)
		}
		if started.Resource != nil {
			t.Fatalf("StartAction().Resource = %+v, want nil", started.Resource)
		}
		if started.Result != nil {
			t.Fatalf("StartAction().Result = %v, want nil", started.Result)
		}
		if started.EndedAt != nil {
			t.Fatalf("StartAction().EndedAt = %v, want nil", started.EndedAt)
		}
		if started.Error != "" {
			t.Fatalf("StartAction().Error = %q, want empty", started.Error)
		}
	})

	t.Run("team scope keeps its team id", func(t *testing.T) {
		action := newAction(org, "act_team", "con_team", "app_1", "github_create_issue", "idem-"+randomHex(8), "user_1", now)
		action.Scope, action.TeamID = "team", "team_1"

		started, ok, err := store.StartAction(ctx, action)
		if err != nil || !ok {
			t.Fatalf("StartAction() = %+v, %v, %v, want ok=true", started, ok, err)
		}
		assertActionRoundTrip(t, started, action)
	})
}

func TestStartActionRepeat(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	org := "org_repeat"

	t.Run("repeat while running returns the first action unchanged", func(t *testing.T) {
		key := "idem-" + randomHex(8)
		first := newAction(org, "act_rep_1", "con_1", "app_1", "github_create_issue", key, "user_1", now)
		firstStarted, ok, err := store.StartAction(ctx, first)
		if err != nil || !ok {
			t.Fatalf("StartAction() first = %+v, %v, %v, want ok=true", firstStarted, ok, err)
		}

		// Same connection, action and idempotency key, but a different id and
		// caller: the repeat must still resolve to the first action.
		repeat := newAction(org, "act_rep_1_dup", "con_1", "app_1", "github_create_issue", key, "user_2", now.Add(time.Minute))
		repeated, ok, err := store.StartAction(ctx, repeat)
		if err != nil {
			t.Fatalf("StartAction() repeat error = %v", err)
		}
		if ok {
			t.Fatalf("StartAction() repeat ok = true, want false")
		}
		if repeated.ID != firstStarted.ID {
			t.Fatalf("StartAction() repeat ID = %s, want %s", repeated.ID, firstStarted.ID)
		}
		if repeated.RequestedBy != first.RequestedBy {
			t.Fatalf("StartAction() repeat RequestedBy = %s, want the first caller's %s (not overwritten)", repeated.RequestedBy, first.RequestedBy)
		}
	})

	t.Run("repeat after finishing returns the finished action with its result", func(t *testing.T) {
		key := "idem-" + randomHex(8)
		action := newAction(org, "act_rep_2", "con_2", "app_1", "github_create_issue", key, "user_1", now)
		started, ok, err := store.StartAction(ctx, action)
		if err != nil || !ok {
			t.Fatalf("StartAction() = %+v, %v, %v, want ok=true", started, ok, err)
		}

		endedAt := now.Add(time.Minute)
		result := json.RawMessage(`{"issue_number":42}`)
		finished := started
		finished.Status = domain.ActionCompleted
		finished.Result = result
		finished.Resource = &domain.ActionResource{Type: "issue", ID: "42", Name: "Bug", URL: "https://example.com/42"}
		finished.EndedAt = &endedAt
		if err := store.FinishAction(ctx, finished, testEvent("integrations.action.completed.v1", endedAt)); err != nil {
			t.Fatalf("FinishAction() error = %v", err)
		}

		repeat := newAction(org, "act_rep_2_dup", "con_2", "app_1", "github_create_issue", key, "user_1", now.Add(2*time.Minute))
		repeated, ok, err := store.StartAction(ctx, repeat)
		if err != nil {
			t.Fatalf("StartAction() repeat after finish error = %v", err)
		}
		if ok {
			t.Fatalf("StartAction() repeat after finish ok = true, want false")
		}
		if repeated.ID != started.ID || repeated.Status != domain.ActionCompleted {
			t.Fatalf("StartAction() repeat after finish = %+v, want the finished action", repeated)
		}
		if !jsonEqual(t, repeated.Result, result) {
			t.Fatalf("StartAction() repeat after finish Result = %s, want %s", repeated.Result, result)
		}
		if repeated.Resource == nil || *repeated.Resource != *finished.Resource {
			t.Fatalf("StartAction() repeat after finish Resource = %+v, want %+v", repeated.Resource, finished.Resource)
		}
		if repeated.EndedAt == nil || !repeated.EndedAt.Equal(endedAt) {
			t.Fatalf("StartAction() repeat after finish EndedAt = %v, want %v", repeated.EndedAt, endedAt)
		}
	})
}

func TestStartActionScopedByConnectionAndName(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	org := "org_scoped"
	key := "idem-" + randomHex(8)

	base := newAction(org, "act_scope_base", "con_1", "app_1", "github_create_issue", key, "user_1", now)
	baseStarted, ok, err := store.StartAction(ctx, base)
	if err != nil || !ok {
		t.Fatalf("StartAction() base = %+v, %v, %v, want ok=true", baseStarted, ok, err)
	}

	t.Run("different action name with the same key is new", func(t *testing.T) {
		other := newAction(org, "act_scope_diff_action", "con_1", "app_1", "github_close_issue", key, "user_1", now)
		started, ok, err := store.StartAction(ctx, other)
		if err != nil {
			t.Fatalf("StartAction() error = %v", err)
		}
		if !ok || started.ID != other.ID {
			t.Fatalf("StartAction() with a different action name = %+v, %v, want a new action %s", started, ok, other.ID)
		}
	})

	t.Run("different connection with the same key is new", func(t *testing.T) {
		other := newAction(org, "act_scope_diff_conn", "con_2", "app_1", "github_create_issue", key, "user_1", now)
		started, ok, err := store.StartAction(ctx, other)
		if err != nil {
			t.Fatalf("StartAction() error = %v", err)
		}
		if !ok || started.ID != other.ID {
			t.Fatalf("StartAction() with a different connection = %+v, %v, want a new action %s", started, ok, other.ID)
		}
	})
}

func TestFinishAction(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	org := "org_finish"

	action := newAction(org, "act_finish_1", "con_1", "app_1", "github_create_issue", "idem-"+randomHex(8), "user_1", now)
	started, ok, err := store.StartAction(ctx, action)
	if err != nil || !ok {
		t.Fatalf("StartAction() = %+v, %v, %v, want ok=true", started, ok, err)
	}

	endedAt := now.Add(30 * time.Second)
	result := json.RawMessage(`{"ok":true}`)
	resource := &domain.ActionResource{Type: "issue", ID: "7", Name: "Something broke", URL: "https://example.com/7"}
	finished := started
	finished.Status = domain.ActionCompleted
	finished.Result = result
	finished.Resource = resource
	finished.EndedAt = &endedAt
	event := testEvent("integrations.action.completed.v1", endedAt)
	if err := store.FinishAction(ctx, finished, event); err != nil {
		t.Fatalf("FinishAction() error = %v", err)
	}

	var outboxCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM integrations_outbox WHERE id = $1`, event.ID).Scan(&outboxCount); err != nil || outboxCount != 1 {
		t.Fatalf("outbox events for %s = %d, error = %v, want 1", event.ID, outboxCount, err)
	}

	reloaded, err := store.actionByIdempotencyKey(ctx, action.ConnectionID, action.Name, action.IdempotencyKey)
	if err != nil {
		t.Fatalf("actionByIdempotencyKey() error = %v", err)
	}
	if reloaded.Status != domain.ActionCompleted {
		t.Fatalf("Action().Status after finish = %s, want %s", reloaded.Status, domain.ActionCompleted)
	}
	if !jsonEqual(t, reloaded.Result, result) {
		t.Fatalf("Action().Result after finish = %s, want %s", reloaded.Result, result)
	}
	if reloaded.Resource == nil || *reloaded.Resource != *resource {
		t.Fatalf("Action().Resource after finish = %+v, want %+v", reloaded.Resource, resource)
	}
	if reloaded.EndedAt == nil || !reloaded.EndedAt.Equal(endedAt) {
		t.Fatalf("Action().EndedAt after finish = %v, want %v", reloaded.EndedAt, endedAt)
	}

	t.Run("second finish is a conflict and writes no second event", func(t *testing.T) {
		again := finished
		again.Status = domain.ActionFailed
		again.Error = "already done"
		secondEvent := testEvent("integrations.action.completed.v1", endedAt.Add(time.Second))
		if err := store.FinishAction(ctx, again, secondEvent); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("FinishAction() second call error = %v, want ErrConflict", err)
		}
		var againCount int
		if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM integrations_outbox WHERE id = $1`, secondEvent.ID).Scan(&againCount); err != nil || againCount != 0 {
			t.Fatalf("outbox events for the conflicting finish = %d, error = %v, want 0", againCount, err)
		}
		reloadedAgain, err := store.actionByIdempotencyKey(ctx, action.ConnectionID, action.Name, action.IdempotencyKey)
		if err != nil {
			t.Fatalf("actionByIdempotencyKey() error = %v", err)
		}
		if reloadedAgain.Status != domain.ActionCompleted || reloadedAgain.Error != "" {
			t.Fatalf("Action() after a conflicting finish = %+v, want it unchanged", reloadedAgain)
		}
	})
}

func TestDeleteActionsBefore(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	org := "org_delete_actions"
	cutoff := now.Add(time.Hour)

	finish := func(id string, endedAt time.Time) domain.Action {
		t.Helper()
		action := newAction(org, id, "con_1", "app_1", "github_create_issue", "idem-"+randomHex(8), "user_1", now)
		started, ok, err := store.StartAction(ctx, action)
		if err != nil || !ok {
			t.Fatalf("StartAction(%s) = %+v, %v, %v, want ok=true", id, started, ok, err)
		}
		started.Status = domain.ActionCompleted
		started.EndedAt = &endedAt
		if err := store.FinishAction(ctx, started, testEvent("integrations.action.completed.v1", endedAt)); err != nil {
			t.Fatalf("FinishAction(%s) error = %v", id, err)
		}
		return started
	}

	old := finish("act_del_old", cutoff.Add(-time.Hour))
	newer := finish("act_del_new", cutoff.Add(time.Hour))

	running := newAction(org, "act_del_running", "con_1", "app_1", "github_create_issue", "idem-"+randomHex(8), "user_1", now)
	if _, ok, err := store.StartAction(ctx, running); err != nil || !ok {
		t.Fatalf("StartAction(running) error = %v, ok = %v", err, ok)
	}
	deleted, err := store.DeleteActionsBefore(ctx, cutoff)
	if err != nil {
		t.Fatalf("DeleteActionsBefore() error = %v", err)
	}
	if deleted != 1 {
		t.Fatalf("DeleteActionsBefore() = %d, want 1", deleted)
	}

	if _, err := store.actionByIdempotencyKey(ctx, "con_1", "github_create_issue", old.IdempotencyKey); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("old action after DeleteActionsBefore() error = %v, want ErrNotFound", err)
	}
	if got, err := store.actionByIdempotencyKey(ctx, "con_1", "github_create_issue", newer.IdempotencyKey); err != nil || got.ID != newer.ID {
		t.Fatalf("newer action after DeleteActionsBefore() = %+v, %v, want it kept", got, err)
	}
	if got, err := store.actionByIdempotencyKey(ctx, "con_1", "github_create_issue", running.IdempotencyKey); err != nil || got.Status != domain.ActionRunning {
		t.Fatalf("running action after DeleteActionsBefore() = %+v, %v, want it kept and still running", got, err)
	}
}

// TestStartActionConcurrent races two processes inserting the same
// idempotency key at once: exactly one must start the action, and every
// caller must see the same action id.
func TestStartActionConcurrent(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	org := "org_concurrent"
	key := "idem-" + randomHex(8)

	const attempts = 8
	var wg sync.WaitGroup
	results := make([]domain.Action, attempts)
	oks := make([]bool, attempts)
	errs := make([]error, attempts)
	for i := range attempts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			action := newAction(org, fmt.Sprintf("act_conc_%d", i), "con_1", "app_1", "github_create_issue", key, "user_1", now)
			results[i], oks[i], errs[i] = store.StartAction(ctx, action)
		}(i)
	}
	wg.Wait()

	trueCount := 0
	var winnerID string
	for i := range attempts {
		if errs[i] != nil {
			t.Fatalf("StartAction() goroutine %d error = %v", i, errs[i])
		}
		if oks[i] {
			trueCount++
			winnerID = results[i].ID
		}
	}
	if trueCount != 1 {
		t.Fatalf("StartAction() concurrent true count = %d, want 1", trueCount)
	}
	for i := range attempts {
		if results[i].ID != winnerID {
			t.Fatalf("StartAction() goroutine %d ID = %s, want the winner's %s", i, results[i].ID, winnerID)
		}
	}
}
