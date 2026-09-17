package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

func TestMemberLimits(t *testing.T) {
	databaseURL := os.Getenv("AUTH_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AUTH_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	suffix := time.Now().UTC().Format("20060102150405.000000000")
	organizationID := "it-limit-org-" + suffix
	eventPrefix := "it-limit-event-" + suffix
	ids := map[string]string{}
	for _, name := range []string{"owner", "ana", "ben", "cat"} {
		ids[name] = "it-limit-" + name + "-" + suffix
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM auth_organizations WHERE id = $1`, organizationID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM auth_users WHERE id LIKE $1`, "it-limit-%-"+suffix)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM auth_outbox WHERE id LIKE $1`, eventPrefix+"%")
	}()

	now := time.Now().UTC()
	users := map[string]domain.User{}
	for name, id := range ids {
		users[name] = domain.User{ID: id, Email: id + "@example.com", CreatedAt: now, UpdatedAt: now}
		if _, err := store.UpsertFederatedUser(ctx, users[name], testEvent(eventPrefix+"-registered-"+name, "auth.user.registered.v1", now), testEvent(eventPrefix+"-signed-in-"+name, "auth.user.signed-in.v1", now)); err != nil {
			t.Fatalf("upsert %s: %v", name, err)
		}
	}
	organization := domain.Organization{ID: organizationID, Name: "Limits", Slug: "limits-" + suffix, CreatedBy: ids["owner"], CreatedAt: now, UpdatedAt: now}
	owner := domain.Membership{OrganizationID: organizationID, UserID: ids["owner"], Role: domain.RoleOwner, JoinedAt: now}
	if err := store.CreateOrganization(ctx, organization, owner, []domain.Event{testEvent(eventPrefix+"-created", "auth.organization.created.v1", now)}); err != nil {
		t.Fatalf("CreateOrganization() error = %v", err)
	}

	event := 0
	nextEvent := func(detailType string) domain.Event {
		event++
		return testEvent(eventPrefix+"-"+string(rune('a'+event)), detailType, now)
	}
	invite := func(name string) (domain.Invitation, error) {
		invitation := domain.Invitation{
			ID: "it-limit-invite-" + name + "-" + suffix, OrganizationID: organizationID, Email: users[name].Email, Role: domain.RoleMember,
			TokenHash: "it-limit-token-" + name + "-" + suffix, InvitedBy: ids["owner"], Status: domain.InvitationPending,
			ExpiresAt: now.Add(time.Hour), CreatedAt: now,
		}
		return invitation, store.CreateInvitation(ctx, invitation, nextEvent("auth.member.invited.v1"))
	}
	accept := func(name string) error {
		membership := domain.Membership{OrganizationID: organizationID, UserID: ids[name], Role: domain.RoleMember, JoinedAt: now.Add(time.Second)}
		return store.AcceptInvitation(ctx, "it-limit-invite-"+name+"-"+suffix, users[name], membership, nextEvent("auth.member.joined.v1"))
	}
	limit := func(revision int64, value *int) {
		t.Helper()
		if err := store.ApplyEntitlements(ctx, organizationID, revision, value, now); err != nil {
			t.Fatalf("ApplyEntitlements(r%d) error = %v", revision, err)
		}
	}
	two, one := 2, 1

	// Without entitlements there is no limit.
	if _, err := invite("ana"); err != nil {
		t.Fatalf("invite ana without a limit: %v", err)
	}

	// A limit of two counts the owner and ana's pending invitation.
	limit(1, &two)
	if _, err := invite("ben"); !errors.Is(err, domain.ErrLimitReached) {
		t.Fatalf("invite ben at the limit: error = %v, want ErrLimitReached", err)
	}
	if err := accept("ana"); err != nil {
		t.Fatalf("accept ana within the limit: %v", err)
	}

	// An older revision doesn't lift the limit.
	limit(0, nil)
	if _, err := invite("ben"); !errors.Is(err, domain.ErrLimitReached) {
		t.Fatalf("invite ben after a stale revision: error = %v, want ErrLimitReached", err)
	}

	// A newer revision without a limit does.
	limit(2, nil)
	if _, err := invite("ben"); err != nil {
		t.Fatalf("invite ben without a limit: %v", err)
	}

	// Lowering the limit keeps existing members but refuses to add more.
	limit(3, &one)
	if err := accept("ben"); !errors.Is(err, domain.ErrLimitReached) {
		t.Fatalf("accept ben over the lowered limit: error = %v, want ErrLimitReached", err)
	}
	members, err := store.Members(ctx, organizationID)
	if err != nil || len(members) != 2 {
		t.Fatalf("members = %d, %v; want the owner and ana to stay", len(members), err)
	}
	var status domain.InvitationStatus
	if err := store.pool.QueryRow(ctx, `SELECT status FROM auth_invitations WHERE token_hash = $1`, "it-limit-token-ben-"+suffix).Scan(&status); err != nil || status != domain.InvitationPending {
		t.Fatalf("ben's invitation = %s, %v; want it still pending", status, err)
	}

	// Entitlements for an organization auth doesn't have are dropped.
	if err := store.ApplyEntitlements(ctx, "it-limit-missing-"+suffix, 1, &two, now); err != nil {
		t.Fatalf("ApplyEntitlements(missing organization) error = %v", err)
	}
	var stray int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM auth_organization_entitlements WHERE organization_id = $1`, "it-limit-missing-"+suffix).Scan(&stray); err != nil || stray != 0 {
		t.Fatalf("entitlements rows for a missing organization = %d, %v", stray, err)
	}

	// Deleting the organization removes its entitlements with it.
	if err := store.DeleteOrganization(ctx, organizationID, testEvent(eventPrefix+"-deleted", "auth.organization.deleted.v1", now),
		func(member domain.Membership) (domain.Event, error) {
			return testEvent(eventPrefix+"-removed-"+member.UserID, "auth.member.removed.v1", now), nil
		}); err != nil {
		t.Fatalf("DeleteOrganization() error = %v", err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM auth_organization_entitlements WHERE organization_id = $1`, organizationID).Scan(&stray); err != nil || stray != 0 {
		t.Fatalf("entitlements rows after deletion = %d, %v", stray, err)
	}
}
