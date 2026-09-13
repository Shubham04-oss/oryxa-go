package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

func (store *Store) CreateOrganization(ctx context.Context, organization domain.Organization, owner domain.Membership, events []domain.Event) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin organization transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `
		INSERT INTO auth_organizations (id, name, slug, created_by, created_at, updated_at)
		VALUES ($1, $2, lower($3), $4, $5, $5)`,
		organization.ID, organization.Name, organization.Slug, organization.CreatedBy, organization.CreatedAt)
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
		SELECT o.id, o.name, o.slug, o.created_by, o.created_at, o.updated_at
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
		SELECT id, name, slug, created_by, created_at, updated_at
		FROM auth_organizations WHERE id = $1`, organizationID).
		Scan(
			&organization.ID,
			&organization.Name,
			&organization.Slug,
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
		SET name = $2, slug = lower($3), updated_at = $4
		WHERE id = $1`, organization.ID, organization.Name, organization.Slug, organization.UpdatedAt)
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

func (store *Store) DeleteOrganization(ctx context.Context, organizationID string, event domain.Event) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin organization deletion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	result, err := tx.Exec(ctx, `DELETE FROM auth_organizations WHERE id = $1`, organizationID)
	if err != nil {
		return fmt.Errorf("delete organization: %w", err)
	}
	if result.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	if err := insertEvents(ctx, tx, []domain.Event{event}); err != nil {
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
	rows, err := store.pool.Query(ctx, `
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

	_, err = tx.Exec(ctx, `
		UPDATE auth_invitations
		SET status = 'expired'
		WHERE organization_id = $1 AND lower(email) = lower($2)
		  AND status = 'pending' AND expires_at <= $3`,
		invitation.OrganizationID, invitation.Email, invitation.CreatedAt)
	if err != nil {
		return fmt.Errorf("expire prior invitations: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO auth_invitations
		    (id, organization_id, email, role, token_hash, invited_by, status, expires_at, created_at)
		VALUES ($1, $2, lower($3), $4, $5, $6, $7, $8, $9)`,
		invitation.ID,
		invitation.OrganizationID,
		invitation.Email,
		invitation.Role,
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
		SELECT id, organization_id, email, role, invited_by, status, expires_at, created_at, accepted_at
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
		SELECT id, organization_id, email, role, invited_by, status, expires_at, created_at, accepted_at
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
		SELECT id, organization_id, email, role, invited_by, status, expires_at, created_at, accepted_at
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

func (store *Store) AcceptInvitation(ctx context.Context, tokenHash string, user domain.User, membership domain.Membership, event domain.Event) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin acceptance transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var email string
	var status domain.InvitationStatus
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `
		SELECT email, status, expires_at
		FROM auth_invitations
		WHERE token_hash = $1
		FOR UPDATE`, tokenHash).Scan(&email, &status, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock invitation: %w", err)
	}
	if status != domain.InvitationPending {
		return domain.ErrConflict
	}
	if !expiresAt.After(membership.JoinedAt) {
		if _, err := tx.Exec(ctx, `UPDATE auth_invitations SET status = 'expired' WHERE token_hash = $1`, tokenHash); err != nil {
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
		WHERE token_hash = $1`, tokenHash, membership.JoinedAt, user.ID)
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
