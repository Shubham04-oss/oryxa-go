package managedagents

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/events"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

//go:embed migrations/*.sql
var migrations embed.FS

const migrationsAdvisoryLock int64 = 7263510702

// Store persists member-owned agents and writes their lifecycle events to the
// agents outbox in the same transaction.
type Store struct{ database *postgreskit.Database }

func NewStore(database *postgreskit.Database) *Store { return &Store{database: database} }

func (store *Store) Migrate(ctx context.Context) error {
	return store.database.Migrate(ctx, "managed-agents", migrations, "migrations/*.sql", migrationsAdvisoryLock)
}

func (store *Store) EnsurePrimary(ctx context.Context, scope Scope) (Definition, error) {
	if err := validateScope(scope); err != nil {
		return Definition{}, err
	}
	for attempt := 0; attempt < 5; attempt++ {
		var result Definition
		err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
			found, err := getPrimary(ctx, tx, scope)
			if err == nil {
				result = found
				return nil
			}
			if !errors.Is(err, ErrNotFound) {
				return err
			}
			id, err := newID("agt_")
			if err != nil {
				return err
			}
			name, err := generatedName()
			if err != nil {
				return err
			}
			now := time.Now().UTC()
			created := Definition{
				ID: id, OrganizationID: scope.OrganizationID, OwnerUserID: scope.UserID,
				Kind: KindPrimary, Name: name, Description: "Your primary Oryxa agent.",
				Status: StatusActive, Revision: 1, CreatedAt: now, UpdatedAt: now,
			}
			row := tx.QueryRow(ctx, `
				INSERT INTO agents_primary.member_agents
					(id, organization_id, owner_user_id, kind, name, description, instructions, status, revision, created_at, updated_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10)
				ON CONFLICT (organization_id, owner_user_id) WHERE kind = 'primary' DO NOTHING
				RETURNING id, organization_id, owner_user_id, kind, name, description, instructions, status, revision, created_at, updated_at`,
				created.ID, created.OrganizationID, created.OwnerUserID, created.Kind, created.Name,
				created.Description, created.Instructions, created.Status, created.Revision, now)
			if err := scanDefinition(row, &result); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					found, getErr := getPrimary(ctx, tx, scope)
					if getErr == nil {
						result = found
					}
					return getErr
				}
				return err
			}
			return recordLifecycle(ctx, tx, agentsv1.AgentCreatedDetailType, result)
		})
		if err == nil {
			return result, nil
		}
		if isNameConflict(err) {
			continue
		}
		return Definition{}, err
	}
	return Definition{}, fmt.Errorf("%w: could not allocate a primary agent name", ErrConflict)
}

func (store *Store) Create(ctx context.Context, scope Scope, definition Definition) (Definition, error) {
	if err := validateScope(scope); err != nil {
		return Definition{}, err
	}
	now := time.Now().UTC()
	definition.OrganizationID = scope.OrganizationID
	definition.OwnerUserID = scope.UserID
	definition.Kind = KindSpecialist
	definition.Status = StatusActive
	definition.Revision = 1
	definition.CreatedAt = now
	definition.UpdatedAt = now
	if definition.ID == "" {
		var err error
		definition.ID, err = newID("agt_")
		if err != nil {
			return Definition{}, err
		}
	}
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		if err := scanDefinition(tx.QueryRow(ctx, `
			INSERT INTO agents_primary.member_agents
				(id, organization_id, owner_user_id, kind, name, description, instructions, status, revision, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10)
			RETURNING id, organization_id, owner_user_id, kind, name, description, instructions, status, revision, created_at, updated_at`,
			definition.ID, definition.OrganizationID, definition.OwnerUserID, definition.Kind, definition.Name,
			definition.Description, definition.Instructions, definition.Status, definition.Revision, now), &definition); err != nil {
			return err
		}
		return recordLifecycle(ctx, tx, agentsv1.AgentCreatedDetailType, definition)
	})
	if isNameConflict(err) {
		return Definition{}, ErrConflict
	}
	if err != nil {
		return Definition{}, fmt.Errorf("create agent: %w", err)
	}
	return definition, nil
}

