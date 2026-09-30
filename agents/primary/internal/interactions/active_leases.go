package interactions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
)

// LeaseQuery pages the member's live access windows, not their bounded
// activity history. Cursor is an opaque position returned by a prior page.
type LeaseQuery struct {
	Limit  int
	Cursor string
}

type LeaseCursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

type ActiveLease struct {
	Lease
	Role      string `json:"role"`
	CanRevoke bool   `json:"can_revoke"`
}

// LeasePage says whether another page of current live windows follows.
// There is no total: concurrent grants, expiration and revocation can change
// the list between reads. ObservedAt is the server's expiry cutoff.
type LeasePage struct {
	Leases     []ActiveLease `json:"leases"`
	ObservedAt time.Time     `json:"observed_at"`
	HasMore    bool          `json:"has_more"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

func (service *Service) ActiveLeases(ctx context.Context, actor agents.InteractionActor, query LeaseQuery) (LeasePage, error) {
	if actor.Session != agents.SessionMember {
		return LeasePage{}, fmt.Errorf("%w: access windows are read in the member's own signed-in session", ErrForbidden)
	}
	if err := validateActor(actor); err != nil {
		return LeasePage{}, err
	}
	if service.consents == nil {
		return LeasePage{}, ErrConsentUnavailable
	}
	before, err := decodeLeaseCursor(query.Cursor)
	if err != nil {
		return LeasePage{}, err
	}
	limit := clamp(query.Limit)
	now := service.now().UTC()
	leases, err := service.consents.ActiveLeases(ctx, actor.OrganizationID, actor.UserID, limit, before, now)
	if err != nil {
		return LeasePage{}, err
	}
	page := LeasePage{Leases: []ActiveLease{}, ObservedAt: now, HasMore: len(leases) > limit}
	if page.HasMore {
		leases = leases[:limit]
		last := leases[len(leases)-1]
		encoded, err := json.Marshal(LeaseCursor{CreatedAt: last.CreatedAt, ID: last.ID})
		if err != nil {
			return LeasePage{}, fmt.Errorf("encode access window cursor: %w", err)
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	for _, lease := range leases {
		role := ActivityAsker
		if lease.OwnerUserID == actor.UserID {
			role = ActivityOwner
		}
		page.Leases = append(page.Leases, ActiveLease{Lease: lease, Role: role, CanRevoke: role == ActivityOwner})
	}
	return page, nil
}

func decodeLeaseCursor(encoded string) (LeaseCursor, error) {
	if encoded == "" {
		return LeaseCursor{}, nil
	}
	var cursor LeaseCursor
	if len(encoded) > 1024 {
		return LeaseCursor{}, fmt.Errorf("%w: cursor is too long", ErrInvalid)
	}
	value, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || json.Unmarshal(value, &cursor) != nil || cursor.CreatedAt.IsZero() ||
		strings.TrimSpace(cursor.ID) != cursor.ID || !validID(cursor.ID) {
		return LeaseCursor{}, fmt.Errorf("%w: cursor must be an access window position returned by this listing", ErrInvalid)
	}
	return cursor, nil
}
