package teamspaces

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/events"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

//go:embed migrations/*.sql
var migrations embed.FS

const migrationsAdvisoryLock int64 = 7263510711

type Store struct{ database *postgreskit.Database }

func NewStore(database *postgreskit.Database) *Store { return &Store{database: database} }

func (store *Store) Migrate(ctx context.Context) error {
	return store.database.Migrate(ctx, "teamspaces", migrations, "migrations/*.sql", migrationsAdvisoryLock)
}

func (store *Store) EnsureOrganizationAgent(ctx context.Context, organizationID, organizationName, createdBy string) (OrganizationAgent, error) {
	var result OrganizationAgent
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		err := scanOrganizationAgent(tx.QueryRow(ctx, `SELECT `+organizationAgentColumns+`
			FROM agents_primary.organization_agents WHERE organization_id=$1 FOR UPDATE`, organizationID), &result)
		if err == nil {
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		id, err := newID("oag_")
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		result = defaultOrganizationAgent(organizationID, organizationName)
		result.ID, result.Revision, result.CreatedAt, result.UpdatedAt = id, 1, now, now
		insertErr := scanOrganizationAgent(tx.QueryRow(ctx, `INSERT INTO agents_primary.organization_agents
			(organization_id, id, name, description, instructions, status, revision, created_by_user_id, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)
			ON CONFLICT (organization_id) DO NOTHING
			RETURNING `+organizationAgentColumns,
			organizationID, id, result.Name, result.Description, result.Instructions, result.Status, result.Revision, createdBy, now), &result)
		if errors.Is(insertErr, pgx.ErrNoRows) {
			result, insertErr = organizationAgentFrom(ctx, tx, organizationID)
			return insertErr
		}
		if insertErr != nil {
			return insertErr
		}
		return recordOrganizationAgentLifecycle(ctx, tx, agentsv1.OrganizationAgentCreatedDetailType, result)
	})
	if err != nil {
		return OrganizationAgent{}, fmt.Errorf("ensure organization agent: %w", err)
	}
	return result, nil
}

func defaultOrganizationAgent(organizationID, organizationName string) OrganizationAgent {
	name := cleanLine(organizationName + " Agent")
	if name == "Agent" {
		name = "Organization Agent"
	}
	return OrganizationAgent{
		OrganizationID: organizationID, Name: name,
		Description:  "Summarizes the work each team publishes in its teamspace, for that team.",
		Instructions: "Keep each teamspace's synthesis current from the entries its team publishes: summarize, record decisions, risks and open questions, and propose follow-ups for people to pick up. You have no tools and cannot assign or start work. Preserve attribution, and never assume a member's identity, connections, or private context.",
		Status:       StatusActive,
	}
}

func (store *Store) GetOrganizationAgent(ctx context.Context, organizationID string) (OrganizationAgent, error) {
	var result OrganizationAgent
	err := scanOrganizationAgent(store.database.Pool.QueryRow(ctx, `SELECT `+organizationAgentColumns+`
		FROM agents_primary.organization_agents WHERE organization_id=$1`, organizationID), &result)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrganizationAgent{}, ErrNotFound
	}
	if err != nil {
		return OrganizationAgent{}, fmt.Errorf("get organization agent: %w", err)
	}
	return result, nil
}

func (store *Store) UpdateOrganizationAgent(ctx context.Context, organizationID string, input OrganizationAgentUpdate) (OrganizationAgent, error) {
	var result OrganizationAgent
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		current, err := getOrganizationAgentForUpdate(ctx, tx, organizationID)
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
		if err := scanOrganizationAgent(tx.QueryRow(ctx, `UPDATE agents_primary.organization_agents SET
			name=$2, description=$3, instructions=$4, status=$5, revision=$6, updated_at=$7
			WHERE organization_id=$1 RETURNING `+organizationAgentColumns,
			organizationID, current.Name, current.Description, current.Instructions, current.Status, current.Revision, current.UpdatedAt), &result); err != nil {
			return err
		}
		return recordOrganizationAgentLifecycle(ctx, tx, agentsv1.OrganizationAgentUpdatedDetailType, result)
	})
	if err != nil {
		return OrganizationAgent{}, fmt.Errorf("update organization agent: %w", err)
	}
	return result, nil
}

