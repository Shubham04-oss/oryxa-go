package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/files/internal/domain"
)

func TestStoreLifecycle(t *testing.T) {
	databaseURL := os.Getenv("FILES_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("FILES_TEST_DATABASE_URL is not set")
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
	fileID := "it-file-" + suffix
	eventPrefix := "it-evt-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM files_objects WHERE organization_id = $1`, organizationID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM files_organizations WHERE organization_id = $1`, organizationID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM files_outbox WHERE id LIKE $1`, eventPrefix+"%")
	}()

	object := domain.Object{
		ID: fileID, OrganizationID: organizationID, Scope: domain.ScopeMember, MemberID: "user-1",
		Filename: "notes.txt", ContentType: "text/plain",
		Purpose: domain.PurposeUpload, SizeBytes: 5, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CreatedBy: "user-1", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
	}
	if err := store.CreateObject(ctx, "Acme", object, testEvent(eventPrefix+"-created", "files.object.created.v1", now)); err != nil {
		t.Fatalf("CreateObject() error = %v", err)
	}
	got, err := store.Object(ctx, organizationID, fileID, now)
	if err != nil || got.Filename != "notes.txt" || got.Purpose != domain.PurposeUpload || got.Scope != domain.ScopeMember || got.MemberID != "user-1" {
		t.Fatalf("Object() = %+v, err = %v", got, err)
	}
	listed, err := store.Objects(ctx, organizationID, now, domain.ObjectQuery{Scope: domain.ScopeMember, MemberID: "user-1"})
	if err != nil || len(listed) != 1 {
		t.Fatalf("Objects() = %+v, err = %v", listed, err)
	}
	teamID := "it-team-" + suffix
	teamFile := domain.Object{
		ID: fileID + "-team", OrganizationID: organizationID, Scope: domain.ScopeTeam, TeamID: teamID,
		Filename: "spec.md", ContentType: "text/markdown",
		Purpose: domain.PurposePipelineArtifact, SizeBytes: 4, SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		CreatedBy: "user-1", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
	}
	if err := store.CreateObject(ctx, "Acme", teamFile, testEvent(eventPrefix+"-team", "files.object.created.v1", now)); err != nil {
		t.Fatalf("CreateObject(team) error = %v", err)
	}
	teamListed, err := store.Objects(ctx, organizationID, now, domain.ObjectQuery{Scope: domain.ScopeTeam, TeamID: teamID})
	if err != nil || len(teamListed) != 1 || teamListed[0].ID != teamFile.ID {
		t.Fatalf("team Objects() = %+v, err = %v", teamListed, err)
	}
	restricted := domain.Object{
		ID: fileID + "-restricted", OrganizationID: organizationID, Scope: domain.ScopeOrganization,
		Filename: "strategy.txt", ContentType: "text/plain", Purpose: domain.PurposeUpload, Access: domain.AccessAdmins,
		SizeBytes: 6, SHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		CreatedBy: "user-1", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
	}
	if err := store.CreateObject(ctx, "Acme", restricted, testEvent(eventPrefix+"-restricted", "files.object.created.v1", now)); err != nil {
		t.Fatal(err)
	}
	readRestricted, err := store.Object(ctx, organizationID, restricted.ID, now)
	if err != nil || readRestricted.Access != domain.AccessAdmins {
		t.Fatalf("restricted row = %+v, %v", readRestricted, err)
	}
	memberOrganization, err := store.Objects(ctx, organizationID, now, domain.ObjectQuery{Scope: domain.ScopeOrganization})
	if err != nil || len(memberOrganization) != 0 {
		t.Fatalf("member org files = %+v, %v", memberOrganization, err)
	}
	adminOrganization, err := store.Objects(ctx, organizationID, now, domain.ObjectQuery{Scope: domain.ScopeOrganization, Admin: true})
	if err != nil || len(adminOrganization) != 1 || adminOrganization[0].ID != restricted.ID {
		t.Fatalf("admin org files = %+v, %v", adminOrganization, err)
	}
	hidden, err := store.Objects(ctx, organizationID, now, domain.ObjectQuery{MemberID: "user-1"})
	if err != nil || len(hidden) != 1 || hidden[0].ID != fileID {
		t.Fatalf("default Objects() should hide the team file = %+v, err = %v", hidden, err)
	}
	day, err := store.Objects(ctx, organizationID, now, domain.ObjectQuery{
		Scope: domain.ScopeMember, MemberID: "user-1",
		CreatedFrom: time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC),
		CreatedTo:   time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1),
	})
	if err != nil || len(day) != 1 || day[0].ID != fileID {
		t.Fatalf("day Objects() = %+v, err = %v", day, err)
	}
	if _, err := store.Object(ctx, organizationID, fileID, now.Add(48*time.Hour)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expired Object() error = %v, want not found", err)
	}
	var recorded bool
	if err := store.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM files_outbox WHERE id = $1)`, eventPrefix+"-created").Scan(&recorded); err != nil || !recorded {
		t.Fatalf("outbox event %s not recorded, found = %v, err = %v", eventPrefix+"-created", recorded, err)
	}
	if err := store.DeleteObject(ctx, organizationID, fileID, testEvent(eventPrefix+"-deleted", "files.object.deleted.v1", now)); err != nil {
		t.Fatalf("DeleteObject() error = %v", err)
	}
	if _, err := store.Object(ctx, organizationID, fileID, now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Object after delete error = %v", err)
	}
}

func testEvent(id, detailType string, occurredAt time.Time) domain.Event {
	detail, _ := json.Marshal(map[string]string{"test": id})
	return domain.Event{ID: id, DetailType: detailType, OccurredAt: occurredAt, Data: detail}
}