func (store *Store) List(ctx context.Context, scope Scope) ([]Definition, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	rows, err := store.database.Pool.Query(ctx, `
		SELECT id, organization_id, owner_user_id, kind, name, description, instructions, status, revision, created_at, updated_at
		FROM agents_primary.member_agents
		WHERE organization_id = $1 AND owner_user_id = $2
		ORDER BY CASE kind WHEN 'primary' THEN 0 ELSE 1 END, lower(name), id`, scope.OrganizationID, scope.UserID)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()
	result := []Definition{}
	for rows.Next() {
		var definition Definition
		if err := scanDefinition(rows, &definition); err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		result = append(result, definition)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	return result, nil
}

// OrganizationAgents returns safe handles for active agents in an
// organization. The caller must establish organization visibility first;
// directoryclient does that through Teams before exposing any handle.
func (store *Store) OrganizationAgents(ctx context.Context, organizationID string) ([]Handle, error) {
	if strings.TrimSpace(organizationID) == "" {
		return nil, fmt.Errorf("%w: organization is required", ErrInvalid)
	}
	rows, err := store.database.Pool.Query(ctx, `
		SELECT id, owner_user_id, kind, name, description
		FROM agents_primary.member_agents
		WHERE organization_id=$1 AND status='active'
		ORDER BY owner_user_id, CASE kind WHEN 'primary' THEN 0 ELSE 1 END, lower(name), id`, organizationID)
	if err != nil {
		return nil, fmt.Errorf("list organization agents: %w", err)
	}
	defer rows.Close()
	handles := []Handle{}
	for rows.Next() {
		var handle Handle
		if err := rows.Scan(&handle.ID, &handle.OwnerUserID, &handle.Kind, &handle.Name, &handle.Description); err != nil {
			return nil, fmt.Errorf("scan organization agent: %w", err)
		}
		handles = append(handles, handle)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list organization agents: %w", err)
	}
	return handles, nil
}

func (store *Store) Get(ctx context.Context, scope Scope, id string) (Definition, error) {
	if err := validateScope(scope); err != nil {
		return Definition{}, err
	}
	var result Definition
	err := scanDefinition(store.database.Pool.QueryRow(ctx, `
		SELECT id, organization_id, owner_user_id, kind, name, description, instructions, status, revision, created_at, updated_at
		FROM agents_primary.member_agents WHERE organization_id=$1 AND owner_user_id=$2 AND id=$3`,
		scope.OrganizationID, scope.UserID, id), &result)
	if errors.Is(err, pgx.ErrNoRows) {
		return Definition{}, ErrNotFound
	}
	if err != nil {
		return Definition{}, fmt.Errorf("get agent: %w", err)
	}
	return result, nil
}

func (store *Store) Update(ctx context.Context, scope Scope, id string, input UpdateInput) (Definition, error) {
	var result Definition
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		current, err := get(ctx, tx, scope, id)
		if err != nil {
			return err
		}
		if input.Name != nil {
			current.Name = *input.Name
		}
		if input.Description != nil {
			current.Description = *input.Description
		}
		if input.Instructions != nil {
			current.Instructions = *input.Instructions
		}
		if input.Status != nil {
			current.Status = *input.Status
		}
		current.Revision++
		current.UpdatedAt = time.Now().UTC()
		if err := scanDefinition(tx.QueryRow(ctx, `
			UPDATE agents_primary.member_agents SET name=$4, description=$5, instructions=$6, status=$7,
				revision=$8, updated_at=$9
			WHERE organization_id=$1 AND owner_user_id=$2 AND id=$3
			RETURNING id, organization_id, owner_user_id, kind, name, description, instructions, status, revision, created_at, updated_at`,
			scope.OrganizationID, scope.UserID, id, current.Name, current.Description, current.Instructions,
			current.Status, current.Revision, current.UpdatedAt), &result); err != nil {
			return err
		}
		return recordLifecycle(ctx, tx, agentsv1.AgentUpdatedDetailType, result)
	})
	if isNameConflict(err) {
		return Definition{}, ErrConflict
	}
	if err != nil {
		return Definition{}, err
	}
	return result, nil
}

