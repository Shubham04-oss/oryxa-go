package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/clisession"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

func cliStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("AUTH_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("AUTH_TEST_DATABASE_URL is not set")
	}
	store, err := Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return store
}
func admitCLITestSession(t *testing.T, store *Store) string {
	t.Helper()
	ctx := context.Background()
	challenge := clisession.NewChallenge()
	hash := clisession.Hash(fmt.Sprintf("test-token-%d", time.Now().UnixNano()))
	t.Cleanup(func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM auth_cli_sessions WHERE token_hash=$1`, hash)
		_, _ = store.pool.Exec(ctx, `DELETE FROM auth_cli_challenges WHERE state_hash=$1`, clisession.Hash(challenge.State))
	})
	if err := store.CreateCLIChallenge(ctx, challenge); err != nil {
		t.Fatal(err)
	}
	if err := store.AdmitCLISession(ctx, challenge.State, challenge.Nonce, hash, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	return hash
}

func TestCLIServerChallengeMustMatchAndCanBeConsumedOnlyOnce(t *testing.T) {
	store := cliStore(t)
	ctx := context.Background()
	challenge := clisession.NewChallenge()
	hash := clisession.Hash(challenge.State + "token")
	t.Cleanup(func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM auth_cli_sessions WHERE token_hash=$1`, hash)
		_, _ = store.pool.Exec(ctx, `DELETE FROM auth_cli_challenges WHERE state_hash=$1`, clisession.Hash(challenge.State))
	})
	if err := store.CreateCLIChallenge(ctx, challenge); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CLIChallenge(ctx, "forged"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatal("forged state accepted", err)
	}
	if err := store.AdmitCLISession(ctx, challenge.State, "wrong-nonce", hash, time.Now().Add(time.Minute)); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatal("wrong nonce accepted", err)
	}
	if _, err := store.CLISession(ctx, hash); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatal("unadmitted token accepted", err)
	}
	if err := store.AdmitCLISession(ctx, challenge.State, challenge.Nonce, hash, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CLIChallenge(ctx, challenge.State); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatal("challenge remained reusable", err)
	}
	if err := store.AdmitCLISession(ctx, challenge.State, challenge.Nonce, hash, time.Now().Add(time.Minute)); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatal("challenge replay accepted", err)
	}
	if err := store.BindCLISession(ctx, hash, "org_1"); err != nil {
		t.Fatal(err)
	}
	// Reopening storage and using another challenge cannot reset a token's binding.
	restarted := cliStore(t)
	session, err := restarted.CLISession(ctx, hash)
	if err != nil || session.OrganizationID != "org_1" {
		t.Fatal("binding did not survive restart", session, err)
	}
	next := clisession.NewChallenge()
	if err := store.CreateCLIChallenge(ctx, next); err != nil {
		t.Fatal(err)
	}
	defer store.pool.Exec(ctx, `DELETE FROM auth_cli_challenges WHERE state_hash=$1`, clisession.Hash(next.State))
	if err := store.AdmitCLISession(ctx, next.State, next.Nonce, hash, time.Now().Add(time.Minute)); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatal("admission reset bound token", err)
	}
}

func TestConcurrentCLIOrganizationBindingAndCreationHaveOneWinner(t *testing.T) {
	store := cliStore(t)
	ctx := context.Background()
	for _, create := range []bool{false, true} {
		hash := admitCLITestSession(t, store)
		var wg sync.WaitGroup
		results := make(chan error, 2)
		for _, org := range []string{"org_a", "org_b"} {
			wg.Go(func() {
				if create && org == "org_a" {
					results <- store.ReserveCLIOrganization(ctx, hash)
				} else {
					results <- store.BindCLISession(ctx, hash, org)
				}
			})
		}
		wg.Wait()
		close(results)
		succeeded := 0
		for err := range results {
			if err == nil {
				succeeded++
			} else if !errors.Is(err, domain.ErrForbidden) {
				t.Fatal(err)
			}
		}
		if succeeded != 1 {
			t.Fatalf("create=%v accepted %d concurrent organization selections", create, succeeded)
		}
		if err := store.BindCLISession(ctx, hash, "org_c"); !errors.Is(err, domain.ErrForbidden) {
			t.Fatal("later token rebind accepted", err)
		}
	}
}

func TestCLIExpiredStateAndTokensAreRejectedAndPruned(t *testing.T) {
	store := cliStore(t)
	ctx := context.Background()
	hash := admitCLITestSession(t, store)
	challenge := clisession.NewChallenge()
	challenge.ExpiresAt = time.Now().Add(-time.Minute)
	if err := store.CreateCLIChallenge(ctx, challenge); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CLIChallenge(ctx, challenge.State); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatal("expired challenge accepted", err)
	}
	if err := store.AdmitCLISession(ctx, challenge.State, challenge.Nonce, clisession.Hash("different"), time.Now().Add(time.Minute)); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatal("expired challenge exchanged", err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE auth_cli_sessions SET expires_at=now()-interval '1 minute' WHERE token_hash=$1`, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CLISession(ctx, hash); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatal("expired session accepted", err)
	}
	if err := store.BindCLISession(ctx, hash, "org_1"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("expired session bound", err)
	}
	fresh := clisession.NewChallenge()
	if err := store.CreateCLIChallenge(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	defer store.pool.Exec(ctx, `DELETE FROM auth_cli_challenges WHERE state_hash=$1`, clisession.Hash(fresh.State))
	var count int
	if err := store.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM auth_cli_sessions WHERE token_hash=$1)+(SELECT count(*) FROM auth_cli_challenges WHERE state_hash=$2)`, hash, clisession.Hash(challenge.State)).Scan(&count); err != nil || count != 0 {
		t.Fatalf("expired records remain: %d %v", count, err)
	}
}
