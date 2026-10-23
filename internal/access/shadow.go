package access

import (
	"context"
	"time"
)

// ActionUse is acting through something, such as a connection's credential,
// rather than reading it.
const ActionUse = "use"

// Ask is one decision a service's code made, in the terms SpiceDB is asked
// about it in shadow (docs/design/permissions, step 4). internal/access/shadow
// turns it into checks.
type Ask struct {
	OrganizationID string
	Service        string
	Action         string
	ResourceKind   string
	ResourceID     string
	Actor          Actor
	UserID         string
	AgentID        string
	// Limited: the caller is a specialist whose grant names some things of
	// this kind rather than all (Actor.Limited).
	Limited bool
}

// Disagreement is an Ask that SpiceDB answered differently from the code.
type Disagreement struct {
	Ask
	At      time.Time
	Code    bool
	SpiceDB bool
	// Checks is what SpiceDB was asked, as resource#permission@subject.
	Checks []string
}

// Disagree keeps a disagreement, once per actor, resource, action and answer
// an hour, so a hot path can't flood the table. Like Record it never fails
// the caller; a failure is logged.
func (log *Log) Disagree(ctx context.Context, disagreement Disagreement) {
	if log == nil {
		return
	}
	if disagreement.At.IsZero() {
		disagreement.At = log.now()
	}
	ask := disagreement.Ask
	if _, err := log.pool.Exec(ctx, `INSERT INTO access.disagreements
		(at, bucket, organization_id, service, action, resource_kind, resource_id, actor_user_id, actor_agent_id, actor_agent_role, code_allowed, spicedb_allowed, checks)
		VALUES ($1, date_trunc('hour', $1::timestamptz, 'UTC'), $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT DO NOTHING`,
		disagreement.At.UTC(), ask.OrganizationID, ask.Service, ask.Action, ask.ResourceKind, ask.ResourceID,
		ask.UserID, ask.AgentID, ask.Actor.AgentRole, disagreement.Code, disagreement.SpiceDB, disagreement.Checks); err != nil {
		log.logger.WarnContext(ctx, "access disagreement not recorded", "service", ask.Service,
			"resource_kind", ask.ResourceKind, "error", err)
	}
}

// Comparer asks SpiceDB in shadow what the code just decided
// (*shadow.Shadow). A service holding a nil one compares nothing.
type Comparer interface {
	Compare(ctx context.Context, ask Ask, allowed bool)
}
