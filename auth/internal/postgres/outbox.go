package postgres

import (
	"context"
	"fmt"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/internal/outbox"
)

func (store *Store) EnqueueEvent(ctx context.Context, event domain.Event) error {
	// The calling request's trace, if it has one, travels with the event from
	// here on; see insertEvents in store.go for the same treatment on the
	// transactional path.
	traceContext := observability.CaptureTraceCarrier(ctx).Encode()
	_, err := store.pool.Exec(ctx, `
		INSERT INTO auth_outbox (id, detail_type, detail, occurred_at, trace_context)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''))`, event.ID, event.DetailType, event.Data, event.OccurredAt, traceContext)
	if err != nil {
		return fmt.Errorf("enqueue event: %w", err)
	}
	return nil
}

// OutboxTable is auth_outbox, for internal/outbox to publish from. The events
// that carry a single-use link or token are blanked once delivered: they are
// needed only until the notification goes out.
func (store *Store) OutboxTable() (*outbox.Table, error) {
	return outbox.NewTable(store.pool, "auth_outbox", outbox.RedactOnPublish(
		autheventsv1.EmailVerificationDeliveryRequestedDetailType,
		autheventsv1.PasswordResetDeliveryRequestedDetailType,
		autheventsv1.MemberInvitedDetailType,
	))
}
