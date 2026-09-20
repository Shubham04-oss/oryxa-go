package interactiondelivery

import (
	"context"
	"errors"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

type fakeCleanupStore struct {
	threads []OwnedThread
	forgot  bool
}

func (store *fakeCleanupStore) AffectedThreads(context.Context, string, string) ([]OwnedThread, error) {
	return store.threads, nil
}

func (store *fakeCleanupStore) Forget(context.Context, string, string) error {
	store.forgot = true
	return nil
}

type fakeThreadDeleter struct {
	deleted []OwnedThread
	fail    string
}

func (deleter *fakeThreadDeleter) DeleteThread(_ context.Context, member threads.Member, agentID, threadID string) error {
	deleter.deleted = append(deleter.deleted, OwnedThread{
		OrganizationID: member.OrganizationID, UserID: member.UserID,
		AgentID: agentID, ThreadID: threadID,
	})
	if threadID == deleter.fail {
		return errors.New("session store down")
	}
	return nil
}

func TestCleanerDeletesBothPartiesThreadsBeforeDeliveryState(t *testing.T) {
	store := &fakeCleanupStore{threads: []OwnedThread{
		{OrganizationID: "org_1", UserID: "user_a", AgentID: "agt_a", ThreadID: "thread_a"},
		{OrganizationID: "org_1", UserID: "user_b", AgentID: "agt_b", ThreadID: "thread_b"},
	}}
	deleter := &fakeThreadDeleter{}
	if err := NewCleaner(store, deleter).Forget(context.Background(), "org_1", "user_a"); err != nil {
		t.Fatal(err)
	}
	if len(deleter.deleted) != 2 || !store.forgot {
		t.Fatalf("deleted=%+v forgot=%v", deleter.deleted, store.forgot)
	}
}

func TestCleanerKeepsMappingsForARetryWhenSessionDeletionFails(t *testing.T) {
	store := &fakeCleanupStore{threads: []OwnedThread{{
		OrganizationID: "org_1", UserID: "user_b", AgentID: "agt_b", ThreadID: "thread_b",
	}}}
	err := NewCleaner(store, &fakeThreadDeleter{fail: "thread_b"}).Forget(context.Background(), "org_1", "user_a")
	if err == nil || store.forgot {
		t.Fatalf("error=%v forgot=%v", err, store.forgot)
	}
}
