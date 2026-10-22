package connect

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	eventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

// emit writes a connection event to the outbox inside tx, the transaction
// that changed the connection, so the event exists if and only if the change
// committed. It reads the row back, so the payload is what was saved: its
// updated_at orders the connection's events for a consumer. The payload is
// the contract's Connection and holds no token, scope or account data.
func (s *Service) emit(ctx context.Context, tx pgx.Tx, connection, detailType string) error {
	c := eventsv1.Connection{ID: connection}
	var member *string
	if err := tx.QueryRow(ctx, `SELECT org_id, member_id, level, app, mode, status, updated_at
		FROM integrations.connections WHERE id = $1`, connection).
		Scan(&c.OrganizationID, &member, &c.Level, &c.App, &c.Mode, &c.Status, &c.UpdatedAt); err != nil {
		return err
	}
	if member != nil {
		c.MemberID = *member
	}
	detail, err := json.Marshal(c)
	if err != nil {
		return err
	}
	id, err := newID("ev_")
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO integrations.outbox (id, detail_type, detail, occurred_at, trace_context)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''))`,
		id, detailType, detail, c.UpdatedAt, observability.CaptureTraceCarrier(ctx).Encode())
	return err
}