func (store *Store) Create(ctx context.Context, space Space) (Space, error) {
	now := time.Now().UTC()
	space.Status, space.Revision, space.CreatedAt, space.UpdatedAt = StatusActive, 1, now, now
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		if err := scanSpace(tx.QueryRow(ctx, `INSERT INTO agents_primary.teamspaces
			(id, organization_id, team_id, name, description, created_by_user_id,
			 organization_agent_id, status, revision, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10)
			RETURNING `+spaceColumns,
			space.ID, space.OrganizationID, space.TeamID, space.Name, space.Description, space.CreatedByUserID,
			space.OrganizationAgentID, space.Status, space.Revision, now), &space); err != nil {
			return err
		}
		agent, agentErr := organizationAgentFrom(ctx, tx, space.OrganizationID)
		if agentErr != nil {
			return agentErr
		}
		space.OrganizationAgent = agent
		space.Contributions = []Contribution{}
		return recordLifecycle(ctx, tx, agentsv1.TeamspaceCreatedDetailType, space)
	})
	if isConflict(err) {
		return Space{}, ErrConflict
	}
	if err != nil {
		return Space{}, fmt.Errorf("create teamspace: %w", err)
	}
	return space, nil
}

func (store *Store) List(ctx context.Context, organizationID string) ([]Space, error) {
	rows, err := store.database.Pool.Query(ctx, `SELECT `+spaceColumns+` FROM agents_primary.teamspaces
		WHERE organization_id=$1 ORDER BY lower(name), id`, organizationID)
	if err != nil {
		return nil, fmt.Errorf("list teamspaces: %w", err)
	}
	defer rows.Close()
	spaces := []Space{}
	for rows.Next() {
		var space Space
		if err := scanSpace(rows, &space); err != nil {
			return nil, fmt.Errorf("scan teamspace: %w", err)
		}
		spaces = append(spaces, space)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list teamspaces: %w", err)
	}
	for index := range spaces {
		if err := store.hydrate(ctx, &spaces[index]); err != nil {
			return nil, err
		}
	}
	return spaces, nil
}

func (store *Store) Get(ctx context.Context, organizationID, id string) (Space, error) {
	var space Space
	err := scanSpace(store.database.Pool.QueryRow(ctx, `SELECT `+spaceColumns+` FROM agents_primary.teamspaces
		WHERE organization_id=$1 AND id=$2`, organizationID, id), &space)
	if errors.Is(err, pgx.ErrNoRows) {
		return Space{}, ErrNotFound
	}
	if err != nil {
		return Space{}, fmt.Errorf("get teamspace: %w", err)
	}
	if err := store.hydrate(ctx, &space); err != nil {
		return Space{}, err
	}
	return space, nil
}

// Name is the name of teamspace id in the organization, or ErrNotFound. It
// reads nothing else about the space, and nobody's standing in it.
func (store *Store) Name(ctx context.Context, organizationID, id string) (string, error) {
	var name string
	err := store.database.Pool.QueryRow(ctx, `SELECT name FROM agents_primary.teamspaces
		WHERE organization_id=$1 AND id=$2`, organizationID, id).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read teamspace name: %w", err)
	}
	return name, nil
}

func (store *Store) Update(ctx context.Context, organizationID, id string, input UpdateInput) (Space, error) {
	var result Space
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		current, err := getForUpdate(ctx, tx, organizationID, id)
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
		current.Revision++
		current.UpdatedAt = time.Now().UTC()
		if err := scanSpace(tx.QueryRow(ctx, `UPDATE agents_primary.teamspaces SET
			name=$3, description=$4, status=$5, revision=$6, updated_at=$7
			WHERE organization_id=$1 AND id=$2 RETURNING `+spaceColumns,
			organizationID, id, current.Name, current.Description, current.Status,
			current.Revision, current.UpdatedAt), &result); err != nil {
			return err
		}
		result.OrganizationAgent, err = organizationAgentFrom(ctx, tx, result.OrganizationID)
		if err != nil {
			return err
		}
		result.Contributions, err = contributionsFrom(ctx, tx, result.ID)
		if err != nil {
			return err
		}
		return recordLifecycle(ctx, tx, agentsv1.TeamspaceUpdatedDetailType, result)
	})
	if isConflict(err) {
		return Space{}, ErrConflict
	}
	if err != nil {
		return Space{}, fmt.Errorf("update teamspace: %w", err)
	}
	return result, nil
}

