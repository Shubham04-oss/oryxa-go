package authz

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

//go:embed migrations/*.sql
var policyMigrations embed.FS

// Replicas that start together must not install the schema at the same
// time; IF NOT EXISTS alone does not serialize catalog changes. This
// continues the low, sequential range billing (402), files (403), rag (404)
// and teams (405) already use.
const migrationsAdvisoryLock int64 = 7263510406

// policyIDPrefix marks a Policy's id, visible in logs and in the UI.
const policyIDPrefix = "pol_"

// newID mints a URL-safe identifier under prefix, with the 96 bits of
// entropy the other services' id helpers mint (see rag/internal/domain's
// NewID).
func newID(prefix string) (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("authz: generate id: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(value), nil
}

// Migrate creates authz_policies and keeps it current. It is a free function
// rather than a method on Store, because most services that need authz's
// tables to exist only ever talk to Engine and PolicyStore, never to
// Postgres directly, and so have no reason to build a Store at all.
func Migrate(ctx context.Context, database *postgreskit.Database) error {
	return database.Migrate(ctx, "authz", policyMigrations, "migrations/*.sql", migrationsAdvisoryLock)
}

// Store is PolicyStore backed by PostgreSQL, plus the writes an admin UI
// needs: creating, listing and deleting the policies an organization has
// stored.
type Store struct {
	db *sqlx.DB
}

// NewStore uses a pool the caller owns, such as the one a host shares
// between services — the same choice rag/internal/postgres.New and
// teams/internal/postgres.New make over building authz its own pool. Store
// never closes it.
func NewStore(database *postgreskit.Database) *Store {
	return &Store{db: database.SQLX}
}

const policyColumns = `id, organization_id, scope_type, scope_id, name, text, created_by, created_at, updated_at`

type policyRow struct {
	ID             string    `db:"id"`
	OrganizationID string    `db:"organization_id"`
	ScopeType      string    `db:"scope_type"`
	ScopeID        string    `db:"scope_id"`
	Name           string    `db:"name"`
	Text           string    `db:"text"`
	CreatedBy      string    `db:"created_by"`
	CreatedAt      time.Time `db:"created_at"`
	UpdatedAt      time.Time `db:"updated_at"`
}

func (row policyRow) policy() Policy {
	return Policy{
		ID:             row.ID,
		OrganizationID: row.OrganizationID,
		Scope:          Scope{Type: row.ScopeType, ID: row.ScopeID},
		Name:           row.Name,
		Text:           row.Text,
		CreatedBy:      row.CreatedBy,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}

// OrganizationPolicies is every policy an organization has stored, in any
// scope. Engine calls this once per cache fill and filters locally to what a
// given resource's scopes are (see Engine.AuthorizeEach), rather than asking
// once per scope.
func (store *Store) OrganizationPolicies(ctx context.Context, organizationID string) ([]Policy, error) {
	var rows []policyRow
	if err := store.db.SelectContext(ctx, &rows, `
		SELECT `+policyColumns+`
		FROM authz_policies
		WHERE organization_id = $1
		ORDER BY created_at, id`, organizationID); err != nil {
		return nil, fmt.Errorf("authz: list organization policies: %w", err)
	}
	policies := make([]Policy, 0, len(rows))
	for _, row := range rows {
		policies = append(policies, row.policy())
	}
	return policies, nil
}

// Policies is what an admin UI shows for one Scope: the organization's own
// policies when scope names the organization, or one resource's when it
// names a resource.
func (store *Store) Policies(ctx context.Context, organizationID string, scope Scope) ([]Policy, error) {
	var rows []policyRow
	if err := store.db.SelectContext(ctx, &rows, `
		SELECT `+policyColumns+`
		FROM authz_policies
		WHERE organization_id = $1 AND scope_type = $2 AND scope_id = $3
		ORDER BY created_at, id`, organizationID, scope.Type, scope.ID); err != nil {
		return nil, fmt.Errorf("authz: list policies: %w", err)
	}
	policies := make([]Policy, 0, len(rows))
	for _, row := range rows {
		policies = append(policies, row.policy())
	}
	return policies, nil
}

// CreatePolicy validates policy's Text, assigns it an id and its timestamps,
// and stores it. A policy already stored under the same organization, scope
// and name is a conflict: the name is how an admin tells their own policies
// apart, so silently keeping either one, or filing both under it, would
// surprise somebody.
func (store *Store) CreatePolicy(ctx context.Context, policy Policy) (Policy, error) {
	if err := ValidatePolicy(policy.Text); err != nil {
		return Policy{}, err
	}
	id, err := newID(policyIDPrefix)
	if err != nil {
		return Policy{}, err
	}
	now := time.Now().UTC()
	policy.ID = id
	policy.CreatedAt = now
	policy.UpdatedAt = now

	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO authz_policies (id, organization_id, scope_type, scope_id, name, text, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		policy.ID, policy.OrganizationID, policy.Scope.Type, policy.Scope.ID, policy.Name, policy.Text, policy.CreatedBy, policy.CreatedAt, policy.UpdatedAt,
	); err != nil {
		return Policy{}, mapWriteError("authz: create policy", err)
	}
	return policy, nil
}

// DeletePolicy removes one stored policy.
func (store *Store) DeletePolicy(ctx context.Context, organizationID string, scope Scope, id string) error {
	result, err := store.db.ExecContext(ctx, `
		DELETE FROM authz_policies
		WHERE organization_id = $1 AND scope_type = $2 AND scope_id = $3 AND id = $4`,
		organizationID, scope.Type, scope.ID, id)
	if err != nil {
		return fmt.Errorf("authz: delete policy: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("authz: delete policy: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteScope removes every policy stored on one Scope, for when the
// resource it names is deleted. Unlike DeletePolicy, deleting nothing is not
// an error: the resource may never have had a policy of its own.
func (store *Store) DeleteScope(ctx context.Context, organizationID string, scope Scope) error {
	if _, err := store.db.ExecContext(ctx, `
		DELETE FROM authz_policies
		WHERE organization_id = $1 AND scope_type = $2 AND scope_id = $3`,
		organizationID, scope.Type, scope.ID); err != nil {
		return fmt.Errorf("authz: delete scope policies: %w", err)
	}
	return nil
}

// DeleteOrganization removes every policy an organization has stored, in
// every scope, for when the organization itself is deleted.
func (store *Store) DeleteOrganization(ctx context.Context, organizationID string) error {
	if _, err := store.db.ExecContext(ctx, `
		DELETE FROM authz_policies WHERE organization_id = $1`, organizationID); err != nil {
		return fmt.Errorf("authz: delete organization policies: %w", err)
	}
	return nil
}

// mapWriteError turns a unique or foreign key violation into ErrConflict,
// the way rag/internal/postgres's store does, so a caller can tell "this
// already exists" from any other write failure with errors.Is.
func mapWriteError(operation string, err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && (postgresError.Code == "23505" || postgresError.Code == "23503") {
		return fmt.Errorf("%s: %w", operation, ErrConflict)
	}
	return fmt.Errorf("%s: %w", operation, err)
}
