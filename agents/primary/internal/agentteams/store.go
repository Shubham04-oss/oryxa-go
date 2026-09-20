package agentteams

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/events"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

//go:embed migrations/*.sql
var migrations embed.FS

const migrationsAdvisoryLock int64 = 7263510705

type Store struct{ database *postgreskit.Database }

func NewStore(database *postgreskit.Database) *Store { return &Store{database: database} }

func (store *Store) Migrate(ctx context.Context) error {
	return store.database.Migrate(ctx, "agent-teams", migrations, "migrations/*.sql", migrationsAdvisoryLock)
}

func (store *Store) Create(ctx context.Context, scope Scope, team Team) (Team, error) {
	if err := validateScope(scope); err != nil {
		return Team{}, err
	}
	var result Team
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		coordinator, err := primary(ctx, tx, scope)
		if err != nil {
			return err
		}
		members, err := resolveMembers(ctx, tx, scope, team.Members)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		team.OrganizationID, team.OwnerUserID = scope.OrganizationID, scope.UserID
		team.CoordinatorAgentID, team.CoordinatorName = coordinator.ID, coordinator.Name
		team.Status, team.Revision, team.CreatedAt, team.UpdatedAt = StatusActive, 1, now, now
		if err := scanTeam(tx.QueryRow(ctx, `
			INSERT INTO agents_primary.member_agent_teams
				(id, organization_id, owner_user_id, name, description, coordinator_agent_id,
				 status, revision, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)
			RETURNING `+teamColumns,
			team.ID, team.OrganizationID, team.OwnerUserID, team.Name, team.Description,
			team.CoordinatorAgentID, team.Status, team.Revision, now), &result); err != nil {
			return err
		}
		if err := replaceMembers(ctx, tx, result.ID, members); err != nil {
			return err
		}
		result.CoordinatorName = coordinator.Name
		result.Members = members
		return recordLifecycle(ctx, tx, agentsv1.AgentTeamCreatedDetailType, result)
	})
	if isNameConflict(err) {
		return Team{}, ErrConflict
	}
	if err != nil {
		return Team{}, fmt.Errorf("create agent team: %w", err)
	}
	return result, nil
}

func (store *Store) List(ctx context.Context, scope Scope) ([]Team, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	rows, err := store.database.Pool.Query(ctx, `SELECT `+teamColumns+`
		FROM agents_primary.member_agent_teams
		WHERE organization_id=$1 AND owner_user_id=$2
		ORDER BY lower(name), id`, scope.OrganizationID, scope.UserID)
	if err != nil {
		return nil, fmt.Errorf("list agent teams: %w", err)
	}
	defer rows.Close()
	teams := []Team{}
	for rows.Next() {
		var team Team
		if err := scanTeam(rows, &team); err != nil {
			return nil, fmt.Errorf("scan agent team: %w", err)
		}
		teams = append(teams, team)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list agent teams: %w", err)
	}
	for index := range teams {
		if err := store.hydrate(ctx, &teams[index]); err != nil {
			return nil, err
		}
	}
	return teams, nil
}

func (store *Store) Get(ctx context.Context, scope Scope, id string) (Team, error) {
	if err := validateScope(scope); err != nil {
		return Team{}, err
	}
	var team Team
	err := scanTeam(store.database.Pool.QueryRow(ctx, `SELECT `+teamColumns+`
		FROM agents_primary.member_agent_teams
		WHERE organization_id=$1 AND owner_user_id=$2 AND id=$3`, scope.OrganizationID, scope.UserID, id), &team)
	if errors.Is(err, pgx.ErrNoRows) {
		return Team{}, ErrNotFound
	}
	if err != nil {
		return Team{}, fmt.Errorf("get agent team: %w", err)
	}
	if err := store.hydrate(ctx, &team); err != nil {
		return Team{}, err
	}
	return team, nil
}

func (store *Store) Update(ctx context.Context, scope Scope, id string, input UpdateInput) (Team, error) {
	if err := validateScope(scope); err != nil {
		return Team{}, err
	}
	var result Team
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		current, err := getForUpdate(ctx, tx, scope, id)
		if err != nil {
			return err
		}
		if input.Name != nil {
			current.Name = *input.Name
		}
		if input.Description != nil {
			current.Description = *input.Description
		}
		if input.Status != nil {
			current.Status = *input.Status
		}
		if input.Members != nil {
			stubs := memberStubs(*input.Members)
			resolved, err := resolveMembers(ctx, tx, scope, stubs)
			if err != nil {
				return err
			}
			if err := replaceMembers(ctx, tx, current.ID, resolved); err != nil {
				return err
			}
			current.Members = resolved
		} else {
			current.Members, err = membersFor(ctx, tx, current.ID)
			if err != nil {
				return err
			}
		}
		current.Revision++
		current.UpdatedAt = time.Now().UTC()
		if err := scanTeam(tx.QueryRow(ctx, `UPDATE agents_primary.member_agent_teams
			SET name=$4, description=$5, status=$6, revision=$7, updated_at=$8
			WHERE organization_id=$1 AND owner_user_id=$2 AND id=$3
			RETURNING `+teamColumns,
			scope.OrganizationID, scope.UserID, id, current.Name, current.Description,
			current.Status, current.Revision, current.UpdatedAt), &result); err != nil {
			return err
		}
		coordinator, err := agentByID(ctx, tx, scope, result.CoordinatorAgentID)
		if err != nil {
			return err
		}
		result.CoordinatorName, result.Members = coordinator.Name, current.Members
		return recordLifecycle(ctx, tx, agentsv1.AgentTeamUpdatedDetailType, result)
	})
	if isNameConflict(err) {
		return Team{}, ErrConflict
	}
	if err != nil {
		return Team{}, err
	}
	return result, nil
}

