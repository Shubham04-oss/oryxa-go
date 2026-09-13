package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/Shubham04-oss/oryxa-go/teams/internal/domain"
)

const teamColumns = `id, organization_id, name, path::text AS path, created_by, created_at, updated_at`

const membershipColumns = `team_id, organization_id, user_id, email, display_name, role, notify_email, joined_at`

func (store *Store) UpsertOrganization(ctx context.Context, organizationID, name string, at time.Time) error {
	if organizationID == "" {
		return fmt.Errorf("organization id is required: %w", domain.ErrInvalid)
	}
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO teams_organizations (organization_id, name, name_updated_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (organization_id) DO UPDATE SET
		    name = CASE
		        WHEN teams_organizations.deleted_at IS NOT NULL THEN teams_organizations.name
		        WHEN $3::timestamptz IS NULL OR teams_organizations.name_updated_at IS NULL OR $3::timestamptz >= teams_organizations.name_updated_at THEN EXCLUDED.name
		        ELSE teams_organizations.name
		    END,
		    name_updated_at = CASE
		        WHEN teams_organizations.deleted_at IS NOT NULL THEN teams_organizations.name_updated_at
		        WHEN $3::timestamptz IS NULL THEN teams_organizations.name_updated_at
		        WHEN teams_organizations.name_updated_at IS NULL OR $3::timestamptz >= teams_organizations.name_updated_at THEN $3::timestamptz
		        ELSE teams_organizations.name_updated_at
		    END,
		    updated_at = now()`, organizationID, name, nullableTime(at))
	if err != nil {
		return fmt.Errorf("upsert organization: %w", err)
	}
	return nil
}

func (store *Store) Organization(ctx context.Context, organizationID string) (domain.Organization, error) {
	var organization domain.Organization
	err := store.db.GetContext(ctx, &organization, `
		SELECT organization_id, name, deleted_at IS NOT NULL AS deleted
		FROM teams_organizations WHERE organization_id = $1`, organizationID)
	if isNoRows(err) {
		return domain.Organization{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Organization{}, fmt.Errorf("get organization: %w", err)
	}
	return organization, nil
}

func (store *Store) CreateTeam(ctx context.Context, organizationName string, team domain.Team, creator domain.Membership, events []domain.Event) error {
	if team.Path == "" {
		team.Path = domain.TeamPath(team.OrganizationID, team.ID)
	}
	err := store.inTx(ctx, func(tx *sqlx.Tx) error {
		if err := ensureActiveOrganization(ctx, tx, team.OrganizationID, organizationName); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO teams (id, organization_id, name, path, created_by, created_at, updated_at)
			VALUES ($1, $2, $3, $4::ltree, $5, $6, $7)`,
			team.ID, team.OrganizationID, team.Name, team.Path, team.CreatedBy, team.CreatedAt, team.UpdatedAt)
		if err != nil {
			return mapWriteError("insert team", err)
		}
		if err := insertMembership(ctx, tx, creator); err != nil {
			return err
		}
		return insertEvents(ctx, tx, events)
	})
	if err != nil {
		return fmt.Errorf("create team: %w", err)
	}
	return nil
}

func (store *Store) UpdateTeam(ctx context.Context, team domain.Team, event domain.Event) error {
	return store.inTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := lockTeam(ctx, tx, team.OrganizationID, team.ID); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE teams SET name = $3, updated_at = $4
			WHERE id = $1 AND organization_id = $2`, team.ID, team.OrganizationID, team.Name, team.UpdatedAt)
		if err != nil {
			return mapWriteError("update team", err)
		}
		if rows, _ := result.RowsAffected(); rows == 0 {
			return domain.ErrNotFound
		}
		return insertEvents(ctx, tx, []domain.Event{event})
	})
}

func (store *Store) DeleteTeam(ctx context.Context, organizationID, teamID string, events []domain.Event) error {
	return store.inTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := lockTeam(ctx, tx, organizationID, teamID); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM teams WHERE id = $1 AND organization_id = $2`, teamID, organizationID)
		if err != nil {
			return fmt.Errorf("delete team: %w", err)
		}
		if rows, _ := result.RowsAffected(); rows == 0 {
			return domain.ErrNotFound
		}
		return insertEvents(ctx, tx, events)
	})
}

