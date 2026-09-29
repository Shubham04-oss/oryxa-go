package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

func (store *Store) CreateOrganization(ctx context.Context, organization domain.Organization, owner domain.Membership, events []domain.Event) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin organization transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `
		INSERT INTO auth_organizations (id, name, slug, domains, timezone, guests_allowed, created_by, created_at, updated_at)
		VALUES ($1, $2, lower($3), COALESCE($4::text[], '{}'::text[]), $5, $6, $7, $8, $8)`,
		organization.ID, organization.Name, organization.Slug, organization.Domains, organization.Timezone, organization.GuestsAllowed, organization.CreatedBy, organization.CreatedAt)
	if err != nil {
		return mapWriteError("insert organization", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO auth_memberships (organization_id, user_id, role, joined_at)
		VALUES ($1, $2, $3, $4)`, owner.OrganizationID, owner.UserID, owner.Role, owner.JoinedAt)
	if err != nil {
		return mapWriteError("insert owner membership", err)
	}
	if err := insertEvents(ctx, tx, events); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit organization transaction: %w", err)
	}
	return nil
}

func (store *Store) OrganizationsForUser(ctx context.Context, userID string) ([]domain.Organization, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT o.id, o.name, o.slug, o.domains, o.timezone, o.guests_allowed, o.created_by, o.created_at, o.updated_at
		FROM auth_organizations o
		JOIN auth_memberships m ON m.organization_id = o.id
		WHERE m.user_id = $1
		ORDER BY lower(o.name), o.id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	defer rows.Close()

	organizations := make([]domain.Organization, 0)
	for rows.Next() {
		var organization domain.Organization
		if err := rows.Scan(
			&organization.ID,
			&organization.Name,
			&organization.Slug,
			&organization.Domains,
			&organization.Timezone,
			&organization.GuestsAllowed,
			&organization.CreatedBy,
			&organization.CreatedAt,
			&organization.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan organization: %w", err)
		}
		organizations = append(organizations, organization)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate organizations: %w", err)
	}
	return organizations, nil
}

func (store *Store) OrganizationByID(ctx context.Context, organizationID string) (domain.Organization, error) {
	var organization domain.Organization
	err := store.pool.QueryRow(ctx, `
		SELECT id, name, slug, domains, timezone, guests_allowed, created_by, created_at, updated_at
		FROM auth_organizations WHERE id = $1`, organizationID).
		Scan(
			&organization.ID,
			&organization.Name,
			&organization.Slug,
			&organization.Domains,
			&organization.Timezone,
			&organization.GuestsAllowed,
			&organization.CreatedBy,
			&organization.CreatedAt,
			&organization.UpdatedAt,
		)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Organization{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Organization{}, fmt.Errorf("get organization: %w", err)
	}
	return organization, nil
}

func (store *Store) UpdateOrganization(ctx context.Context, organization domain.Organization, event domain.Event) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin organization update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	result, err := tx.Exec(ctx, `
		UPDATE auth_organizations
		SET name = $2, slug = lower($3), domains = COALESCE($4::text[], '{}'::text[]), timezone = $5,
		    guests_allowed = $6, updated_at = $7
		WHERE id = $1`, organization.ID, organization.Name, organization.Slug,
		organization.Domains, organization.Timezone, organization.GuestsAllowed, organization.UpdatedAt)
	if err != nil {
		return mapWriteError("update organization", err)
	}
	if result.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	if err := insertEvents(ctx, tx, []domain.Event{event}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit organization update: %w", err)
	}
	return nil
}

func (store *Store) DeleteOrganization(ctx context.Context, organizationID string, deleted domain.Event, removed func(domain.Membership) (domain.Event, error)) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin organization deletion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The lock also holds off an invitation being accepted, whose membership
	// insert needs this row, so the members read here are exactly the
	// memberships the deletion ends.
	if err := lockOrganization(ctx, tx, organizationID); err != nil {
		return err
	}
	members, err := listMembers(ctx, tx, organizationID)
	if err != nil {
		return err
	}
	events := []domain.Event{deleted}
	for _, member := range members {
		event, err := removed(member)
		if err != nil {
			return err
		}
		events = append(events, event)
	}

	result, err := tx.Exec(ctx, `DELETE FROM auth_organizations WHERE id = $1`, organizationID)
	if err != nil {
		return fmt.Errorf("delete organization: %w", err)
	}
	if result.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	if err := insertEvents(ctx, tx, events); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit organization deletion: %w", err)
	}
	return nil
}

func (store *Store) Membership(ctx context.Context, organizationID, userID string) (domain.Membership, error) {
	var membership domain.Membership
	err := store.pool.QueryRow(ctx, `
		SELECT m.organization_id, m.user_id, u.email, u.display_name, m.role, m.joined_at
		FROM auth_memberships m
		JOIN auth_users u ON u.id = m.user_id
		WHERE m.organization_id = $1 AND m.user_id = $2`, organizationID, userID).
		Scan(
			&membership.OrganizationID,
			&membership.UserID,
			&membership.Email,
			&membership.DisplayName,
			&membership.Role,
			&membership.JoinedAt,
		)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Membership{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Membership{}, fmt.Errorf("get membership: %w", err)
	}
	return membership, nil
}

func (store *Store) Members(ctx context.Context, organizationID string) ([]domain.Membership, error) {
	return listMembers(ctx, store.pool, organizationID)
}

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func listMembers(ctx context.Context, db queryer, organizationID string) ([]domain.Membership, error) {
	rows, err := db.Query(ctx, `
		SELECT m.organization_id, m.user_id, u.email, u.display_name, m.role, m.joined_at
		FROM auth_memberships m
		JOIN auth_users u ON u.id = m.user_id
		WHERE m.organization_id = $1
		ORDER BY CASE m.role WHEN 'owner' THEN 1 WHEN 'admin' THEN 2 ELSE 3 END,
		         lower(u.email), m.user_id`, organizationID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()

	members := make([]domain.Membership, 0)
	for rows.Next() {
		var member domain.Membership
		if err := rows.Scan(
			&member.OrganizationID,
			&member.UserID,
			&member.Email,
			&member.DisplayName,
			&member.Role,
			&member.JoinedAt,
		); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		members = append(members, member)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate members: %w", err)
	}
	return members, nil
}

func (store *Store) CreateInvitation(ctx context.Context, invitation domain.Invitation, event domain.Event) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin invitation transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Locked so concurrent invitations can't together pass the member limit.
	if err := lockOrganization(ctx, tx, invitation.OrganizationID); err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		UPDATE auth_invitations
		SET status = 'expired'
		WHERE organization_id = $1 AND lower(email) = lower($2)
		  AND status = 'pending' AND expires_at <= $3`,
		invitation.OrganizationID, invitation.Email, invitation.CreatedAt)
	if err != nil {
		return fmt.Errorf("expire prior invitations: %w", err)
	}
	if err := checkMemberLimit(ctx, tx, invitation.OrganizationID, &invitation.CreatedAt); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO auth_invitations
		    (id, organization_id, email, role, guest, token_hash, invited_by, status, expires_at, created_at)
		VALUES ($1, $2, lower($3), $4, $5, $6, $7, $8, $9, $10)`,
		invitation.ID,
		invitation.OrganizationID,
		invitation.Email,
		invitation.Role,
		invitation.Guest,
		invitation.TokenHash,
		invitation.InvitedBy,
		invitation.Status,
		invitation.ExpiresAt,
		invitation.CreatedAt,
	)
	if err != nil {
		return mapWriteError("insert invitation", err)
	}
	if err := insertEvents(ctx, tx, []domain.Event{event}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit invitation transaction: %w", err)
	}
	return nil
}

func (store *Store) Invitations(ctx context.Context, organizationID string) ([]domain.Invitation, error) {
	if _, err := store.pool.Exec(ctx, `
		UPDATE auth_invitations
		SET status = 'expired'
		WHERE organization_id = $1 AND status = 'pending' AND expires_at <= now()`, organizationID); err != nil {
		return nil, fmt.Errorf("expire invitations: %w", err)
	}
	rows, err := store.pool.Query(ctx, `
		SELECT id, organization_id, email, role, guest, invited_by, status, expires_at, created_at, accepted_at
		FROM auth_invitations
		WHERE organization_id = $1
		ORDER BY created_at DESC, id`, organizationID)
	if err != nil {
		return nil, fmt.Errorf("list invitations: %w", err)
	}
	defer rows.Close()

	invitations := make([]domain.Invitation, 0)
	for rows.Next() {
		invitation, err := scanInvitation(rows)
		if err != nil {
			return nil, err
		}
		invitations = append(invitations, invitation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate invitations: %w", err)
	}
	return invitations, nil
}

func (store *Store) InvitationByTokenHash(ctx context.Context, tokenHash string) (domain.Invitation, error) {
	invitation, err := scanInvitation(store.pool.QueryRow(ctx, `
		SELECT id, organization_id, email, role, guest, invited_by, status, expires_at, created_at, accepted_at
		FROM auth_invitations WHERE token_hash = $1`, tokenHash))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Invitation{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Invitation{}, fmt.Errorf("get invitation: %w", err)
	}
	return invitation, nil
}

func (store *Store) InvitationByID(ctx context.Context, organizationID, invitationID string) (domain.Invitation, error) {
	invitation, err := scanInvitation(store.pool.QueryRow(ctx, `
		SELECT id, organization_id, email, role, guest, invited_by, status, expires_at, created_at, accepted_at
		FROM auth_invitations WHERE organization_id = $1 AND id = $2`, organizationID, invitationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Invitation{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Invitation{}, fmt.Errorf("get invitation: %w", err)
	}
	return invitation, nil
}

func (store *Store) RevokeInvitation(ctx context.Context, organizationID, invitationID string, event domain.Event) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin invitation revocation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	result, err := tx.Exec(ctx, `
		UPDATE auth_invitations
		SET status = 'revoked'
		WHERE organization_id = $1 AND id = $2 AND status = 'pending'`, organizationID, invitationID)
	if err != nil {
		return fmt.Errorf("revoke invitation: %w", err)
	}
	if result.RowsAffected() == 0 {
		return domain.ErrConflict
	}
	if err := insertEvents(ctx, tx, []domain.Event{event}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit invitation revocation: %w", err)
	}
	return nil
}

func (store *Store) ReceivedInvitations(ctx context.Context, userID, email string, at time.Time) ([]domain.ReceivedInvitation, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT i.id, i.organization_id, o.name, i.role, i.expires_at, i.created_at
		FROM auth_invitations i
		JOIN auth_organizations o ON o.id = i.organization_id
		WHERE lower(i.email) = lower($1)
		  AND i.status = 'pending' AND i.expires_at > $2
		  AND NOT EXISTS (
		      SELECT 1 FROM auth_memberships m
		      WHERE m.organization_id = i.organization_id AND m.user_id = $3)
		ORDER BY i.created_at DESC, i.id`, email, at, userID)
	if err != nil {
		return nil, fmt.Errorf("list received invitations: %w", err)
	}
	defer rows.Close()

	invitations := make([]domain.ReceivedInvitation, 0)
	for rows.Next() {
		var invitation domain.ReceivedInvitation
		if err := rows.Scan(
			&invitation.ID,
			&invitation.OrganizationID,
			&invitation.OrganizationName,
			&invitation.Role,
			&invitation.ExpiresAt,
			&invitation.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan received invitation: %w", err)
		}
		invitations = append(invitations, invitation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate received invitations: %w", err)
	}
	return invitations, nil
}

