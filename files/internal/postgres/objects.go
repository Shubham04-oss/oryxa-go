package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/Shubham04-oss/oryxa-go/files/internal/domain"
)

const objectColumns = `id, organization_id, scope, COALESCE(team_id, '') AS team_id, COALESCE(member_id, '') AS member_id, filename, content_type, purpose, size_bytes, sha256, created_by, created_at, expires_at`

func (store *Store) UpsertOrganization(ctx context.Context, organizationID, name string, at time.Time) error {
	if organizationID == "" {
		return fmt.Errorf("organization id is required: %w", domain.ErrInvalid)
	}
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO files_organizations (organization_id, name, name_updated_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (organization_id) DO UPDATE SET
		    name = CASE
		        WHEN files_organizations.deleted_at IS NOT NULL THEN files_organizations.name
		        WHEN $3::timestamptz IS NULL OR files_organizations.name_updated_at IS NULL OR $3::timestamptz >= files_organizations.name_updated_at THEN EXCLUDED.name
		        ELSE files_organizations.name
		    END,
		    name_updated_at = CASE
		        WHEN files_organizations.deleted_at IS NOT NULL THEN files_organizations.name_updated_at
		        WHEN $3::timestamptz IS NULL THEN files_organizations.name_updated_at
		        WHEN files_organizations.name_updated_at IS NULL OR $3::timestamptz >= files_organizations.name_updated_at THEN $3::timestamptz
		        ELSE files_organizations.name_updated_at
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
		FROM files_organizations WHERE organization_id = $1`, organizationID)
	if isNoRows(err) {
		return domain.Organization{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Organization{}, fmt.Errorf("get organization: %w", err)
	}
	return organization, nil
}

func (store *Store) CreateObject(ctx context.Context, organizationName string, object domain.Object, event domain.Event) error {
	return store.inTx(ctx, func(tx *sqlx.Tx) error {
		if err := upsertOrganizationTx(ctx, tx, object.OrganizationID, organizationName, object.CreatedAt); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO files_objects (
			    id, organization_id, scope, team_id, member_id, filename, content_type, purpose,
			    size_bytes, sha256, created_by, created_at, expires_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
			object.ID, object.OrganizationID, object.Scope, nullableString(object.TeamID), nullableString(object.MemberID),
			object.Filename, object.ContentType, object.Purpose, object.SizeBytes, object.SHA256,
			object.CreatedBy, object.CreatedAt, object.ExpiresAt,
		); err != nil {
			return mapWriteError("create object", err)
		}
		return insertEvents(ctx, tx, []domain.Event{event})
	})
}

func (store *Store) Object(ctx context.Context, organizationID, fileID string, now time.Time) (domain.Object, error) {
	var object domain.Object
	err := store.db.GetContext(ctx, &object, `
		SELECT `+objectColumns+`
		FROM files_objects
		WHERE organization_id = $1 AND id = $2 AND expires_at > $3`, organizationID, fileID, now)
	if isNoRows(err) {
		return domain.Object{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Object{}, fmt.Errorf("get object: %w", err)
	}
	return object, nil
}

func (store *Store) Objects(ctx context.Context, organizationID string, now time.Time, query domain.ObjectQuery) ([]domain.Object, error) {
	var objects []domain.Object
	sql, args := listQuery(organizationID, now, query)
	if err := store.db.SelectContext(ctx, &objects, sql, args...); err != nil {
		return nil, fmt.Errorf("list objects: %w", err)
	}
	if objects == nil {
		objects = []domain.Object{}
	}
	return objects, nil
}

func listQuery(organizationID string, now time.Time, query domain.ObjectQuery) (string, []any) {
	sql := `
		SELECT ` + objectColumns + `
		FROM files_objects
		WHERE organization_id = $1 AND expires_at > $2`
	args := []any{organizationID, now}
	if query.Purpose != "" {
		args = append(args, query.Purpose)
		sql += fmt.Sprintf(` AND purpose = $%d`, len(args))
	}
	if !query.CreatedFrom.IsZero() {
		args = append(args, query.CreatedFrom)
		sql += fmt.Sprintf(` AND created_at >= $%d`, len(args))
	}
	if !query.CreatedTo.IsZero() {
		args = append(args, query.CreatedTo)
		sql += fmt.Sprintf(` AND created_at < $%d`, len(args))
	}
	switch query.Scope {
	case domain.ScopeTeam:
		args = append(args, query.TeamID)
		sql += fmt.Sprintf(` AND scope = 'team' AND team_id = $%d`, len(args))
	case domain.ScopeMember:
		sql += ` AND scope = 'member'`
		if !query.Admin {
			args = append(args, query.MemberID)
			sql += fmt.Sprintf(` AND member_id = $%d`, len(args))
		}
	case domain.ScopeOrganization:
		sql += ` AND scope = 'organization'`
		if !query.Admin {
			sql += ` AND purpose <> 'billing.attachment'`
		}
	default:
		sql += ` AND ((scope = 'member'`
		if !query.Admin {
			args = append(args, query.MemberID)
			sql += fmt.Sprintf(` AND member_id = $%d`, len(args))
		}
		sql += `) OR (scope = 'organization'`
		if !query.Admin {
			sql += ` AND purpose <> 'billing.attachment'`
		}
		sql += `))`
	}
	sql += ` ORDER BY created_at DESC, id`
	return sql, args
}

func (store *Store) DeleteObject(ctx context.Context, organizationID, fileID string, event domain.Event) error {
	return store.inTx(ctx, func(tx *sqlx.Tx) error {
		result, err := tx.ExecContext(ctx, `
			DELETE FROM files_objects WHERE organization_id = $1 AND id = $2`, organizationID, fileID)
		if err != nil {
			return fmt.Errorf("delete object: %w", err)
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			return domain.ErrNotFound
		}
		return insertEvents(ctx, tx, []domain.Event{event})
	})
}

func (store *Store) DeleteMemberScope(ctx context.Context, organizationID, memberID string, emit func(domain.Object) (domain.Event, error)) error {
	return store.inTx(ctx, func(tx *sqlx.Tx) error {
		var objects []domain.Object
		if err := tx.SelectContext(ctx, &objects, `
			SELECT `+objectColumns+`
			FROM files_objects
			WHERE organization_id = $1 AND scope = 'member' AND member_id = $2
			ORDER BY id
			FOR UPDATE`, organizationID, memberID); err != nil {
			return fmt.Errorf("list member objects: %w", err)
		}
		var events []domain.Event
		for _, object := range objects {
			event, err := emit(object)
			if err != nil {
				return err
			}
			events = append(events, event)
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM files_objects
			WHERE organization_id = $1 AND scope = 'member' AND member_id = $2`, organizationID, memberID); err != nil {
			return fmt.Errorf("delete member objects: %w", err)
		}
		return insertEvents(ctx, tx, events)
	})
}

