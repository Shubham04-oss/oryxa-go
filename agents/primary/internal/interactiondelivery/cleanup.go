package interactiondelivery

import (
	"context"
	"errors"
	"fmt"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

type cleanupStore interface {
	AffectedThreads(context.Context, string, string) ([]OwnedThread, error)
	Forget(context.Context, string, string) error
}

// ThreadDeleter removes a persistent agent conversation.
type ThreadDeleter interface {
	DeleteThread(context.Context, threads.Member, string, string) error
}

// Cleaner removes the copies of interaction content that agent conversations
// accumulated through tool results before the authoritative ledger is
// deleted for a departing member or organization.
type Cleaner struct {
	store  cleanupStore
	agents ThreadDeleter
}

func NewCleaner(store cleanupStore, agents ThreadDeleter) *Cleaner {
	return &Cleaner{store: store, agents: agents}
}

func (cleaner *Cleaner) Forget(ctx context.Context, organizationID, userID string) error {
	owned, err := cleaner.store.AffectedThreads(ctx, organizationID, userID)
	if err != nil {
		return err
	}
	var failures []error
	for _, thread := range owned {
		member := threads.Member{UserID: thread.UserID, OrganizationID: thread.OrganizationID}
		err := cleaner.agents.DeleteThread(ctx, member, thread.AgentID, thread.ThreadID)
		if err != nil && !errors.Is(err, threads.ErrNotFound) {
			failures = append(failures, fmt.Errorf("delete interaction thread %s: %w", thread.ThreadID, err))
		}
	}
	if err := errors.Join(failures...); err != nil {
		return err
	}
	return cleaner.store.Forget(ctx, organizationID, userID)
}
