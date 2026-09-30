package interactions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/policy"
)

// TestASecretIsSharedOnlyForARequestApprovedWithAccess: what the owner may put,
// what the member who asked may open and what the owner may take back, each
// only in their own signed-in session, and only in the states that make sense.
func TestASecretIsSharedOnlyForARequestApprovedWithAccess(t *testing.T) {
	ledger := newSlackLedger(t)
	ctx := context.Background()
	result := ledger.ask("1790000000.000100", raya, maya)
	pending := requestTo(t, result, raya)
	summary := ledger.decide(maya, requestTo(t, result, maya), ChoiceSummary).Request
	access := ledger.decide(raya, pending, ChoiceAccess).Request

	// Access allows a secret: shared by its owner, for the lifetime asked for,
	// and by default for the longest there is.
	share, ttl, err := ledger.service.AuthorizeSecretPut(ctx, ledger.owner(raya), access.ID, 10*time.Minute)
	if err != nil || ttl != 10*time.Minute || share != (SecretShare{
		RequestID: access.ID, OrganizationID: ledger.org, AskID: result.Ask.ID, OwnerUserID: raya, AskerUserID: asker,
	}) {
		t.Fatalf("share = %+v, ttl %s, %v", share, ttl, err)
	}
	if _, ttl, err := ledger.service.AuthorizeSecretPut(ctx, ledger.owner(raya), access.ID, 0); err != nil || ttl != MaxSecretTTL {
		t.Fatalf("a secret with no lifetime asked lives %s, %v; want %s", ttl, err, MaxSecretTTL)
	}
	for _, asked := range []time.Duration{time.Second, 59 * time.Second, MaxSecretTTL + time.Second, -time.Minute} {
		if _, _, err := ledger.service.AuthorizeSecretPut(ctx, ledger.owner(raya), access.ID, asked); !errors.Is(err, ErrInvalid) {
			t.Errorf("a lifetime of %s: %v, want ErrInvalid", asked, err)
		}
	}
	// A summary allows only saying where it lives.
	if _, _, err := ledger.service.AuthorizeSecretPut(ctx, ledger.owner(maya), summary.ID, 0); !errors.Is(err, ErrInvalid) {
		t.Errorf("a secret for a summary: %v, want ErrInvalid", err)
	}
	// Nothing is shared before the owner approves.
	pendingAsk := requestTo(t, ledger.askIn(inAnotherThread("1790000001.000100"), raya), raya)
	if pendingAsk.Status != StatusPending {
		t.Fatalf("pending = %+v", pendingAsk)
	}
	if _, _, err := ledger.service.AuthorizeSecretPut(ctx, ledger.owner(raya), pendingAsk.ID, 0); !errors.Is(err, ErrConflict) {
		t.Errorf("a secret for a request nobody approved: %v, want ErrConflict", err)
	}

	// Who may do what.
	for name, call := range map[string]func() error{
		"the asker shares": func() error {
			_, _, err := ledger.service.AuthorizeSecretPut(ctx, ledger.owner(asker), access.ID, 0)
			return err
		},
		"the owner opens": func() error {
			_, err := ledger.service.AuthorizeSecretView(ctx, ledger.owner(raya), access.ID)
			return err
		},
		"the asker revokes": func() error {
			_, err := ledger.service.AuthorizeSecretRevoke(ctx, ledger.owner(asker), access.ID)
			return err
		},
	} {
		if err := call(); !errors.Is(err, ErrForbidden) {
			t.Errorf("%s: %v, want ErrForbidden", name, err)
		}
	}
	for name, call := range map[string]func(agents.InteractionActor) error{
		"share": func(actor agents.InteractionActor) error {
			_, _, err := ledger.service.AuthorizeSecretPut(ctx, actor, access.ID, 0)
			return err
		},
		"open": func(actor agents.InteractionActor) error {
			_, err := ledger.service.AuthorizeSecretView(ctx, actor, access.ID)
			return err
		},
		"revoke": func(actor agents.InteractionActor) error {
			_, err := ledger.service.AuthorizeSecretRevoke(ctx, actor, access.ID)
			return err
		},
	} {
		// The owner's agent, an agent in a run, is no member in their own session.
		agentActor := ledger.agentOf(raya)
		if name == "open" {
			agentActor = ledger.agentOf(asker)
		}
		if err := call(agentActor); !errors.Is(err, ErrForbidden) {
			t.Errorf("an agent tries to %s: %v, want ErrForbidden", name, err)
		}
		// Somebody who is no party to the request cannot tell it is there.
		if err := call(ledger.owner(maya)); !errors.Is(err, ErrNotFound) {
			t.Errorf("a stranger tries to %s: %v, want ErrNotFound", name, err)
		}
		elsewhere := ledger.owner(raya)
		if name == "open" {
			elsewhere = ledger.owner(asker)
		}
		elsewhere.OrganizationID = "org_other"
		if err := call(elsewhere); !errors.Is(err, ErrNotFound) {
			t.Errorf("a member of another organization tries to %s: %v, want ErrNotFound", name, err)
		}
	}
	if _, _, err := ledger.service.AuthorizeSecretPut(ctx, ledger.owner(raya), "irq_missing", 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing request: %v", err)
	}

	// The member who asked opens it.
	opened, err := ledger.service.AuthorizeSecretView(ctx, ledger.owner(asker), access.ID)
	if err != nil || opened != share {
		t.Fatalf("view share = %+v, %v; want %+v", opened, err, share)
	}
	// Before approval there is nothing to open, however it is asked.
	if _, err := ledger.service.AuthorizeSecretView(ctx, ledger.owner(asker), pendingAsk.ID); !errors.Is(err, ErrSecretGone) {
		t.Errorf("opening a secret of a request nobody approved: %v, want ErrSecretGone", err)
	}
	revoking, err := ledger.service.AuthorizeSecretRevoke(ctx, ledger.owner(raya), pendingAsk.ID)
	if err != nil || revoking.RequestID != pendingAsk.ID {
		t.Errorf("revoking is the owner's whatever the state: %+v, %v", revoking, err)
	}
}

