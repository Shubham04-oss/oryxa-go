package connect

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	eventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
)

const connectionColumns = `id, org_id, member_id, level, app, mode, status, updated_at`

// scanConnection reads connectionColumns. Nothing secret is selected: the
// view is the event contract's Connection and no more.
func scanConnection(row pgx.Row) (eventsv1.Connection, error) {
	var c eventsv1.Connection
	var member *string
	if err := row.Scan(&c.ID, &c.OrganizationID, &member, &c.Level, &c.App, &c.Mode, &c.Status, &c.UpdatedAt); err != nil {
		return c, err
	}
	if member != nil {
		c.MemberID = *member
	}
	return c, nil
}

// Get answers one connection of the org. A connection of another org is
// ErrNotFound, the same answer as one that does not exist.
func (s *Service) Get(ctx context.Context, org, id string) (eventsv1.Connection, error) {
	c, err := scanConnection(s.pool.QueryRow(ctx,
		`SELECT `+connectionColumns+` FROM integrations.connections WHERE id = $1 AND org_id = $2`, id, org))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// List answers the org's live connections, oldest first. With a member it is
// the connections that member may use: their own and the org-level ones;
// without, every connection of the org.
func (s *Service) List(ctx context.Context, org, member string) ([]eventsv1.Connection, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+connectionColumns+` FROM integrations.connections
		WHERE org_id = $1 AND status <> 'revoked' AND ($2 = '' OR member_id IS NULL OR member_id = $2)
		ORDER BY created_at, id`, org, member)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []eventsv1.Connection{}
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	return list, rows.Err()
}