func (store *Store) Team(ctx context.Context, organizationID, teamID string) (domain.Team, error) {
	return getTeam(ctx, store.db, `
		SELECT `+teamColumns+` FROM teams WHERE id = $1 AND organization_id = $2`, teamID, organizationID)
}

func (store *Store) Teams(ctx context.Context, organizationID string) ([]domain.Team, error) {
	return listTeams(ctx, store.db, `
		SELECT `+teamColumns+` FROM teams
		WHERE organization_id = $1
		ORDER BY lower(name), id`, organizationID)
}

func (store *Store) TeamsForUser(ctx context.Context, organizationID, userID string) ([]domain.Team, error) {
	return listTeams(ctx, store.db, `
		SELECT t.id, t.organization_id, t.name, t.path::text AS path, t.created_by, t.created_at, t.updated_at
		FROM teams t
		JOIN teams_memberships m ON m.team_id = t.id
		WHERE t.organization_id = $1 AND m.user_id = $2
		ORDER BY lower(t.name), t.id`, organizationID, userID)
}

func (store *Store) Membership(ctx context.Context, teamID, userID string) (domain.Membership, error) {
	var membership domain.Membership
	err := store.db.GetContext(ctx, &membership, `
		SELECT `+membershipColumns+` FROM teams_memberships
		WHERE team_id = $1 AND user_id = $2`, teamID, userID)
	if isNoRows(err) {
		return domain.Membership{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Membership{}, fmt.Errorf("get membership: %w", err)
	}
	membership.JoinedAt = membership.JoinedAt.UTC()
	return membership, nil
}

func (store *Store) Members(ctx context.Context, teamID string) ([]domain.Membership, error) {
	return listMembers(ctx, store.db, teamID)
}

func (store *Store) AddMember(ctx context.Context, membership domain.Membership, event domain.Event) error {
	return store.inTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := lockTeam(ctx, tx, membership.OrganizationID, membership.TeamID); err != nil {
			return err
		}
		if err := insertMembership(ctx, tx, membership); err != nil {
			return err
		}
		return insertEvents(ctx, tx, []domain.Event{event})
	})
}

func (store *Store) ChangeMemberRole(ctx context.Context, teamID, userID string, expected, replacement domain.Role, event domain.Event) error {
	return store.inTx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.GetContext(ctx, new(string), `SELECT organization_id FROM teams WHERE id = $1 FOR UPDATE`, teamID); err != nil {
			if isNoRows(err) {
				return domain.ErrNotFound
			}
			return fmt.Errorf("lock team: %w", err)
		}
		if err := protectLastAdmin(ctx, tx, teamID, expected, replacement); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE teams_memberships SET role = $3
			WHERE team_id = $1 AND user_id = $2 AND role = $4`, teamID, userID, replacement, expected)
		if err != nil {
			return fmt.Errorf("change member role: %w", err)
		}
		if rows, _ := result.RowsAffected(); rows == 0 {
			return domain.ErrConflict
		}
		return insertEvents(ctx, tx, []domain.Event{event})
	})
}

func (store *Store) RemoveMember(ctx context.Context, teamID, userID string, expected domain.Role, event domain.Event) error {
	return store.inTx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.GetContext(ctx, new(string), `SELECT organization_id FROM teams WHERE id = $1 FOR UPDATE`, teamID); err != nil {
			if isNoRows(err) {
				return domain.ErrNotFound
			}
			return fmt.Errorf("lock team: %w", err)
		}
		if err := protectLastAdmin(ctx, tx, teamID, expected, ""); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `
			DELETE FROM teams_memberships WHERE team_id = $1 AND user_id = $2 AND role = $3`, teamID, userID, expected)
		if err != nil {
			return fmt.Errorf("remove member: %w", err)
		}
		if rows, _ := result.RowsAffected(); rows == 0 {
			return domain.ErrConflict
		}
		return insertEvents(ctx, tx, []domain.Event{event})
	})
}

func (store *Store) RemoveUserFromOrganization(ctx context.Context, organizationID, userID string, removed func(domain.Team, domain.Membership) (domain.Event, error)) error {
	return store.inTx(ctx, func(tx *sqlx.Tx) error {
		if err := lockOrganization(ctx, tx, organizationID); err != nil {
			if err == domain.ErrNotFound {
				return nil
			}
			return err
		}
		var rows []userTeamRow
		if err := tx.SelectContext(ctx, &rows, `
			SELECT t.id AS team_id, t.organization_id, t.name, t.path::text AS path, t.created_by, t.created_at, t.updated_at,
			       m.user_id, m.email, m.display_name, m.role, m.notify_email, m.joined_at
			FROM teams_memberships m
			JOIN teams t ON t.id = m.team_id
			WHERE m.organization_id = $1 AND m.user_id = $2
			FOR UPDATE OF m`, organizationID, userID); err != nil {
			return fmt.Errorf("list user teams: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		events := make([]domain.Event, 0, len(rows))
		for _, row := range rows {
			team, membership := row.asDomain()
			event, err := removed(team, membership)
			if err != nil {
				return err
			}
			events = append(events, event)
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM teams_memberships WHERE organization_id = $1 AND user_id = $2`, organizationID, userID); err != nil {
			return fmt.Errorf("delete user memberships: %w", err)
		}
		return insertEvents(ctx, tx, events)
	})
}

