package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
	"go.yaml.in/yaml/v3"
)

const cleanupDocument = `apiVersion: oryxa.in/v1
kind: Workspace
metadata: {organization: org_1}
people:
  members: [{email: left@example.com}, {email: stays@example.com}]
teams:
  items:
    - name: Old Team
      members: [left@example.com, stays@example.com]
      leads: [left@example.com]
    - name: Keep Team
  prune: true
directory:
  identities:
    - {source: google:example.com, externalId: external-user, member: left@example.com}
  groups:
    - {source: google:example.com, externalId: external-team, team: Old Team}
orgChart:
  relationships:
    - {from: left@example.com, relation: reports_to, to: stays@example.com}
    - {from: stays@example.com, relation: reports_to, to: other@example.com}
  authority: [reports_to]
  executives: [left@example.com, stays@example.com]
ownership:
  items:
    - {owner: left@example.com, what: payroll}
    - {owner: stays@example.com, what: operations}
  prune: true
tools: {offer: [gmail]}
futureField: {mustSurvive: true}
`

func TestForgetReferencesPreservesUnknownFieldsAndOtherMembers(t *testing.T) {
	got, err := forgetReferences([]byte(cleanupDocument), "user", map[string]bool{"left@example.com": true})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(got, []byte("left@example.com")) {
		t.Fatalf("departed user remains:\n%s", got)
	}
	for _, keep := range []string{"futureField", "stays@example.com", "reports_to", "Old Team", "offer"} {
		if !bytes.Contains(got, []byte(keep)) {
			t.Errorf("lost %s", keep)
		}
	}
	got, err = forgetReferences(got, "team", map[string]bool{"old team": true})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(got, []byte("Old Team")) || !bytes.Contains(got, []byte("Keep Team")) {
		t.Fatalf("team cleanup=%s", got)
	}
	var valid yaml.Node
	if err := yaml.Unmarshal(got, &valid); err != nil {
		t.Fatal(err)
	}
}

func cleanupStore(t *testing.T) (*Store, string) {
	t.Helper()
	url := os.Getenv("WORKSPACE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WORKSPACE_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := postgreskit.Open(ctx, postgreskit.Config{URL: url, ApplicationName: "workspace-cleanup-test", MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	storage := New(db)
	if err := storage.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	org := fmt.Sprintf("org_cleanup_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		for _, table := range []string{"reviewed_plans", "apply_attempts", "external_mappings", "versions", "configs", "lifecycle_receipts", "deleted_organizations"} {
			_, _ = db.Pool.Exec(ctx, `DELETE FROM workspace.`+table+` WHERE organization_id=$1`, org)
		}
	})
	document := []byte(strings.ReplaceAll(cleanupDocument, "org_1", org))
	for _, table := range []string{"configs", "versions"} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO workspace.`+table+` (organization_id,version,document,applied_by,applied_at,policy_settings) VALUES ($1,1,$2,'owner',$3,'{"holdForReview":true}')`, org, document, time.Now().Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []ExternalMapping{{Source: "google:example.com", Kind: "user", ExternalID: "external-user", Reference: "left@example.com", ResourceID: "user_1"}, {Source: "google:example.com", Kind: "team", ExternalID: "external-team", Reference: "Old Team", ResourceID: "team_1"}} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO workspace.external_mappings (organization_id,source,kind,external_id,reference,resource_id) VALUES($1,$2,$3,$4,$5,$6)`, org, m.Source, m.Kind, m.ExternalID, m.Reference, m.ResourceID); err != nil {
			t.Fatal(err)
		}
	}
	return storage, org
}

