package adksession

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"google.golang.org/adk/v2/session"
)

// memberFixture inserts a user, organization and membership into auth's
// tables, the way auth itself writes them, and returns a cleanup.
func memberFixture(t *testing.T, pool *pgx.Conn, userID, orgID string) {
	t.Helper()
	now := time.Now().UTC()
	inserts := []struct {
		statement string
		args      []any
	}{
		{
			`INSERT INTO auth_users (id, email, display_name, avatar_url, created_at, updated_at)
			 VALUES ($1, $2, $3, '', $4, $4)`,
			[]any{userID, userID + "@members.oryxa", "Member " + userID, now},
		},
		{
			`INSERT INTO auth_organizations (id, name, slug, created_by, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $5)`,
			[]any{orgID, "Seeding Test Org", "seed-org-" + orgID, userID, now},
		},
		{
			`INSERT INTO auth_memberships (organization_id, user_id, role, joined_at)
			 VALUES ($1, $2, $3, $4)`,
			[]any{orgID, userID, "member", now},
		},
	}
	for _, insert := range inserts {
		if _, err := pool.Exec(context.Background(), insert.statement, insert.args...); err != nil {
			t.Fatalf("seed member fixture: %v", err)
		}
	}
}

func cleanupMember(t *testing.T, pool *pgx.Conn, userID, orgID string) {
	t.Helper()
	// auth_memberships cascades on both deletes.
	if _, err := pool.Exec(context.Background(), `DELETE FROM auth_organizations WHERE id = $1`, orgID); err != nil {
		t.Fatalf("clean organization fixture: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `DELETE FROM auth_users WHERE id = $1`, userID); err != nil {
		t.Fatalf("clean user fixture: %v", err)
	}
}

func TestPostgresSessionsSeedMemberState(t *testing.T) {
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	const (
		userID = "user-seeding-test"
		orgID  = "org_seeding_test"
	)
	ctx := context.Background()
	pool, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	defer pool.Close(ctx)
	memberFixture(t, pool, userID, orgID)
	defer cleanupMember(t, pool, userID, orgID)

	store, err := Open(ctx, databaseURL, true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	sessions := store.Sessions()
	created, err := sessions.Create(ctx, &session.CreateRequest{
		AppName: "krude-test", UserID: userID,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = sessions.Delete(ctx, &session.DeleteRequest{
			AppName: "krude-test", UserID: userID, SessionID: created.Session.ID(),
		})
	}()
	state := created.Session.State()
	for key, want := range map[string]string{
		StateUserName:       "Member user-seeding-test",
		StateUserAbout:      "a member of their organization",
		StateOrgName:        "Seeding Test Org",
		StateOrganizationID: orgID,
	} {
		got, err := state.Get(key)
		if err != nil || got != want {
			t.Errorf("state[%q] = %v, want %q (error %v)", key, got, want, err)
		}
	}
}

func TestPostgresSessionsDoNotSeedNonMembers(t *testing.T) {
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	store, err := Open(ctx, databaseURL, true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	sessions := store.Sessions()
	created, err := sessions.Create(ctx, &session.CreateRequest{
		AppName: "krude-test", UserID: "user-not-a-member-anywhere",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = sessions.Delete(ctx, &session.DeleteRequest{
			AppName: "krude-test", UserID: "user-not-a-member-anywhere", SessionID: created.Session.ID(),
		})
	}()
	if _, err := created.Session.State().Get(StateOrgName); !errors.Is(err, session.ErrStateKeyNotExist) {
		t.Errorf("a non-member was seeded: org_name error = %v", err)
	}
}

func TestPostgresIsMemberAnswersFromAuthsTables(t *testing.T) {
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	const (
		userID = "user-is-member-test"
		orgID  = "org_is_member_test"
	)
	ctx := context.Background()
	pool, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	defer pool.Close(ctx)
	memberFixture(t, pool, userID, orgID)
	defer cleanupMember(t, pool, userID, orgID)

	store, err := Open(ctx, databaseURL, true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, check := range []struct {
		organizationID, userID string
		want                   bool
	}{
		{orgID, userID, true},
		{"org_elsewhere_test", userID, false},
		{orgID, "user-nobody-test", false},
		{"", userID, false},
	} {
		got, err := store.IsMember(ctx, check.organizationID, check.userID)
		if err != nil || got != check.want {
			t.Errorf("IsMember(%q, %q) = %t, %v, want %t", check.organizationID, check.userID, got, err, check.want)
		}
	}
}