func (store *Store) DeleteOrganizationTeams(ctx context.Context, organizationID string, emit func(domain.Team, []domain.Membership) ([]domain.Event, error)) error {
	return store.inTx(ctx, func(tx *sqlx.Tx) error {
		if err := lockOrganization(ctx, tx, organizationID); err != nil {
			if err == domain.ErrNotFound {
				return nil
			}
			return err
		}
		var deleted bool
		if err := tx.GetContext(ctx, &deleted, `SELECT deleted_at IS NOT NULL FROM teams_organizations WHERE organization_id = $1`, organizationID); err != nil {
			return fmt.Errorf("read organization: %w", err)
		}
		if deleted {
			return nil
		}
		teams, err := listTeams(ctx, tx, `SELECT `+teamColumns+` FROM teams WHERE organization_id = $1 ORDER BY id`, organizationID)
		if err != nil {
			return err
		}
		var events []domain.Event
		for _, team := range teams {
			members, err := listMembers(ctx, tx, team.ID)
			if err != nil {
				return err
			}
			emitted, err := emit(team, members)
			if err != nil {
				return err
			}
			events = append(events, emitted...)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM teams WHERE organization_id = $1`, organizationID); err != nil {
			return fmt.Errorf("delete organization teams: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE teams_organizations SET deleted_at = coalesce(deleted_at, now()), updated_at = now()
			WHERE organization_id = $1`, organizationID); err != nil {
			return fmt.Errorf("mark organization deleted: %w", err)
		}
		return insertEvents(ctx, tx, events)
	})
}