func (store *Store) Delete(ctx context.Context, organizationID, id string) error {
	return pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		current, err := getForUpdate(ctx, tx, organizationID, id)
		if err != nil {
			return err
		}
		current.Contributions, err = contributionsFrom(ctx, tx, current.ID)
		if err != nil {
			return err
		}
		current.OrganizationAgent, err = organizationAgentFrom(ctx, tx, current.OrganizationID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM agents_primary.teamspaces WHERE organization_id=$1 AND id=$2`, organizationID, id); err != nil {
			return fmt.Errorf("delete teamspace: %w", err)
		}
		current.Revision++
		current.UpdatedAt = time.Now().UTC()
		return recordLifecycle(ctx, tx, agentsv1.TeamspaceDeletedDetailType, current)
	})
}

func (store *Store) AddContribution(ctx context.Context, actor Actor, id string, input ContributionInput) (Space, error) {
	return store.changeContribution(ctx, actor.OrganizationID, id, func(tx pgx.Tx, space *Space) error {
		var owner, kind, status string
		err := tx.QueryRow(ctx, `SELECT owner_user_id, kind, status FROM agents_primary.member_agents
			WHERE organization_id=$1 AND owner_user_id=$2 AND id=$3`, actor.OrganizationID, actor.UserID, input.AgentID).
			Scan(&owner, &kind, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: specialist does not belong to this member", ErrInvalid)
		}
		if err != nil {
			return err
		}
		if kind != "specialist" || status != "active" {
			return fmt.Errorf("%w: contribution must be an active specialist", ErrInvalid)
		}
		var position int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(position), -1) + 1
			FROM agents_primary.teamspace_agent_contributions WHERE teamspace_id=$1`, id).Scan(&position); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO agents_primary.teamspace_agent_contributions
			(teamspace_id, agent_id, owner_user_id, role, delegation_mode, position)
			VALUES ($1,$2,$3,$4,$5,$6)`, id, input.AgentID, owner, input.Role, input.DelegationMode, position)
		return err
	})
}

func (store *Store) UpdateContribution(ctx context.Context, actor Actor, id, agentID string, input ContributionUpdate) (Space, error) {
	return store.changeContribution(ctx, actor.OrganizationID, id, func(tx pgx.Tx, _ *Space) error {
		command, err := tx.Exec(ctx, `UPDATE agents_primary.teamspace_agent_contributions SET
			role=COALESCE($4, role), delegation_mode=COALESCE($5, delegation_mode)
			WHERE teamspace_id=$1 AND agent_id=$2 AND owner_user_id=$3`, id, agentID, actor.UserID, input.Role, input.DelegationMode)
		if err != nil {
			return err
		}
		if command.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (store *Store) DeleteContribution(ctx context.Context, organizationID, id, agentID string) (Space, error) {
	return store.changeContribution(ctx, organizationID, id, func(tx pgx.Tx, _ *Space) error {
		command, err := tx.Exec(ctx, `DELETE FROM agents_primary.teamspace_agent_contributions
			WHERE teamspace_id=$1 AND agent_id=$2`, id, agentID)
		if err != nil {
			return err
		}
		if command.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (store *Store) PublishEntry(ctx context.Context, entry Entry) (Entry, error) {
	if entry.ToolsUsed == nil {
		entry.ToolsUsed = []string{}
	}
	if entry.Artifacts == nil {
		entry.Artifacts = []EntryArtifact{}
	}
	tools, err := json.Marshal(entry.ToolsUsed)
	if err != nil {
		return Entry{}, fmt.Errorf("encode entry tools: %w", err)
	}
	artifacts, err := json.Marshal(entry.Artifacts)
	if err != nil {
		return Entry{}, fmt.Errorf("encode entry artifacts: %w", err)
	}
	entry.CreatedAt = time.Now().UTC()
	err = pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		if err := scanEntry(tx.QueryRow(ctx, `INSERT INTO agents_primary.teamspace_entries
			(id, teamspace_id, organization_id, author_user_id, author_agent_id, author_agent_name,
			 kind, title, summary, tools_used, artifacts, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			RETURNING `+entryColumns,
			entry.ID, entry.TeamspaceID, entry.OrganizationID, entry.AuthorUserID, entry.AuthorAgentID,
			entry.AuthorAgentName, entry.Kind, entry.Title, entry.Summary, tools, artifacts, entry.CreatedAt), &entry); err != nil {
			return err
		}
		return recordEntryPublished(ctx, tx, entry)
	})
	if err != nil {
		return Entry{}, fmt.Errorf("publish teamspace entry: %w", err)
	}
	return entry, nil
}

func (store *Store) ListEntries(ctx context.Context, organizationID, teamspaceID string, limit int) ([]Entry, error) {
	rows, err := store.database.Pool.Query(ctx, `SELECT `+entryColumns+`
		FROM agents_primary.teamspace_entries
		WHERE organization_id=$1 AND teamspace_id=$2
		ORDER BY created_at DESC, id DESC LIMIT $3`, organizationID, teamspaceID, limit)
	if err != nil {
		return nil, fmt.Errorf("list teamspace entries: %w", err)
	}
	defer rows.Close()
	entries := []Entry{}
	for rows.Next() {
		var entry Entry
		if err := scanEntry(rows, &entry); err != nil {
			return nil, fmt.Errorf("scan teamspace entry: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (store *Store) GetEntry(ctx context.Context, organizationID, id string) (Entry, error) {
	var entry Entry
	err := scanEntry(store.database.Pool.QueryRow(ctx, `SELECT `+entryColumns+`
		FROM agents_primary.teamspace_entries WHERE organization_id=$1 AND id=$2`, organizationID, id), &entry)
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, ErrNotFound
	}
	if err != nil {
		return Entry{}, fmt.Errorf("get teamspace entry: %w", err)
	}
	return entry, nil
}

func (store *Store) changeContribution(ctx context.Context, organizationID, id string, change func(pgx.Tx, *Space) error) (Space, error) {
	var result Space
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		current, err := getForUpdate(ctx, tx, organizationID, id)
		if err != nil {
			return err
		}
		if err := change(tx, &current); err != nil {
			return err
		}
		current.Revision++
		current.UpdatedAt = time.Now().UTC()
		if err := scanSpace(tx.QueryRow(ctx, `UPDATE agents_primary.teamspaces SET revision=$3, updated_at=$4
			WHERE organization_id=$1 AND id=$2 RETURNING `+spaceColumns,
			organizationID, id, current.Revision, current.UpdatedAt), &result); err != nil {
			return err
		}
		result.Contributions, err = contributionsFrom(ctx, tx, result.ID)
		if err != nil {
			return err
		}
		result.OrganizationAgent, err = organizationAgentFrom(ctx, tx, result.OrganizationID)
		if err != nil {
			return err
		}
		return recordLifecycle(ctx, tx, agentsv1.TeamspaceContributionChangedDetailType, result)
	})
	if isContributionConflict(err) {
		return Space{}, ErrConflict
	}
	if err != nil {
		return Space{}, fmt.Errorf("change teamspace contribution: %w", err)
	}
	return result, nil
}

// Forget removes an exiting member's contributions but leaves the team-owned
// space intact. Organization deletion removes the spaces themselves.
func (store *Store) Forget(ctx context.Context, organizationID, userID string) error {
	if strings.TrimSpace(userID) == "" {
		return pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `DELETE FROM agents_primary.teamspaces WHERE organization_id=$1`, organizationID); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `DELETE FROM agents_primary.organization_agents WHERE organization_id=$1`, organizationID)
			return err
		})
	}
	return pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		return forgetMemberContributions(ctx, tx, organizationID, "", userID)
	})
}

// ForgetTeamMember is what a person's place on a human team ending takes with
// it, when Teams reports it. Every specialist they contributed to the team's
// teamspace is withdrawn. And when the team has a teamspace, their leaving it
// is recorded as agents.teamspace.member.removed.v1, whether or not they had
// contributed any, so realtime has the web app purge their side conversations
// there. Both happen in one transaction, through the outbox. removedAt is
// when the membership ended. A Teams event delivered again records the event
// again, with the same removedAt, which its consumers take as a repeat.
func (store *Store) ForgetTeamMember(ctx context.Context, organizationID, teamID, userID string, removedAt time.Time) error {
	// An empty team would widen the withdrawal to every teamspace in the
	// organization.
	if organizationID == "" || teamID == "" || userID == "" {
		return errors.New("forget a team member: the organization, the team and the member are required")
	}
	if removedAt.IsZero() {
		return errors.New("forget a team member: when their membership ended is required")
	}
	return pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		// A team has one teamspace at most.
		var teamspaceID string
		err := tx.QueryRow(ctx, `SELECT id FROM agents_primary.teamspaces
			WHERE organization_id=$1 AND team_id=$2 FOR UPDATE`, organizationID, teamID).Scan(&teamspaceID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := forgetMemberContributions(ctx, tx, organizationID, teamID, userID); err != nil {
			return err
		}
		if teamspaceID == "" {
			return nil
		}
		return recordTeamspaceMemberRemoved(ctx, tx, agentsv1.TeamspaceMemberRemoved{
			OrganizationID: organizationID, TeamspaceID: teamspaceID, TeamID: teamID, UserID: userID,
			RemovedAt: removedAt.UTC(),
		})
	})
}

// forgetMemberContributions withdraws, in tx, every specialist userID
// contributed to the organization's teamspaces, or only to teamID's when it
// is named, recording each teamspace's change.
func forgetMemberContributions(ctx context.Context, tx pgx.Tx, organizationID, teamID, userID string) error {
	rows, err := tx.Query(ctx, `SELECT `+spaceColumns+` FROM agents_primary.teamspaces space
		WHERE space.organization_id=$1 AND ($2='' OR space.team_id=$2) AND EXISTS (
			SELECT 1 FROM agents_primary.teamspace_agent_contributions contribution
			WHERE contribution.teamspace_id=space.id AND contribution.owner_user_id=$3
		) FOR UPDATE OF space`, organizationID, teamID, userID)
	if err != nil {
		return err
	}
	spaces := []Space{}
	for rows.Next() {
		var space Space
		if err := scanSpace(rows, &space); err != nil {
			rows.Close()
			return err
		}
		spaces = append(spaces, space)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, space := range spaces {
		if _, err := tx.Exec(ctx, `DELETE FROM agents_primary.teamspace_agent_contributions
			WHERE teamspace_id=$1 AND owner_user_id=$2`, space.ID, userID); err != nil {
			return err
		}
		space.Revision++
		space.UpdatedAt = time.Now().UTC()
		var updated Space
		if err := scanSpace(tx.QueryRow(ctx, `UPDATE agents_primary.teamspaces SET revision=$2, updated_at=$3
			WHERE id=$1 RETURNING `+spaceColumns, space.ID, space.Revision, space.UpdatedAt), &updated); err != nil {
			return err
		}
		updated.Contributions, err = contributionsFrom(ctx, tx, updated.ID)
		if err != nil {
			return err
		}
		updated.OrganizationAgent, err = organizationAgentFrom(ctx, tx, updated.OrganizationID)
		if err != nil {
			return err
		}
		if err := recordLifecycle(ctx, tx, agentsv1.TeamspaceContributionChangedDetailType, updated); err != nil {
			return err
		}
	}
	return nil
}

// ForgetTeam removes the shared space when Teams reports that its human team
// is gone. Repeated EventBridge delivery is harmless.
func (store *Store) ForgetTeam(ctx context.Context, organizationID, teamID string) error {
	return pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		var current Space
		err := scanSpace(tx.QueryRow(ctx, `SELECT `+spaceColumns+` FROM agents_primary.teamspaces
			WHERE organization_id=$1 AND team_id=$2 FOR UPDATE`, organizationID, teamID), &current)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		current.Contributions, err = contributionsFrom(ctx, tx, current.ID)
		if err != nil {
			return err
		}
		current.OrganizationAgent, err = organizationAgentFrom(ctx, tx, current.OrganizationID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM agents_primary.teamspaces WHERE id=$1`, current.ID); err != nil {
			return err
		}
		current.Revision++
		current.UpdatedAt = time.Now().UTC()
		return recordLifecycle(ctx, tx, agentsv1.TeamspaceDeletedDetailType, current)
	})
}

