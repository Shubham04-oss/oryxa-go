package connect

import (
	"context"
	"errors"
	"fmt"

	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
)

// A connection is a member's standing access to their app, so it must not
// outlive their place in the organization (design §7.2, "Consumed"): when
// they leave, their personal connections there are revoked, and when the
// organization goes, all of its connections are. Each is revoked at the app
// where it can be, and here whatever the app says, so the member loses
// access either way; the removed event goes out through the outbox as for
// any removal.
//
// The bus delivers at least once and in no set order. Revoking only ever
// moves a connection to revoked, so a repeat, or the organization's deletion
// and a member's removal in either order, end the same. A member's removal
// that arrives late spares what they connected after it, should they have
// come back.

// signInGrace is how long after a member's removal a connection can still
// appear from a sign-in that was under way when they left: its state was
// spent before the removal, and the callback has this long to finish. A
// removal handled after such a connection appears revokes it too.
const signInGrace = completeTimeout

// MemberRemoved revokes the personal connections the member made in the
// organization, and the sign-ins they had started there. Org-level
// connections stay: they are the organization's.
func (s *Service) MemberRemoved(ctx context.Context, event authv1.MemberRemoved) error {
	if event.OrganizationID == "" || event.UserID == "" {
		return fmt.Errorf("%w: a member's removal names no organization or member", ErrInvalid)
	}
	// Sign-ins started before the removal will not finish. One started
	// since, by a member who came back, is theirs to keep. A state expires
	// StateLifetime after it was started.
	startedBy := event.RemovedAt.Add(StateLifetime)
	if event.RemovedAt.IsZero() {
		startedBy = s.now().Add(StateLifetime)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE integrations.oauth_states SET used_at = $3
		WHERE org_id = $1 AND member_id = $2 AND used_at IS NULL AND expires_at <= $4`,
		event.OrganizationID, event.UserID, s.now(), startedBy); err != nil {
		return err
	}
	madeBy := event.RemovedAt.Add(signInGrace)
	if event.RemovedAt.IsZero() {
		madeBy = s.now()
	}
	return s.revokeWhere(ctx, `org_id = $1 AND member_id = $2 AND created_at <= $3`,
		event.OrganizationID, event.UserID, madeBy)
}

// OrganizationDeleted revokes every connection the organization had,
// personal and org-level, and every sign-in under way in it.
func (s *Service) OrganizationDeleted(ctx context.Context, event authv1.OrganizationDeleted) error {
	if event.OrganizationID == "" {
		return fmt.Errorf("%w: an organization's deletion names no organization", ErrInvalid)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE integrations.oauth_states SET used_at = $2
		WHERE org_id = $1 AND used_at IS NULL`, event.OrganizationID, s.now()); err != nil {
		return err
	}
	return s.revokeWhere(ctx, `org_id = $1`, event.OrganizationID)
}

// revokeWhere revokes every live connection the condition selects, each at
// the app and then here, or here only when Revoke cannot get that far. One
// that cannot be revoked even here does not stop the rest; the error asks
// for the event again, and revoking is idempotent.
func (s *Service) revokeWhere(ctx context.Context, condition string, args ...any) error {
	ids, err := s.liveConnections(ctx, condition, args...)
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range ids {
		atApp, err := s.Revoke(ctx, id)
		switch {
		case errors.Is(err, ErrNotFound):
			continue
		case err != nil:
			s.config.Logger.WarnContext(ctx, "revoke a connection at the app failed; revoking it here only",
				"connection_id", id, "error", err)
			if err := s.revokeHere(ctx, id); err != nil {
				failures = append(failures, fmt.Errorf("revoke connection %s: %w", id, err))
				continue
			}
		}
		s.config.Logger.InfoContext(ctx, "connection revoked on departure", "connection_id", id, "provider_revoked", atApp)
	}
	return errors.Join(failures...)
}

func (s *Service) liveConnections(ctx context.Context, condition string, args ...any) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM integrations.connections
		WHERE status <> 'revoked' AND `+condition+` ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