// A secret is not shared for a request that is over: closed, rejected,
// withheld or expired; and it is not opened for one either, while revoking it
// is always the owner's.
func TestNoSecretIsSharedOrOpenedForARequestThatIsOver(t *testing.T) {
	ledger := newSlackLedger(t)
	ctx := context.Background()
	result := ledger.ask("1790000000.000100", raya, maya)
	rejected := ledger.decide(raya, requestTo(t, result, raya), ChoiceDeny).Request
	access := ledger.decide(maya, requestTo(t, result, maya), ChoiceAccess).Request
	// Raya's is rejected, and Maya's approved with access.
	if _, _, err := ledger.service.AuthorizeSecretPut(ctx, ledger.owner(raya), rejected.ID, 0); !errors.Is(err, ErrConflict) {
		t.Errorf("a secret for a rejected request: %v, want ErrConflict", err)
	}
	if _, err := ledger.service.AuthorizeSecretView(ctx, ledger.owner(asker), rejected.ID); !errors.Is(err, ErrSecretGone) {
		t.Errorf("opening one: %v, want ErrSecretGone", err)
	}
	if _, err := ledger.service.AuthorizeSecretRevoke(ctx, ledger.owner(raya), rejected.ID); err != nil {
		t.Errorf("revoking one: %v", err)
	}
	// Answered, the request still carries a secret its owner shared for it.
	answered, err := ledger.answer(access)
	if err != nil || answered.Status != StatusCompleted {
		t.Fatal(answered, err)
	}
	if _, _, err := ledger.service.AuthorizeSecretPut(ctx, ledger.owner(maya), access.ID, 0); err != nil {
		t.Errorf("a secret for an answered request: %v", err)
	}
	if _, err := ledger.service.AuthorizeSecretView(ctx, ledger.owner(asker), access.ID); err != nil {
		t.Errorf("opening a secret of an answered request: %v", err)
	}
	// Past its expiry a request takes no more secrets, but its owner can still
	// take one back.
	ledger.clock.Advance(defaultExpiry + time.Minute)
	if _, _, err := ledger.service.AuthorizeSecretPut(ctx, ledger.owner(maya), access.ID, 0); !errors.Is(err, ErrExpired) {
		t.Errorf("a secret for an expired request: %v, want ErrExpired", err)
	}
	if _, err := ledger.service.AuthorizeSecretRevoke(ctx, ledger.owner(maya), access.ID); err != nil {
		t.Errorf("revoking after the expiry: %v", err)
	}
}

// A secret lives no longer than the organization lets consent last.
func TestASecretLivesNoLongerThanTheOrganizationLetsConsentLast(t *testing.T) {
	ledger := newSlackLedger(t)
	request := ledger.decide(raya, requestTo(t, ledger.ask("1790000000.000100", raya), raya), ChoiceAccess).Request
	ledger.service.policy = fakeWorkspacePolicy{settings: policy.Settings{ExpireAfter: 20 * time.Minute}}
	for asked, want := range map[time.Duration]time.Duration{0: 20 * time.Minute, 5 * time.Minute: 5 * time.Minute, time.Hour: 20 * time.Minute} {
		if _, ttl, err := ledger.service.AuthorizeSecretPut(context.Background(), ledger.owner(raya), request.ID, asked); err != nil || ttl != want {
			t.Errorf("asked %s: lives %s, %v; want %s", asked, ttl, err, want)
		}
	}
	ledger.service.policy = fakeWorkspacePolicy{err: errors.New("policy storage unavailable")}
	if _, _, err := ledger.service.AuthorizeSecretPut(context.Background(), ledger.owner(raya), request.ID, 0); err == nil {
		t.Error("a secret was authorized while the policy could not be read")
	}
}

