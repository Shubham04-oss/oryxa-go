package secretshares

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// These tests need the shared local PostgreSQL:
//
//	AGENTS_PRIMARY_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' go test ./agents/primary/internal/secretshares
//
// They seal with a fake KMS that is as strict about the encryption context as
// KMS is, and move a clock by hand.

// testOrgPrefix marks every row these tests write, so a run that died halfway
// is cleaned up by the next.
const testOrgPrefix = "org_secretshares_test_"

const (
	ownerID = "user_owner"
	askerID = "user_asker"
	// distinctive is a value no other row or log line would hold by accident.
	distinctive = "sk_live_51NqWrEsecretshares_4eC39HqLyjWDarjtT1zdp7dc"
)

type harness struct {
	t        *testing.T
	ctx      context.Context
	database *postgreskit.Database
	ledger   *interactions.Store
	store    *Store
	kms      *fakeKMS
	sealer   secrets.Envelope
	clock    *fakeClock
	logs     *syncBuffer
	org      string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{
		URL: databaseURL, ApplicationName: "oryxa-secret-shares-test", Schema: "public",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	// The outbox and the requests, which recording a view writes to.
	if err := runstore.New(database).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	ledger := interactions.NewStore(database)
	if err := ledger.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	fake := newFakeKMS()
	sealer := secrets.Envelope{Keys: fake, KeyID: "alias/test-agents-secrets", Service: SealService}
	logs := &syncBuffer{}
	store, err := New(database, sealer, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err != nil {
		t.Fatal(err)
	}
	// Migrating twice is what a restart does.
	for range 2 {
		if err := store.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	clock := newFakeClock()
	store.now = clock.Now
	h := &harness{
		t: t, ctx: ctx, database: database, ledger: ledger, store: store, kms: fake, sealer: sealer, clock: clock, logs: logs,
		org: fmt.Sprintf("%s%d", testOrgPrefix, time.Now().UnixNano()),
	}
	h.clean()
	t.Cleanup(h.clean)
	return h
}

func (h *harness) clean() {
	for _, statement := range []string{
		`DELETE FROM agents_primary.secret_shares WHERE organization_id LIKE $1`,
		`DELETE FROM agents_primary.agents_outbox WHERE detail->>'organization_id' LIKE $1`,
		`DELETE FROM agents_primary.agent_interactions WHERE organization_id LIKE $1`,
	} {
		_, _ = h.database.Pool.Exec(h.ctx, statement, testOrgPrefix+"%")
	}
}

// request records a request from the asker to the owner, which is what a view
// is recorded on, and returns the share that names it.
func (h *harness) request(name string) interactions.SecretShare {
	h.t.Helper()
	now := h.clock.Now()
	id := fmt.Sprintf("irq_%s_%s", strings.TrimPrefix(h.org, testOrgPrefix), name)
	request := agents.Interaction{
		ID: id, OrganizationID: h.org,
		Requester: agents.InteractionParty{UserID: askerID, AgentID: "agt_asker", AgentName: "Crew"},
		Target:    agents.InteractionParty{UserID: ownerID, AgentID: "agt_owner", AgentName: "Carl", UserEmail: "owner@example.com"},
		Purpose:   "Credentials", Task: "Where is the staging key?",
		Context:        []agents.InteractionContextItem{{Label: "service", Value: "agents"}},
		RequestedTools: []string{"knowledge_search"}, RequestedPermissions: []string{"read:architecture"},
		Status: interactions.StatusPending, Revision: 1, ExpiresAt: now.Add(24 * time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	if _, err := h.ledger.Create(h.ctx, request, "key-"+id); err != nil {
		h.t.Fatal(err)
	}
	return interactions.SecretShare{RequestID: id, OrganizationID: h.org, OwnerUserID: ownerID, AskerUserID: askerID}
}

// share names a secret with no request behind it, for what the store does
// without one: keeping, revoking, sweeping and forgetting.
func (h *harness) share(name, owner, asker string) interactions.SecretShare {
	return interactions.SecretShare{RequestID: h.org + "_" + name, OrganizationID: h.org, OwnerUserID: owner, AskerUserID: asker}
}

func (h *harness) put(share interactions.SecretShare, value string, ttl time.Duration) interactions.SecretMeta {
	h.t.Helper()
	meta, err := h.store.Put(h.ctx, share, value, ttl)
	if err != nil {
		h.t.Fatalf("Put: %v", err)
	}
	return meta
}

func (h *harness) count(query string, args ...any) int {
	h.t.Helper()
	var n int
	if err := h.database.Pool.QueryRow(h.ctx, query, args...).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func (h *harness) rows(requestID string) int {
	return h.count(`SELECT count(*) FROM agents_primary.secret_shares WHERE request_id=$1`, requestID)
}

func (h *harness) viewedEvents(requestID string) int {
	return h.count(`SELECT count(*) FROM agents_primary.agents_outbox WHERE detail_type='agents.interaction.secret.viewed.v1' AND detail->>'request_id'=$1`, requestID)
}

func (h *harness) viewedAt(requestID string) *time.Time {
	h.t.Helper()
	var at *time.Time
	if err := h.database.Pool.QueryRow(h.ctx, `SELECT secret_viewed_at FROM agents_primary.agent_interactions WHERE id=$1`, requestID).Scan(&at); err != nil {
		h.t.Fatal(err)
	}
	return at
}

// assertNowhere fails if value, or its base64 or hex, is anywhere in the agents
// schema, as any table's rows read as text; or in what the store logged.
func (h *harness) assertNowhere(value string) {
	h.t.Helper()
	needles := []string{
		value,
		base64.StdEncoding.EncodeToString([]byte(value)),
		base64.RawURLEncoding.EncodeToString([]byte(value)),
		hex.EncodeToString([]byte(value)),
	}
	rows, err := h.database.Pool.Query(h.ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema='agents_primary' AND table_type='BASE TABLE'`)
	if err != nil {
		h.t.Fatal(err)
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		h.t.Fatal(err)
	}
	if len(tables) < 3 {
		h.t.Fatalf("only %d tables to look in: %v", len(tables), tables)
	}
	for _, table := range tables {
		for _, needle := range needles {
			found := h.count(`SELECT count(*) FROM `+pgx.Identifier{"agents_primary", table}.Sanitize()+` t WHERE strpos(t::text, $1) > 0`, needle)
			if found != 0 {
				h.t.Errorf("the value (%.6s…) is in %d rows of agents_primary.%s", needle, found, table)
			}
		}
	}
	for _, needle := range needles {
		if strings.Contains(h.logs.String(), needle) {
			h.t.Errorf("the value (%.6s…) was logged", needle)
		}
	}
}

// TestASecretIsOpenedOnceAndTheViewIsRecorded: the member who asked opens it,
// and in the same commit it is deleted, the request is stamped with when and
// the event that says who opened what is written; a second view finds nothing
// and records nothing more.
func TestASecretIsOpenedOnceAndTheViewIsRecorded(t *testing.T) {
	h := newHarness(t)
	share := h.request("once")

	meta := h.put(share, distinctive, 20*time.Minute)
	expiresAt := h.clock.Now().Add(20 * time.Minute)
	if !meta.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("expires %s, want %s", meta.ExpiresAt, expiresAt)
	}
	if h.rows(share.RequestID) != 1 || h.viewedAt(share.RequestID) != nil {
		t.Fatal("sharing a secret is not opening it")
	}

	h.clock.Advance(5 * time.Minute)
	opened, err := h.store.View(h.ctx, share)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Value != distinctive || !opened.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("opened %v expiring %s, want the value expiring %s", opened, opened.ExpiresAt, expiresAt)
	}
	if h.rows(share.RequestID) != 0 {
		t.Fatal("an opened secret is still kept")
	}
	if at := h.viewedAt(share.RequestID); at == nil || !at.Equal(h.clock.Now()) {
		t.Fatalf("the request was stamped %v, want %s", at, h.clock.Now())
	}
	if h.viewedEvents(share.RequestID) != 1 {
		t.Fatalf("%d viewed events, want 1", h.viewedEvents(share.RequestID))
	}
	var event string
	if err := h.database.Pool.QueryRow(h.ctx, `SELECT detail::text FROM agents_primary.agents_outbox WHERE detail_type='agents.interaction.secret.viewed.v1' AND detail->>'request_id'=$1`, share.RequestID).Scan(&event); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(event, `"asker_user_id": "`+askerID+`"`) || !strings.Contains(event, `"owner_user_id": "`+ownerID+`"`) {
		t.Errorf("the event does not say who: %s", event)
	}

	stampedAt := *h.viewedAt(share.RequestID)
	h.clock.Advance(time.Minute)
	if _, err := h.store.View(h.ctx, share); !errors.Is(err, interactions.ErrSecretGone) {
		t.Fatalf("a second view: %v, want ErrSecretGone", err)
	}
	if got := h.viewedAt(share.RequestID); got == nil || !got.Equal(stampedAt) || h.viewedEvents(share.RequestID) != 1 {
		t.Fatalf("a refused view changed the record: %v, %d events", got, h.viewedEvents(share.RequestID))
	}
	h.assertNowhere(distinctive)
}

// TestOnlyCiphertextIsKept: at rest there is the ciphertext, the wrapped data
// key and who and when, bound to the agents service and the request. The value
// is in no table of the schema, in any encoding, before it is opened or after.
func TestOnlyCiphertextIsKept(t *testing.T) {
	h := newHarness(t)
	share := h.request("sealed")
	h.put(share, distinctive, time.Hour)

	contexts := h.kms.generatedContexts()
	want := map[string]string{"service": "oryxa-agents", "resource_type": "secret_share", "resource_id": share.RequestID}
	if len(contexts) != 1 || len(contexts[0]) != len(want) {
		t.Fatalf("KMS was asked for %d data keys under %v", len(contexts), contexts)
	}
	for key, value := range want {
		if contexts[0][key] != value {
			t.Errorf("encryption context %s = %q, want %q", key, contexts[0][key], value)
		}
	}

	h.assertNowhere(distinctive)

	var encoded []byte
	if err := h.database.Pool.QueryRow(h.ctx, `SELECT sealed FROM agents_primary.secret_shares WHERE request_id=$1`, share.RequestID).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var sealed secrets.SealedData
	if err := json.Unmarshal(encoded, &sealed); err != nil {
		t.Fatal(err)
	}
	if len(sealed.Ciphertext) < len(distinctive) || len(sealed.Nonce) == 0 || len(sealed.EncryptedDataKey) == 0 {
		t.Fatalf("what is kept is not a sealed value: %d ciphertext bytes, %d nonce, %d wrapped key", len(sealed.Ciphertext), len(sealed.Nonce), len(sealed.EncryptedDataKey))
	}
	// It is the value that is sealed, not a stand-in, and it opens for its own
	// request alone.
	plaintext, err := h.sealer.Open(h.ctx, bindingOf(share.RequestID), sealed)
	if err != nil || string(plaintext) != distinctive {
		t.Fatalf("the sealed value opens to %q, %v", plaintext, err)
	}
	if _, err := h.sealer.Open(h.ctx, bindingOf(share.RequestID+"_other"), sealed); err == nil {
		t.Error("the sealed value opened for another request")
	}

	if _, err := h.store.View(h.ctx, share); err != nil {
		t.Fatal(err)
	}
	h.assertNowhere(distinctive)
}

// TestASecretExpiresAndIsDeletedByWhoeverMeetsIt: a second before its time it
// opens; at its time it does not, and is deleted without KMS being asked; a
// revoke that comes late says it is gone; and the sweeper deletes what nobody
// met.
func TestASecretExpiresAndIsDeletedByWhoeverMeetsIt(t *testing.T) {
	h := newHarness(t)
	ttl := 10 * time.Minute

	before := h.request("before")
	h.put(before, "value-before", ttl)
	h.clock.Advance(ttl - time.Second)
	if opened, err := h.store.View(h.ctx, before); err != nil || opened.Value != "value-before" {
		t.Fatalf("a secret a second before its time: %v, %v", opened, err)
	}

	at := h.request("at")
	h.put(at, "value-at", ttl)
	opensBefore := h.kms.openCount()
	h.clock.Advance(ttl)
	if _, err := h.store.View(h.ctx, at); !errors.Is(err, interactions.ErrSecretGone) {
		t.Fatalf("a secret at its time: %v, want ErrSecretGone", err)
	}
	if h.rows(at.RequestID) != 0 {
		t.Error("an expired secret was left where it was met")
	}
	if h.viewedAt(at.RequestID) != nil || h.viewedEvents(at.RequestID) != 0 {
		t.Error("a view of an expired secret was recorded")
	}
	if h.kms.openCount() != opensBefore {
		t.Error("KMS was asked to open a secret that had expired")
	}

	late := h.request("late")
	h.put(late, "value-late", ttl)
	h.clock.Advance(ttl + time.Minute)
	if err := h.store.Revoke(h.ctx, late); !errors.Is(err, interactions.ErrSecretGone) {
		t.Fatalf("a late revoke: %v, want ErrSecretGone", err)
	}
	if h.rows(late.RequestID) != 0 {
		t.Error("a late revoke left the expired secret")
	}

	unmet := h.request("unmet")
	h.put(unmet, "value-unmet", ttl)
	h.clock.Advance(ttl)
	live := h.request("live")
	h.put(live, "value-live", ttl)
	swept, err := h.store.Sweep(h.ctx, 100)
	if err != nil || swept < 1 {
		t.Fatalf("swept %d, %v", swept, err)
	}
	if h.rows(unmet.RequestID) != 0 {
		t.Error("the sweeper left a secret that expired")
	}
	if h.rows(live.RequestID) != 1 {
		t.Error("the sweeper deleted a secret that is still good")
	}
	if opened, err := h.store.View(h.ctx, live); err != nil || opened.Value != "value-live" {
		t.Fatalf("the live secret: %v, %v", opened, err)
	}
}

// TestRevokeTakesTheSecretBack: whether or not anyone opened it, and only for
// the member who shared it.
func TestRevokeTakesTheSecretBack(t *testing.T) {
	h := newHarness(t)
	share := h.request("revoked")
	h.put(share, distinctive, time.Hour)

	stranger := share
	stranger.OwnerUserID = "user_stranger"
	if err := h.store.Revoke(h.ctx, stranger); !errors.Is(err, interactions.ErrSecretGone) {
		t.Fatalf("a revoke by someone who did not share it: %v, want ErrSecretGone", err)
	}
	otherOrganization := share
	otherOrganization.OrganizationID = h.org + "_elsewhere"
	if err := h.store.Revoke(h.ctx, otherOrganization); !errors.Is(err, interactions.ErrSecretGone) {
		t.Fatalf("a revoke from another organization: %v, want ErrSecretGone", err)
	}
	if h.rows(share.RequestID) != 1 {
		t.Fatal("a revoke that was not its owner's took the secret")
	}

	if err := h.store.Revoke(h.ctx, share); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := h.store.View(h.ctx, share); !errors.Is(err, interactions.ErrSecretGone) {
		t.Fatalf("a view after a revoke: %v, want ErrSecretGone", err)
	}
	if err := h.store.Revoke(h.ctx, share); !errors.Is(err, interactions.ErrSecretGone) {
		t.Fatalf("a second revoke: %v, want ErrSecretGone", err)
	}
	if h.viewedAt(share.RequestID) != nil || h.viewedEvents(share.RequestID) != 0 {
		t.Error("a revoked secret was recorded as viewed")
	}

	// One that was opened cannot be revoked: it is gone already.
	opened := h.request("opened-then-revoked")
	h.put(opened, distinctive, time.Hour)
	if _, err := h.store.View(h.ctx, opened); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Revoke(h.ctx, opened); !errors.Is(err, interactions.ErrSecretGone) {
		t.Fatalf("a revoke after a view: %v, want ErrSecretGone", err)
	}
}

// TestSharingAgainReplacesTheSecret: one secret for a request, the last one
// shared, with its own lifetime.
func TestSharingAgainReplacesTheSecret(t *testing.T) {
	h := newHarness(t)
	share := h.request("replaced")
	h.put(share, "the first value", 30*time.Minute)
	h.clock.Advance(time.Minute)
	second := h.put(share, "the second value", 5*time.Minute)

	if got := h.rows(share.RequestID); got != 1 {
		t.Fatalf("%d secrets for one request", got)
	}
	opened, err := h.store.View(h.ctx, share)
	if err != nil || opened.Value != "the second value" || !opened.ExpiresAt.Equal(second.ExpiresAt) {
		t.Fatalf("opened %v, %v; want the second value expiring %s", opened, err, second.ExpiresAt)
	}
	if _, err := h.store.View(h.ctx, share); !errors.Is(err, interactions.ErrSecretGone) {
		t.Fatalf("the replaced value was still there: %v", err)
	}

	// After it was opened, its owner can share another.
	h.put(share, "a third value", time.Hour)
	if opened, err := h.store.View(h.ctx, share); err != nil || opened.Value != "a third value" {
		t.Fatalf("opened %v, %v", opened, err)
	}
	if h.viewedEvents(share.RequestID) != 2 {
		t.Errorf("%d viewed events for two views", h.viewedEvents(share.RequestID))
	}
}

// TestAViewByAnotherMemberOrOrganizationFindsNothing: the row is kept with who
// may open it, and a view that names anyone else takes nothing.
func TestAViewByAnotherMemberOrOrganizationFindsNothing(t *testing.T) {
	h := newHarness(t)
	share := h.request("wrong-viewer")
	h.put(share, distinctive, time.Hour)

	owner := share
	owner.AskerUserID = ownerID
	other := share
	other.AskerUserID = "user_other"
	elsewhere := share
	elsewhere.OrganizationID = h.org + "_elsewhere"
	for name, wrong := range map[string]interactions.SecretShare{"the owner": owner, "another member": other, "another organization": elsewhere} {
		if _, err := h.store.View(h.ctx, wrong); !errors.Is(err, interactions.ErrSecretGone) {
			t.Errorf("a view by %s: %v, want ErrSecretGone", name, err)
		}
	}
	if h.rows(share.RequestID) != 1 || h.kms.openCount() != 0 {
		t.Fatalf("refused views took the secret or asked KMS: %d rows, %d opens", h.rows(share.RequestID), h.kms.openCount())
	}
	if opened, err := h.store.View(h.ctx, share); err != nil || opened.Value != distinctive {
		t.Fatalf("the member who asked: %v, %v", opened, err)
	}
}

// TestOfTwoViewsAtOnceOneWins: however many members' calls arrive together,
// one gets the value, the rest are told it is gone without KMS being asked to
// open it, and one view is recorded.
func TestOfTwoViewsAtOnceOneWins(t *testing.T) {
	h := newHarness(t)
	share := h.request("race")
	h.put(share, distinctive, time.Hour)
	// The winner holds the secret for a while, opening it, so the others reach
	// the row while it does.
	h.kms.slowDecrypts(40 * time.Millisecond)

	const callers = 12
	var (
		wg      sync.WaitGroup
		start   = make(chan struct{})
		mu      sync.Mutex
		winners []string
		gone    int
		other   []error
	)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			opened, err := h.store.View(h.ctx, share)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				winners = append(winners, opened.Value)
			case errors.Is(err, interactions.ErrSecretGone):
				gone++
			default:
				other = append(other, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(other) != 0 {
		t.Fatalf("views failed: %v", other)
	}
	if len(winners) != 1 || winners[0] != distinctive || gone != callers-1 {
		t.Fatalf("%d views won with %q and %d were told it is gone, of %d", len(winners), winners, gone, callers)
	}
	if h.kms.openCount() != 1 {
		t.Errorf("KMS was asked to open the secret %d times", h.kms.openCount())
	}
	if h.viewedEvents(share.RequestID) != 1 || h.rows(share.RequestID) != 0 {
		t.Errorf("%d viewed events, %d rows", h.viewedEvents(share.RequestID), h.rows(share.RequestID))
	}
}

// TestAViewAndARevokeAtOnceCannotBothTakeTheSecret: whoever gets the row takes
// it, the other is told it is gone, and a view that lost left no record.
func TestAViewAndARevokeAtOnceCannotBothTakeTheSecret(t *testing.T) {
	h := newHarness(t)
	for round := range 25 {
		share := h.request(fmt.Sprintf("view-or-revoke-%d", round))
		h.put(share, distinctive, time.Hour)
		var (
			wg                   sync.WaitGroup
			start                = make(chan struct{})
			viewError, revokeErr error
		)
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, viewError = h.store.View(h.ctx, share)
		}()
		go func() {
			defer wg.Done()
			<-start
			revokeErr = h.store.Revoke(h.ctx, share)
		}()
		close(start)
		wg.Wait()

		viewed, revoked := viewError == nil, revokeErr == nil
		if viewed == revoked {
			t.Fatalf("round %d: view %v, revoke %v: exactly one takes the secret", round, viewError, revokeErr)
		}
		if viewed && !errors.Is(revokeErr, interactions.ErrSecretGone) || revoked && !errors.Is(viewError, interactions.ErrSecretGone) {
			t.Fatalf("round %d: the one that lost was told %v, %v", round, viewError, revokeErr)
		}
		wantEvents := 0
		if viewed {
			wantEvents = 1
		}
		if h.viewedEvents(share.RequestID) != wantEvents || (h.viewedAt(share.RequestID) != nil) != viewed || h.rows(share.RequestID) != 0 {
			t.Fatalf("round %d: viewed=%v but %d events, stamped %v, %d rows", round, viewed, h.viewedEvents(share.RequestID), h.viewedAt(share.RequestID), h.rows(share.RequestID))
		}
	}
}

// TestAViewThatFailsLeavesTheSecretToBeOpenedAgain: a secret is never used up
// by a view that did not deliver it, and never delivered by one that was not
// recorded.
func TestAViewThatFailsLeavesTheSecretToBeOpenedAgain(t *testing.T) {
	h := newHarness(t)
	share := h.request("failing")
	h.put(share, distinctive, time.Hour)

	intact := func(when string) {
		t.Helper()
		if h.rows(share.RequestID) != 1 || h.viewedAt(share.RequestID) != nil || h.viewedEvents(share.RequestID) != 0 {
			t.Fatalf("%s: %d rows, stamped %v, %d events", when, h.rows(share.RequestID), h.viewedAt(share.RequestID), h.viewedEvents(share.RequestID))
		}
	}

	// The view cannot be recorded.
	h.store.recordViewed = func(context.Context, pgx.Tx, string, string, time.Time) error {
		return errors.New("the record is refused")
	}
	if opened, err := h.store.View(h.ctx, share); err == nil || errors.Is(err, interactions.ErrSecretGone) || opened.Value != "" {
		t.Fatalf("a view that could not be recorded: %v, %v", opened, err)
	}
	intact("after a record that failed")
	h.store.recordViewed = interactions.RecordSecretViewed

	// KMS is away.
	h.kms.failDecrypts(errors.New("KMS is unavailable"))
	if opened, err := h.store.View(h.ctx, share); err == nil || errors.Is(err, interactions.ErrSecretGone) || opened.Value != "" {
		t.Fatalf("a view while KMS is away: %v, %v", opened, err)
	}
	intact("after KMS failed")
	h.kms.failDecrypts(nil)

	// Nobody but the member who asked is on the request: the record refuses,
	// with the ledger's own answer, and the secret stays.
	disagreeing := share
	disagreeing.AskerUserID = "user_the_ledger_never_named"
	h.put(disagreeing, "value for a share the request does not agree with", time.Hour)
	if _, err := h.store.View(h.ctx, disagreeing); !errors.Is(err, interactions.ErrNotFound) {
		t.Fatalf("a view the request does not agree with: %v, want ErrNotFound", err)
	}
	if h.rows(share.RequestID) != 1 || h.viewedEvents(share.RequestID) != 0 {
		t.Fatal("a view the request did not agree with took the secret")
	}
	h.put(share, distinctive, time.Hour)

	opened, err := h.store.View(h.ctx, share)
	if err != nil || opened.Value != distinctive {
		t.Fatalf("the view after them: %v, %v", opened, err)
	}
	if h.viewedEvents(share.RequestID) != 1 {
		t.Fatalf("%d viewed events for one view", h.viewedEvents(share.RequestID))
	}
}

// TestASecretMovedToAnotherRequestDoesNotOpen: someone who can write to the
// table cannot make one request's secret another's by copying its ciphertext.
func TestASecretMovedToAnotherRequestDoesNotOpen(t *testing.T) {
	h := newHarness(t)
	first, second := h.request("first"), h.request("second")
	h.put(first, "the first value", time.Hour)
	h.put(second, "the second value", time.Hour)
	if _, err := h.database.Pool.Exec(h.ctx, `UPDATE agents_primary.secret_shares
		SET sealed=(SELECT sealed FROM agents_primary.secret_shares WHERE request_id=$1) WHERE request_id=$2`,
		first.RequestID, second.RequestID); err != nil {
		t.Fatal(err)
	}

	opened, err := h.store.View(h.ctx, second)
	if err == nil || errors.Is(err, interactions.ErrSecretGone) || opened.Value != "" {
		t.Fatalf("the first request's ciphertext opened for the second: %v, %v", opened, err)
	}
	if h.rows(second.RequestID) != 1 || h.viewedEvents(second.RequestID) != 0 {
		t.Error("a ciphertext that would not open was deleted or recorded")
	}
	if opened, err := h.store.View(h.ctx, first); err != nil || opened.Value != "the first value" {
		t.Fatalf("the first request's own secret: %v, %v", opened, err)
	}
}

// TestForgetRemovesWhatAMemberSharedOrWasToOpen: a member who leaves, or an
// organization that is deleted, leaves no secret behind, and no other
// member's or organization's goes with them.
func TestForgetRemovesWhatAMemberSharedOrWasToOpen(t *testing.T) {
	h := newHarness(t)
	sharedByAlice := h.share("by-alice", "alice", "bob")
	openedByAlice := h.share("for-alice", "carol", "alice")
	between := h.share("between", "carol", "bob")
	elsewhere := h.share("elsewhere", "alice", "bob")
	elsewhere.OrganizationID = h.org + "_elsewhere"
	for _, share := range []interactions.SecretShare{sharedByAlice, openedByAlice, between, elsewhere} {
		h.put(share, "value for "+share.RequestID, time.Hour)
	}

	if err := h.store.Forget(h.ctx, h.org, "alice"); err != nil {
		t.Fatal(err)
	}
	for share, want := range map[string]int{sharedByAlice.RequestID: 0, openedByAlice.RequestID: 0, between.RequestID: 1, elsewhere.RequestID: 1} {
		if got := h.rows(share); got != want {
			t.Errorf("%s: %d rows after forgetting alice, want %d", share, got, want)
		}
	}
	if err := h.store.Forget(h.ctx, h.org, ""); err != nil {
		t.Fatal(err)
	}
	if h.rows(between.RequestID) != 0 || h.rows(elsewhere.RequestID) != 1 {
		t.Errorf("forgetting the organization left %d of its rows and %d of another's", h.rows(between.RequestID), h.rows(elsewhere.RequestID))
	}
	if err := h.store.Forget(h.ctx, h.org, "someone-with-nothing"); err != nil {
		t.Errorf("forgetting a member with nothing kept: %v", err)
	}
}

// TestSweepDeletesOnlyWhatExpiredAndOnlyAsManyAsItIsAsked: bounded batches,
// oldest first.
func TestSweepDeletesOnlyWhatExpiredAndOnlyAsManyAsItIsAsked(t *testing.T) {
	h := newHarness(t)
	var expired []interactions.SecretShare
	for i := range 5 {
		share := h.share(fmt.Sprintf("expired-%d", i), ownerID, askerID)
		h.put(share, "value", 10*time.Minute)
		expired = append(expired, share)
	}
	h.clock.Advance(11 * time.Minute)
	live := []interactions.SecretShare{h.share("live-0", ownerID, askerID), h.share("live-1", ownerID, askerID)}
	for _, share := range live {
		h.put(share, "value", 10*time.Minute)
	}

	removed := 0
	for range 10 {
		count, err := h.store.Sweep(h.ctx, 3)
		if err != nil {
			t.Fatal(err)
		}
		if count > 3 {
			t.Fatalf("a sweep of at most 3 removed %d", count)
		}
		if count == 0 {
			break
		}
		removed += count
	}
	if removed < len(expired) {
		t.Fatalf("the sweeps removed %d, want at least the %d that expired", removed, len(expired))
	}
	for _, share := range expired {
		if h.rows(share.RequestID) != 0 {
			t.Errorf("%s expired and is still kept", share.RequestID)
		}
	}
	for _, share := range live {
		if h.rows(share.RequestID) != 1 {
			t.Errorf("%s is still good and was swept", share.RequestID)
		}
	}
}

// TestTheTableRefusesALifetimeOverAnHour and the rows that name nobody.
func TestTheTableRefusesALifetimeOverAnHour(t *testing.T) {
	h := newHarness(t)
	created := h.clock.Now()
	insert := func(name string, expires time.Time, owner string) error {
		_, err := h.database.Pool.Exec(h.ctx, `INSERT INTO agents_primary.secret_shares
			(request_id, organization_id, owner_user_id, asker_user_id, sealed, created_at, expires_at)
			VALUES ($1,$2,$3,'user_asker','{}'::jsonb,$4,$5)`, h.org+"_"+name, h.org, owner, created, expires)
		return err
	}
	if err := insert("an-hour", created.Add(time.Hour), ownerID); err != nil {
		t.Fatalf("a secret that lives an hour: %v", err)
	}
	for name, err := range map[string]error{
		"over-an-hour": insert("over-an-hour", created.Add(time.Hour+time.Second), ownerID),
		"already-over": insert("already-over", created, ownerID),
		"no-owner":     insert("no-owner", created.Add(time.Minute), ""),
	} {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Errorf("%s: %v, want a check violation", name, err)
		}
	}
}

// TestPutTakesTheLongestLifetimeAndValueThereAre: the bounds themselves are
// allowed; a value is kept as it is, with its whitespace and beyond ASCII.
func TestPutTakesTheLongestLifetimeAndValueThereAre(t *testing.T) {
	h := newHarness(t)
	share := h.request("bounds")
	value := "  " + strings.Repeat("é", interactions.MaxSecretRunes-4) + "\n "
	meta := h.put(share, value, interactions.MaxSecretTTL)
	if !meta.ExpiresAt.Equal(h.clock.Now().Add(time.Hour)) {
		t.Fatalf("expires %s", meta.ExpiresAt)
	}
	opened, err := h.store.View(h.ctx, share)
	if err != nil || opened.Value != value {
		t.Fatalf("opened %d bytes, %v; want the value as it was shared", len(opened.Value), err)
	}
}

// TestRunDeletesWhatExpiredAtOnceAndStopsWithItsContext.
func TestRunDeletesWhatExpiredAtOnceAndStopsWithItsContext(t *testing.T) {
	h := newHarness(t)
	share := h.share("run", ownerID, askerID)
	h.put(share, "value", time.Minute)
	h.clock.Advance(2 * time.Minute)

	ctx, cancel := context.WithCancel(h.ctx)
	done := make(chan struct{})
	go func() {
		h.store.Run(ctx)
		close(done)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for h.rows(share.RequestID) != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop with its context")
	}
	if h.rows(share.RequestID) != 0 {
		t.Fatal("Run left an expired secret")
	}
}

// TestNothingLoggedOrReturnedInAnErrorHoldsTheValue runs a secret through
// everything that can happen to one, and looks for it in what came out.
func TestNothingLoggedOrReturnedInAnErrorHoldsTheValue(t *testing.T) {
	h := newHarness(t)
	share := h.request("silent")
	var errs []error
	note := func(err error) { errs = append(errs, err) }

	h.put(share, distinctive, time.Hour)
	h.kms.failDecrypts(errors.New("KMS is unavailable"))
	_, err := h.store.View(h.ctx, share)
	note(err)
	h.kms.failDecrypts(nil)
	h.store.recordViewed = func(context.Context, pgx.Tx, string, string, time.Time) error { return errors.New("refused") }
	_, err = h.store.View(h.ctx, share)
	note(err)
	h.store.recordViewed = interactions.RecordSecretViewed
	opened, err := h.store.View(h.ctx, share)
	note(err)
	_, err = h.store.View(h.ctx, share)
	note(err)
	note(h.store.Revoke(h.ctx, share))
	_, err = h.store.Put(h.ctx, share, distinctive, 2*time.Hour)
	note(err)
	_, err = h.store.Sweep(h.ctx, 10)
	note(err)

	if opened.Value != distinctive {
		t.Fatalf("opened %v", opened)
	}
	for _, err := range errs {
		if err != nil && strings.Contains(err.Error(), distinctive) {
			t.Errorf("an error quotes the value: %v", err)
		}
	}
	// The value formatted any way at all says nothing.
	for _, printed := range []string{fmt.Sprint(opened), fmt.Sprintf("%v %+v %#v %s", opened, opened, opened, opened)} {
		if strings.Contains(printed, distinctive) {
			t.Errorf("a value printed itself: %s", printed)
		}
	}
	h.assertNowhere(distinctive)
}