func ensureActiveOrganization(ctx context.Context, tx execer, organizationID, name string) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO teams_organizations (organization_id, name)
		VALUES ($1, $2)
		ON CONFLICT (organization_id) DO NOTHING`, organizationID, name); err != nil {
		return fmt.Errorf("record organization: %w", err)
	}
	var deleted bool
	if err := tx.GetContext(ctx, &deleted, `
		SELECT deleted_at IS NOT NULL FROM teams_organizations WHERE organization_id = $1 FOR UPDATE`, organizationID); err != nil {
		return fmt.Errorf("lock organization: %w", err)
	}
	if deleted {
		return domain.ErrNotFound
	}
	if name != "" {
		if _, err := tx.ExecContext(ctx, `
			UPDATE teams_organizations SET name = $2, updated_at = now()
			WHERE organization_id = $1 AND name = ''`, organizationID, name); err != nil {
			return fmt.Errorf("name organization: %w", err)
		}
	}
	return nil
}

func lockOrganization(ctx context.Context, tx execer, organizationID string) error {
	var id string
	err := tx.GetContext(ctx, &id, `SELECT organization_id FROM teams_organizations WHERE organization_id = $1 FOR UPDATE`, organizationID)
	if isNoRows(err) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock organization: %w", err)
	}
	return nil
}

func lockTeam(ctx context.Context, tx execer, organizationID, teamID string) (domain.Team, error) {
	return getTeam(ctx, tx, `
		SELECT `+teamColumns+` FROM teams WHERE id = $1 AND organization_id = $2 FOR UPDATE`, teamID, organizationID)
}

func protectLastAdmin(ctx context.Context, tx execer, teamID string, current, next domain.Role) error {
	if current != domain.RoleAdmin || next == domain.RoleAdmin {
		return nil
	}
	var admins int
	if err := tx.GetContext(ctx, &admins, `
		SELECT count(*) FROM teams_memberships WHERE team_id = $1 AND role = 'admin'`, teamID); err != nil {
		return fmt.Errorf("count team admins: %w", err)
	}
	if admins <= 1 {
		return domain.ErrConflict
	}
	return nil
}

func (store *Store) SetNotifyEmail(ctx context.Context, teamID, userID string, notifyEmail bool) error {
	result, err := store.db.ExecContext(ctx, `
		UPDATE teams_memberships SET notify_email = $3
		WHERE team_id = $1 AND user_id = $2`, teamID, userID, notifyEmail)
	if err != nil {
		return fmt.Errorf("set notify email: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func insertMembership(ctx context.Context, tx execer, membership domain.Membership) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO teams_memberships (team_id, organization_id, user_id, email, display_name, role, notify_email, joined_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		membership.TeamID, membership.OrganizationID, membership.UserID, membership.Email, membership.DisplayName, membership.Role, membership.NotifyEmail, membership.JoinedAt)
	if err != nil {
		return mapWriteError("insert membership", err)
	}
	return nil
}

func getTeam(ctx context.Context, ext execer, query string, args ...any) (domain.Team, error) {
	var team domain.Team
	err := ext.GetContext(ctx, &team, query, args...)
	if isNoRows(err) {
		return domain.Team{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Team{}, fmt.Errorf("get team: %w", err)
	}
	team.CreatedAt = team.CreatedAt.UTC()
	team.UpdatedAt = team.UpdatedAt.UTC()
	return team, nil
}

func listTeams(ctx context.Context, ext execer, query string, args ...any) ([]domain.Team, error) {
	teams := make([]domain.Team, 0)
	if err := ext.SelectContext(ctx, &teams, query, args...); err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	for i := range teams {
		teams[i].CreatedAt = teams[i].CreatedAt.UTC()
		teams[i].UpdatedAt = teams[i].UpdatedAt.UTC()
	}
	return teams, nil
}

func listMembers(ctx context.Context, ext execer, teamID string) ([]domain.Membership, error) {
	members := make([]domain.Membership, 0)
	if err := ext.SelectContext(ctx, &members, `
		SELECT `+membershipColumns+` FROM teams_memberships
		WHERE team_id = $1
		ORDER BY role DESC, lower(email), user_id`, teamID); err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	for i := range members {
		members[i].JoinedAt = members[i].JoinedAt.UTC()
	}
	return members, nil
}

type userTeamRow struct {
	TeamID         string      `db:"team_id"`
	OrganizationID string      `db:"organization_id"`
	Name           string      `db:"name"`
	Path           string      `db:"path"`
	CreatedBy      string      `db:"created_by"`
	CreatedAt      time.Time   `db:"created_at"`
	UpdatedAt      time.Time   `db:"updated_at"`
	UserID         string      `db:"user_id"`
	Email          string      `db:"email"`
	DisplayName    string      `db:"display_name"`
	Role           domain.Role `db:"role"`
	NotifyEmail    bool        `db:"notify_email"`
	JoinedAt       time.Time   `db:"joined_at"`
}

func (row userTeamRow) asDomain() (domain.Team, domain.Membership) {
	return domain.Team{
			ID:             row.TeamID,
			OrganizationID: row.OrganizationID,
			Name:           row.Name,
			Path:           row.Path,
			CreatedBy:      row.CreatedBy,
			CreatedAt:      row.CreatedAt.UTC(),
			UpdatedAt:      row.UpdatedAt.UTC(),
		}, domain.Membership{
			TeamID:         row.TeamID,
			OrganizationID: row.OrganizationID,
			UserID:         row.UserID,
			Email:          row.Email,
			DisplayName:    row.DisplayName,
			Role:           row.Role,
			NotifyEmail:    row.NotifyEmail,
			JoinedAt:       row.JoinedAt.UTC(),
		}
}

func nullableTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}