func (store *Store) Delete(ctx context.Context, scope Scope, id string) error {
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		current, err := getForUpdate(ctx, tx, scope, id)
		if err != nil {
			return err
		}
		current.Members, err = membersFor(ctx, tx, current.ID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM agents_primary.member_agent_teams WHERE id=$1`, id); err != nil {
			return fmt.Errorf("delete agent team: %w", err)
		}
		current.Revision++
		current.UpdatedAt = time.Now().UTC()
		return recordLifecycle(ctx, tx, agentsv1.AgentTeamDeletedDetailType, current)
	})
	// A workflow's team is ON DELETE RESTRICT, which Postgres reports as
	// restrict_violation rather than the foreign_key_violation a deferred
	// constraint would raise. Without reading it the member is told an
	// internal error happened, when what happened is that their own workflow
	// still runs this team.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "23001" || pgErr.Code == "23503") &&
		pgErr.ConstraintName == "member_agent_workflows_team_id_fkey" {
		return fmt.Errorf("%w: delete the workflows that run this team first", ErrInUse)
	}
	return err
}

func (store *Store) Forget(ctx context.Context, organizationID, userID string) error {
	query := `DELETE FROM agents_primary.member_agent_teams WHERE organization_id=$1`
	args := []any{organizationID}
	if userID != "" {
		query += ` AND owner_user_id=$2`
		args = append(args, userID)
	}
	_, err := store.database.Pool.Exec(ctx, query, args...)
	return err
}

func (store *Store) hydrate(ctx context.Context, team *Team) error {
	if err := store.database.Pool.QueryRow(ctx, `SELECT name FROM agents_primary.member_agents WHERE id=$1`, team.CoordinatorAgentID).Scan(&team.CoordinatorName); err != nil {
		return fmt.Errorf("read team coordinator: %w", err)
	}
	var err error
	team.Members, err = membersFromPool(ctx, store.database, team.ID)
	return err
}

const teamColumns = `id, organization_id, owner_user_id, name, description,
	coordinator_agent_id, status, revision, created_at, updated_at`

type scanner interface{ Scan(...any) error }

func scanTeam(row scanner, team *Team) error {
	return row.Scan(&team.ID, &team.OrganizationID, &team.OwnerUserID, &team.Name, &team.Description,
		&team.CoordinatorAgentID, &team.Status, &team.Revision, &team.CreatedAt, &team.UpdatedAt)
}

func getForUpdate(ctx context.Context, tx pgx.Tx, scope Scope, id string) (Team, error) {
	var team Team
	err := scanTeam(tx.QueryRow(ctx, `SELECT `+teamColumns+` FROM agents_primary.member_agent_teams
		WHERE organization_id=$1 AND owner_user_id=$2 AND id=$3 FOR UPDATE`, scope.OrganizationID, scope.UserID, id), &team)
	if errors.Is(err, pgx.ErrNoRows) {
		return Team{}, ErrNotFound
	}
	return team, err
}

func primary(ctx context.Context, tx pgx.Tx, scope Scope) (managedagents.Definition, error) {
	var definition managedagents.Definition
	err := scanAgent(tx.QueryRow(ctx, `SELECT id, organization_id, owner_user_id, kind, name, description,
		instructions, status, revision, created_at, updated_at FROM agents_primary.member_agents
		WHERE organization_id=$1 AND owner_user_id=$2 AND kind='primary' AND status='active'`, scope.OrganizationID, scope.UserID), &definition)
	if errors.Is(err, pgx.ErrNoRows) {
		return definition, fmt.Errorf("%w: an active primary agent is required", ErrInvalid)
	}
	return definition, err
}

func agentByID(ctx context.Context, tx pgx.Tx, scope Scope, id string) (managedagents.Definition, error) {
	var definition managedagents.Definition
	err := scanAgent(tx.QueryRow(ctx, `SELECT id, organization_id, owner_user_id, kind, name, description,
		instructions, status, revision, created_at, updated_at FROM agents_primary.member_agents
		WHERE organization_id=$1 AND owner_user_id=$2 AND id=$3`, scope.OrganizationID, scope.UserID, id), &definition)
	if errors.Is(err, pgx.ErrNoRows) {
		return definition, fmt.Errorf("%w: agent %q does not belong to this member", ErrInvalid, id)
	}
	return definition, err
}

func scanAgent(row scanner, definition *managedagents.Definition) error {
	return row.Scan(&definition.ID, &definition.OrganizationID, &definition.OwnerUserID, &definition.Kind,
		&definition.Name, &definition.Description, &definition.Instructions, &definition.Status,
		&definition.Revision, &definition.CreatedAt, &definition.UpdatedAt)
}

func resolveMembers(ctx context.Context, tx pgx.Tx, scope Scope, stubs []Member) ([]Member, error) {
	members := make([]Member, 0, len(stubs))
	for position, stub := range stubs {
		definition, err := agentByID(ctx, tx, scope, stub.AgentID)
		if err != nil {
			return nil, err
		}
		if definition.Kind != managedagents.KindSpecialist {
			return nil, fmt.Errorf("%w: team member %q must be a specialist", ErrInvalid, stub.AgentID)
		}
		if definition.Status != managedagents.StatusActive {
			return nil, fmt.Errorf("%w: team member %q is disabled", ErrInvalid, stub.AgentID)
		}
		members = append(members, Member{
			AgentID: definition.ID, Name: definition.Name, Description: definition.Description,
			Status: definition.Status, Role: stub.Role, Position: position,
		})
	}
	return members, nil
}

func replaceMembers(ctx context.Context, tx pgx.Tx, teamID string, members []Member) error {
	if _, err := tx.Exec(ctx, `DELETE FROM agents_primary.member_agent_team_members WHERE team_id=$1`, teamID); err != nil {
		return fmt.Errorf("replace agent team members: %w", err)
	}
	for _, member := range members {
		if _, err := tx.Exec(ctx, `INSERT INTO agents_primary.member_agent_team_members
			(team_id, agent_id, role, position) VALUES ($1,$2,$3,$4)`, teamID, member.AgentID, member.Role, member.Position); err != nil {
			return fmt.Errorf("add agent team member: %w", err)
		}
	}
	return nil
}

func membersFor(ctx context.Context, tx pgx.Tx, teamID string) ([]Member, error) {
	rows, err := tx.Query(ctx, membersQuery, teamID)
	if err != nil {
		return nil, fmt.Errorf("list agent team members: %w", err)
	}
	return collectMembers(rows)
}

func membersFromPool(ctx context.Context, database *postgreskit.Database, teamID string) ([]Member, error) {
	rows, err := database.Pool.Query(ctx, membersQuery, teamID)
	if err != nil {
		return nil, fmt.Errorf("list agent team members: %w", err)
	}
	return collectMembers(rows)
}

const membersQuery = `SELECT membership.agent_id, agent.name, agent.description, agent.status,
	membership.role, membership.position
	FROM agents_primary.member_agent_team_members membership
	JOIN agents_primary.member_agents agent ON agent.id=membership.agent_id
	WHERE membership.team_id=$1 ORDER BY membership.position, membership.agent_id`

func collectMembers(rows pgx.Rows) ([]Member, error) {
	defer rows.Close()
	members, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Member, error) {
		var member Member
		err := row.Scan(&member.AgentID, &member.Name, &member.Description, &member.Status, &member.Role, &member.Position)
		return member, err
	})
	if err != nil {
		return nil, fmt.Errorf("read agent team members: %w", err)
	}
	if members == nil {
		members = []Member{}
	}
	return members, nil
}

func recordLifecycle(ctx context.Context, tx pgx.Tx, detailType string, team Team) error {
	memberIDs := make([]string, 0, len(team.Members))
	for _, member := range team.Members {
		memberIDs = append(memberIDs, member.AgentID)
	}
	event, err := events.NewEvent(detailType, agentsv1.AgentTeamChanged{
		TeamID: team.ID, UserID: team.OwnerUserID, OrganizationID: team.OrganizationID,
		Name: team.Name, CoordinatorAgentID: team.CoordinatorAgentID, MemberAgentIDs: memberIDs,
		Status: string(team.Status), Revision: team.Revision, ChangedAt: team.UpdatedAt,
	}, team.UpdatedAt)
	if err != nil {
		return err
	}
	trace := observability.CaptureTraceCarrier(ctx).Encode()
	_, err = tx.Exec(ctx, `INSERT INTO agents_primary.agents_outbox
		(id, detail_type, detail, occurred_at, trace_context) VALUES ($1,$2,$3,$4,NULLIF($5,''))`,
		event.ID, event.DetailType, event.Data, event.OccurredAt, trace)
	return err
}

func validateScope(scope Scope) error {
	if strings.TrimSpace(scope.OrganizationID) == "" || strings.TrimSpace(scope.UserID) == "" {
		return fmt.Errorf("%w: organization and user are required", ErrInvalid)
	}
	return nil
}

func isNameConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "member_agent_teams_owner_name_idx"
}
