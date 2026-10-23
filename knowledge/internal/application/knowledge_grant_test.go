package application

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

// TestASpecialistReadsOnlyTheSourcesItsGrantNames: with a grant naming some
// knowledge sources, every read of another one, its runs and its documents,
// and asking for it to sync, is the same not found a missing source gets,
// and the list leaves it out. All ("*"), no knowledge field, a member and a
// primary are not limited; an empty grant reads nothing.
func TestASpecialistReadsOnlyTheSourcesItsGrantNames(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	for _, id := range []string{"src_a", "src_b"} {
		store.sources[id] = domain.Source{
			ID: id, OrganizationID: "org_1", Provider: "files",
			Scope: domain.ScopeOrganization, CreatedBy: "user-1", Status: domain.StatusActive,
		}
		document := "doc_" + id[len("src_"):]
		store.documents[id+"\x00"+document] = domain.Document{
			ID: document, OrganizationID: "org_1", SourceID: id, ExternalID: document,
			Body: "the body of " + document, Metadata: map[string]string{"purpose": "upload", "access": "members"},
		}
	}
	credentials := authclient.Credentials{Authorization: "Bearer user-1"}
	specialist := func(knowledge ...string) domain.OrgMembership {
		caller := member()
		caller.Agent, caller.AgentRole, caller.AgentKnowledge = true, "specialist", knowledge
		if knowledge == nil {
			caller.AgentKnowledge = []string{}
		}
		return caller
	}
	reads := func(caller domain.OrgMembership, sourceID, documentID string) []error {
		service := registrar(t, store, caller)
		_, sourceErr := service.Source(ctx, credentials, "org_1", sourceID)
		_, runsErr := service.Runs(ctx, credentials, "org_1", sourceID, 10)
		_, syncErr := service.SyncSource(ctx, credentials, "org_1", sourceID)
		_, documentErr := service.Document(ctx, credentials, "org_1", documentID)
		return []error{sourceErr, runsErr, syncErr, documentErr}
	}
	listed := func(caller domain.OrgMembership) []string {
		sources, err := registrar(t, store, caller).Sources(ctx, credentials, "org_1", domain.SourceQuery{})
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, source := range sources {
			ids = append(ids, source.ID)
		}
		slices.Sort(ids)
		return ids
	}

	one := specialist("src_a")
	for i, err := range reads(one, "src_a", "doc_a") {
		if err != nil {
			t.Errorf("granted read %d: %v", i, err)
		}
	}
	// Outside the grant, each read is exactly what a missing source gives.
	for i, err := range reads(one, "src_b", "doc_b") {
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("read %d of an ungranted source: %v, want not found", i, err)
		}
	}
	for i, err := range reads(member(), "src_missing", "doc_missing") {
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("read %d of a missing source: %v", i, err)
		}
	}
	if got := listed(one); !slices.Equal(got, []string{"src_a"}) {
		t.Errorf("a specialist granted one source lists %v", got)
	}
	// It cannot register a source: no agent may, and one limited to some
	// sources could not read a new one.
	if _, err := registrar(t, store, one).RegisterSource(ctx, credentials, "org_1", RegisterInput{Provider: "files"}); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("register by a limited specialist: %v, want forbidden", err)
	}
	if _, err := registrar(t, store, one).register(ctx, credentials, one, "org_1", RegisterInput{Provider: "files"}); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("register for a limited specialist: %v, want forbidden", err)
	}

	nothing := specialist()
	for i, err := range reads(nothing, "src_a", "doc_a") {
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("read %d by a specialist granted nothing: %v, want not found", i, err)
		}
	}
	if got := listed(nothing); len(got) != 0 {
		t.Errorf("a specialist granted nothing lists %v", got)
	}

	unlimited := specialist("*")
	noField := specialist("*")
	noField.AgentKnowledge = nil
	primary := specialist()
	primary.AgentRole = "primary"
	for name, caller := range map[string]domain.OrgMembership{
		"a specialist granted all":     unlimited,
		"a specialist with no field":   noField,
		"a primary with a stray grant": primary,
		"a member":                     member(),
	} {
		for i, err := range reads(caller, "src_b", "doc_b") {
			if err != nil {
				t.Errorf("%s read %d: %v", name, i, err)
			}
		}
		if got := listed(caller); !slices.Equal(got, []string{"src_a", "src_b"}) {
			t.Errorf("%s lists %v, want both", name, got)
		}
	}
}