func TestCleanupIsAtomicSerializedAndReplaySafe(t *testing.T) {
	storage, org := cleanupStore(t)
	ctx := context.Background()
	event := Departure{EventID: org + "_removed", OrganizationID: org, Kind: "user", ResourceID: "user_1", Reference: "left@example.com", OccurredAt: time.Now()}
	if err := storage.SavePlan(ctx, ReviewedPlan{ID: org + "_plan", OrganizationID: org, ActorID: "owner", DocumentHash: "hash", BaseVersion: 1, Plan: []byte(`{}`), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := storage.WithApplyLock(ctx, org, func() error {
		if err := storage.Cleanup(ctx, event); !errors.Is(err, ErrApplyBusy) {
			t.Fatalf("cleanup raced apply: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := storage.Cleanup(ctx, event); err != nil {
		t.Fatal(err)
	}
	latest, err := storage.Latest(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Number != 2 || bytes.Contains(latest.Document, []byte("left@example.com")) {
		t.Fatalf("latest=%+v %s", latest, latest.Document)
	}
	original, err := storage.AtVersion(ctx, org, 1)
	if err != nil || !bytes.Contains(original.Document, []byte("left@example.com")) {
		t.Fatalf("audit changed: %v", err)
	}
	mappings, err := storage.Mappings(ctx, org)
	if err != nil || len(mappings) != 1 || mappings[0].Kind != "team" {
		t.Fatalf("mappings=%+v %v", mappings, err)
	}
	if _, err := storage.ReviewedPlan(ctx, org, "owner", org+"_plan"); !errors.Is(err, ErrPlanNotFound) {
		t.Fatalf("old approval survived: %v", err)
	}
	// A later deliberate rebind is not removed by redelivery of the old event.
	if _, err := storage.database.Pool.Exec(ctx, `INSERT INTO workspace.external_mappings (organization_id,source,kind,external_id,reference,resource_id) VALUES($1,'google:example.com','user','external-user','left@example.com','user_2')`, org); err != nil {
		t.Fatal(err)
	}
	if err := storage.Cleanup(ctx, event); err != nil {
		t.Fatal(err)
	}
	mappings, err = storage.Mappings(ctx, org)
	if err != nil || len(mappings) != 2 {
		t.Fatalf("replay removed binding: %+v %v", mappings, err)
	}
	latest, err = storage.Latest(ctx, org)
	if err != nil || latest.Number != 2 {
		t.Fatalf("replay created version: %+v %v", latest, err)
	}
	if err := storage.Cleanup(ctx, Departure{EventID: org + "_team", OrganizationID: org, Kind: "team", ResourceID: "team_1", Reference: "Old Team", OccurredAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	latest, err = storage.Latest(ctx, org)
	if err != nil || bytes.Contains(latest.Document, []byte("Old Team")) {
		t.Fatalf("deleted team remains: %s %v", latest.Document, err)
	}
}

func TestOrganizationDeletionPurgesAllPayloadsAndRejectsRecreation(t *testing.T) {
	storage, org := cleanupStore(t)
	ctx := context.Background()
	plan := ReviewedPlan{ID: org + "_plan", OrganizationID: org, ActorID: "owner", DocumentHash: "hash", BaseVersion: 1, Plan: []byte(`{"email":"left@example.com"}`), ExpiresAt: time.Now().Add(time.Hour)}
	if err := storage.SavePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if err := storage.SaveAttempt(ctx, ApplyAttempt{ID: org + "_attempt", OrganizationID: org, ActorID: "owner", Status: "pending", StartedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	event := Departure{EventID: org + "_deleted", OrganizationID: org, Kind: "organization", OccurredAt: time.Now()}
	for range 2 {
		if err := storage.Cleanup(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"configs", "versions", "external_mappings", "reviewed_plans", "apply_attempts", "lifecycle_receipts"} {
		var count int
		if err := storage.database.Pool.QueryRow(ctx, `SELECT count(*) FROM workspace.`+table+` WHERE organization_id=$1`, org).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s retains %d rows: %v", table, count, err)
		}
	}
	if err := storage.SavePlan(ctx, plan); err == nil {
		t.Fatal("deleted organization's delayed plan recreated state")
	}
	if _, err := storage.database.Pool.Exec(ctx, `INSERT INTO workspace.configs (organization_id,version,document,applied_by,applied_at) VALUES($1,1,$2,'owner',now())`, org, []byte(cleanupDocument)); err == nil {
		t.Fatal("deleted organization recreated config")
	}
}

func TestExpiredReviewedPlansArePhysicallyPurged(t *testing.T) {
	storage, org := cleanupStore(t)
	ctx := context.Background()
	for _, age := range []time.Duration{-time.Minute, time.Hour} {
		plan := ReviewedPlan{ID: org + age.String(), OrganizationID: org, ActorID: "owner", DocumentHash: "hash", BaseVersion: 1, Plan: []byte(`{"email":"left@example.com"}`), ExpiresAt: time.Now().Add(age)}
		if err := storage.SavePlan(ctx, plan); err != nil {
			t.Fatal(err)
		}
	}
	if err := storage.PurgeExpiredPlans(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := storage.database.Pool.QueryRow(ctx, `SELECT count(*) FROM workspace.reviewed_plans WHERE organization_id=$1`, org).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestDelayedDeparturesPreserveNewerUnboundReferences(t *testing.T) {
	storage, org := cleanupStore(t)
	ctx := context.Background()
	if _, err := storage.database.Pool.Exec(ctx, `DELETE FROM workspace.external_mappings WHERE organization_id=$1`, org); err != nil {
		t.Fatal(err)
	}
	for _, event := range []Departure{
		{EventID: org + "_user", OrganizationID: org, Kind: "user", ResourceID: "old_user", Reference: "left@example.com", OccurredAt: time.Now().Add(-time.Hour)},
		{EventID: org + "_team", OrganizationID: org, Kind: "team", ResourceID: "old_team", Reference: "Old Team", OccurredAt: time.Now().Add(-time.Hour)},
	} {
		if err := storage.Cleanup(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	latest, err := storage.Latest(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(latest.Document, []byte("left@example.com")) || !bytes.Contains(latest.Document, []byte("Old Team")) {
		t.Fatalf("late event deleted recreated references: %s", latest.Document)
	}
}

func TestOutOfOrderDeparturesUseLastAdministratorApplyTime(t *testing.T) {
	storage, org := cleanupStore(t)
	ctx := context.Background()
	if _, err := storage.database.Pool.Exec(ctx, `DELETE FROM workspace.external_mappings WHERE organization_id=$1`, org); err != nil {
		t.Fatal(err)
	}
	occurred := time.Now()
	// Both events happened after the same file was applied, but arrive in the
	// opposite order. A cleanup timestamp must not make the second look stale.
	for _, event := range []Departure{
		{EventID: org + "_user", OrganizationID: org, Kind: "user", ResourceID: "user_1", Reference: "left@example.com", OccurredAt: occurred.Add(time.Millisecond)},
		{EventID: org + "_team", OrganizationID: org, Kind: "team", ResourceID: "team_1", Reference: "Old Team", OccurredAt: occurred},
	} {
		if err := storage.Cleanup(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	latest, err := storage.Latest(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(latest.Document, []byte("left@example.com")) || bytes.Contains(latest.Document, []byte("Old Team")) {
		t.Fatalf("out-of-order departure kept stale facts: %s", latest.Document)
	}
}
