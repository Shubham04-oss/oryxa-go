package interactions

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestActiveLeasesFilterBeforePagingAndKeepTheMemberScope(t *testing.T) {
	ledger := newSlackLedger(t)
	ctx := context.Background()
	now := ledger.clock.Now()
	member := ledger.owner(raya)
	for index, fixture := range []struct {
		id, asker, owner string
		expires, revoked bool
	}{
		{"expired", asker, raya, true, false},
		{"revoked", asker, raya, false, true},
		{"others", asker, maya, false, false},
		{"owner", asker, raya, false, false},
		{"asker", raya, maya, false, false},
		{"old", asker, raya, false, false},
	} {
		created := now.Add(-time.Duration(index+1) * time.Minute)
		expires := now.Add(time.Hour)
		if fixture.expires {
			expires = now
		}
		var revoked any
		if fixture.revoked {
			revoked = now
		}
		_, err := ledger.database.Pool.Exec(ctx, `INSERT INTO agents_primary.interaction_leases
			(id,organization_id,asker_user_id,owner_user_id,channel_id,thread_ts,created_at,expires_at,revoked_at)
			VALUES ($1,$2,$3,$4,'C1',$5,$6,$7,$8)`, "ils_"+fixture.id+"_"+ledger.org, ledger.org,
			fixture.asker, fixture.owner, fmt.Sprintf("1790000000.%06d", index), created, expires, revoked)
		if err != nil {
			t.Fatal(err)
		}
	}
	var ids []string
	query := LeaseQuery{Limit: 1}
	for pageNumber := 0; ; pageNumber++ {
		page, err := ledger.service.ActiveLeases(ctx, member, query)
		if err != nil || len(page.Leases) != 1 || page.HasMore != (pageNumber < 2) {
			t.Fatalf("page %d=%+v err=%v", pageNumber, page, err)
		}
		lease := page.Leases[0]
		ids = append(ids, lease.ID)
		if lease.CanRevoke != (pageNumber != 1) || lease.Role != map[bool]string{true: ActivityOwner, false: ActivityAsker}[lease.CanRevoke] {
			t.Fatalf("member lease role=%+v", lease)
		}
		if !page.HasMore {
			break
		}
		query.Cursor = page.NextCursor
	}
	want := []string{"ils_owner_" + ledger.org, "ils_asker_" + ledger.org, "ils_old_" + ledger.org}
	if !slices.Equal(ids, want) {
		t.Fatalf("live page ids=%v, want %v", ids, want)
	}
	other := member
	other.OrganizationID = "org_elsewhere"
	if page, err := ledger.service.ActiveLeases(ctx, other, LeaseQuery{}); err != nil || len(page.Leases) != 0 {
		t.Fatalf("cross-org leases=%+v err=%v", page, err)
	}
	if _, err := ledger.service.RevokeLease(ctx, member, ids[0]); err != nil {
		t.Fatal(err)
	}
	page, err := ledger.service.ActiveLeases(ctx, member, LeaseQuery{})
	if err != nil || len(page.Leases) != 2 || page.HasMore {
		t.Fatalf("after revoke=%+v err=%v", page, err)
	}
	ledger.clock.Advance(time.Hour)
	if page, err := ledger.service.ActiveLeases(ctx, member, LeaseQuery{}); err != nil || len(page.Leases) != 0 || page.HasMore {
		t.Fatalf("after expiry=%+v err=%v", page, err)
	}
}