func (store *Store) Delete(ctx context.Context, scope Scope, id string) error {
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		current, err := get(ctx, tx, scope, id)
		if err != nil {
			return err
		}
		if current.Kind == KindPrimary {
			return fmt.Errorf("%w: the primary agent cannot be deleted", ErrInvalid)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM agents_primary.member_agents WHERE id=$1`, id); err != nil {
			return fmt.Errorf("delete agent: %w", err)
		}
		current.Revision++
		current.UpdatedAt = time.Now().UTC()
		return recordLifecycle(ctx, tx, agentsv1.AgentDeletedDetailType, current)
	})
	var pgErr *pgconn.PgError
	// Both references are ON DELETE RESTRICT, which Postgres reports as
	// restrict_violation (23001) rather than the foreign_key_violation
	// (23503) a deferred constraint would raise. Reading only the latter let
	// the raw database error reach the member, who was told an internal
	// error had happened rather than that their specialist is on a team.
	if errors.As(err, &pgErr) && (pgErr.Code == "23001" || pgErr.Code == "23503") && agentInUse(pgErr.ConstraintName) {
		return fmt.Errorf("%w: remove the specialist from its teams and teamspaces first", ErrInUse)
	}
	return err
}

// Forget removes agent definitions when their member leaves or their
// organization is deleted. Auth's deletion event already owns the lifecycle,
// so this cleanup does not publish per-agent events for identities that are no
// longer addressable.
func (store *Store) Forget(ctx context.Context, organizationID, userID string) error {
	query := `DELETE FROM agents_primary.member_agents WHERE organization_id=$1`
	args := []any{organizationID}
	if userID != "" {
		query += ` AND owner_user_id=$2`
		args = append(args, userID)
	}
	_, err := store.database.Pool.Exec(ctx, query, args...)
	return err
}

type scanner interface{ Scan(...any) error }

func scanDefinition(row scanner, target *Definition) error {
	return row.Scan(&target.ID, &target.OrganizationID, &target.OwnerUserID, &target.Kind, &target.Name,
		&target.Description, &target.Instructions, &target.Status, &target.Revision, &target.CreatedAt, &target.UpdatedAt)
}

func get(ctx context.Context, tx pgx.Tx, scope Scope, id string) (Definition, error) {
	var result Definition
	err := scanDefinition(tx.QueryRow(ctx, `
		SELECT id, organization_id, owner_user_id, kind, name, description, instructions, status, revision, created_at, updated_at
		FROM agents_primary.member_agents WHERE organization_id=$1 AND owner_user_id=$2 AND id=$3 FOR UPDATE`,
		scope.OrganizationID, scope.UserID, id), &result)
	if errors.Is(err, pgx.ErrNoRows) {
		return Definition{}, ErrNotFound
	}
	return result, err
}

func getPrimary(ctx context.Context, tx pgx.Tx, scope Scope) (Definition, error) {
	var result Definition
	err := scanDefinition(tx.QueryRow(ctx, `
		SELECT id, organization_id, owner_user_id, kind, name, description, instructions, status, revision, created_at, updated_at
		FROM agents_primary.member_agents WHERE organization_id=$1 AND owner_user_id=$2 AND kind='primary' FOR UPDATE`,
		scope.OrganizationID, scope.UserID), &result)
	if errors.Is(err, pgx.ErrNoRows) {
		return Definition{}, ErrNotFound
	}
	return result, err
}

func recordLifecycle(ctx context.Context, tx pgx.Tx, detailType string, definition Definition) error {
	event, err := events.NewEvent(detailType, agentsv1.AgentChanged{
		AgentID: definition.ID, UserID: definition.OwnerUserID, OrganizationID: definition.OrganizationID,
		Kind: string(definition.Kind), Name: definition.Name, Status: string(definition.Status),
		Revision: definition.Revision, ChangedAt: definition.UpdatedAt,
	}, definition.UpdatedAt)
	if err != nil {
		return err
	}
	trace := observability.CaptureTraceCarrier(ctx).Encode()
	_, err = tx.Exec(ctx, `INSERT INTO agents_primary.agents_outbox
		(id, detail_type, detail, occurred_at, trace_context) VALUES ($1,$2,$3,$4,NULLIF($5,''))`,
		event.ID, event.DetailType, event.Data, event.OccurredAt, trace)
	if err != nil {
		return fmt.Errorf("record agent event: %w", err)
	}
	return nil
}

func validateScope(scope Scope) error {
	if strings.TrimSpace(scope.OrganizationID) == "" || strings.TrimSpace(scope.UserID) == "" {
		return fmt.Errorf("%w: organization and user are required", ErrInvalid)
	}
	return nil
}

func newID(prefix string) (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("create agent id: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(bytes), nil
}

var friendlyNames = []string{"Ada", "Arlo", "Aster", "Atlas", "Avery", "Cleo", "Cora", "Elio", "Iris", "Juno", "Kai", "Leo", "Maya", "Milo", "Nia", "Nova", "Orion", "Remy", "Sage", "Theo", "Vera", "Zara"}

func generatedName() (string, error) {
	bytes := make([]byte, 4)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("create agent name: %w", err)
	}
	base := friendlyNames[int(bytes[0])%len(friendlyNames)]
	suffix := strings.ToUpper(base64.RawURLEncoding.EncodeToString(bytes[1:]))
	return base + "-" + suffix, nil
}

func isNameConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "member_agents_org_name_idx"
}

// agentInUse names every reference that keeps an agent from being deleted: a
// team it is on, a teamspace it was contributed to, and a team it coordinates.
// Each is ON DELETE RESTRICT, and each means the same thing to a member —
// something still points at this agent, and they are told which rather than
// being shown a database error.
//
// The coordinator reference cannot be reached today: a team's coordinator is
// always its member's primary agent, and deleting that is refused above. It is
// listed so that this stays true by accident rather than by luck.
func agentInUse(constraint string) bool {
	switch constraint {
	case "member_agent_team_members_agent_fk",
		"member_agent_teams_coordinator_agent_id_fkey",
		"teamspace_agent_contributions_agent_fk":
		return true
	default:
		return false
	}
}
