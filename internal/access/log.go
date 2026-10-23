package access

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// Why an allow was given: the rule, or the relation, that granted it.
const (
	// ReasonOwner: it is the caller's own (logged only when an agent reached it).
	ReasonOwner = "owner"
	// ReasonOrgAdmin: an owner's or admin's own session reaches everything.
	ReasonOrgAdmin = "org_admin"
	// ReasonTeamMember and ReasonTeamAdmin: the caller is on the team it
	// belongs to, or is that team's admin.
	ReasonTeamMember = "team_member"
	ReasonTeamAdmin  = "team_admin"
	// ReasonOrganization: it is shared with the whole organization.
	ReasonOrganization = "organization"
	// ReasonGrant: a specialist's grant names it.
	ReasonGrant = "grant"
)

// What the caller did with it.
const (
	ActionRead   = "read"
	ActionSearch = "search"
	ActionWrite  = "write"
	ActionDelete = "delete"
)

// Decision is one allow and the rule that gave it.
type Decision struct {
	At             time.Time `json:"at"`
	OrganizationID string    `json:"organization_id"`
	Service        string    `json:"service"`
	Action         string    `json:"action"`
	ResourceKind   string    `json:"resource_kind"`
	ResourceID     string    `json:"resource_id"`
	// OwnerID is the member whose resource it is; empty for a team's or the
	// organization's.
	OwnerID        string `json:"owner_id,omitempty"`
	ActorUserID    string `json:"actor_user_id"`
	ActorAgentID   string `json:"actor_agent_id,omitempty"`
	ActorAgentRole string `json:"actor_agent_role,omitempty"`
	Reason         string `json:"reason"`
}

// Worth is whether a decision belongs in the log: a member reaching what is
// theirs in their own session tells nobody anything; everything else does.
func (decision Decision) Worth() bool {
	return decision.ActorAgentID != "" || decision.OwnerID != decision.ActorUserID
}

//go:embed migrations/*.sql
var migrations embed.FS

// migrationsAdvisoryLock follows the services' (7263510400 to integrations'
// 7263511100).
const migrationsAdvisoryLock int64 = 7263511200

// Migrate installs the log's table. Every service that records or reads
// decisions calls it; whichever runs first installs it.
func Migrate(ctx context.Context, database *postgreskit.Database) error {
	return database.Migrate(ctx, "access", migrations, "migrations/*.sql", migrationsAdvisoryLock)
}

// Log keeps decisions in access.decisions.
type Log struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
	now    func() time.Time
}

// NewLog returns the log on the shared database.
func NewLog(pool *pgxpool.Pool, logger *slog.Logger) *Log {
	if logger == nil {
		logger = slog.Default()
	}
	return &Log{pool: pool, logger: logger, now: time.Now}
}

// Record keeps a decision worth keeping, once per actor, resource, action and
// rule an hour. A nil log records nothing. It never fails the caller: the
// allow it records was already made, and denying a read because its record
// could not be written would make the log an outage. A failure is logged.
// ponytail: best effort; a deny-on-failure mode is for when audit must be
// complete (Enterprise), with an outbox in the same transaction.
func (log *Log) Record(ctx context.Context, decision Decision) {
	if log == nil || !decision.Worth() {
		return
	}
	if decision.At.IsZero() {
		decision.At = log.now()
	}
	at := decision.At.UTC()
	if _, err := log.pool.Exec(ctx, `INSERT INTO access.decisions
		(at, bucket, organization_id, service, action, resource_kind, resource_id, owner_id, actor_user_id, actor_agent_id, actor_agent_role, reason)
		VALUES ($1, date_trunc('hour', $1::timestamptz, 'UTC'), $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT DO NOTHING`,
		at, decision.OrganizationID, decision.Service, decision.Action, decision.ResourceKind, decision.ResourceID,
		decision.OwnerID, decision.ActorUserID, decision.ActorAgentID, decision.ActorAgentRole, decision.Reason); err != nil {
		log.logger.WarnContext(ctx, "access decision not recorded", "service", decision.Service,
			"resource_kind", decision.ResourceKind, "reason", decision.Reason, "error", err)
	}
}

// ForMember answers, newest first, what reached the member's things and what
// the member's own agents reached: their part of the log.
func (log *Log) ForMember(ctx context.Context, organizationID, userID string, limit int) ([]Decision, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := log.pool.Query(ctx, `SELECT at, organization_id, service, action, resource_kind, resource_id,
		owner_id, actor_user_id, actor_agent_id, actor_agent_role, reason
		FROM access.decisions
		WHERE organization_id = $1 AND (owner_id = $2 OR (actor_user_id = $2 AND actor_agent_id <> ''))
		ORDER BY at DESC, id DESC LIMIT $3`, organizationID, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("read access decisions: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Decision, error) {
		var d Decision
		err := row.Scan(&d.At, &d.OrganizationID, &d.Service, &d.Action, &d.ResourceKind, &d.ResourceID,
			&d.OwnerID, &d.ActorUserID, &d.ActorAgentID, &d.ActorAgentRole, &d.Reason)
		return d, err
	})
}

// Purge deletes decisions older than keep, and answers how many.
// ponytail: one retention for every organization; plans set their own once
// entitlements carry features (design §6 step 7).
func (log *Log) Purge(ctx context.Context, keep time.Duration) (int64, error) {
	tag, err := log.pool.Exec(ctx, `DELETE FROM access.decisions WHERE at < $1`, log.now().Add(-keep))
	if err != nil {
		return 0, fmt.Errorf("purge access decisions: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Retention is how long decisions are kept.
const Retention = 30 * 24 * time.Hour
