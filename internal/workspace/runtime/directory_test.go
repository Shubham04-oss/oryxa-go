package runtime

import (
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/store"
)

func directoryState() Snapshot {
	return Snapshot{
		Members: map[string]Member{
			"ada@acme.com": {UserID: "u1", Email: "ada@acme.com"},
			"bob@acme.com": {UserID: "u2", Email: "bob@acme.com"},
		},
		Invitations: map[string]Invitation{},
		Teams:       map[string]Team{"Engineering": {ID: "t1", Name: "Engineering"}},
	}
}

func TestDirectorySnapshotMergesAndPrunesReviewedMappings(t *testing.T) {
	old := contextDocument()
	old.Directory = &spec.Directory{Identities: []spec.ExternalIdentity{{Source: "google_workspace:acme.com", ExternalID: "100", Member: "ada@acme.com"}}}
	submitted := contextDocument()
	submitted.Directory = &spec.Directory{Groups: []spec.ExternalGroup{{Source: "google_workspace:acme.com", ExternalID: "grp-1", Team: "Engineering"}}}
	mapped := []store.ExternalMapping{{Source: "google_workspace:acme.com", Kind: "user", ExternalID: "100", Reference: "ada@acme.com", ResourceID: "u1"}}
	effective, plan, err := MergeDirectory(old, submitted, directoryState(), mapped, false)
	if err != nil || effective.Directory == nil || len(effective.Directory.Identities) != 1 || len(effective.Directory.Groups) != 1 || len(plan.Changes) != 1 || plan.Changes[0].Op != "create" {
		t.Fatalf("merge = %+v %+v %v", effective.Directory, plan, err)
	}
	submitted.Directory.Prune = true
	effective, plan, err = MergeDirectory(old, submitted, directoryState(), mapped, false)
	if err != nil || len(effective.Directory.Identities) != 0 || len(plan.Changes) != 2 || !RemovalsDiffer(plan, Plan{}) {
		t.Fatalf("prune = %+v %+v %v", effective.Directory, plan, err)
	}
}

func TestDirectoryExternalIDCannotMoveToAnotherUser(t *testing.T) {
	old := contextDocument()
	old.Directory = &spec.Directory{Identities: []spec.ExternalIdentity{{Source: "microsoft_365:tenant-1", ExternalID: "uuid-1", Member: "ada@acme.com"}}}
	submitted := contextDocument()
	submitted.Directory = &spec.Directory{Identities: []spec.ExternalIdentity{{Source: "microsoft_365:tenant-1", ExternalID: "uuid-1", Member: "bob@acme.com"}}}
	mapped := []store.ExternalMapping{{Source: "microsoft_365:tenant-1", Kind: "user", ExternalID: "uuid-1", Reference: "ada@acme.com", ResourceID: "u1"}}
	if _, _, err := MergeDirectory(old, submitted, directoryState(), mapped, false); err == nil || !strings.Contains(err.Error(), "already bound") {
		t.Fatalf("rebind error = %v", err)
	}
}

func TestDirectoryTeamReferenceMatchesDatabaseNameSemantics(t *testing.T) {
	submitted := contextDocument()
	submitted.Directory = &spec.Directory{Groups: []spec.ExternalGroup{{Source: "google_workspace:acme.com", ExternalID: "group1", Team: " engineering "}}}
	effective, _, err := MergeDirectory(contextDocument(), submitted, directoryState(), nil, false)
	if err != nil || effective.Directory.Groups[0].Team != "Engineering" {
		t.Fatalf("group reference failed to resolve: %+v %v", effective.Directory, err)
	}
}

func TestDirectoryPendingInviteBindsAfterJoin(t *testing.T) {
	submitted := contextDocument()
	submitted.People = &spec.People{Members: []spec.Member{{Email: "new@acme.com"}}}
	submitted.Directory = &spec.Directory{Identities: []spec.ExternalIdentity{{Source: "google_workspace:acme.com", ExternalID: "101", Member: "new@acme.com"}}}
	state := directoryState()
	_, plan, err := MergeDirectory(contextDocument(), submitted, state, nil, false)
	if err != nil || len(plan.NeedsHuman) != 1 {
		t.Fatalf("pending = %+v %v", plan, err)
	}
	state.Members["new@acme.com"] = Member{UserID: "u3", Email: "new@acme.com"}
	effective, _, err := MergeDirectory(contextDocument(), submitted, state, nil, false)
	if err != nil {
		t.Fatalf("joined = %+v %v", effective.Directory, err)
	}
	mapped := []store.ExternalMapping{{Source: "google_workspace:acme.com", Kind: "user", ExternalID: "101", Reference: "new@acme.com"}}
	_, plan, err = MergeDirectory(effective, effective, state, mapped, false)
	if err != nil || len(plan.Changes) != 1 || plan.Changes[0].Op != "update" {
		t.Fatalf("binding drift = %+v %v", plan, err)
	}
}