const spaceColumns = `id, organization_id, team_id, name, description, created_by_user_id,
	organization_agent_id, status, revision, created_at, updated_at`

const entryColumns = `id, teamspace_id, organization_id, author_user_id, author_agent_id,
	author_agent_name, kind, title, summary, tools_used, artifacts, created_at`

type scanner interface{ Scan(...any) error }

const organizationAgentColumns = `organization_id, id, name, description, instructions,
	status, revision, created_at, updated_at`

func scanOrganizationAgent(row scanner, agent *OrganizationAgent) error {
	return row.Scan(&agent.OrganizationID, &agent.ID, &agent.Name, &agent.Description, &agent.Instructions,
		&agent.Status, &agent.Revision, &agent.CreatedAt, &agent.UpdatedAt)
}

func getOrganizationAgentForUpdate(ctx context.Context, tx pgx.Tx, organizationID string) (OrganizationAgent, error) {
	var agent OrganizationAgent
	err := scanOrganizationAgent(tx.QueryRow(ctx, `SELECT `+organizationAgentColumns+`
		FROM agents_primary.organization_agents WHERE organization_id=$1 FOR UPDATE`, organizationID), &agent)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrganizationAgent{}, ErrNotFound
	}
	return agent, err
}

func organizationAgentFrom(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, organizationID string) (OrganizationAgent, error) {
	var agent OrganizationAgent
	err := scanOrganizationAgent(queryer.QueryRow(ctx, `SELECT `+organizationAgentColumns+`
		FROM agents_primary.organization_agents WHERE organization_id=$1`, organizationID), &agent)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrganizationAgent{}, ErrNotFound
	}
	return agent, err
}

