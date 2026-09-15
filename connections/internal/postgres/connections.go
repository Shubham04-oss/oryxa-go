package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// connectionColumns lists a connection row's columns, qualified by table when
// it is not empty. scanConnection reads them in this order.
func connectionColumns(table string) string {
	prefix := ""
	if table != "" {
		prefix = table + "."
	}
	return strings.Join([]string{
		prefix + "id", prefix + "organization_id", prefix + "app_id", prefix + "provider", prefix + "credential_type",
		prefix + "scope", "COALESCE(" + prefix + "team_id, '')", "COALESCE(" + prefix + "member_id, '')", prefix + "usage",
		prefix + "provider_account_id", prefix + "provider_account_name", prefix + "label", prefix + "is_default",
		prefix + "scopes", prefix + "status", prefix + "status_reason", prefix + "status_changed_at", prefix + "last_used_at",
		prefix + "created_by", prefix + "created_at", prefix + "updated_at",
		prefix + "encrypted_credentials", prefix + "credentials_deleted_at", prefix + "version", prefix + "last_reported_at",
	}, ", ")
}

func scanConnection(row pgx.Row, extra ...any) (domain.Connection, error) {
	var connection domain.Connection
	var credentials []byte
	destinations := append([]any{
		&connection.ID, &connection.OrganizationID, &connection.AppID, &connection.Provider, &connection.CredentialType,
		&connection.Scope, &connection.TeamID, &connection.MemberID, &connection.Usage,
		&connection.ProviderAccount.ID, &connection.ProviderAccount.Name, &connection.Label, &connection.Default,
		&connection.Scopes, &connection.Status, &connection.Reason, &connection.StatusChangedAt, &connection.LastUsedAt,
		&connection.CreatedBy, &connection.CreatedAt, &connection.UpdatedAt,
		&credentials, &connection.CredentialsDeletedAt, &connection.Version, &connection.LastReportedAt,
	}, extra...)
	if err := row.Scan(destinations...); err != nil {
		return domain.Connection{}, err
	}
	sealed, err := sealedFrom(credentials)
	if err != nil {
		return domain.Connection{}, err
	}
	connection.EncryptedCredentials = sealed
	return connection, nil
}

const listedFrom = `
	FROM connections c
	JOIN connections_apps a ON a.organization_id = c.organization_id AND a.id = c.app_id
	LEFT JOIN connections_health h ON h.organization_id = c.organization_id AND h.connection_id = c.id`

func listedColumns() string {
	return connectionColumns("c") + `, a.status, COALESCE(h.state, 'unknown'), h.last_checked_at`
}

// scanListedConnection reads a connection with its health and whether its
// app currently blocks it.
func scanListedConnection(row pgx.Row) (domain.Connection, error) {
	var appStatus domain.AppStatus
	var health domain.Health
	var checkedAt *time.Time
	connection, err := scanConnection(row, &appStatus, &health, &checkedAt)
	if err != nil {
		return domain.Connection{}, err
	}
	connection.Health = domain.ConnectionHealth{State: health, CheckedAt: checkedAt}
	if appStatus != domain.AppStatusActive && connection.Status != domain.StatusDisconnected {
		connection.Blocked = "app_disabled"
	}
	return connection, nil
}

