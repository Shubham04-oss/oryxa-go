package interactions

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
)

type activeLeaseStore struct {
	Consents
	rows                 []Lease
	err                  error
	organization, member string
	limit                int
	before               LeaseCursor
	at                   time.Time
}

func (store *activeLeaseStore) ActiveLeases(_ context.Context, organization, member string, limit int, before LeaseCursor, at time.Time) ([]Lease, error) {
	store.organization, store.member, store.limit, store.before, store.at = organization, member, limit, before, at
	return store.rows, store.err
}

func TestActiveLeasePageUsesMemberScopeAndReportsContinuation(t *testing.T) {
	service, _ := testService(t)
	actor := requester
	actor.Session = agents.SessionMember
	now := time.Now().UTC()
	service.now = func() time.Time { return now }
	store := &activeLeaseStore{rows: []Lease{
		{ID: "ils_new", OrganizationID: actor.OrganizationID, OwnerUserID: actor.UserID, AskerUserID: "other", CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)},
		{ID: "ils_middle", OrganizationID: actor.OrganizationID, OwnerUserID: "other", AskerUserID: actor.UserID, CreatedAt: now.Add(-2 * time.Minute), ExpiresAt: now.Add(time.Hour)},
		{ID: "ils_old", OrganizationID: actor.OrganizationID, OwnerUserID: actor.UserID, AskerUserID: "other", CreatedAt: now.Add(-3 * time.Minute), ExpiresAt: now.Add(time.Hour)},
	}}
	service.consents = store
	page, err := service.ActiveLeases(context.Background(), actor, LeaseQuery{Limit: 2})
	if err != nil || len(page.Leases) != 2 || !page.HasMore || page.NextCursor == "" || !page.ObservedAt.Equal(now) {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if store.organization != actor.OrganizationID || store.member != actor.UserID || store.limit != 2 || !store.at.Equal(now) {
		t.Fatalf("scope=%+v", store)
	}
	if !page.Leases[0].CanRevoke || page.Leases[0].Role != ActivityOwner || page.Leases[1].CanRevoke || page.Leases[1].Role != ActivityAsker {
		t.Fatalf("lease roles=%+v", page.Leases)
	}
	store.rows = store.rows[2:]
	last, err := service.ActiveLeases(context.Background(), actor, LeaseQuery{Limit: 2, Cursor: page.NextCursor})
	if err != nil || last.HasMore || last.NextCursor != "" || len(last.Leases) != 1 || store.before.ID != "ils_middle" {
		t.Fatalf("next=%+v store=%+v err=%v", last, store, err)
	}
}

func TestActiveLeaseReadNeverTurnsARefusalOrFailureIntoEmptySuccess(t *testing.T) {
	service, _ := testService(t)
	actor := requester
	actor.Session = agents.SessionAgent
	if _, err := service.ActiveLeases(context.Background(), actor, LeaseQuery{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("agent read=%v", err)
	}
	actor.Session = agents.SessionMember
	if _, err := service.ActiveLeases(context.Background(), actor, LeaseQuery{}); !errors.Is(err, ErrConsentUnavailable) {
		t.Fatalf("missing store=%v", err)
	}
	store := &activeLeaseStore{err: errors.New("database unavailable")}
	service.consents = store
	if page, err := service.ActiveLeases(context.Background(), actor, LeaseQuery{}); err == nil || page.Leases != nil {
		t.Fatalf("failed read returned page=%+v err=%v", page, err)
	}
	store.err = nil
	for _, cursor := range []string{"bad cursor", base64.RawURLEncoding.EncodeToString([]byte(`{"id":"ils_1"}`)), strings.Repeat("a", 1025)} {
		if _, err := service.ActiveLeases(context.Background(), actor, LeaseQuery{Cursor: cursor}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("cursor %q=%v", cursor, err)
		}
	}
	page, err := service.ActiveLeases(context.Background(), actor, LeaseQuery{Limit: 200})
	if err != nil || page.Leases == nil || page.HasMore || store.limit != 100 {
		t.Fatalf("empty page=%+v store=%+v err=%v", page, store, err)
	}
}
