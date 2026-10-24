package managedagents

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/events"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/modelrouter"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runprofile"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

//go:embed migrations/*.sql
var migrations embed.FS

const migrationsAdvisoryLock int64 = 7263510702

// Store persists member-owned agents and writes their lifecycle events to the
// agents outbox in the same transaction.
type Store struct {
	database      *postgreskit.Database
	relationships Relationships
	logger        *slog.Logger
}

func NewStore(database *postgreskit.Database) *Store { return &Store{database: database} }

// Relationships writes an agent and its grants to SpiceDB as they commit,
// sooner than the projector writes them from the agent's events.
type Relationships interface {
	AgentChanged(ctx context.Context, agent Definition) error
	AgentDeleted(ctx context.Context, organizationID, agentID string) error
}

// WithRelationships writes each agent created, updated or deleted to SpiceDB.
func (store *Store) WithRelationships(relationships Relationships, logger *slog.Logger) *Store {
	store.relationships, store.logger = relationships, logger
	return store
}

// wrote writes a committed change's relationships. It runs after the commit:
// an agent's grants are read back from the tables, so a grant narrowed is
// seen only once it is there. A failed write is logged and the change
// stands; the projector follows from its event.
func (store *Store) wrote(ctx context.Context, what, agentID string, write func(Relationships) error) {
	if store.relationships == nil {
		return
	}
	if err := write(store.relationships); err != nil {
		store.logger.WarnContext(ctx, what+" not written to spicedb; the projector will", "agent_id", agentID, "error", err)
	}
}

func (store *Store) Migrate(ctx context.Context) error {
	return store.database.Migrate(ctx, "managed-agents", migrations, "migrations/*.sql", migrationsAdvisoryLock)
}

