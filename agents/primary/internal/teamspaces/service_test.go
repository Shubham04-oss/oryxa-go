package teamspaces

import (
	"context"
	"errors"
	"testing"
	"time"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
)

type fakeDirectory struct {
	directory primaryagents.OrganizationDirectory
}

func (directory fakeDirectory) Load(context.Context, string, string) (primaryagents.OrganizationDirectory, error) {
	return directory.directory, nil
}

type fakeRepository struct {
	spaces            []Space
	organizationAgent OrganizationAgent
	entries           []Entry
}

func (repo *fakeRepository) EnsureOrganizationAgent(_ context.Context, organizationID, organizationName, _ string) (OrganizationAgent, error) {
	if repo.organizationAgent.ID == "" {
		repo.organizationAgent = OrganizationAgent{
			ID: "oag_1", OrganizationID: organizationID, Name: organizationName + " Agent",
			Instructions: "Coordinate shared organizational context.", Status: StatusActive, Revision: 1,
		}
	}
	return repo.organizationAgent, nil
}

func (repo *fakeRepository) GetOrganizationAgent(_ context.Context, organizationID string) (OrganizationAgent, error) {
	if repo.organizationAgent.OrganizationID != organizationID {
		return OrganizationAgent{}, ErrNotFound
	}
	return repo.organizationAgent, nil
}

func (repo *fakeRepository) UpdateOrganizationAgent(_ context.Context, organizationID string, input OrganizationAgentUpdate) (OrganizationAgent, error) {
	if repo.organizationAgent.OrganizationID != organizationID {
		return OrganizationAgent{}, ErrNotFound
	}
	if input.Name != nil {
		repo.organizationAgent.Name = *input.Name
	}
	if input.Description != nil {
		repo.organizationAgent.Description = *input.Description
	}
	if input.Instructions != nil {
		repo.organizationAgent.Instructions = *input.Instructions
	}
	if input.Status != nil {
		repo.organizationAgent.Status = *input.Status
	}
	repo.organizationAgent.Revision++
	return repo.organizationAgent, nil
}

func (repo *fakeRepository) Create(_ context.Context, space Space) (Space, error) {
	space.CreatedAt, space.UpdatedAt = time.Unix(1, 0).UTC(), time.Unix(1, 0).UTC()
	space.OrganizationAgent = repo.organizationAgent
	space.Contributions = []Contribution{}
	repo.spaces = append(repo.spaces, space)
	return space, nil
}

func (repo *fakeRepository) List(context.Context, string) ([]Space, error) { return repo.spaces, nil }

func (repo *fakeRepository) Get(_ context.Context, _ string, id string) (Space, error) {
	for _, space := range repo.spaces {
		if space.ID == id {
			return space, nil
		}
	}
	return Space{}, ErrNotFound
}

func (repo *fakeRepository) Update(_ context.Context, _ string, id string, input UpdateInput) (Space, error) {
	for index := range repo.spaces {
		if repo.spaces[index].ID != id {
			continue
		}
		repo.spaces[index] = applyUpdate(repo.spaces[index], input)
		repo.spaces[index].Revision++
		return repo.spaces[index], nil
	}
	return Space{}, ErrNotFound
}