// Connection returns a connection in any status.
func (store *Store) Connection(ctx context.Context, organizationID, connectionID string) (domain.Connection, error) {
	connection, err := scanListedConnection(store.pool.QueryRow(ctx, `
		SELECT `+listedColumns()+listedFrom+`
		WHERE c.organization_id = $1 AND c.id = $2`, organizationID, connectionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Connection{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Connection{}, fmt.Errorf("get connection: %w", err)
	}
	return connection, nil
}

// Connections lists live connections; disconnected ones are left out.
func (store *Store) Connections(ctx context.Context, organizationID string, query domain.ConnectionQuery) ([]domain.Connection, error) {
	sql := `SELECT ` + listedColumns() + listedFrom + `
		WHERE c.organization_id = $1 AND c.status <> 'disconnected'`
	args := []any{organizationID}
	bind := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if query.AppID != "" {
		sql += ` AND c.app_id = ` + bind(query.AppID)
	}
	switch query.Scope {
	case domain.ScopeTeam:
		sql += ` AND c.scope = 'team' AND c.team_id = ` + bind(query.TeamID)
	case domain.ScopeOrganization:
		sql += ` AND c.scope = 'organization'`
	case domain.ScopeMember:
		sql += ` AND c.scope = 'member'`
		if !query.Admin {
			sql += ` AND c.member_id = ` + bind(query.MemberID)
		}
	default:
		if query.Admin {
			sql += ` AND c.scope IN ('member', 'organization')`
		} else {
			sql += ` AND (c.scope = 'organization' OR (c.scope = 'member' AND c.member_id = ` + bind(query.MemberID) + `))`
		}
	}
	sql += ` ORDER BY c.created_at, c.id`

	rows, err := store.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	defer rows.Close()
	connections := []domain.Connection{}
	for rows.Next() {
		connection, err := scanListedConnection(rows)
		if err != nil {
			return nil, fmt.Errorf("scan connection: %w", err)
		}
		connections = append(connections, connection)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	return connections, nil
}

// SharesProviderGrant reports whether another connection of the same app and
// provider account still holds credentials, so revoking the provider grant
// behind exceptConnectionID would also cut that connection off. An empty
// exceptConnectionID excludes nothing.
func (store *Store) SharesProviderGrant(ctx context.Context, organizationID, appID, providerAccountID, exceptConnectionID string) (bool, error) {
	var shared bool
	if err := store.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM connections
			WHERE organization_id = $1 AND app_id = $2 AND provider_account_id = $3 AND id <> $4
			  AND credentials_deleted_at IS NULL)`,
		organizationID, appID, providerAccountID, exceptConnectionID).Scan(&shared); err != nil {
		return false, fmt.Errorf("check shared provider grant: %w", err)
	}
	return shared, nil
}

// lockOwner takes a per-owner advisory lock scoped to one app's connections
// in one scope, so two transactions never both decide "no default yet" or
// both swap the default at once.
func lockOwner(ctx context.Context, tx pgx.Tx, organizationID, appID string, scope domain.Scope, teamID, memberID string) error {
	owner := strings.Join([]string{organizationID, appID, string(scope), teamID, memberID}, "\x1f")
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, owner); err != nil {
		return fmt.Errorf("lock connection owner: %w", err)
	}
	return nil
}

// createConnectionTx inserts a new connection, chooses whether it becomes
// its scope's default, and opens its health row. The caller takes the
// per-owner lock first.
func createConnectionTx(ctx context.Context, tx pgx.Tx, connection domain.Connection, credentials []byte, nextCheck time.Time) (domain.Connection, error) {
	usage := connection.Usage
	if usage == "" {
		usage = domain.UsageMembers
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO connections (
			id, organization_id, app_id, provider, credential_type, scope, team_id, member_id, usage,
			provider_account_id, provider_account_name, label, is_default, scopes, status, status_reason,
			status_changed_at, encrypted_credentials, version, created_by, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, false, $13, $14, $15, $16, $17, 1, $18, $19, $20)`,
		connection.ID, connection.OrganizationID, connection.AppID, connection.Provider, connection.CredentialType,
		connection.Scope, nullable(connection.TeamID), nullable(connection.MemberID), usage,
		connection.ProviderAccount.ID, connection.ProviderAccount.Name, connection.Label, stringSlice(connection.Scopes),
		connection.Status, connection.Reason, connection.StatusChangedAt, credentials,
		connection.CreatedBy, connection.CreatedAt, connection.UpdatedAt); err != nil {
		return domain.Connection{}, mapWriteError("create connection", err)
	}
	if err := tx.QueryRow(ctx, `
		UPDATE connections SET is_default = NOT EXISTS (
			SELECT 1 FROM connections other
			WHERE other.organization_id = $1 AND other.app_id = $2 AND other.scope = $3
			  AND COALESCE(other.team_id, '') = $4 AND COALESCE(other.member_id, '') = $5
			  AND other.is_default AND other.id <> $6)
		WHERE organization_id = $1 AND id = $6
		RETURNING is_default`,
		connection.OrganizationID, connection.AppID, connection.Scope, connection.TeamID, connection.MemberID, connection.ID,
	).Scan(&connection.Default); err != nil {
		return domain.Connection{}, mapWriteError("choose default connection", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO connections_health (organization_id, connection_id, next_check_at)
		VALUES ($1, $2, $3)`, connection.OrganizationID, connection.ID, nextCheck); err != nil {
		return domain.Connection{}, mapWriteError("create connection health", err)
	}
	connection.Usage = usage
	connection.Version = 1
	connection.Health = domain.ConnectionHealth{State: domain.HealthUnknown}
	return connection, nil
}

// CompleteConnect consumes a session and creates its connection in one
// transaction, with its health row and event. The first live connection to
// an app in a scope becomes the default.
func (store *Store) CompleteConnect(ctx context.Context, stateHash string, connection domain.Connection, event domain.Event, nextCheck time.Time) (domain.Connection, error) {
	credentials, err := sealedValue(connection.EncryptedCredentials)
	if err != nil {
		return domain.Connection{}, err
	}
	err = store.inTx(ctx, func(tx pgx.Tx) error {
		if err := consumeSession(ctx, tx, stateHash, connection.UpdatedAt); err != nil {
			return err
		}
		// Serialize completions for the same app and owner, so two can never
		// both see "no default yet".
		if err := lockOwner(ctx, tx, connection.OrganizationID, connection.AppID, connection.Scope, connection.TeamID, connection.MemberID); err != nil {
			return err
		}
		created, err := createConnectionTx(ctx, tx, connection, credentials, nextCheck)
		if err != nil {
			return err
		}
		connection = created
		return insertEvents(ctx, tx, event)
	})
	if err != nil {
		return domain.Connection{}, err
	}
	return connection, nil
}

// CreateConnection makes a connection without a session, for key-based and
// client-credentials connections whose identity check already ran. It is
// otherwise exactly CompleteConnect.
func (store *Store) CreateConnection(ctx context.Context, connection domain.Connection, event domain.Event, nextCheck time.Time) (domain.Connection, error) {
	credentials, err := sealedValue(connection.EncryptedCredentials)
	if err != nil {
		return domain.Connection{}, err
	}
	err = store.inTx(ctx, func(tx pgx.Tx) error {
		if err := lockOwner(ctx, tx, connection.OrganizationID, connection.AppID, connection.Scope, connection.TeamID, connection.MemberID); err != nil {
			return err
		}
		created, err := createConnectionTx(ctx, tx, connection, credentials, nextCheck)
		if err != nil {
			return err
		}
		connection = created
		return insertEvents(ctx, tx, event)
	})
	if err != nil {
		return domain.Connection{}, err
	}
	return connection, nil
}

// DisconnectConnection ends a live connection for reason and records its
// event. It needs no version: nothing brings a disconnected connection back,
// so a disconnect wins over any change that raced it. A connection that is
// already disconnected is left alone and reported unchanged.
func (store *Store) DisconnectConnection(ctx context.Context, organizationID, connectionID string, reason domain.DisconnectReason,
	at time.Time, connectionEvent func(domain.Connection) (domain.Event, error)) (bool, error) {
	if !reason.Valid() {
		return false, fmt.Errorf("disconnect reason is not allowed: %w", domain.ErrInvalid)
	}
	var changed []domain.Connection
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		changed, err = changeConnections(ctx, tx, `
			UPDATE connections
			SET status = 'disconnected', status_reason = $3, is_default = false,
			    status_changed_at = $4, updated_at = $4, version = version + 1
			WHERE organization_id = $1 AND id = $2 AND status <> 'disconnected'
			RETURNING `+connectionColumns(""),
			[]any{organizationID, connectionID, string(reason), at}, connectionEvent)
		return err
	})
	if err != nil {
		return false, err
	}
	return len(changed) > 0, nil
}

// DisconnectConnections ends every live connection an owner filter selects:
// a member's own connections, a team's, or with neither set, the whole
// organization's. It is idempotent, since only live rows match.
func (store *Store) DisconnectConnections(ctx context.Context, organizationID string, owner domain.OwnerFilter, reason domain.DisconnectReason,
	at time.Time, connectionEvent func(domain.Connection) (domain.Event, error)) (int, error) {
	if !reason.Valid() {
		return 0, fmt.Errorf("disconnect reason is not allowed: %w", domain.ErrInvalid)
	}
	if owner.MemberID != "" && owner.TeamID != "" {
		return 0, fmt.Errorf("owner filter cannot name both a member and a team: %w", domain.ErrInvalid)
	}
	sql := `
		UPDATE connections
		SET status = 'disconnected', status_reason = $2, is_default = false,
		    status_changed_at = $3, updated_at = $3, version = version + 1
		WHERE organization_id = $1 AND status <> 'disconnected'`
	args := []any{organizationID, string(reason), at}
	switch {
	case owner.MemberID != "":
		sql += ` AND scope = 'member' AND member_id = $4`
		args = append(args, owner.MemberID)
	case owner.TeamID != "":
		sql += ` AND scope = 'team' AND team_id = $4`
		args = append(args, owner.TeamID)
	}
	sql += ` RETURNING ` + connectionColumns("")

	var changed []domain.Connection
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		changed, err = changeConnections(ctx, tx, sql, args, connectionEvent)
		return err
	})
	if err != nil {
		return 0, err
	}
	return len(changed), nil
}

// replaceCredentialsTx updates a connection's credentials and granted scopes
// in place and returns it to active, if its version is still
// expectedVersion. The credentials_deleted_at check is implied by
// status <> 'disconnected' (the schema never leaves credentials deleted on a
// live connection), but is named to match the store's contract.
func replaceCredentialsTx(ctx context.Context, tx pgx.Tx, connection domain.Connection, credentials []byte, expectedVersion int64) (int64, error) {
	var version int64
	err := tx.QueryRow(ctx, `
		UPDATE connections
		SET encrypted_credentials = $3, scopes = $4, status = 'active', status_reason = '',
		    status_changed_at = $5, provider_account_name = $6, updated_at = $7, version = version + 1
		WHERE organization_id = $1 AND id = $2 AND version = $8 AND status <> 'disconnected' AND credentials_deleted_at IS NULL
		RETURNING version`,
		connection.OrganizationID, connection.ID, credentials, stringSlice(connection.Scopes),
		connection.StatusChangedAt, connection.ProviderAccount.Name, connection.UpdatedAt, expectedVersion,
	).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, domain.ErrConflict
	}
	if err != nil {
		return 0, mapWriteError("replace connection credentials", err)
	}
	return version, nil
}

// CompleteReconnect consumes a session and replaces a connection's
// credentials in place, if its version is still expectedVersion.
func (store *Store) CompleteReconnect(ctx context.Context, stateHash string, connection domain.Connection, expectedVersion int64, event domain.Event) (domain.Connection, error) {
	credentials, err := sealedValue(connection.EncryptedCredentials)
	if err != nil {
		return domain.Connection{}, err
	}
	err = store.inTx(ctx, func(tx pgx.Tx) error {
		if err := consumeSession(ctx, tx, stateHash, connection.UpdatedAt); err != nil {
			return err
		}
		version, err := replaceCredentialsTx(ctx, tx, connection, credentials, expectedVersion)
		if err != nil {
			return err
		}
		connection.Version = version
		return insertEvents(ctx, tx, event)
	})
	if err != nil {
		return domain.Connection{}, err
	}
	return connection, nil
}

// ReplaceCredentials replaces a connection's credentials without a session,
// for a key-based connection whose replacement identity check already ran.
// It is otherwise exactly CompleteReconnect.
func (store *Store) ReplaceCredentials(ctx context.Context, connection domain.Connection, expectedVersion int64, event domain.Event) (domain.Connection, error) {
	credentials, err := sealedValue(connection.EncryptedCredentials)
	if err != nil {
		return domain.Connection{}, err
	}
	err = store.inTx(ctx, func(tx pgx.Tx) error {
		version, err := replaceCredentialsTx(ctx, tx, connection, credentials, expectedVersion)
		if err != nil {
			return err
		}
		connection.Version = version
		return insertEvents(ctx, tx, event)
	})
	if err != nil {
		return domain.Connection{}, err
	}
	return connection, nil
}

// UpdateConnection changes a live connection's label or makes it its scope's
// default. It never bumps version, which guards credential updates only.
func (store *Store) UpdateConnection(ctx context.Context, organizationID, connectionID string, change domain.ConnectionChange, at time.Time) (domain.Connection, error) {
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		var appID string
		var scope domain.Scope
		var teamID, memberID string
		var status domain.Status
		err := tx.QueryRow(ctx, `
			SELECT app_id, scope, COALESCE(team_id, ''), COALESCE(member_id, ''), status
			FROM connections
			WHERE organization_id = $1 AND id = $2
			FOR UPDATE`, organizationID, connectionID).Scan(&appID, &scope, &teamID, &memberID, &status)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return domain.ErrNotFound
		case err != nil:
			return fmt.Errorf("lock connection: %w", err)
		case status == domain.StatusDisconnected:
			return domain.ErrNotFound
		}
		if change.MakeDefault {
			// The same per-owner lock CreateConnection takes, so a default
			// chosen for a brand new connection can never race a swap here.
			if err := lockOwner(ctx, tx, organizationID, appID, scope, teamID, memberID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				UPDATE connections SET is_default = false
				WHERE organization_id = $1 AND app_id = $2 AND scope = $3
				  AND COALESCE(team_id, '') = $4 AND COALESCE(member_id, '') = $5 AND is_default AND id <> $6`,
				organizationID, appID, scope, teamID, memberID, connectionID); err != nil {
				return mapWriteError("clear other defaults", err)
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE connections SET label = COALESCE($3, label), is_default = is_default OR $4, updated_at = $5
			WHERE organization_id = $1 AND id = $2`,
			organizationID, connectionID, change.Label, change.MakeDefault, at); err != nil {
			return mapWriteError("update connection", err)
		}
		return nil
	})
	if err != nil {
		return domain.Connection{}, err
	}
	return store.Connection(ctx, organizationID, connectionID)
}

// MarkNeedsReconnect records a permanent credential failure on an active
// connection whose version is still expectedVersion, with one event, and
// clears any renewal lease. A connection that changed since the failure was
// seen, such as one reconnected meanwhile, is left alone: the failure was
// about credentials it no longer holds. Returns whether it changed.
func (store *Store) MarkNeedsReconnect(ctx context.Context, organizationID, connectionID string, expectedVersion int64, reason domain.ReconnectReason, at time.Time,
	connectionEvent func(domain.Connection) (domain.Event, error)) (bool, error) {
	if !reason.Valid() {
		return false, fmt.Errorf("reconnect reason is not allowed: %w", domain.ErrInvalid)
	}
	var changed []domain.Connection
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		changed, err = changeConnections(ctx, tx, `
			UPDATE connections
			SET status = 'needs_reconnect', status_reason = $3, status_changed_at = $4, updated_at = $4,
			    version = version + 1, refresh_lease_token = NULL, refresh_lease_until = NULL
			WHERE organization_id = $1 AND id = $2 AND status = 'active' AND version = $5
			RETURNING `+connectionColumns(""),
			[]any{organizationID, connectionID, string(reason), at, expectedVersion}, connectionEvent)
		return err
	})
	if err != nil {
		return false, err
	}
	return len(changed) > 0, nil
}

// MarkUsed records that a connection issued a token, but only when its last
// use is unknown or stale by more than a minute, so a hot connection does
// not write on every token.
func (store *Store) MarkUsed(ctx context.Context, organizationID, connectionID string, at time.Time) error {
	if _, err := store.pool.Exec(ctx, `
		UPDATE connections SET last_used_at = $3
		WHERE organization_id = $1 AND id = $2 AND (last_used_at IS NULL OR last_used_at < $4)`,
		organizationID, connectionID, at, at.Add(-time.Minute)); err != nil {
		return fmt.Errorf("mark connection used: %w", err)
	}
	return nil
}