func (store *Store) EnsurePrimary(ctx context.Context, scope Scope) (Definition, error) {
	if err := validateScope(scope); err != nil {
		return Definition{}, err
	}
	for attempt := 0; attempt < 5; attempt++ {
		var result Definition
		inserted := false
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
			taken, err := takenNames(ctx, tx, scope.OrganizationID)
			if err != nil {
				return err
			}
			name, err := generatedName(taken)
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
				RETURNING `+definitionColumns,
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
			inserted = true
			return recordLifecycle(ctx, tx, agentsv1.AgentCreatedDetailType, result)
		})
		if err == nil {
			if inserted {
				store.wrote(ctx, "primary agent", result.ID, func(r Relationships) error { return r.AgentChanged(ctx, result) })
			}
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
			RETURNING `+definitionColumns,
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
	store.wrote(ctx, "agent created", definition.ID, func(r Relationships) error { return r.AgentChanged(ctx, definition) })
	return definition, nil
}

func (store *Store) List(ctx context.Context, scope Scope) ([]Definition, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	rows, err := store.database.Pool.Query(ctx, `
		SELECT `+definitionColumns+`
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
		SELECT `+definitionColumns+`
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
		previousName := current.Name
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
		// Only a grant sent is written, so an untouched specialist keeps the
		// default rather than a copy of it.
		var grant any
		if input.Grant != nil {
			if current.Kind != KindSpecialist {
				return fmt.Errorf("%w: only a specialist has a grant", ErrInvalid)
			}
			encoded, err := json.Marshal(input.Grant)
			if err != nil {
				return fmt.Errorf("encode the grant: %w", err)
			}
			grant = string(encoded)
		}
		var tier any
		if input.ModelTier != nil {
			if current.Kind != KindSpecialist {
				return fmt.Errorf("%w: only a specialist has a model tier", ErrInvalid)
			}
			tier = string(*input.ModelTier)
		}
		current.Revision++
		current.UpdatedAt = time.Now().UTC()
		if err := scanDefinition(tx.QueryRow(ctx, `
			UPDATE agents_primary.member_agents SET name=$4, description=$5, instructions=$6, status=$7,
				revision=$8, updated_at=$9, app_grant=COALESCE($10::jsonb, app_grant), model_tier=COALESCE($11, model_tier)
			WHERE organization_id=$1 AND owner_user_id=$2 AND id=$3
			RETURNING `+definitionColumns,
			scope.OrganizationID, scope.UserID, id, current.Name, current.Description, current.Instructions,
			current.Status, current.Revision, current.UpdatedAt, grant, tier), &result); err != nil {
			return err
		}
		if err := renameCopies(ctx, tx, result, previousName); err != nil {
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
	store.wrote(ctx, "agent updated", result.ID, func(r Relationships) error { return r.AgentChanged(ctx, result) })
	return result, nil
}

// nameCopies are the other tables that keep an agent's name beside its id,
// for display: the requests it made or was asked, what it posted in a
// teamspace, and what it said in an agent team's room. A room's handles are
// not here: a message addressed @Old keeps the handle it was written to.
var nameCopies = []struct{ table, name, id string }{
	{"agents_primary.agent_interactions", "requester_agent_name", "requester_agent_id"},
	{"agents_primary.agent_interactions", "target_agent_name", "target_agent_id"},
	{"agents_primary.teamspace_entries", "author_agent_name", "author_agent_id"},
	{"agents_primary.member_agent_team_messages", "author_name", "author_agent_id"},
}

// renameCopies writes a renamed agent's new name into every copy of it, in
// the rename's transaction, so the agent's own row stays the one source of
// its name. A table another service has not migrated yet holds no copy.
func renameCopies(ctx context.Context, tx pgx.Tx, renamed Definition, previousName string) error {
	if renamed.Name == previousName {
		return nil
	}
	for _, held := range nameCopies {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, held.table).Scan(&exists); err != nil {
			return fmt.Errorf("find %s: %w", held.table, err)
		}
		if !exists {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE `+held.table+` SET `+held.name+`=$2 WHERE `+held.id+`=$1`, renamed.ID, renamed.Name); err != nil {
			return fmt.Errorf("rename the agent in %s: %w", held.table, err)
		}
	}
	return nil
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
	if err != nil {
		return err
	}
	store.wrote(ctx, "agent deleted", id, func(r Relationships) error { return r.AgentDeleted(ctx, scope.OrganizationID, id) })
	return nil
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

// definitionColumns is what scanDefinition reads, in its order.
const definitionColumns = `id, organization_id, owner_user_id, kind, name, description, instructions, status, revision, created_at, updated_at, app_grant, model_tier`

// scanDefinition reads a row; a specialist with no grant stored has the
// default one, and with no tier stored the lite one. A primary always
// answers on main.
func scanDefinition(row scanner, target *Definition) error {
	var grant []byte
	var tier *string
	if err := row.Scan(&target.ID, &target.OrganizationID, &target.OwnerUserID, &target.Kind, &target.Name,
		&target.Description, &target.Instructions, &target.Status, &target.Revision, &target.CreatedAt, &target.UpdatedAt, &grant, &tier); err != nil {
		return err
	}
	target.Grant = nil
	target.ModelTier = modelrouter.Main
	if target.Kind != KindSpecialist {
		return nil
	}
	target.ModelTier = modelrouter.Lite
	if tier != nil {
		target.ModelTier = modelrouter.Tier(*tier)
	}
	decoded := runprofile.DefaultGrant()
	if grant != nil {
		if err := json.Unmarshal(grant, &decoded); err != nil {
			return fmt.Errorf("decode the grant of agent %s: %w", target.ID, err)
		}
	}
	target.Grant = &decoded
	return nil
}

func get(ctx context.Context, tx pgx.Tx, scope Scope, id string) (Definition, error) {
	var result Definition
	err := scanDefinition(tx.QueryRow(ctx, `
		SELECT `+definitionColumns+`
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
		SELECT `+definitionColumns+`
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

var friendlyNames = []string{
	"Ada", "Alma", "Arlo", "Aster", "Atlas", "Avery", "Bea", "Cleo", "Cora", "Dara",
	"Eden", "Elio", "Ezra", "Faye", "Finn", "Gia", "Hana", "Ines", "Iris", "Ivo",
	"Jade", "Juno", "Kai", "Kira", "Lena", "Leo", "Luca", "Mara", "Maya", "Milo",
	"Mira", "Nia", "Noor", "Nova", "Odin", "Orion", "Pia", "Quinn", "Remy", "Rhea",
	"Rio", "Sage", "Sami", "Tara", "Theo", "Uma", "Vera", "Wren", "Yara", "Zara",
}

// generatedName is a name for a new primary agent that no agent in its
// organization has yet: one of friendlyNames as it is, or, once every one is
// taken, the first numbered one free ("Nia 2"). Names are unique in an
// organization whatever their case (member_agents_org_name_idx), so taken
// holds lowercased names. Which free name is chosen is random, so two members
// starting together are unlikely to want the same one; when they do, the
// index refuses the second and EnsurePrimary asks again.
func generatedName(taken map[string]bool) (string, error) {
	order := make([]byte, len(friendlyNames))
	if _, err := rand.Read(order); err != nil {
		return "", fmt.Errorf("create agent name: %w", err)
	}
	names := slices.Clone(friendlyNames)
	// Fisher-Yates, from the random bytes; the slight bias of a byte modulo a
	// small length does not matter for picking a name.
	for i := len(names) - 1; i > 0; i-- {
		j := int(order[i]) % (i + 1)
		names[i], names[j] = names[j], names[i]
	}
	for number := 1; ; number++ {
		for _, base := range names {
			candidate := base
			if number > 1 {
				candidate = fmt.Sprintf("%s %d", base, number)
			}
			if !taken[strings.ToLower(candidate)] {
				return candidate, nil
			}
		}
	}
}

// suffixedName is a name an earlier build gave a primary agent: one of the
// friendly names it had, a hyphen, and three random bytes in uppercased
// base64url ("Nia-WETP", "Nia--W_P"). No member writes a name like that, so
// one is still the generated name nobody chose.
var suffixedName = regexp.MustCompile(`^(Ada|Arlo|Aster|Atlas|Avery|Cleo|Cora|Elio|Iris|Juno|Kai|Leo|Maya|Milo|Nia|Nova|Orion|Remy|Sage|Theo|Vera|Zara)-[A-Z0-9_-]{4}$`)

// RenameSuffixedPrimaries gives every primary agent still named the way an
// earlier build named them ("Nia-WETP") the plain name ("Nia"), or, where an
// agent in its organization has that already, the first one numbered free
// ("Nia 2"). Each is renamed as its member renaming it would be: the copies of
// its name with it, and agents.agent.updated.v1. It is safe to run again and
// from replicas at once: a renamed agent no longer matches, and an agent
// renamed by someone else in between is left as they named it. It returns how
// many it renamed.
func (store *Store) RenameSuffixedPrimaries(ctx context.Context) (int, error) {
	rows, err := store.database.Pool.Query(ctx, `
		SELECT id, organization_id, name FROM agents_primary.member_agents
		WHERE kind = 'primary' AND name ~ $1`, suffixedName.String())
	if err != nil {
		return 0, fmt.Errorf("find suffixed primary agents: %w", err)
	}
	type suffixed struct{ id, organizationID, name string }
	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (suffixed, error) {
		var agent suffixed
		err := row.Scan(&agent.id, &agent.organizationID, &agent.name)
		return agent, err
	})
	if err != nil {
		return 0, fmt.Errorf("find suffixed primary agents: %w", err)
	}
	renamed := 0
	for _, agent := range found {
		done, err := store.renameSuffixed(ctx, agent.id, agent.organizationID, agent.name)
		if err != nil {
			return renamed, err
		}
		if done {
			renamed++
		}
	}
	return renamed, nil
}

// renameSuffixed renames one suffixed primary agent, trying again when
// another agent takes the name it chose first.
func (store *Store) renameSuffixed(ctx context.Context, id, organizationID, oldName string) (bool, error) {
	base, _, _ := strings.Cut(oldName, "-")
	for attempt := 0; attempt < 5; attempt++ {
		renamed := false
		err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
			var current Definition
			if err := scanDefinition(tx.QueryRow(ctx, `SELECT `+definitionColumns+`
				FROM agents_primary.member_agents WHERE id = $1 AND organization_id = $2 FOR UPDATE`,
				id, organizationID), &current); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return nil
				}
				return err
			}
			if current.Name != oldName {
				return nil
			}
			taken, err := takenNames(ctx, tx, organizationID)
			if err != nil {
				return err
			}
			name := numberedName(base, taken)
			current.Revision++
			current.UpdatedAt = time.Now().UTC()
			var result Definition
			if err := scanDefinition(tx.QueryRow(ctx, `
				UPDATE agents_primary.member_agents SET name=$3, revision=$4, updated_at=$5
				WHERE id=$1 AND organization_id=$2
				RETURNING `+definitionColumns,
				id, organizationID, name, current.Revision, current.UpdatedAt), &result); err != nil {
				return err
			}
			if err := renameCopies(ctx, tx, result, oldName); err != nil {
				return err
			}
			renamed = true
			return recordLifecycle(ctx, tx, agentsv1.AgentUpdatedDetailType, result)
		})
		if isNameConflict(err) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("rename primary agent %s: %w", id, err)
		}
		return renamed, nil
	}
	return false, fmt.Errorf("%w: could not find a free name for primary agent %s", ErrConflict, id)
}

// numberedName is base when no agent in the organization has it, and
// otherwise the first of "base 2", "base 3" and on that none has. taken holds
// lowercased names.
func numberedName(base string, taken map[string]bool) string {
	candidate := base
	for number := 2; taken[strings.ToLower(candidate)]; number++ {
		candidate = fmt.Sprintf("%s %d", base, number)
	}
	return candidate
}

// takenNames is every agent name already used in the organization,
// lowercased, as the unique index compares them.
func takenNames(ctx context.Context, tx pgx.Tx, organizationID string) (map[string]bool, error) {
	rows, err := tx.Query(ctx, `SELECT lower(name) FROM agents_primary.member_agents WHERE organization_id = $1`, organizationID)
	if err != nil {
		return nil, fmt.Errorf("read agent names: %w", err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read agent names: %w", err)
	}
	taken := make(map[string]bool, len(names))
	for _, name := range names {
		taken[name] = true
	}
	return taken, nil
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
