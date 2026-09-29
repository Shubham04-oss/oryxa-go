package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/projection"
)

func TestSavedPolicyProjectionIsSmallAndVersioned(t *testing.T) {
	store, org := newIntegrationStore(t)
	ctx := context.Background()
	manifest := []byte("apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\npreset: regulated\nsafety: {guard: {holdForReview: true, pii: {minimum: {owner: secrets, peer: strict}}, relevance: shadow}}\ntools: {offer: []}\nagents:\n  organization:\n    instructions: " + strings.Repeat("Use the documented workflow. ", 600) + "\n")
	saved, err := store.Save(ctx, org, "owner", manifest, nil)
	if err != nil {
		t.Fatal(err)
	}
	version, encoded, err := store.Policy(ctx, org)
	if err != nil || version != saved.Number || version != 1 {
		t.Fatalf("policy version = %d, %v; saved = %d", version, err, saved.Number)
	}
	if len(encoded) > 4096 {
		t.Fatalf("operational projection contains %d bytes for a %d-byte manifest", len(encoded), len(manifest))
	}
	var settings projection.Settings
	if err := json.Unmarshal(encoded, &settings); err != nil {
		t.Fatal(err)
	}
	if !settings.HoldForReview || !settings.Regulated || settings.PIIMinimumPeer != "strict" || settings.PIIMinimumOwner != "secrets" || settings.GuardRelevance != "shadow" || settings.Offer == nil || len(*settings.Offer) != 0 {
		t.Fatalf("saved settings = %+v", settings)
	}
	var historical []byte
	if err := store.database.Pool.QueryRow(ctx, `SELECT policy_settings FROM workspace.versions WHERE organization_id=$1 AND version=1`, org).Scan(&historical); err != nil || string(historical) != string(encoded) {
		t.Fatalf("historical projection = %s, %v; current = %s", historical, err, encoded)
	}
	// Steady-state reads must never parse the manifest. A future release may
	// write syntax or validation rules this binary cannot understand.
	if _, err := store.database.Pool.Exec(ctx, `UPDATE workspace.configs SET document=$2 WHERE organization_id=$1`, org, []byte("future syntax this release cannot parse: [")); err != nil {
		t.Fatal(err)
	}
	if _, next, err := store.Policy(ctx, org); err != nil || string(next) != string(encoded) {
		t.Fatalf("policy re-read parsed unrelated manifest: %s, %v", next, err)
	}
}

func TestLegacyPolicyBackfillsUnknownFieldsOnce(t *testing.T) {
	store, org := newIntegrationStore(t)
	ctx := context.Background()
	legacy := []byte("futureRelease: {value: true}\nsafety: {guard: {holdForReview: true, futureGuard: true}}\npeople: {futureDirectory: unknown}\n")
	if _, err := store.database.Pool.Exec(ctx, `INSERT INTO workspace.configs (organization_id,version,document,applied_by,applied_at) VALUES ($1,7,$2,'owner',now())`, org, legacy); err != nil {
		t.Fatal(err)
	}
	version, encoded, err := store.Policy(ctx, org)
	if err != nil || version != 7 {
		t.Fatalf("legacy projection = %d %s, %v", version, encoded, err)
	}
	var settings projection.Settings
	if err := json.Unmarshal(encoded, &settings); err != nil || !settings.HoldForReview {
		t.Fatalf("legacy settings = %+v, %v", settings, err)
	}
	var persisted []byte
	if err := store.database.Pool.QueryRow(ctx, `SELECT policy_settings FROM workspace.configs WHERE organization_id=$1`, org).Scan(&persisted); err != nil || persisted == nil {
		t.Fatalf("projection was not backfilled: %s, %v", persisted, err)
	}
	if _, _, err := store.Policy(ctx, org+"_missing"); !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("missing org = %v", err)
	}
}
