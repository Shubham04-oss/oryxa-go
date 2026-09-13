package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/teams/internal/domain"
)

// TestStoreLifecycle runs the store against a real PostgreSQL, such as the one
// in docker-compose.yml:
//
//	TEAMS_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' go test ./teams/internal/postgres
func TestStoreLifecycle(t *testing.T) {
	databaseURL := os.Getenv("TEAMS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEAMS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}

	suffix := time.Now().UTC().Format("20060102150405.000000000")
	organizationID := "it-org-" + suffix
	teamID := "it-team-" + suffix
	eventPrefix := "it-evt-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM teams WHERE organization_id = $1`, organizationID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM teams_organizations WHERE organization_id = $1`, organizationID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM teams_outbox WHERE id LIKE $1`, eventPrefix+"%")
	}()

	team := domain.Team{ID: teamID, OrganizationID: organizationID, Name: "Engineering", CreatedBy: "user-1", CreatedAt: now, UpdatedAt: now}
	creator := domain.Membership{TeamID: teamID, OrganizationID: organizationID, UserID: "user-1", Email: "asha@example.com", Role: domain.RoleAdmin, JoinedAt: now}
	if err := store.CreateTeam(ctx, "Acme", team, creator, []domain.Event{testEvent(eventPrefix+"-created", "teams.team.created.v1", now)}); err != nil {
		t.Fatalf("CreateTeam() error = %v", err)
	}
	if err := store.CreateTeam(ctx, "Acme", team, creator, nil); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate CreateTeam() error = %v, want conflict", err)
	}

	member := domain.Membership{TeamID: teamID, OrganizationID: organizationID, UserID: "user-2", Email: "ben@example.com", Role: domain.RoleMember, JoinedAt: now}
	if err := store.AddMember(ctx, member, testEvent(eventPrefix+"-added", "teams.member.added.v1", now)); err != nil {
		t.Fatalf("AddMember() error = %v", err)
	}
	if err := store.ChangeMemberRole(ctx, teamID, "user-1", domain.RoleAdmin, domain.RoleMember, testEvent(eventPrefix+"-demote", "teams.member.role-changed.v1", now)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("demote last admin error = %v, want conflict", err)
	}

	claimed, err := store.ClaimOutbox(ctx, 10)
	if err != nil || len(claimed) < 2 {
		t.Fatalf("ClaimOutbox() = %d, err = %v", len(claimed), err)
	}
	if err := store.MarkOutboxPublished(ctx, claimed[0].ID); err != nil {
		t.Fatalf("MarkOutboxPublished() error = %v", err)
	}

	if err := store.DeleteTeam(ctx, organizationID, teamID, []domain.Event{testEvent(eventPrefix+"-deleted", "teams.team.deleted.v1", now)}); err != nil {
		t.Fatalf("DeleteTeam() error = %v", err)
	}
	if _, err := store.Team(ctx, organizationID, teamID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Team() after delete error = %v, want not found", err)
	}
}

func TestStoreExtensions(t *testing.T) {
	databaseURL := os.Getenv("TEAMS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEAMS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	var extensions []string
	if err := store.db.SelectContext(ctx, &extensions, `
		SELECT extname FROM pg_extension WHERE extname IN ('vector', 'ltree', 'pg_trgm') ORDER BY extname`); err != nil {
		t.Fatalf("list extensions: %v", err)
	}
	if got := fmt.Sprint(extensions); got != "[ltree pg_trgm vector]" {
		t.Fatalf("extensions = %s, want ltree, pg_trgm and vector", got)
	}

	suffix := time.Now().UTC().Format("20060102150405.000000000")
	organizationID := "it-org-" + suffix
	teamID := "it-team-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM teams WHERE organization_id = $1`, organizationID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM teams_organizations WHERE organization_id = $1`, organizationID)
	}()

	team := domain.Team{ID: teamID, OrganizationID: organizationID, Name: "Engineering", Path: domain.TeamPath(organizationID, teamID), CreatedBy: "user-1", CreatedAt: now, UpdatedAt: now}
	creator := domain.Membership{TeamID: teamID, OrganizationID: organizationID, UserID: "user-1", Email: "asha@example.com", Role: domain.RoleAdmin, JoinedAt: now}
	if err := store.CreateTeam(ctx, "Acme", team, creator, nil); err != nil {
		t.Fatalf("CreateTeam() error = %v", err)
	}

	got, err := store.Team(ctx, organizationID, teamID)
	if err != nil || got.Path != domain.TeamPath(organizationID, teamID) {
		t.Fatalf("Team() = %+v, err = %v", got, err)
	}
	descendants, err := store.TeamDescendants(ctx, organizationID, domain.LtreeLabel(organizationID))
	if err != nil || len(descendants) != 1 || descendants[0].ID != teamID {
		t.Fatalf("TeamDescendants() = %+v, err = %v", descendants, err)
	}
	matches, err := store.SearchTeams(ctx, organizationID, "Engine", 5)
	if err != nil || len(matches) != 1 || matches[0].ID != teamID {
		t.Fatalf("SearchTeams() = %+v, err = %v", matches, err)
	}

	embedding := make([]float32, domain.EmbeddingDimensions)
	embedding[0] = 1
	if err := store.SetTeamEmbedding(ctx, organizationID, teamID, embedding); err != nil {
		t.Fatalf("SetTeamEmbedding() error = %v", err)
	}
	nearest, err := store.TeamsNearest(ctx, organizationID, embedding, 3)
	if err != nil || len(nearest) != 1 || nearest[0].ID != teamID {
		t.Fatalf("TeamsNearest() = %+v, err = %v", nearest, err)
	}
}

func testEvent(id, detailType string, at time.Time) domain.Event {
	return domain.Event{ID: id, DetailType: detailType, OccurredAt: at, Data: json.RawMessage(`{}`)}
}
