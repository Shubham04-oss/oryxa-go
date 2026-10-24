package projector

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
)

// The reconcile reads each service's own tables, read only, from the
// database the single binary shares. No service has an API that lists every
// organization's rows, and these queries are the one place that knows their
// shape. ponytail: a service that moves to its own database needs an export
// endpoint here instead.
//
// Each takes an organization, or "" for all of them.
var sourceQueries = map[string]func(context.Context, *pgxpool.Pool, string, *World) error{
	"auth": func(ctx context.Context, pool *pgxpool.Pool, org string, world *World) error {
		return scan(ctx, pool, `SELECT m.organization_id, m.user_id, m.role, u.email
			FROM auth_memberships m JOIN auth_users u ON u.id = m.user_id
			WHERE $1 = '' OR m.organization_id = $1`, org, func(rows pgx.Rows) error {
			var row Membership
			err := rows.Scan(&row.Org, &row.User, &row.Role, &row.Email)
			world.Memberships = append(world.Memberships, row)
			return err
		})
	},
	"teams": func(ctx context.Context, pool *pgxpool.Pool, org string, world *World) error {
		// A team's parent is the team one level up its ltree path.
		err := scan(ctx, pool, `SELECT t.id, t.organization_id, coalesce(p.id, '')
			FROM teams t
			JOIN teams_organizations o ON o.organization_id = t.organization_id AND o.deleted_at IS NULL
			LEFT JOIN teams p ON p.organization_id = t.organization_id AND p.path @> t.path AND nlevel(p.path) = nlevel(t.path) - 1
			WHERE $1 = '' OR t.organization_id = $1`, org, func(rows pgx.Rows) error {
			var row Team
			err := rows.Scan(&row.ID, &row.Org, &row.Parent)
			world.Teams = append(world.Teams, row)
			return err
		})
		if err != nil {
			return err
		}
		return scan(ctx, pool, `SELECT m.team_id, m.user_id, m.role
			FROM teams_memberships m
			JOIN teams_organizations o ON o.organization_id = m.organization_id AND o.deleted_at IS NULL
			WHERE $1 = '' OR m.organization_id = $1`, org, func(rows pgx.Rows) error {
			var row TeamMember
			err := rows.Scan(&row.Team, &row.User, &row.Role)
			world.TeamMembers = append(world.TeamMembers, row)
			return err
		})
	},
	"rag": func(ctx context.Context, pool *pgxpool.Pool, org string, world *World) error {
		return scan(ctx, pool, `SELECT id, organization_id, scope, coalesce(team_id, ''), coalesce(member_id, ''), created_by
			FROM rag_corpora WHERE $1 = '' OR organization_id = $1`, org, func(rows pgx.Rows) error {
			var row PlacedRow
			err := rows.Scan(&row.ID, &row.Org, &row.Scope, &row.Team, &row.Member, &row.CreatedBy)
			world.Corpora = append(world.Corpora, row)
			return err
		})
	},
	"files": func(ctx context.Context, pool *pgxpool.Pool, org string, world *World) error {
		return scan(ctx, pool, `SELECT id, organization_id, scope, coalesce(team_id, ''), coalesce(member_id, ''), access, created_by
			FROM files_objects WHERE $1 = '' OR organization_id = $1`, org, func(rows pgx.Rows) error {
			var row PlacedRow
			err := rows.Scan(&row.ID, &row.Org, &row.Scope, &row.Team, &row.Member, &row.Access, &row.CreatedBy)
			world.Files = append(world.Files, row)
			return err
		})
	},
	"knowledge": func(ctx context.Context, pool *pgxpool.Pool, org string, world *World) error {
		return scan(ctx, pool, `SELECT id, organization_id, connection_id, created_by
			FROM knowledge_sources WHERE ($1 = '' OR organization_id = $1)`, org, func(rows pgx.Rows) error {
			var row Source
			err := rows.Scan(&row.ID, &row.Org, &row.Connection, &row.CreatedBy)
			world.Sources = append(world.Sources, row)
			return err
		})
	},
	"apps": func(ctx context.Context, pool *pgxpool.Pool, org string, world *World) error {
		return scan(ctx, pool, `SELECT id, organization_id, member_id, toolkit
			FROM apps_connections WHERE ($1 = '' OR organization_id = $1)`, org, func(rows pgx.Rows) error {
			var row Connection
			err := rows.Scan(&row.ID, &row.Org, &row.Member, &row.Toolkit)
			world.Connections = append(world.Connections, row)
			return err
		})
	},
	"agents": func(ctx context.Context, pool *pgxpool.Pool, org string, world *World) error {
		err := scan(ctx, pool, `SELECT id, organization_id, owner_user_id, kind, app_grant
			FROM agents_primary.member_agents WHERE $1 = '' OR organization_id = $1`, org, func(rows pgx.Rows) error {
			var row Agent
			var grant []byte
			if err := rows.Scan(&row.ID, &row.Org, &row.User, &row.Kind, &grant); err != nil {
				return err
			}
			if grant != nil {
				row.Grant = &Grant{}
				if err := json.Unmarshal(grant, row.Grant); err != nil {
					return fmt.Errorf("agent %s grant: %w", row.ID, err)
				}
			}
			world.Agents = append(world.Agents, row)
			return nil
		})
		if err != nil {
			return err
		}
		return scan(ctx, pool, `SELECT id, organization_id, requester_user_id, requester_agent_id, target_user_id, target_agent_id
			FROM agents_primary.agent_interactions WHERE $1 = '' OR organization_id = $1`, org, func(rows pgx.Rows) error {
			var row Request
			err := rows.Scan(&row.ID, &row.Org, &row.RequesterUser, &row.RequesterAgent, &row.TargetUser, &row.TargetAgent)
			world.Requests = append(world.Requests, row)
			return err
		})
	},
	"workspace": func(ctx context.Context, pool *pgxpool.Pool, org string, world *World) error {
		return scan(ctx, pool, `SELECT organization_id, document FROM workspace.configs
			WHERE $1 = '' OR organization_id = $1`, org, func(rows pgx.Rows) error {
			var row Chart
			var document []byte
			if err := rows.Scan(&row.Org, &document); err != nil {
				return err
			}
			parsed, err := spec.ParseStored(document)
			if err != nil {
				return fmt.Errorf("workspace %s: %w", row.Org, err)
			}
			if parsed.OrgChart != nil {
				row.Chart = *parsed.OrgChart
				world.Charts = append(world.Charts, row)
			}
			return nil
		})
	},
}