func scanSpace(row scanner, space *Space) error {
	return row.Scan(&space.ID, &space.OrganizationID, &space.TeamID, &space.Name, &space.Description,
		&space.CreatedByUserID, &space.OrganizationAgentID, &space.Status, &space.Revision, &space.CreatedAt, &space.UpdatedAt)
}

func scanEntry(row scanner, entry *Entry) error {
	var tools, artifacts []byte
	if err := row.Scan(&entry.ID, &entry.TeamspaceID, &entry.OrganizationID, &entry.AuthorUserID,
		&entry.AuthorAgentID, &entry.AuthorAgentName, &entry.Kind, &entry.Title, &entry.Summary,
		&tools, &artifacts, &entry.CreatedAt); err != nil {
		return err
	}
	if err := json.Unmarshal(tools, &entry.ToolsUsed); err != nil {
		return fmt.Errorf("decode entry tools: %w", err)
	}
	if err := json.Unmarshal(artifacts, &entry.Artifacts); err != nil {
		return fmt.Errorf("decode entry artifacts: %w", err)
	}
	if entry.ToolsUsed == nil {
		entry.ToolsUsed = []string{}
	}
	if entry.Artifacts == nil {
		entry.Artifacts = []EntryArtifact{}
	}
	return nil
}

func getForUpdate(ctx context.Context, tx pgx.Tx, organizationID, id string) (Space, error) {
	var space Space
	err := scanSpace(tx.QueryRow(ctx, `SELECT `+spaceColumns+` FROM agents_primary.teamspaces
		WHERE organization_id=$1 AND id=$2 FOR UPDATE`, organizationID, id), &space)
	if errors.Is(err, pgx.ErrNoRows) {
		return Space{}, ErrNotFound
	}
	return space, err
}