func TestDirectoryMappingMustBePrunedWithRemovedMember(t *testing.T) {
	old := contextDocument()
	old.Directory = &spec.Directory{Identities: []spec.ExternalIdentity{{Source: "google_workspace:acme.com", ExternalID: "100", Member: "ada@acme.com"}}}
	submitted := contextDocument()
	submitted.People = &spec.People{Prune: true, Members: []spec.Member{{Email: "bob@acme.com"}}}
	mapped := []store.ExternalMapping{{Source: "google_workspace:acme.com", Kind: "user", ExternalID: "100", Reference: "ada@acme.com", ResourceID: "u1"}}
	if _, _, err := MergeDirectory(old, submitted, directoryState(), mapped, false); err == nil || !strings.Contains(err.Error(), "must be pruned") {
		t.Fatalf("member prune without mapping prune = %v", err)
	}
	submitted.Directory = &spec.Directory{Prune: true}
	_, plan, err := MergeDirectory(old, submitted, directoryState(), mapped, false)
	if err != nil || len(plan.Changes) != 1 || plan.Changes[0].Op != "delete" {
		t.Fatalf("paired prune = %+v %v", plan, err)
	}
}

func TestRetainedDeletedDirectoryTargetsAreDropped(t *testing.T) {
	previous := contextDocument()
	previous.Directory = &spec.Directory{Identities: []spec.ExternalIdentity{{Source: "google_workspace:acme.com", ExternalID: "departed1", Member: "departed@acme.com"}}, Groups: []spec.ExternalGroup{{Source: "google_workspace:acme.com", ExternalID: "deleted1", Team: "Deleted"}}}
	effective, plan, err := MergeDirectory(previous, contextDocument(), directoryState(), nil, false)
	if err != nil || len(effective.Directory.Identities) != 0 || len(effective.Directory.Groups) != 0 || len(plan.Notes) != 2 {
		t.Fatalf("stale directory prevented unrelated apply: %+v %+v %v", effective.Directory, plan, err)
	}
	if _, _, err := MergeDirectory(previous, previous, directoryState(), nil, false); err == nil {
		t.Fatal("explicit invalid mapping silently removed")
	}
}

func TestDirectoryResolvesJoinedIDsFromAPISnapshot(t *testing.T) {
	document := contextDocument()
	document.Directory = &spec.Directory{Identities: []spec.ExternalIdentity{{Source: "google_workspace:acme.com", ExternalID: "user1", Member: "ada@acme.com"}}, Groups: []spec.ExternalGroup{{Source: "google_workspace:acme.com", ExternalID: "group1", Team: "Engineering"}}}
	resolved, err := ResolveDirectory(document, directoryState(), nil)
	if err != nil || len(resolved) != 2 {
		t.Fatalf("resolve = %+v %v", resolved, err)
	}
	ids := map[string]string{}
	for _, mapping := range resolved {
		ids[mapping.Kind] = mapping.ResourceID
	}
	if ids["user"] != "u1" || ids["team"] != "t1" {
		t.Fatalf("resolved wrong API resources: %+v", resolved)
	}
}

func TestDeletedGroupBindingIsDetectedBeforeRecreationAndRecoversAfterCleanup(t *testing.T) {
	document := contextDocument()
	document.Teams.Items = []spec.Team{{Name: "Engineering"}}
	document.Directory = &spec.Directory{Groups: []spec.ExternalGroup{{Source: "google_workspace:acme.com", ExternalID: "group1", Team: "Engineering"}}}
	state := directoryState()
	delete(state.Teams, "Engineering")
	mapped := []store.ExternalMapping{{Source: "google_workspace:acme.com", Kind: "team", ExternalID: "group1", Reference: "Engineering", ResourceID: "deleted-team"}}
	if _, _, err := MergeDirectory(document, document, state, mapped, false); err == nil || !strings.Contains(err.Error(), "deleted team") {
		t.Fatalf("stale binding not preflighted: %v", err)
	}
	// The team-deleted event clears the obsolete binding. The exact same file
	// can then plan and materialize the new API-assigned team ID.
	if _, _, err := MergeDirectory(document, document, state, nil, false); err != nil {
		t.Fatalf("same manifest did not recover after event cleanup: %v", err)
	}
	state.Teams["Engineering"] = Team{ID: "new-api-team", Name: "Engineering"}
	resolved, err := ResolveDirectory(document, state, nil)
	if err != nil || len(resolved) != 1 || resolved[0].ResourceID != "new-api-team" {
		t.Fatalf("recreated target bound incorrectly: %+v %v", resolved, err)
	}
}