func (store *Store) AcceptInvitation(ctx context.Context, invitationID string, user domain.User, membership domain.Membership, event domain.Event) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin acceptance transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the organization before the invitation, in the order deleting an
	// organization takes them, and so concurrent acceptances can't together
	// pass the member limit.
	var organizationID string
	err = tx.QueryRow(ctx, `SELECT organization_id FROM auth_invitations WHERE id = $1`, invitationID).Scan(&organizationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("find invitation: %w", err)
	}
	if err := lockOrganization(ctx, tx, organizationID); err != nil {
		return err
	}

	var email string
	var role domain.Role
	var guest bool
	var status domain.InvitationStatus
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `
		SELECT email, role, guest, status, expires_at
		FROM auth_invitations
		WHERE id = $1
		FOR UPDATE`, invitationID).Scan(&email, &role, &guest, &status, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock invitation: %w", err)
	}
	if status != domain.InvitationPending {
		return domain.ErrConflict
	}
	var domains []string
	var guestsAllowed bool
	if err := tx.QueryRow(ctx, `SELECT domains, guests_allowed FROM auth_organizations WHERE id = $1`, organizationID).Scan(&domains, &guestsAllowed); err != nil {
		return fmt.Errorf("check organization domains: %w", err)
	}
	_, emailDomain, hasDomain := strings.Cut(strings.ToLower(email), "@")
	if len(domains) > 0 && (!hasDomain || (!slices.Contains(domains, emailDomain) && !(guest && guestsAllowed))) {
		return domain.ErrForbidden
	}
	if !expiresAt.After(membership.JoinedAt) {
		if _, err := tx.Exec(ctx, `UPDATE auth_invitations SET status = 'expired' WHERE id = $1`, invitationID); err != nil {
			return fmt.Errorf("expire invitation: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit expired invitation: %w", err)
		}
		return domain.ErrExpired
	}
	if normalizeEmail(email) != normalizeEmail(user.Email) {
		return domain.ErrForbidden
	}
	if organizationID != membership.OrganizationID || role != membership.Role {
		return fmt.Errorf("membership does not match invitation %s: %w", invitationID, domain.ErrConflict)
	}
	if err := checkMemberLimit(ctx, tx, organizationID, nil); err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO auth_memberships (organization_id, user_id, role, joined_at)
		VALUES ($1, $2, $3, $4)`,
		membership.OrganizationID, membership.UserID, membership.Role, membership.JoinedAt)
	if err != nil {
		return mapWriteError("insert invited membership", err)
	}
	_, err = tx.Exec(ctx, `
		UPDATE auth_invitations
		SET status = 'accepted', accepted_at = $2, accepted_by = $3
		WHERE id = $1`, invitationID, membership.JoinedAt, user.ID)
	if err != nil {
		return fmt.Errorf("accept invitation: %w", err)
	}
	if err := insertEvents(ctx, tx, []domain.Event{event}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit acceptance transaction: %w", err)
	}
	return nil
}

func (store *Store) ChangeMemberRole(ctx context.Context, organizationID, userID string, expected, replacement domain.Role, event domain.Event) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin role transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockOrganization(ctx, tx, organizationID); err != nil {
		return err
	}

	if expected == domain.RoleOwner && replacement != domain.RoleOwner {
		var owners int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM auth_memberships
			WHERE organization_id = $1 AND role = 'owner'`, organizationID).Scan(&owners); err != nil {
			return fmt.Errorf("count owners: %w", err)
		}
		if owners <= 1 {
			return domain.ErrConflict
		}
	}
	result, err := tx.Exec(ctx, `
		UPDATE auth_memberships
		SET role = $4
		WHERE organization_id = $1 AND user_id = $2 AND role = $3`,
		organizationID, userID, expected, replacement)
	if err != nil {
		return mapWriteError("change member role", err)
	}
	if result.RowsAffected() == 0 {
		return domain.ErrConflict
	}
	if err := insertEvents(ctx, tx, []domain.Event{event}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit role transaction: %w", err)
	}
	return nil
}