func (repo *fakeRepository) Delete(_ context.Context, _ string, id string) error {
	for index := range repo.spaces {
		if repo.spaces[index].ID == id {
			repo.spaces = append(repo.spaces[:index], repo.spaces[index+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

func (repo *fakeRepository) AddContribution(_ context.Context, actor Actor, id string, input ContributionInput) (Space, error) {
	for index := range repo.spaces {
		if repo.spaces[index].ID != id {
			continue
		}
		repo.spaces[index].Contributions = append(repo.spaces[index].Contributions, Contribution{
			AgentID: input.AgentID, OwnerUserID: actor.UserID, Name: "Researcher", Status: managedagents.StatusActive,
			Role: input.Role, DelegationMode: input.DelegationMode,
		})
		return repo.spaces[index], nil
	}
	return Space{}, ErrNotFound
}

func (repo *fakeRepository) UpdateContribution(_ context.Context, actor Actor, id, agentID string, input ContributionUpdate) (Space, error) {
	for index := range repo.spaces {
		if repo.spaces[index].ID != id {
			continue
		}
		for contributionIndex := range repo.spaces[index].Contributions {
			contribution := &repo.spaces[index].Contributions[contributionIndex]
			if contribution.AgentID != agentID || contribution.OwnerUserID != actor.UserID {
				continue
			}
			if input.Role != nil {
				contribution.Role = *input.Role
			}
			if input.DelegationMode != nil {
				contribution.DelegationMode = *input.DelegationMode
			}
			return repo.spaces[index], nil
		}
	}
	return Space{}, ErrNotFound
}

func (repo *fakeRepository) DeleteContribution(_ context.Context, _ string, id, agentID string) (Space, error) {
	for index := range repo.spaces {
		if repo.spaces[index].ID != id {
			continue
		}
		for contributionIndex, contribution := range repo.spaces[index].Contributions {
			if contribution.AgentID == agentID {
				repo.spaces[index].Contributions = append(repo.spaces[index].Contributions[:contributionIndex], repo.spaces[index].Contributions[contributionIndex+1:]...)
				return repo.spaces[index], nil
			}
		}
	}
	return Space{}, ErrNotFound
}

func (repo *fakeRepository) PublishEntry(_ context.Context, entry Entry) (Entry, error) {
	entry.CreatedAt = time.Unix(2, 0).UTC()
	repo.entries = append(repo.entries, entry)
	return entry, nil
}

func (repo *fakeRepository) ListEntries(_ context.Context, organizationID, teamspaceID string, limit int) ([]Entry, error) {
	entries := []Entry{}
	for index := len(repo.entries) - 1; index >= 0 && len(entries) < limit; index-- {
		entry := repo.entries[index]
		if entry.OrganizationID == organizationID && entry.TeamspaceID == teamspaceID {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func teamDirectory(canManage, member bool) primaryagents.OrganizationDirectory {
	person := primaryagents.DirectoryPerson{
		UserID: "user_1", DisplayName: "Ada", OrgRole: "owner", Teams: []primaryagents.DirectoryTeamRef{},
		Agents: []primaryagents.DirectoryAgent{{ID: "agt_ada", Kind: "primary", Name: "Carl"}},
	}
	if member {
		person.Teams = append(person.Teams, primaryagents.DirectoryTeamRef{ID: "team_1", Name: "Engineering"})
	}
	return primaryagents.OrganizationDirectory{
		Organization: primaryagents.DirectoryOrganization{ID: "org_1", Name: "Oryxa"},
		Teams:        []primaryagents.DirectoryTeam{{ID: "team_1", Name: "Engineering", CanManage: canManage}},
		People: []primaryagents.DirectoryPerson{person, {
			UserID: "user_2", DisplayName: "Grace", OrgRole: "member",
			Teams:  []primaryagents.DirectoryTeamRef{{ID: "team_1", Name: "Engineering"}},
			Agents: []primaryagents.DirectoryAgent{{ID: "agt_grace", Kind: "primary", Name: "Cleo"}, {ID: "agt_private", Kind: "specialist", Name: "Private Specialist"}},
		}},
	}
}

func actor() Actor { return Actor{OrganizationID: "org_1", UserID: "user_1", AccessToken: "token"} }

func TestCreateBindsTheOrganizationAgentAndLiveWhisperersToAManagedHumanTeam(t *testing.T) {
	repo := &fakeRepository{}
	service, err := New(repo, fakeDirectory{directory: teamDirectory(true, true)})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), actor(), CreateInput{
		TeamID: " team_1 ", Name: " Engineering   Room ", Description: " Shared launch decisions. ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.OrganizationAgent.ID != "oag_1" || created.TeamName != "Engineering" || !created.CanManage || !created.IsMember {
		t.Fatalf("created=%+v", created)
	}
	if created.Name != "Engineering Room" || created.OrganizationAgent.Name != "Oryxa Agent" || !created.OrganizationAgent.CanManage {
		t.Fatalf("normalized=%+v", created)
	}
	if len(created.Whisperers) != 2 || created.Whisperers[0].AgentID != "agt_ada" || created.Whisperers[1].AgentID != "agt_grace" {
		t.Fatalf("whisperers=%+v", created.Whisperers)
	}
	if created.Whisperers[0].Mode != "whisperer" || created.Whisperers[1].Mode != "whisperer" {
		t.Fatalf("whisperer mode=%+v", created.Whisperers)
	}
}

func TestOrganizationAgentIsSingletonAndOnlyOrganizationManagersCanEditIt(t *testing.T) {
	repo := &fakeRepository{}
	service, _ := New(repo, fakeDirectory{directory: teamDirectory(true, true)})
	first, err := service.OrganizationAgent(context.Background(), actor())
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), actor(), CreateInput{TeamID: "team_1", Name: "Engineering"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != created.OrganizationAgent.ID {
		t.Fatalf("organization agent changed: first=%s space=%s", first.ID, created.OrganizationAgent.ID)
	}
	name := "Atlas"
	updated, err := service.UpdateOrganizationAgent(context.Background(), actor(), OrganizationAgentUpdate{Name: &name})
	if err != nil || updated.Name != "Atlas" || updated.Revision != 2 {
		t.Fatalf("updated=%+v error=%v", updated, err)
	}

	directory := teamDirectory(true, true)
	for index := range directory.People {
		if directory.People[index].UserID == "user_1" {
			directory.People[index].OrgRole = "member"
		}
	}
	memberService, _ := New(repo, fakeDirectory{directory: directory})
	if _, err := memberService.UpdateOrganizationAgent(context.Background(), actor(), OrganizationAgentUpdate{Name: ptr("Nope")}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member update error=%v", err)
	}
}

func TestTeamspaceCreationRequiresCurrentTeamControl(t *testing.T) {
	service, _ := New(&fakeRepository{}, fakeDirectory{directory: teamDirectory(false, true)})
	_, err := service.Create(context.Background(), actor(), CreateInput{
		TeamID: "team_1", Name: "Engineering",
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("error=%v", err)
	}
}

func TestListConcealsSpacesOutsideTheLiveTeamDirectory(t *testing.T) {
	repo := &fakeRepository{spaces: []Space{
		{ID: "tsp_visible", OrganizationID: "org_1", TeamID: "team_1"},
		{ID: "tsp_hidden", OrganizationID: "org_1", TeamID: "team_2"},
	}}
	service, _ := New(repo, fakeDirectory{directory: teamDirectory(false, true)})
	spaces, err := service.List(context.Background(), actor())
	if err != nil || len(spaces) != 1 || spaces[0].ID != "tsp_visible" {
		t.Fatalf("spaces=%+v error=%v", spaces, err)
	}
}

func TestTeamspaceMetadataIsConcealedFromCurrentNonMembers(t *testing.T) {
	repo := &fakeRepository{spaces: []Space{{
		ID: "tsp_hidden", OrganizationID: "org_1", TeamID: "team_1", Name: "Private planning",
	}}}
	service, _ := New(repo, fakeDirectory{directory: teamDirectory(false, false)})
	spaces, err := service.List(context.Background(), actor())
	if err != nil || len(spaces) != 0 {
		t.Fatalf("spaces=%+v error=%v", spaces, err)
	}
	if _, err := service.Get(context.Background(), actor(), "tsp_hidden"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get error=%v, want ErrNotFound", err)
	}
}

func TestContributionIsAnExplicitOwnerGrant(t *testing.T) {
	repo := &fakeRepository{spaces: []Space{{
		ID: "tsp_1", OrganizationID: "org_1", TeamID: "team_1", Name: "Engineering",
		OrganizationAgent: OrganizationAgent{ID: "oag_1", OrganizationID: "org_1", Name: "Oryxa Agent", Instructions: "Coordinate.", Status: StatusActive}, Status: StatusActive,
	}}}
	service, _ := New(repo, fakeDirectory{directory: teamDirectory(false, true)})
	space, err := service.AddContribution(context.Background(), actor(), "tsp_1", ContributionInput{
		AgentID: "agt_research", Role: " Investigate   technical risks ", DelegationMode: DelegationAutomatic,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(space.Contributions) != 1 || space.Contributions[0].OwnerUserID != "user_1" || space.Contributions[0].Role != "Investigate technical risks" || space.Contributions[0].DelegationMode != DelegationAutomatic {
		t.Fatalf("space=%+v", space)
	}

	other := actor()
	other.UserID = "user_2"
	if _, err := service.UpdateContribution(context.Background(), other, "tsp_1", "agt_research", ContributionUpdate{DelegationMode: ptr(DelegationRequest)}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("other owner update error=%v", err)
	}
}

func TestOnlyCurrentMembersCanContributeAgents(t *testing.T) {
	repo := &fakeRepository{spaces: []Space{{ID: "tsp_1", OrganizationID: "org_1", TeamID: "team_1", Status: StatusActive}}}
	service, _ := New(repo, fakeDirectory{directory: teamDirectory(true, false)})
	_, err := service.AddContribution(context.Background(), actor(), "tsp_1", ContributionInput{
		AgentID: "agt_1", Role: "Review", DelegationMode: DelegationRequest,
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("error=%v", err)
	}
}

func TestWhispererPublishesAttributedWorkButCannotClaimAnotherMember(t *testing.T) {
	repo := &fakeRepository{spaces: []Space{{
		ID: "tsp_1", OrganizationID: "org_1", TeamID: "team_1", Name: "Engineering", Status: StatusActive,
		OrganizationAgent: OrganizationAgent{ID: "oag_1", OrganizationID: "org_1", Name: "Oryxa Agent", Instructions: "Coordinate.", Status: StatusActive},
	}}}
	service, _ := New(repo, fakeDirectory{directory: teamDirectory(false, true)})
	entry, err := service.PublishEntry(context.Background(), actor(), "tsp_1", PublishEntryInput{
		AuthorAgentID: "agt_ada", Kind: EntryResult, Title: " Architecture   review ",
		Summary: "The design is ready.", ToolsUsed: []string{" github.search ", "github.search"},
		Artifacts: []EntryArtifact{{Name: " Review ", URI: " https://example.test/review ", MediaType: " text/markdown "}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.AuthorUserID != "user_1" || entry.AuthorAgentName != "Carl" || entry.Title != "Architecture review" || len(entry.ToolsUsed) != 1 {
		t.Fatalf("entry=%+v", entry)
	}
	entries, err := service.ListEntries(context.Background(), actor(), "tsp_1", 10)
	if err != nil || len(entries) != 1 || entries[0].ID != entry.ID {
		t.Fatalf("entries=%+v error=%v", entries, err)
	}
	if _, err := service.PublishEntry(context.Background(), actor(), "tsp_1", PublishEntryInput{
		AuthorAgentID: "agt_grace", Kind: EntryUpdate, Title: "Claim", Summary: "Not mine.",
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-member author error=%v", err)
	}
}

func TestSharedArtifactsRejectUnsafeOrAmbiguousLocations(t *testing.T) {
	repo := &fakeRepository{spaces: []Space{{
		ID: "tsp_1", OrganizationID: "org_1", TeamID: "team_1", Name: "Engineering", Status: StatusActive,
		OrganizationAgent: OrganizationAgent{ID: "oag_1", OrganizationID: "org_1", Name: "Oryxa Agent", Instructions: "Coordinate.", Status: StatusActive},
	}}}
	service, _ := New(repo, fakeDirectory{directory: teamDirectory(false, true)})
	for _, uri := range []string{"javascript:alert(1)", "data:text/html,unsafe", "http://example.test/report", "//example.test/report", "https://user@example.test/report", "report.md"} {
		_, err := service.PublishEntry(context.Background(), actor(), "tsp_1", PublishEntryInput{
			AuthorAgentID: "agt_ada", Kind: EntryKindArtifact, Title: "Report", Summary: "Shared report.",
			Artifacts: []EntryArtifact{{Name: "Report", URI: uri}},
		})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("uri %q error=%v, want ErrInvalid", uri, err)
		}
	}
	for _, uri := range []string{"https://example.test/report", "/v1/files/report"} {
		if _, err := service.PublishEntry(context.Background(), actor(), "tsp_1", PublishEntryInput{
			AuthorAgentID: "agt_ada", Kind: EntryKindArtifact, Title: "Report", Summary: "Shared report.",
			Artifacts: []EntryArtifact{{Name: "Report", URI: uri}},
		}); err != nil {
			t.Fatalf("safe uri %q error=%v", uri, err)
		}
	}
}

func TestAgentCollaborationRequiresWhispererOrExplicitContribution(t *testing.T) {
	repo := &fakeRepository{spaces: []Space{{
		ID: "tsp_1", OrganizationID: "org_1", TeamID: "team_1", Name: "Engineering", Status: StatusActive,
		OrganizationAgent: OrganizationAgent{ID: "oag_1", OrganizationID: "org_1", Name: "Oryxa Agent", Instructions: "Coordinate.", Status: StatusActive},
		Contributions: []Contribution{{
			AgentID: "agt_review", OwnerUserID: "user_1", Name: "Reviewer", Status: managedagents.StatusActive,
			Role: "Review architecture", DelegationMode: DelegationAutomatic,
		}},
	}}}
	service, _ := New(repo, fakeDirectory{directory: teamDirectory(false, true)})
	specialist := primaryagents.TeamspaceActor{
		OrganizationID: "org_1", UserID: "user_1", AgentID: "agt_review", AgentName: "Reviewer", AgentKind: "specialist",
	}
	spaces, err := service.CollaborationSpaces(context.Background(), specialist, "token")
	if err != nil || len(spaces) != 1 || spaces[0].ID != "tsp_1" {
		t.Fatalf("spaces=%+v error=%v", spaces, err)
	}
	entry, err := service.CollaborationPublish(context.Background(), specialist, "token", "tsp_1", primaryagents.SharedEntryInput{
		Kind: "result", Title: "Review", Summary: "One bounded finding.",
	})
	if err != nil || entry.AuthorAgentID != "agt_review" || entry.AuthorUserID != "user_1" {
		t.Fatalf("entry=%+v error=%v", entry, err)
	}

	uncontributed := specialist
	uncontributed.AgentID = "agt_other"
	spaces, err = service.CollaborationSpaces(context.Background(), uncontributed, "token")
	if err != nil || len(spaces) != 0 {
		t.Fatalf("uncontributed spaces=%+v error=%v", spaces, err)
	}
	if _, err := service.CollaborationEntries(context.Background(), uncontributed, "token", "tsp_1", 10); !errors.Is(err, ErrForbidden) {
		t.Fatalf("uncontributed read error=%v", err)
	}
}

func TestDisabledSharedWorkRemainsMemberReadableButNotAgentVisible(t *testing.T) {
	tests := []struct {
		name              string
		spaceStatus       Status
		organizationState Status
	}{
		{name: "disabled teamspace", spaceStatus: StatusDisabled, organizationState: StatusActive},
		{name: "disabled organization agent", spaceStatus: StatusActive, organizationState: StatusDisabled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &fakeRepository{spaces: []Space{{
				ID: "tsp_1", OrganizationID: "org_1", TeamID: "team_1", Name: "Engineering",
				Status: test.spaceStatus,
				OrganizationAgent: OrganizationAgent{
					ID: "oag_1", OrganizationID: "org_1", Name: "Oryxa Agent",
					Instructions: "Coordinate.", Status: test.organizationState,
				},
			}}}
			service, err := New(repo, fakeDirectory{directory: teamDirectory(false, true)})
			if err != nil {
				t.Fatal(err)
			}
			primary := primaryagents.TeamspaceActor{
				OrganizationID: "org_1", UserID: "user_1", AgentID: "agt_ada", AgentKind: "primary",
			}

			// The member API keeps its existing read behavior; disabling a
			// space only stops persistent-agent participation.
			memberSpaces, err := service.List(context.Background(), actor())
			if err != nil || len(memberSpaces) != 1 || memberSpaces[0].ID != "tsp_1" {
				t.Fatalf("member spaces=%+v error=%v", memberSpaces, err)
			}
			if entries, err := service.ListEntries(context.Background(), actor(), "tsp_1", 10); err != nil || entries == nil {
				t.Fatalf("member entries=%+v error=%v", entries, err)
			}

			spaces, err := service.CollaborationSpaces(context.Background(), primary, "token")
			if err != nil || len(spaces) != 0 {
				t.Fatalf("agent spaces=%+v error=%v", spaces, err)
			}
			if _, err := service.CollaborationEntries(context.Background(), primary, "token", "tsp_1", 10); !errors.Is(err, ErrDisabled) {
				t.Fatalf("agent entries error=%v, want ErrDisabled", err)
			}
		})
	}
}

func ptr[T any](value T) *T { return &value }