// A secret's value cannot leak through anything that prints or logs it.
func TestASecretValueIsNotPrintedOrLogged(t *testing.T) {
	value := SecretValue{Value: "sk-live-DO-NOT-PRINT", ExpiresAt: time.Now()}
	var logs strings.Builder
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	logger.Info("opened", "secret", value, "value", value.Value[:0])
	for name, printed := range map[string]string{
		"%v": fmt.Sprintf("%v", value), "%+v": fmt.Sprintf("%+v", value), "%#v": fmt.Sprintf("%#v", value), "%s": fmt.Sprintf("%s", value),
		"a pointer": fmt.Sprintf("%v", &value), "the log": logs.String(),
	} {
		if strings.Contains(printed, "DO-NOT-PRINT") {
			t.Errorf("%s printed the secret: %s", name, printed)
		}
	}
}

// TestOpeningASecretIsRecordedWhereItWasOpened: the store of secrets records
// a view in its own transaction, which stamps the request and sends the event
// that says who opened what, and never the value; a view by anyone but the
// member who asked is refused.
func TestOpeningASecretIsRecordedWhereItWasOpened(t *testing.T) {
	ledger := newSlackLedger(t)
	ctx := context.Background()
	request := ledger.decide(raya, requestTo(t, ledger.ask("1790000000.000100", raya), raya), ChoiceAccess).Request
	viewedAt := ledger.clock.Now().Add(5 * time.Minute)

	record := func(viewer string, at time.Time, commit bool) error {
		tx, err := ledger.database.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := RecordSecretViewed(ctx, tx, request.ID, viewer, at); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if !commit {
			return tx.Rollback(ctx)
		}
		return tx.Commit(ctx)
	}
	if err := record(raya, viewedAt, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the owner opened it: %v, want ErrNotFound", err)
	}
	if err := record("user_other", viewedAt, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a stranger opened it: %v, want ErrNotFound", err)
	}
	if got := ledger.read(request.ID); got.SecretViewedAt != nil {
		t.Fatalf("a refused view was recorded: %+v", got.SecretViewedAt)
	}
	// A view that is rolled back, as when the secret could not be deleted, was
	// not a view.
	if err := record(asker, viewedAt, false); err != nil {
		t.Fatal(err)
	}
	if got := ledger.read(request.ID); got.SecretViewedAt != nil {
		t.Fatalf("a rolled back view was recorded: %+v", got.SecretViewedAt)
	}
	if got := ledger.count(`SELECT count(*) FROM agents_primary.agents_outbox WHERE detail_type=$1 AND detail->>'organization_id'=$2`,
		agentsv1.InteractionSecretViewedDetailType, ledger.org); got != 0 {
		t.Fatalf("%d viewed events after nothing was viewed", got)
	}

	if err := record(asker, viewedAt, true); err != nil {
		t.Fatal(err)
	}
	got := ledger.read(request.ID)
	if got.SecretViewedAt == nil || !got.SecretViewedAt.Equal(viewedAt) || got.Revision != request.Revision {
		t.Fatalf("request = %+v; want it stamped with when, its revision unchanged", got)
	}
	var event string
	if err := ledger.database.Pool.QueryRow(ctx, `SELECT detail::text FROM agents_primary.agents_outbox WHERE detail_type=$1 AND detail->>'organization_id'=$2`,
		agentsv1.InteractionSecretViewedDetailType, ledger.org).Scan(&event); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"request_id": "` + request.ID + `"`, `"ask_id": "` + request.AskID + `"`, `"owner_user_id": "` + raya + `"`, `"asker_user_id": "` + asker + `"`, `"viewed_at": `} {
		if !strings.Contains(event, want) {
			t.Errorf("the viewed event is missing %s: %s", want, event)
		}
	}
	// A second recording keeps the first time.
	if err := record(asker, viewedAt.Add(time.Hour), true); err != nil {
		t.Fatal(err)
	}
	if again := ledger.read(request.ID); !again.SecretViewedAt.Equal(viewedAt) {
		t.Fatalf("a second view moved the time to %s", again.SecretViewedAt)
	}
}
