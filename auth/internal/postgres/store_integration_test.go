package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

func TestMigrateSerializesConcurrentCallers(t *testing.T) {
	databaseURL := os.Getenv("AUTH_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AUTH_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	first, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	start := make(chan struct{})
	results := make(chan error, 2)
	for _, store := range []*Store{first, second} {
		go func(store *Store) {
			<-start
			results <- store.Migrate(ctx)
		}(store)
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent migration: %v", err)
		}
	}
}

func TestStoreLifecycle(t *testing.T) {
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
	ownerID := "it-owner-" + suffix
	memberID := "it-member-" + suffix
	organizationID := "it-org-" + suffix
	eventPrefix := "it-event-" + suffix
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM auth_organizations WHERE id = $1`, organizationID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM auth_action_tokens WHERE user_id = ANY($1)`, []string{ownerID, memberID})
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM auth_users WHERE id = ANY($1)`, []string{ownerID, memberID})
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM auth_outbox WHERE id LIKE $1`, eventPrefix+"%")
	}()

	now := time.Now().UTC()
	owner := domain.User{ID: ownerID, Email: ownerID + "@example.com", CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: memberID, Email: memberID + "@example.com", CreatedAt: now, UpdatedAt: now}
	if created, err := store.UpsertFederatedUser(ctx, owner, testEvent(eventPrefix+"-owner-registered", "auth.user.registered.v1", now), testEvent(eventPrefix+"-owner-signed-in", "auth.user.signed-in.v1", now)); err != nil || !created {
		t.Fatalf("upsert owner: created=%v error=%v", created, err)
	}
	if created, err := store.UpsertFederatedUser(ctx, member, testEvent(eventPrefix+"-member-registered", "auth.user.registered.v1", now), testEvent(eventPrefix+"-member-signed-in", "auth.user.signed-in.v1", now)); err != nil || !created {
		t.Fatalf("upsert member: created=%v error=%v", created, err)
	}

	verification := domain.ActionToken{
		ID: "it-verification-" + suffix, UserID: ownerID, Email: owner.Email,
		Purpose: domain.ActionEmailVerification, TokenHash: "it-verification-hash-" + suffix,
		Status: domain.ActionPending, ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}
	if err := store.CreateActionToken(ctx, verification, testEvent(eventPrefix+"-verification-requested", "auth.user.email-verification.delivery-requested.v1", now)); err != nil {
		t.Fatalf("CreateActionToken(verification) error = %v", err)
	}
	if _, err := store.ClaimActionToken(ctx, domain.ActionEmailVerification, verification.TokenHash, now); err != nil {
		t.Fatalf("ClaimActionToken(verification) error = %v", err)
	}
	if _, err := store.ClaimActionToken(ctx, domain.ActionEmailVerification, verification.TokenHash, now); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("ClaimActionToken(already processing) error = %v, want conflict", err)
	}
	if err := store.CompleteActionToken(ctx, domain.ActionEmailVerification, verification.TokenHash, now.Add(time.Second), testEvent(eventPrefix+"-email-verified", "auth.user.email-verified.v1", now)); err != nil {
		t.Fatalf("CompleteActionToken(verification) error = %v", err)
	}
	if err := store.MarkOutboxPublished(ctx, eventPrefix+"-verification-requested"); err != nil {
		t.Fatalf("MarkOutboxPublished(verification) error = %v", err)
	}
	var scrubbedDetail string
	if err := store.pool.QueryRow(ctx, `SELECT detail::text FROM auth_outbox WHERE id = $1`, eventPrefix+"-verification-requested").Scan(&scrubbedDetail); err != nil || scrubbedDetail != "{}" {
		t.Fatalf("delivery event was not scrubbed: detail=%q error=%v", scrubbedDetail, err)
	}
	reset := domain.ActionToken{
		ID: "it-reset-" + suffix, UserID: ownerID, Email: owner.Email,
		Purpose: domain.ActionPasswordReset, TokenHash: "it-reset-hash-" + suffix,
		Status: domain.ActionPending, ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}
	if err := store.CreateActionToken(ctx, reset, testEvent(eventPrefix+"-reset-requested", "auth.user.password-reset.delivery-requested.v1", now)); err != nil {
		t.Fatalf("CreateActionToken(reset) error = %v", err)
	}
	if _, err := store.ClaimActionToken(ctx, domain.ActionPasswordReset, reset.TokenHash, now); err != nil {
		t.Fatalf("ClaimActionToken(reset) error = %v", err)
	}
	if err := store.CompleteActionToken(ctx, domain.ActionPasswordReset, reset.TokenHash, now.Add(time.Second), testEvent(eventPrefix+"-password-reset", "auth.user.password-reset.v1", now)); err != nil {
		t.Fatalf("CompleteActionToken(reset) error = %v", err)
	}

	organization := domain.Organization{
		ID: organizationID, Name: "Integration Org", Slug: organizationID,
		CreatedBy: ownerID, CreatedAt: now, UpdatedAt: now,
	}
	ownerMembership := domain.Membership{OrganizationID: organizationID, UserID: ownerID, Role: domain.RoleOwner, JoinedAt: now}
	if err := store.CreateOrganization(ctx, organization, ownerMembership, []domain.Event{
		testEvent(eventPrefix+"-org-created", "auth.organization.created.v1", now),
		testEvent(eventPrefix+"-owner-joined", "auth.member.joined.v1", now),
	}); err != nil {
		t.Fatalf("CreateOrganization() error = %v", err)
	}

	organization.Name = "Integration Org Updated"
	organization.UpdatedAt = now.Add(time.Second)
	if err := store.UpdateOrganization(ctx, organization, testEvent(eventPrefix+"-org-updated", "auth.organization.updated.v1", now)); err != nil {
		t.Fatalf("UpdateOrganization() error = %v", err)
	}

	invitation := domain.Invitation{
		ID:             "it-invite-" + suffix,
		OrganizationID: organizationID,
		Email:          member.Email,
		Role:           domain.RoleMember,
		TokenHash:      "it-token-hash-" + suffix,
		InvitedBy:      ownerID,
		Status:         domain.InvitationPending,
		ExpiresAt:      now.Add(time.Hour),
		CreatedAt:      now,
	}
	if err := store.CreateInvitation(ctx, invitation, testEvent(eventPrefix+"-invited", "auth.member.invited.v1", now)); err != nil {
		t.Fatalf("CreateInvitation() error = %v", err)
	}
	membership := domain.Membership{OrganizationID: organizationID, UserID: memberID, Role: domain.RoleMember, JoinedAt: now.Add(time.Second)}
	if err := store.AcceptInvitation(ctx, invitation.TokenHash, member, membership, testEvent(eventPrefix+"-joined", "auth.member.joined.v1", now)); err != nil {
		t.Fatalf("AcceptInvitation() error = %v", err)
	}
	if err := store.ChangeMemberRole(ctx, organizationID, memberID, domain.RoleMember, domain.RoleOwner, testEvent(eventPrefix+"-role-changed", "auth.member.role-changed.v1", now)); err != nil {
		t.Fatalf("ChangeMemberRole() error = %v", err)
	}
	if err := store.RemoveMember(ctx, organizationID, memberID, domain.RoleOwner, testEvent(eventPrefix+"-member-removed", "auth.member.removed.v1", now)); err != nil {
		t.Fatalf("RemoveMember(second owner) error = %v", err)
	}
	if err := store.RemoveMember(ctx, organizationID, ownerID, domain.RoleOwner, testEvent(eventPrefix+"-last-owner", "auth.member.removed.v1", now)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("RemoveMember(last owner) error = %v, want conflict", err)
	}

	revoked := invitation
	revoked.ID = "it-revoked-invite-" + suffix
	revoked.Email = "another-" + suffix + "@example.com"
	revoked.TokenHash = "it-revoked-token-hash-" + suffix
	if err := store.CreateInvitation(ctx, revoked, testEvent(eventPrefix+"-second-invited", "auth.member.invited.v1", now)); err != nil {
		t.Fatalf("CreateInvitation(second) error = %v", err)
	}
	if err := store.RevokeInvitation(ctx, organizationID, revoked.ID, testEvent(eventPrefix+"-invite-revoked", "auth.member.invitation-revoked.v1", now)); err != nil {
		t.Fatalf("RevokeInvitation() error = %v", err)
	}

	var removedMembers []domain.Membership
	removed := func(member domain.Membership) (domain.Event, error) {
		removedMembers = append(removedMembers, member)
		return testEvent(eventPrefix+"-removed-with-org-"+member.UserID, "auth.member.removed.v1", now), nil
	}
	if err := store.DeleteOrganization(ctx, organizationID, testEvent(eventPrefix+"-org-deleted", "auth.organization.deleted.v1", now), removed); err != nil {
		t.Fatalf("DeleteOrganization() error = %v", err)
	}
	if _, err := store.OrganizationByID(ctx, organizationID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("OrganizationByID(deleted) error = %v, want not found", err)
	}
	// Only the owner is still a member by now; the second owner was removed above.
	if len(removedMembers) != 1 || removedMembers[0].UserID != ownerID || removedMembers[0].Email != owner.Email || removedMembers[0].Role != domain.RoleOwner {
		t.Fatalf("DeleteOrganization() ended memberships %+v, want only the owner's", removedMembers)
	}
	if err := store.DeleteOrganization(ctx, organizationID, testEvent(eventPrefix+"-deleted-again", "auth.organization.deleted.v1", now), removed); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("DeleteOrganization(deleted) error = %v, want not found", err)
	}
	var outboxCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM auth_outbox WHERE id LIKE $1`, eventPrefix+"%").Scan(&outboxCount); err != nil {
		t.Fatalf("count outbox events: %v", err)
	}
	if outboxCount != 19 {
		t.Fatalf("outbox count = %d, want 19", outboxCount)
	}
}

func TestIndexes(t *testing.T) {
	databaseURL := os.Getenv("AUTH_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AUTH_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	rows, err := store.pool.Query(ctx, `
		SELECT indexname FROM pg_indexes
		WHERE schemaname = current_schema()
		  AND indexname = ANY($1)
		ORDER BY indexname`, []string{
		"auth_memberships_owners_idx",
		"auth_invitations_org_created_idx",
		"auth_invitations_pending_expiry_idx",
		"auth_outbox_pending_idx",
	})
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := "auth_invitations_org_created_idx auth_invitations_pending_expiry_idx auth_memberships_owners_idx auth_outbox_pending_idx"
	if got := strings.Join(names, " "); got != want {
		t.Fatalf("indexes = %q, want %q", got, want)
	}
	var outboxDef string
	if err := store.pool.QueryRow(ctx, `
		SELECT indexdef FROM pg_indexes
		WHERE schemaname = current_schema() AND indexname = 'auth_outbox_pending_idx'`).Scan(&outboxDef); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(outboxDef, "available_at, occurred_at, id") {
		t.Fatalf("outbox indexdef = %s", outboxDef)
	}
}

func testEvent(id, detailType string, occurredAt time.Time) domain.Event {
	detail, _ := json.Marshal(map[string]string{"test": id})
	return domain.Event{ID: id, DetailType: detailType, OccurredAt: occurredAt, Data: detail}
}
