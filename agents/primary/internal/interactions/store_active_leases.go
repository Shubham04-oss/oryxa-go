package interactions

import (
	"context"
	"fmt"
	"time"
)

// ActiveLeases filters before it limits, so old revoked/expired windows cannot
// hide live ones. One extra row tells the caller whether another page exists.
func (store *Store) ActiveLeases(ctx context.Context, organizationID, userID string, limit int, before LeaseCursor, at time.Time) ([]Lease, error) {
	statement := `SELECT ` + leaseColumns + ` FROM agents_primary.interaction_leases
		WHERE organization_id=$1 AND (owner_user_id=$2 OR asker_user_id=$2)
		AND revoked_at IS NULL AND expires_at>$3`
	args := []any{organizationID, userID, at, clamp(limit) + 1}
	if before.ID != "" {
		statement += ` AND (created_at,id)<($5,$6)`
		args = append(args, before.CreatedAt, before.ID)
	}
	statement += ` ORDER BY created_at DESC, id DESC LIMIT $4`
	rows, err := store.database.Pool.Query(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("list live access windows: %w", err)
	}
	defer rows.Close()
	leases := []Lease{}
	for rows.Next() {
		var lease Lease
		if err := scanLease(rows, &lease); err != nil {
			return nil, fmt.Errorf("scan live access window: %w", err)
		}
		leases = append(leases, lease)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list live access windows: %w", err)
	}
	return leases, nil
}