func scan(ctx context.Context, pool *pgxpool.Pool, query, org string, each func(pgx.Rows) error) error {
	rows, err := pool.Query(ctx, query, org)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := each(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ReadWorld reads the named sources (all when none are named). A source that
// fails is logged and left out of World.Read, and its slices are skipped.
func ReadWorld(ctx context.Context, pool *pgxpool.Pool, org string, logger *slog.Logger, names ...string) World {
	if len(names) == 0 {
		for name := range sourceQueries {
			names = append(names, name)
		}
	}
	world := World{Read: map[string]bool{}}
	for _, name := range names {
		// Each source reads into a scratch World, so a failure part way
		// leaves no half-read rows behind.
		var partial World
		if err := sourceQueries[name](ctx, pool, org, &partial); err != nil {
			logger.WarnContext(ctx, "spicedb reconcile: source unreadable; its relationships are left as they are", "source", name, "error", err)
			continue
		}
		world.Read[name] = true
		world.Memberships = append(world.Memberships, partial.Memberships...)
		world.Teams = append(world.Teams, partial.Teams...)
		world.TeamMembers = append(world.TeamMembers, partial.TeamMembers...)
		world.Corpora = append(world.Corpora, partial.Corpora...)
		world.Files = append(world.Files, partial.Files...)
		world.Sources = append(world.Sources, partial.Sources...)
		world.Connections = append(world.Connections, partial.Connections...)
		world.Agents = append(world.Agents, partial.Agents...)
		world.Requests = append(world.Requests, partial.Requests...)
		world.Charts = append(world.Charts, partial.Charts...)
	}
	return world
}