func (store *Store) RemoveMember(ctx context.Context, organizationID, userID string, expected domain.Role, event domain.Event) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin removal transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockOrganization(ctx, tx, organizationID); err != nil {
		return err
	}

	if expected == domain.RoleOwner {
		var owners int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM auth_memberships
			WHERE organization_id = $1 AND role = 'owner'`, organizationID).Scan(&owners); err != nil {
			return fmt.Errorf("count owners: %w", err)
		}
		if owners <= 1 {
			return domain.ErrConflict
		}
	}
	result, err := tx.Exec(ctx, `
		DELETE FROM auth_memberships
		WHERE organization_id = $1 AND user_id = $2 AND role = $3`, organizationID, userID, expected)
	if err != nil {
		return fmt.Errorf("remove member: %w", err)
	}
	if result.RowsAffected() == 0 {
		return domain.ErrConflict
	}
	if err := insertEvents(ctx, tx, []domain.Event{event}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit removal transaction: %w", err)
	}
	return nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanInvitation(row rowScanner) (domain.Invitation, error) {
	var invitation domain.Invitation
	if err := row.Scan(
		&invitation.ID,
		&invitation.OrganizationID,
		&invitation.Email,
		&invitation.Role,
		&invitation.Guest,
		&invitation.InvitedBy,
		&invitation.Status,
		&invitation.ExpiresAt,
		&invitation.CreatedAt,
		&invitation.AcceptedAt,
	); err != nil {
		return domain.Invitation{}, err
	}
	return invitation, nil
}

// checkMemberLimit refuses one more member when the organization is at the
// limit billing set for it. Inviting counts pending invitations toward the
// limit, given the time they are judged at; accepting counts only members, as
// the invitation being accepted was counted when it was sent.
func checkMemberLimit(ctx context.Context, tx pgx.Tx, organizationID string, pendingAt *time.Time) error {
	var limit *int
	err := tx.QueryRow(ctx, `SELECT member_limit FROM auth_organization_entitlements WHERE organization_id = $1`, organizationID).Scan(&limit)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read member limit: %w", err)
	}
	if limit == nil {
		return nil
	}

	var used int
	if pendingAt == nil {
		err = tx.QueryRow(ctx, `SELECT count(*) FROM auth_memberships WHERE organization_id = $1`, organizationID).Scan(&used)
	} else {
		err = tx.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM auth_memberships WHERE organization_id = $1)
			     + (SELECT count(*) FROM auth_invitations
			        WHERE organization_id = $1 AND status = 'pending' AND expires_at > $2)`,
			organizationID, *pendingAt).Scan(&used)
	}
	if err != nil {
		return fmt.Errorf("count members: %w", err)
	}
	if used >= *limit {
		return domain.ErrLimitReached
	}
	return nil
}

