package managedagents

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/session"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/events"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/pricing"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runprofile"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/adksession"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

type fakeRepository struct {
	primary Definition
	agents  map[string]Definition
	created Definition
	updated UpdateInput
	ensured int
}

func newFakeRepository() *fakeRepository {
	primary := Definition{ID: "agt_primary", OrganizationID: "org_1", OwnerUserID: "user_1", Kind: KindPrimary, Name: "Carl-X7P4", Status: StatusActive, Revision: 1}
	return &fakeRepository{primary: primary, agents: map[string]Definition{primary.ID: primary}}
}

func (repo *fakeRepository) EnsurePrimary(context.Context, Scope) (Definition, error) {
	repo.ensured++
	return repo.primary, nil
}
func (repo *fakeRepository) Create(_ context.Context, scope Scope, definition Definition) (Definition, error) {
	definition.ID, definition.OrganizationID, definition.OwnerUserID = "agt_sales", scope.OrganizationID, scope.UserID
	definition.Kind, definition.Status, definition.Revision = KindSpecialist, StatusActive, 1
	repo.created = definition
	repo.agents[definition.ID] = definition
	return definition, nil
}
func (repo *fakeRepository) List(context.Context, Scope) ([]Definition, error) {
	return []Definition{repo.primary}, nil
}
func (repo *fakeRepository) Get(_ context.Context, scope Scope, id string) (Definition, error) {
	definition, ok := repo.agents[id]
	if !ok || definition.OrganizationID != scope.OrganizationID || definition.OwnerUserID != scope.UserID {
		return Definition{}, ErrNotFound
	}
	return definition, nil
}
func (repo *fakeRepository) Update(_ context.Context, _ Scope, id string, input UpdateInput) (Definition, error) {
	repo.updated = input
	definition := repo.agents[id]
	if input.Name != nil {
		definition.Name = *input.Name
	}
	if input.Description != nil {
		definition.Description = *input.Description
	}
	if input.Instructions != nil {
		definition.Instructions = *input.Instructions
	}
	if input.Status != nil {
		definition.Status = *input.Status
	}
	if input.Grant != nil {
		definition.Grant = input.Grant
	}
	repo.agents[id] = definition
	return definition, nil
}
func (repo *fakeRepository) Delete(_ context.Context, _ Scope, id string) error {
	if _, ok := repo.agents[id]; !ok {
		return ErrNotFound
	}
	delete(repo.agents, id)
	return nil
}

func TestListEnsuresTheMembersPrimaryAgent(t *testing.T) {
	repo := newFakeRepository()
	service := &Service{repository: repo}
	listed, err := service.List(context.Background(), Scope{OrganizationID: "org_1", UserID: "user_1"})
	if err != nil {
		t.Fatal(err)
	}
	if repo.ensured != 1 || len(listed) != 1 || listed[0].Kind != KindPrimary {
		t.Fatalf("ensure=%d agents=%+v", repo.ensured, listed)
	}
}

func TestPrimaryIsEnsuredOnAnyEntryPoint(t *testing.T) {
	repo := newFakeRepository()
	service := &Service{repository: repo}
	primary, err := service.Primary(context.Background(), Scope{OrganizationID: "org_1", UserID: "user_1"})
	if err != nil {
		t.Fatal(err)
	}
	if primary.ID != "agt_primary" || repo.ensured != 1 {
		t.Fatalf("primary=%+v ensured=%d", primary, repo.ensured)
	}
}