func (store *Store) hydrate(ctx context.Context, space *Space) error {
	agent, err := organizationAgentFrom(ctx, store.database.Pool, space.OrganizationID)
	if err != nil {
		return fmt.Errorf("read organization agent: %w", err)
	}
	space.OrganizationAgent = agent
	space.Contributions, err = store.contributions(ctx, space.ID)
	return err
}

func (store *Store) contributions(ctx context.Context, teamspaceID string) ([]Contribution, error) {
	return contributionsFrom(ctx, store.database.Pool, teamspaceID)
}

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func contributionsFrom(ctx context.Context, queryer queryer, teamspaceID string) ([]Contribution, error) {
	rows, err := queryer.Query(ctx, `SELECT contribution.agent_id, contribution.owner_user_id,
		agent.name, agent.description, agent.status, contribution.role,
		contribution.delegation_mode, contribution.position, contribution.created_at
		FROM agents_primary.teamspace_agent_contributions contribution
		JOIN agents_primary.member_agents agent ON agent.id=contribution.agent_id
		WHERE contribution.teamspace_id=$1 ORDER BY contribution.position, contribution.agent_id`, teamspaceID)
	if err != nil {
		return nil, fmt.Errorf("list teamspace contributions: %w", err)
	}
	defer rows.Close()
	contributions := []Contribution{}
	for rows.Next() {
		var contribution Contribution
		if err := rows.Scan(&contribution.AgentID, &contribution.OwnerUserID, &contribution.Name,
			&contribution.Description, &contribution.Status, &contribution.Role,
			&contribution.DelegationMode, &contribution.Position, &contribution.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan teamspace contribution: %w", err)
		}
		contributions = append(contributions, contribution)
	}
	return contributions, rows.Err()
}

