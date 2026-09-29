package store

import (
	"context"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
)

func TestResolvedMappingsMustExactlyMatchReviewedDirectory(t *testing.T) {
	directory := &spec.Directory{Identities: []spec.ExternalIdentity{{Source: "google_workspace:acme.com", ExternalID: "external1", Member: "member@acme.com"}}}
	correct := ExternalMapping{Source: "google_workspace:acme.com", Kind: "user", ExternalID: "external1", Reference: "member@acme.com", ResourceID: "api-user1"}
	for _, mappings := range [][]ExternalMapping{nil, {correct, correct}, {{Source: correct.Source, Kind: correct.Kind, ExternalID: correct.ExternalID, Reference: "other@acme.com", ResourceID: "other"}}} {
		if err := validateResolvedMappings(directory, mappings); err == nil {
			t.Fatalf("accepted mismatched API identities: %+v", mappings)
		}
	}
	if err := validateResolvedMappings(directory, []ExternalMapping{correct}); err != nil {
		t.Fatal(err)
	}
}

func TestSaveUsesResolvedAPIMappingsAndRejectsRebindingAtomically(t *testing.T) {
	storage, organizationID := newIntegrationStore(t)
	ctx := context.Background()
	document := spec.Document{APIVersion: spec.APIVersion, Kind: "Workspace", Metadata: spec.Metadata{Organization: "mapping-test"}, Directory: &spec.Directory{Identities: []spec.ExternalIdentity{{Source: "google_workspace:acme.com", ExternalID: "external1", Member: "member@acme.com"}}}}
	encoded, err := document.YAML()
	if err != nil {
		t.Fatal(err)
	}
	mappings := []ExternalMapping{{Source: "google_workspace:acme.com", Kind: "user", ExternalID: "external1", Reference: "member@acme.com", ResourceID: "api-user-with-no-auth-row"}}
	version, err := storage.Save(ctx, organizationID, "api-actor", encoded, mappings)
	if err != nil || version.Number != 1 {
		t.Fatalf("save consulted service tables or rejected API identity: %+v %v", version, err)
	}
	document.Preset = "team"
	encoded, err = document.YAML()
	if err != nil {
		t.Fatal(err)
	}
	mappings[0].ResourceID = "different-api-user"
	if _, err := storage.Save(ctx, organizationID, "api-actor", encoded, mappings); err == nil {
		t.Fatal("external identity silently rebound")
	}
	latest, err := storage.Latest(ctx, organizationID)
	if err != nil || latest.Number != 1 {
		t.Fatalf("failed binding update committed a version: %+v %v", latest, err)
	}
	actual, err := storage.Mappings(ctx, organizationID)
	if err != nil || len(actual) != 1 || actual[0].ResourceID != "api-user-with-no-auth-row" {
		t.Fatalf("binding changed after failed save: %+v %v", actual, err)
	}
}