func TestCreateNormalizesAndValidatesASpecialist(t *testing.T) {
	repo := newFakeRepository()
	service := &Service{repository: repo}
	created, err := service.Create(context.Background(), Scope{OrganizationID: "org_1", UserID: "user_1"}, CreateInput{
		Name: "  Sales   Agent ", Description: "  Owns pipeline reviews.  ", Instructions: "  Review CRM opportunities.  ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if repo.ensured != 1 || created.Name != "Sales Agent" || created.Description != "Owns pipeline reviews." || created.Instructions != "Review CRM opportunities." {
		t.Fatalf("created = %+v; ensured=%d", created, repo.ensured)
	}
	_, err = service.Create(context.Background(), Scope{OrganizationID: "org_1", UserID: "user_1"}, CreateInput{Name: "Legal"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing instructions error = %v", err)
	}
}

func TestUpdateCanRenameThePrimaryButNeedsARealChange(t *testing.T) {
	repo := newFakeRepository()
	service := &Service{repository: repo}
	name := "  Crew  "
	updated, err := service.Update(context.Background(), Scope{OrganizationID: "org_1", UserID: "user_1"}, repo.primary.ID, UpdateInput{Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Crew" || repo.updated.Name == nil || *repo.updated.Name != "Crew" {
		t.Fatalf("updated = %+v, input = %+v", updated, repo.updated)
	}
	_, err = service.Update(context.Background(), Scope{OrganizationID: "org_1", UserID: "user_1"}, repo.primary.ID, UpdateInput{})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty update error = %v", err)
	}
}

// TestASpecialistsGrantIsNormalizedAndOnlyASpecialistHasOne: a grant is
// stored as apps compares it, refused when apps could never match it or
// when it grants a write to every app at once, and the primary has none to
// set. A specialist that never had one reads every app.
func TestASpecialistsGrantIsNormalizedAndOnlyASpecialistHasOne(t *testing.T) {
	repo := newFakeRepository()
	service := &Service{repository: repo}
	scope := Scope{OrganizationID: "org_1", UserID: "user_1"}
	specialist, err := service.Create(context.Background(), scope, CreateInput{Name: "Mail", Instructions: "Triage mail."})
	if err != nil {
		t.Fatal(err)
	}
	if grant := specialist.SpecialistGrant(); !slices.Equal(grant.Apps, []string{runprofile.AllApps}) || grant.Writes() {
		t.Fatalf("a specialist with no grant reaches %+v, want every app read-only", grant)
	}

	updated, err := service.Update(context.Background(), scope, specialist.ID, UpdateInput{Grant: &runprofile.Grant{
		Apps: []string{" Gmail ", "gmail", "*"}, Tools: []string{"gmail_fetch_emails"}, WriteApps: []string{"GMAIL"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := runprofile.Grant{Apps: []string{"gmail", "*"}, Tools: []string{"GMAIL_FETCH_EMAILS"}, WriteApps: []string{"gmail"}, WriteTools: []string{}}
	if !reflect.DeepEqual(*updated.Grant, want) {
		t.Fatalf("stored grant = %+v, want %+v", *updated.Grant, want)
	}

	for name, grant := range map[string]runprofile.Grant{
		"a write to every app":    {Apps: []string{"*"}, WriteApps: []string{"*"}},
		"a tool apps cannot name": {Apps: []string{"gmail"}, Tools: []string{"send email!"}},
		"an app apps cannot name": {Apps: []string{"g mail"}},
	} {
		if _, err := service.Update(context.Background(), scope, specialist.ID, UpdateInput{Grant: &grant}); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: error = %v, want invalid", name, err)
		}
	}
	if _, err := service.Update(context.Background(), scope, repo.primary.ID, UpdateInput{Grant: &want}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a grant for the primary: error = %v, want invalid", err)
	}
}

// TestASpecialistsRunCarriesItsGrant: every run of a specialist writes its
// grant as it is now into the session, where its agent token is asked for
// from; the primary's carries none.
func TestASpecialistsRunCarriesItsGrant(t *testing.T) {
	grant := runprofile.Grant{Apps: []string{"gmail"}, WriteTools: []string{"GMAIL_SEND_EMAIL"}}
	for definition, want := range map[*Definition]string{
		{ID: "agt_sales", Kind: KindSpecialist, Grant: &grant}: `{"apps":["gmail"],"tools":null,"write_apps":null,"write_tools":["GMAIL_SEND_EMAIL"]}`,
		{ID: "agt_old", Kind: KindSpecialist}:                  `{"apps":["*"],"tools":[],"write_apps":[],"write_tools":[]}`,
		{ID: "agt_primary", Kind: KindPrimary}:                 "",
	} {
		state, err := runState(*definition)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := state[memberauth.GrantStateKey].(string); got != want || state[primaryagents.AgentIDKey] != definition.ID {
			t.Errorf("%s: the run's state = %v, want the grant %q", definition.ID, state, want)
		}
	}
}

func TestDisabledAgentCannotRun(t *testing.T) {
	repo := newFakeRepository()
	disabled := Definition{ID: "agt_legal", OrganizationID: "org_1", OwnerUserID: "user_1", Kind: KindSpecialist, Name: "Legal", Instructions: "Review.", Status: StatusDisabled}
	repo.agents[disabled.ID] = disabled
	service := &Service{repository: repo}
	_, err := service.StartRun(context.Background(), threads.Member{OrganizationID: "org_1", UserID: "user_1"}, disabled.ID, "", threads.RunInput{Message: "Review this"})
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("StartRun error = %v", err)
	}
}

func TestAppNameDoesNotChangeWithDisplayName(t *testing.T) {
	if got := appName("agt_ab-cd"); got != "managed_agt_ab_cd" {
		t.Fatalf("appName = %q", got)
	}
}

func TestDeleteThreadWorksAfterTheAgentDefinitionIsGone(t *testing.T) {
	ctx := context.Background()
	sessions := session.InMemoryService()
	created, err := sessions.Create(ctx, &session.CreateRequest{
		AppName: appName("agt_deleted"), UserID: "user_1",
		State: map[string]any{adksession.StateOrganizationID: "org_1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{sessions: sessions}
	if err := service.DeleteThread(ctx, threads.Member{OrganizationID: "org_1", UserID: "user_1"}, "agt_deleted", created.Session.ID()); err != nil {
		t.Fatal(err)
	}
	_, err = sessions.Get(ctx, &session.GetRequest{
		AppName: appName("agt_deleted"), UserID: "user_1", SessionID: created.Session.ID(),
	})
	if !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("deleted session error=%v", err)
	}
}

type noRuns struct{}

func (noRuns) Start(context.Context, runstore.Run, time.Duration) error { return nil }
func (noRuns) Renew(context.Context, string, time.Duration) error       { return nil }
func (noRuns) Finish(context.Context, string, runstore.Outcome, events.Event) error {
	return nil
}
func (noRuns) Cancel(context.Context, string) error { return nil }

func namedAgent(t *testing.T, name string) agent.Agent {
	t.Helper()
	built, err := llmagent.New(llmagent.Config{Name: name, Instruction: "Answer."})
	if err != nil {
		t.Fatal(err)
	}
	return built
}

// TestAnUnwatchedRunAnswersOnTheRestrictedAgent: every engine a persistent
// agent runs in is given the reduced agent as well, so a run nobody is
// watching is not answered by the one a member talks to. The profile the
// caller asked for decides how little that agent may reach.
func TestAnUnwatchedRunAnswersOnTheRestrictedAgent(t *testing.T) {
	repo := newFakeRepository()
	prices, err := pricing.NewTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	var asked []UnattendedProfile
	service := &Service{
		repository: repo,
		factory:    func(Definition) (agent.Agent, error) { return namedAgent(t, "managed_agt_primary"), nil },
		unattended: func(_ Definition, profile UnattendedProfile) (agent.Agent, error) {
			asked = append(asked, profile)
			return namedAgent(t, "managed_agt_primary"), nil
		},
		sessions: session.InMemoryService(), runs: noRuns{}, prices: prices,
	}
	for _, profile := range []UnattendedProfile{UnattendedQuiet, UnattendedReads} {
		if _, err := service.engine(repo.primary, profile); err != nil {
			t.Fatalf("engine(%q) error = %v", profile, err)
		}
	}
	if len(asked) != 2 || asked[0] != UnattendedQuiet || asked[1] != UnattendedReads {
		t.Fatalf("the restricted agent was asked for %v", asked)
	}

	// Without a restricted agent the engine still runs, which is what makes
	// wiring one the thing that matters: say so rather than assume it.
	service.unattended = nil
	if _, err := service.engine(repo.primary, UnattendedQuiet); err != nil {
		t.Fatalf("engine() without a restricted agent error = %v", err)
	}

	service.unattended = func(Definition, UnattendedProfile) (agent.Agent, error) {
		return nil, errors.New("no restricted agent")
	}
	if _, err := service.engine(repo.primary, UnattendedQuiet); err == nil {
		t.Fatal("engine() ran on with no restricted agent to fall back to")
	}
}

// TestAProfileSaysWhatItReachesAndWhetherItDelivers: only the reading
// profiles reach the member's apps, and only the delivery profiles belong to
// a run delivering one interaction. A profile nobody knows reaches neither.
func TestAProfileSaysWhatItReachesAndWhetherItDelivers(t *testing.T) {
	for profile, want := range map[UnattendedProfile]struct{ apps, delivers bool }{
		UnattendedReads: {apps: true},
		UnattendedQuiet: {},
		DeliveryReads:   {apps: true, delivers: true},
		DeliveryQuiet:   {delivers: true},
		DeliveryReport:  {delivers: true},
		"everything":    {},
	} {
		if profile.ReadsApps() != want.apps || profile.Delivers() != want.delivers {
			t.Errorf("%q: reads apps %t, delivers %t; want %t, %t", profile, profile.ReadsApps(), profile.Delivers(), want.apps, want.delivers)
		}
	}
}
