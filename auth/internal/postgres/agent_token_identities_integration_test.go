package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

func TestAgentTokenIdentityAnswersOnlyItsOwnLiveToken(t *testing.T) {
	store := cliStore(t)
	ctx := context.Background()
	hash := fmt.Sprintf("agent-token-%d", time.Now().UnixNano())
	expired := hash + "-expired"
	t.Cleanup(func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM auth_agent_token_identities WHERE token_hash IN ($1,$2)`, hash, expired)
	})
	specialist := domain.AgentIdentity{Agent: "agt_writer", Role: domain.AgentRoleSpecialist}
	if err := store.BindAgentToken(ctx, hash, "member-1", specialist, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, err := store.AgentTokenIdentity(ctx, hash, "member-1"); err != nil || got != specialist {
		t.Fatalf("AgentTokenIdentity = %+v, %v; want %+v", got, err, specialist)
	}
	if _, err := store.AgentTokenIdentity(ctx, hash, "member-2"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("another member's lookup = %v, want not found", err)
	}
	if err := store.BindAgentToken(ctx, expired, "member-1", specialist, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AgentTokenIdentity(ctx, expired, "member-1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expired lookup = %v, want not found", err)
	}
}