func (store *Store) DeleteTeamFiles(ctx context.Context, organizationID, teamID string, emit func(domain.Object) (domain.Event, error)) error {
	return store.inTx(ctx, func(tx *sqlx.Tx) error {
		var objects []domain.Object
		if err := tx.SelectContext(ctx, &objects, `
			SELECT `+objectColumns+`
			FROM files_objects
			WHERE organization_id = $1 AND scope = 'team' AND team_id = $2
			ORDER BY id
			FOR UPDATE`, organizationID, teamID); err != nil {
			return fmt.Errorf("list team objects: %w", err)
		}
		var events []domain.Event
		for _, object := range objects {
			event, err := emit(object)
			if err != nil {
				return err
			}
			events = append(events, event)
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM files_objects
			WHERE organization_id = $1 AND scope = 'team' AND team_id = $2`, organizationID, teamID); err != nil {
			return fmt.Errorf("delete team objects: %w", err)
		}
		return insertEvents(ctx, tx, events)
	})
}

func (store *Store) DeleteOrganizationFiles(ctx context.Context, organizationID string, emit func(domain.Object) (domain.Event, error)) error {
	return store.inTx(ctx, func(tx *sqlx.Tx) error {
		var objects []domain.Object
		if err := tx.SelectContext(ctx, &objects, `
			SELECT `+objectColumns+`
			FROM files_objects
			WHERE organization_id = $1
			ORDER BY id
			FOR UPDATE`, organizationID); err != nil {
			return fmt.Errorf("list organization objects: %w", err)
		}
		var events []domain.Event
		for _, object := range objects {
			event, err := emit(object)
			if err != nil {
				return err
			}
			events = append(events, event)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM files_objects WHERE organization_id = $1`, organizationID); err != nil {
			return fmt.Errorf("delete organization objects: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE files_organizations
			SET deleted_at = now(), updated_at = now()
			WHERE organization_id = $1`, organizationID); err != nil {
			return fmt.Errorf("mark organization deleted: %w", err)
		}
		return insertEvents(ctx, tx, events)
	})
}

func upsertOrganizationTx(ctx context.Context, tx *sqlx.Tx, organizationID, name string, at time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO files_organizations (organization_id, name, name_updated_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (organization_id) DO UPDATE SET
		    name = CASE
		        WHEN files_organizations.deleted_at IS NOT NULL THEN files_organizations.name
		        WHEN $3::timestamptz IS NULL OR files_organizations.name_updated_at IS NULL OR $3::timestamptz >= files_organizations.name_updated_at THEN EXCLUDED.name
		        ELSE files_organizations.name
		    END,
		    deleted_at = CASE
		        WHEN files_organizations.deleted_at IS NOT NULL THEN NULL
		        ELSE files_organizations.deleted_at
		    END,
		    name_updated_at = CASE
		        WHEN $3::timestamptz IS NULL THEN files_organizations.name_updated_at
		        WHEN files_organizations.name_updated_at IS NULL OR $3::timestamptz >= files_organizations.name_updated_at THEN $3::timestamptz
		        ELSE files_organizations.name_updated_at
		    END,
		    updated_at = now()`, organizationID, name, nullableTime(at))
	if err != nil {
		return fmt.Errorf("upsert organization: %w", err)
	}
	return nil
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
