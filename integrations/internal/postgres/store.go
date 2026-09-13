package postgres

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("configure integrations postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect integrations postgres: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Migrate(ctx context.Context) error {
	entries, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list integrations migrations: %w", err)
	}
	sort.Strings(entries)
	for _, entry := range entries {
		contents, err := migrations.ReadFile(entry)
		if err != nil {
			return fmt.Errorf("read integrations migration %s: %w", entry, err)
		}
		if _, err := s.pool.Exec(ctx, string(contents)); err != nil {
			return fmt.Errorf("apply integrations migration %s: %w", entry, err)
		}
	}
	return nil
}

func (s *Store) CreateOAuthApp(ctx context.Context, profile domain.OAuthAppProfile) error {
	sealed, err := json.Marshal(profile.EncryptedSecret)
	if err != nil {
		return fmt.Errorf("encode oauth app secret envelope: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO integrations_oauth_apps (
			id, organization_id, provider, piece_name, piece_version, mode,
			client_id, encrypted_secret, scopes, status, created_by,
			created_at, updated_at, credential_revision
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		profile.ID, profile.OrganizationID, profile.Provider, profile.PieceName,
		profile.PieceVersion, profile.Mode, profile.ClientID, sealed,
		profile.Scopes, profile.Status, profile.CreatedBy, profile.CreatedAt,
		profile.UpdatedAt, profile.CredentialRevision,
	)
	if err != nil {
		return mapWriteError("create oauth app", err)
	}
	return nil
}

func (s *Store) CreateOAuthAppWithEvent(ctx context.Context, profile domain.OAuthAppProfile, event domain.Event) error {
	sealed, err := json.Marshal(profile.EncryptedSecret)
	if err != nil {
		return fmt.Errorf("encode oauth app secret envelope: %w", err)
	}
	detail, err := json.Marshal(event.Data)
	if err != nil {
		return fmt.Errorf("encode oauth app event: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin oauth app transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `
		INSERT INTO integrations_oauth_apps (
			id, organization_id, provider, piece_name, piece_version, mode,
			client_id, encrypted_secret, scopes, status, created_by,
			created_at, updated_at, credential_revision
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		profile.ID, profile.OrganizationID, profile.Provider, profile.PieceName,
		profile.PieceVersion, profile.Mode, profile.ClientID, sealed, profile.Scopes,
		profile.Status, profile.CreatedBy, profile.CreatedAt, profile.UpdatedAt, profile.CredentialRevision)
	if err != nil {
		return mapWriteError("create oauth app", err)
	}
	if err := insertOutbox(ctx, tx, event, detail); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit oauth app transaction: %w", err)
	}
	return nil
}

func (s *Store) UpdateOAuthAppCredentialsWithEvent(ctx context.Context, profile domain.OAuthAppProfile, event domain.Event) error {
	sealed, err := json.Marshal(profile.EncryptedSecret)
	if err != nil {
		return err
	}
	detail, err := json.Marshal(event.Data)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE integrations_oauth_apps
		SET client_id=$3, encrypted_secret=$4, credential_revision=$5, updated_at=$6
		WHERE organization_id=$1 AND id=$2 AND status='active' AND mode='byoc'`,
		profile.OrganizationID, profile.ID, profile.ClientID, sealed, profile.CredentialRevision, profile.UpdatedAt)
	if err != nil {
		return fmt.Errorf("rotate oauth app: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	if err := insertOutbox(ctx, tx, event, detail); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit oauth app rotation: %w", err)
	}
	return nil
}

func (s *Store) DisableOAuthAppWithEvent(ctx context.Context, organizationID, profileID string, now time.Time, event domain.Event) error {
	detail, err := json.Marshal(event.Data)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE integrations_oauth_apps SET status='disabled', updated_at=$3
		WHERE organization_id=$1 AND id=$2 AND status='active'`, organizationID, profileID, now)
	if err != nil {
		return fmt.Errorf("disable oauth app: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	if _, err := tx.Exec(ctx, `UPDATE integrations_triggers
		SET status='disabled', version=version+1, updated_at=$3
		WHERE organization_id=$1 AND profile_id=$2 AND status='active'`, organizationID, profileID, now); err != nil {
		return fmt.Errorf("disable oauth app triggers: %w", err)
	}
	if err := insertOutbox(ctx, tx, event, detail); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit oauth app disable: %w", err)
	}
	return nil
}

func (s *Store) OAuthApp(ctx context.Context, organizationID, profileID string) (domain.OAuthAppProfile, error) {
	var profile domain.OAuthAppProfile
	var sealed []byte
	err := s.pool.QueryRow(ctx, `
		SELECT id, organization_id, provider, piece_name, piece_version, mode,
		       client_id, encrypted_secret, scopes, status, created_by,
		       created_at, updated_at, credential_revision
		FROM integrations_oauth_apps
		WHERE organization_id = $1 AND id = $2`, organizationID, profileID).
		Scan(
			&profile.ID, &profile.OrganizationID, &profile.Provider, &profile.PieceName,
			&profile.PieceVersion, &profile.Mode, &profile.ClientID, &sealed,
			&profile.Scopes, &profile.Status, &profile.CreatedBy, &profile.CreatedAt,
			&profile.UpdatedAt, &profile.CredentialRevision,
		)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.OAuthAppProfile{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.OAuthAppProfile{}, fmt.Errorf("get oauth app: %w", err)
	}
	if err := json.Unmarshal(sealed, &profile.EncryptedSecret); err != nil {
		return domain.OAuthAppProfile{}, fmt.Errorf("decode oauth app secret envelope: %w", err)
	}
	return profile, nil
}

func (s *Store) CreateConnectionWithEvent(ctx context.Context, connection domain.Connection, event domain.Event) error {
	if connection.OrganizationID == "" || connection.ID == "" || event.EventID == "" || event.OrganizationID != connection.OrganizationID {
		return errors.New("connection and matching event identity are required")
	}
	credentials, err := json.Marshal(connection.EncryptedCredentials)
	if err != nil {
		return fmt.Errorf("encode connection credential envelope: %w", err)
	}
	detail, err := json.Marshal(event.Data)
	if err != nil {
		return fmt.Errorf("encode connection event: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin connection transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `
		INSERT INTO integrations_connections (
			id, organization_id, member_id, profile_id, provider, piece_name,
			piece_version, provider_subject_id, provider_account_id,
			provider_account_name, provider_tenant_id, provider_tenant_name,
			granted_scopes, encrypted_credentials, status, version,
			created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9,
			$10, $11, $12, $13, $14, $15, $16, $17, $18
		)`,
		connection.ID, connection.OrganizationID, connection.MemberID,
		connection.ProfileID, connection.Provider, connection.PieceName,
		connection.PieceVersion, connection.Identity.SubjectID,
		connection.Identity.AccountID, connection.Identity.AccountName,
		connection.Identity.TenantID, connection.Identity.TenantName,
		connection.GrantedScopes, credentials, connection.Status,
		connection.Version, connection.CreatedAt, connection.UpdatedAt,
	)
	if err != nil {
		return mapWriteError("create connection", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO integrations_outbox (
			id, source, detail_type, version, organization_id, detail, occurred_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		event.EventID, event.Source, event.DetailType, event.Version,
		event.OrganizationID, detail, event.OccurredAt,
	)
	if err != nil {
		return mapWriteError("create connection event", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit connection transaction: %w", err)
	}
	return nil
}

func (s *Store) DisconnectConnectionWithEvent(ctx context.Context, organizationID, memberID, connectionID string, now time.Time, event domain.Event) error {
	detail, err := json.Marshal(event.Data)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE integrations_connections
		SET status='disconnected', version=version+1, updated_at=$4
		WHERE organization_id=$1 AND member_id=$2 AND id=$3 AND status='active'`, organizationID, memberID, connectionID, now)
	if err != nil {
		return fmt.Errorf("disconnect integration: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	// A disconnected connection must stop all its trigger fan-out in the same
	// transaction; provider webhook routes may remain shared by other members.
	if _, err := tx.Exec(ctx, `UPDATE integrations_triggers SET status='disabled',version=version+1,updated_at=$2 WHERE organization_id=$1 AND connection_id=$3 AND status='active'`, organizationID, now, connectionID); err != nil {
		return fmt.Errorf("disable disconnected triggers: %w", err)
	}
	if err := insertOutbox(ctx, tx, event, detail); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit integration disconnect: %w", err)
	}
	return nil
}

func (s *Store) ActiveConnection(ctx context.Context, organizationID, memberID, profileID string) (domain.Connection, bool, error) {
	connection, err := s.connection(ctx, `
		SELECT id, organization_id, member_id, profile_id, provider, piece_name,
		       piece_version, provider_subject_id, provider_account_id,
		       provider_account_name, provider_tenant_id, provider_tenant_name,
		       granted_scopes, encrypted_credentials, status, version,
		       created_at, updated_at
		FROM integrations_connections
		WHERE organization_id = $1 AND member_id = $2 AND profile_id = $3
		  AND status = 'active'
		ORDER BY created_at DESC LIMIT 1`, organizationID, memberID, profileID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Connection{}, false, nil
	}
	if err != nil {
		return domain.Connection{}, false, err
	}
	return connection, true, nil
}

func (s *Store) ConnectionForExecution(ctx context.Context, organizationID, connectionID string) (domain.Connection, error) {
	return s.connection(ctx, `
		SELECT id, organization_id, member_id, profile_id, provider, piece_name,
		       piece_version, provider_subject_id, provider_account_id,
		       provider_account_name, provider_tenant_id, provider_tenant_name,
		       granted_scopes, encrypted_credentials, status, version,
		       created_at, updated_at
		FROM integrations_connections
		WHERE organization_id = $1 AND id = $2 AND status = 'active'`, organizationID, connectionID)
}

func (s *Store) connection(ctx context.Context, query string, arguments ...any) (domain.Connection, error) {
	var connection domain.Connection
	var credentials []byte
	err := s.pool.QueryRow(ctx, query, arguments...).Scan(
		&connection.ID, &connection.OrganizationID, &connection.MemberID,
		&connection.ProfileID, &connection.Provider, &connection.PieceName,
		&connection.PieceVersion, &connection.Identity.SubjectID,
		&connection.Identity.AccountID, &connection.Identity.AccountName,
		&connection.Identity.TenantID, &connection.Identity.TenantName,
		&connection.GrantedScopes, &credentials, &connection.Status,
		&connection.Version, &connection.CreatedAt, &connection.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Connection{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Connection{}, fmt.Errorf("get connection: %w", err)
	}
	if err := json.Unmarshal(credentials, &connection.EncryptedCredentials); err != nil {
		return domain.Connection{}, fmt.Errorf("decode connection credential envelope: %w", err)
	}
	return connection, nil
}

func (s *Store) ListMemberIntegrations(ctx context.Context, organizationID, memberID string) ([]domain.MemberIntegration, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.provider, p.piece_name, p.piece_version, p.mode, p.scopes,
		       c.id, c.provider_subject_id, c.provider_account_id,
		       c.provider_account_name, c.provider_tenant_id,
		       c.provider_tenant_name, c.created_at
		FROM integrations_oauth_apps p
		LEFT JOIN integrations_connections c
		  ON c.organization_id = p.organization_id
		 AND c.profile_id = p.id
		 AND c.member_id = $2
		 AND c.status = 'active'
		WHERE p.organization_id = $1 AND p.status = 'active'
		ORDER BY p.provider, p.id`, organizationID, memberID)
	if err != nil {
		return nil, fmt.Errorf("query member integrations: %w", err)
	}
	defer rows.Close()
	var result []domain.MemberIntegration
	for rows.Next() {
		var item domain.MemberIntegration
		var connectionID, subjectID, accountID, accountName, tenantID, tenantName *string
		var connectedAt *time.Time
		if err := rows.Scan(
			&item.ProfileID, &item.Provider, &item.PieceName, &item.PieceVersion,
			&item.Mode, &item.Scopes, &connectionID, &subjectID, &accountID,
			&accountName, &tenantID, &tenantName, &connectedAt,
		); err != nil {
			return nil, fmt.Errorf("scan member integration: %w", err)
		}
		item.State = domain.MemberIntegrationAvailable
		if connectionID != nil {
			item.State = domain.MemberIntegrationConnected
			item.Connection = &domain.MemberConnection{
				ID: *connectionID,
				Identity: domain.ProviderIdentity{
					SubjectID: value(subjectID), AccountID: value(accountID),
					AccountName: value(accountName), TenantID: value(tenantID),
					TenantName: value(tenantName),
				},
				ConnectedAt: *connectedAt,
			}
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate member integrations: %w", err)
	}
	return result, nil
}

func (s *Store) ConnectionCatalog(ctx context.Context, organizationID string) ([]domain.ConnectionCatalogEntry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT connection_id, organization_id, member_id, profile_id, provider,
		       piece_name, piece_version, provider_subject_id,
		       provider_account_id, provider_account_name, provider_tenant_id,
		       provider_tenant_name, granted_scopes, connected_at, updated_at
		FROM integrations_connection_catalog
		WHERE organization_id = $1
		ORDER BY provider, connection_id`, organizationID)
	if err != nil {
		return nil, fmt.Errorf("query connection catalog: %w", err)
	}
	defer rows.Close()
	var result []domain.ConnectionCatalogEntry
	for rows.Next() {
		var item domain.ConnectionCatalogEntry
		if err := rows.Scan(
			&item.ConnectionID, &item.OrganizationID, &item.MemberID,
			&item.ProfileID, &item.Provider, &item.PieceName, &item.PieceVersion,
			&item.Identity.SubjectID, &item.Identity.AccountID,
			&item.Identity.AccountName, &item.Identity.TenantID,
			&item.Identity.TenantName, &item.Scopes, &item.ConnectedAt,
			&item.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan connection catalog: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate connection catalog: %w", err)
	}
	return result, nil
}

func value(input *string) string {
	if input == nil {
		return ""
	}
	return *input
}

func mapWriteError(operation string, err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && (postgresError.Code == "23505" || postgresError.Code == "23503") {
		return fmt.Errorf("%s: %w", operation, domain.ErrConflict)
	}
	return fmt.Errorf("%s: %w", operation, err)
}