func recordLifecycle(ctx context.Context, tx pgx.Tx, detailType string, space Space) error {
	contributors := make([]string, 0, len(space.Contributions))
	for _, contribution := range space.Contributions {
		if !slices.Contains(contributors, contribution.OwnerUserID) {
			contributors = append(contributors, contribution.OwnerUserID)
		}
	}
	slices.Sort(contributors)
	event, err := events.NewEvent(detailType, agentsv1.TeamspaceChanged{
		TeamspaceID: space.ID, OrganizationID: space.OrganizationID, TeamID: space.TeamID,
		OrganizationAgentID: space.OrganizationAgent.ID, Name: space.Name, Status: string(space.Status),
		Revision: space.Revision, ContributorUserIDs: contributors, ChangedAt: space.UpdatedAt,
	}, space.UpdatedAt)
	if err != nil {
		return err
	}
	trace := observability.CaptureTraceCarrier(ctx).Encode()
	_, err = tx.Exec(ctx, `INSERT INTO agents_primary.agents_outbox
		(id, detail_type, detail, occurred_at, trace_context) VALUES ($1,$2,$3,$4,NULLIF($5,''))`,
		event.ID, event.DetailType, event.Data, event.OccurredAt, trace)
	return err
}

// recordTeamspaceMemberRemoved records, in tx, that a person left a
// teamspace. It happened when their membership ended.
func recordTeamspaceMemberRemoved(ctx context.Context, tx pgx.Tx, removed agentsv1.TeamspaceMemberRemoved) error {
	event, err := events.NewEvent(agentsv1.TeamspaceMemberRemovedDetailType, removed, removed.RemovedAt)
	if err != nil {
		return err
	}
	trace := observability.CaptureTraceCarrier(ctx).Encode()
	_, err = tx.Exec(ctx, `INSERT INTO agents_primary.agents_outbox
		(id, detail_type, detail, occurred_at, trace_context) VALUES ($1,$2,$3,$4,NULLIF($5,''))`,
		event.ID, event.DetailType, event.Data, event.OccurredAt, trace)
	return err
}

func recordOrganizationAgentLifecycle(ctx context.Context, tx pgx.Tx, detailType string, agent OrganizationAgent) error {
	event, err := events.NewEvent(detailType, agentsv1.OrganizationAgentChanged{
		OrganizationID: agent.OrganizationID, AgentID: agent.ID, Name: agent.Name,
		Status: string(agent.Status), Revision: agent.Revision, ChangedAt: agent.UpdatedAt,
	}, agent.UpdatedAt)
	if err != nil {
		return err
	}
	trace := observability.CaptureTraceCarrier(ctx).Encode()
	_, err = tx.Exec(ctx, `INSERT INTO agents_primary.agents_outbox
		(id, detail_type, detail, occurred_at, trace_context) VALUES ($1,$2,$3,$4,NULLIF($5,''))`,
		event.ID, event.DetailType, event.Data, event.OccurredAt, trace)
	return err
}

func recordEntryPublished(ctx context.Context, tx pgx.Tx, entry Entry) error {
	event, err := events.NewEvent(agentsv1.TeamspaceEntryPublishedDetailType, agentsv1.TeamspaceEntryPublished{
		EntryID: entry.ID, TeamspaceID: entry.TeamspaceID, OrganizationID: entry.OrganizationID,
		AuthorUserID: entry.AuthorUserID, AuthorAgentID: entry.AuthorAgentID,
		Kind: string(entry.Kind), PublishedAt: entry.CreatedAt,
	}, entry.CreatedAt)
	if err != nil {
		return err
	}
	trace := observability.CaptureTraceCarrier(ctx).Encode()
	_, err = tx.Exec(ctx, `INSERT INTO agents_primary.agents_outbox
		(id, detail_type, detail, occurred_at, trace_context) VALUES ($1,$2,$3,$4,NULLIF($5,''))`,
		event.ID, event.DetailType, event.Data, event.OccurredAt, trace)
	return err
}

func isConflict(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return pgErr.ConstraintName == "teamspaces_organization_id_team_id_key" || pgErr.ConstraintName == "teamspaces_org_name_idx"
}

func isContributionConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