// ApplyEntitlements records the member limit billing set for an organization.
// A revision no newer than the stored one changes nothing, and entitlements
// for an organization that no longer exists are dropped.
func (store *Store) ApplyEntitlements(ctx context.Context, organizationID string, revision int64, memberLimit *int, changedAt time.Time) error {
	_, err := store.pool.Exec(ctx, `
		INSERT INTO auth_organization_entitlements (organization_id, revision, member_limit, updated_at)
		SELECT $1, $2, $3, $4
		WHERE EXISTS (SELECT 1 FROM auth_organizations WHERE id = $1)
		ON CONFLICT (organization_id) DO UPDATE
		SET revision = EXCLUDED.revision,
		    member_limit = EXCLUDED.member_limit,
		    updated_at = EXCLUDED.updated_at
		WHERE auth_organization_entitlements.revision < EXCLUDED.revision`,
		organizationID, revision, memberLimit, changedAt)
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23503" {
		return nil
	}
	if err != nil {
		return fmt.Errorf("apply entitlements: %w", err)
	}
	return nil
}

func lockOrganization(ctx context.Context, tx pgx.Tx, organizationID string) error {
	var lockedID string
	err := tx.QueryRow(ctx, `SELECT id FROM auth_organizations WHERE id = $1 FOR UPDATE`, organizationID).Scan(&lockedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock organization: %w", err)
	}
	return nil
}
