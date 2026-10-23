package managedagents

import (
	"context"
	"encoding/json"
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
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/modelrouter"
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
	if input.ModelTier != nil {
		definition.ModelTier = *input.ModelTier
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

// TestThePrimaryTakesNoInstructions: the primary answers on Krude's own
// instruction, so text written to it would be stored and never followed.
// A specialist's instructions are still its owner's to change.
func TestThePrimaryTakesNoInstructions(t *testing.T) {
	repo := newFakeRepository()
	service := &Service{repository: repo}
	scope := Scope{OrganizationID: "org_1", UserID: "user_1"}
	instructions := "Always answer in French."
	if _, err := service.Update(context.Background(), scope, repo.primary.ID, UpdateInput{Instructions: &instructions}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("instructions for the primary: error = %v, want ErrInvalid", err)
	}
	specialist, err := service.Create(context.Background(), scope, CreateInput{Name: "Mail", Instructions: "Triage mail."})
	if err != nil {
		t.Fatal(err)
	}
	if updated, err := service.Update(context.Background(), scope, specialist.ID, UpdateInput{Instructions: &instructions}); err != nil || updated.Instructions != instructions {
		t.Fatalf("instructions for a specialist: %+v, %v", updated, err)
	}
}

// TestOnlyASpecialistMovesToAnotherModelTier: an owner may move a specialist
// to main and back; the primary always answers on main, and a tier that is
// neither is refused.
func TestOnlyASpecialistMovesToAnotherModelTier(t *testing.T) {
	repo := newFakeRepository()
	service := &Service{repository: repo}
	scope := Scope{OrganizationID: "org_1", UserID: "user_1"}
	specialist, err := service.Create(context.Background(), scope, CreateInput{Name: "Mail", Instructions: "Triage mail."})
	if err != nil {
		t.Fatal(err)
	}
	main, unknown := modelrouter.Main, modelrouter.Tier("opus")
	if updated, err := service.Update(context.Background(), scope, specialist.ID, UpdateInput{ModelTier: &main}); err != nil || updated.ModelTier != modelrouter.Main {
		t.Fatalf("move to main: %+v, %v", updated, err)
	}
	if _, err := service.Update(context.Background(), scope, specialist.ID, UpdateInput{ModelTier: &unknown}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown tier: error = %v, want ErrInvalid", err)
	}
	if _, err := service.Update(context.Background(), scope, repo.primary.ID, UpdateInput{ModelTier: &main}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tier for the primary: error = %v, want ErrInvalid", err)
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
	want := runprofile.Grant{Apps: []string{"gmail", "*"}, Tools: []string{"GMAIL_FETCH_EMAILS"}, WriteApps: []string{"gmail"}, WriteTools: []string{}, Corpora: []string{"*"}, Files: []string{"*"}, Knowledge: []string{"*"}}
	if !reflect.DeepEqual(*updated.Grant, want) {
		t.Fatalf("stored grant = %+v, want %+v", *updated.Grant, want)
	}

	for name, grant := range map[string]runprofile.Grant{
		"a write to every app":    {Apps: []string{"*"}, WriteApps: []string{"*"}},
		"a tool apps cannot name": {Apps: []string{"gmail"}, Tools: []string{"send email!"}},
		"an app apps cannot name": {Apps: []string{"g mail"}},
		"a bad corpus id":         {Apps: []string{"*"}, Corpora: []string{"nope"}},
		"all mixed with an id":    {Apps: []string{"*"}, Corpora: []string{"*", "cor_abc"}},
		"a bad file scope":        {Apps: []string{"*"}, Files: []string{"teams"}},
		"all files mixed":         {Apps: []string{"*"}, Files: []string{"member", "*"}},
		"a bad source id":         {Apps: []string{"*"}, Knowledge: []string{"cor_abc"}},
		"all sources mixed":       {Apps: []string{"*"}, Knowledge: []string{"*", "src_abc"}},
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
		{ID: "agt_sales", Kind: KindSpecialist, Grant: &grant}: `{"apps":["gmail"],"tools":null,"write_apps":null,"write_tools":["GMAIL_SEND_EMAIL"],"corpora":null,"files":null,"knowledge":null}`,
		{ID: "agt_old", Kind: KindSpecialist}:                  `{"apps":["*"],"tools":[],"write_apps":[],"write_tools":[],"corpora":["*"],"files":["*"],"knowledge":["*"]}`,
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

// A specialist's conversation is renamed in its own engine, by the rules and
// within the ownership boundary of the primary agent's threads.
func TestASpecialistsConversationIsRenamedInItsOwnEngine(t *testing.T) {
	repo := newFakeRepository()
	prices, err := pricing.NewTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.InMemoryService()
	service := &Service{
		repository: repo,
		factory:    func(Definition) (agent.Agent, error) { return namedAgent(t, "managed_agt_primary"), nil },
		sessions:   sessions, runs: noRuns{}, prices: prices,
	}
	ctx := context.Background()
	member := threads.Member{OrganizationID: "org_1", UserID: "user_1"}
	engine, err := service.engine(repo.primary, UnattendedReads)
	if err != nil {
		t.Fatal(err)
	}
	created, err := engine.Create(ctx, member)
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := service.RenameThread(ctx, member, repo.primary.ID, created.ID, " Q3 review ")
	if err != nil || renamed.Title != "Q3 review" {
		t.Fatalf("RenameThread() = %+v, %v", renamed, err)
	}
	listed, err := service.ListThreads(ctx, member, repo.primary.ID)
	if err != nil || len(listed) != 1 || listed[0].Title != "Q3 review" {
		t.Fatalf("ListThreads() = %+v, %v", listed, err)
	}
	// Another member does not find the specialist, so never reaches its
	// conversations.
	someoneElse := threads.Member{OrganizationID: "org_1", UserID: "user_2"}
	if _, err := service.RenameThread(ctx, someoneElse, repo.primary.ID, created.ID, "Mine"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another member's rename = %v, want ErrNotFound", err)
	}
	// A conversation the member does not own is not found in the engine, even
	// when the specialist is theirs.
	stranger := threads.Member{OrganizationID: "org_1", UserID: "user_2"}
	strangers, err := engine.Create(ctx, stranger)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RenameThread(ctx, member, repo.primary.ID, strangers.ID, "Mine"); !errors.Is(err, threads.ErrNotFound) {
		t.Fatalf("a rename of another member's conversation = %v, want threads.ErrNotFound", err)
	}
	if _, err := service.RenameThread(ctx, member, repo.primary.ID, created.ID, " "); !errors.Is(err, threads.ErrInvalid) {
		t.Fatalf("a blank title = %v, want ErrInvalid", err)
	}
}

// TestAGrantsCorporaDefaultToAllAndEmptyMeansNone: a grant sent without
// corpora (or stored before they existed) reads every corpus; an explicit
// empty list is none; ids are kept, without duplicates.
func TestAGrantsCorporaDefaultToAllAndEmptyMeansNone(t *testing.T) {
	if got := runprofile.DefaultGrant().Corpora; !slices.Equal(got, []string{"*"}) {
		t.Fatalf("default corpora = %v, want all", got)
	}
	var stored runprofile.Grant = runprofile.DefaultGrant()
	if err := json.Unmarshal([]byte(`{"apps":["gmail"]}`), &stored); err != nil || !slices.Equal(stored.Corpora, []string{"*"}) {
		t.Fatalf("a stored grant without corpora = %v (%v), want all", stored.Corpora, err)
	}
	for in, want := range map[string][]string{
		`{"apps":["gmail"]}`:                                     {"*"},
		`{"apps":["gmail"],"corpora":[]}`:                        {},
		`{"apps":["gmail"],"corpora":["cor_a","cor_a","cor_b"]}`: {"cor_a", "cor_b"},
	} {
		var grant runprofile.Grant
		if err := json.Unmarshal([]byte(in), &grant); err != nil {
			t.Fatal(err)
		}
		cleaned, err := cleanGrant(grant)
		if err != nil || !slices.Equal(cleaned.Corpora, want) || cleaned.Corpora == nil {
			t.Errorf("%s: corpora = %#v (%v), want %#v", in, cleaned.Corpora, err, want)
		}
	}
}

// TestAGrantsFilesAndKnowledgeDefaultToAllAndEmptyMeansNone: like corpora, a
// grant sent without files or knowledge (or stored before they existed)
// reaches all of them; an explicit empty list is none; scopes and ids are
// kept, trimmed, once each; anything files or knowledge could not match is
// refused.
func TestAGrantsFilesAndKnowledgeDefaultToAllAndEmptyMeansNone(t *testing.T) {
	defaults := runprofile.DefaultGrant()
	if !slices.Equal(defaults.Files, []string{"*"}) || !slices.Equal(defaults.Knowledge, []string{"*"}) {
		t.Fatalf("default files, knowledge = %v, %v, want all", defaults.Files, defaults.Knowledge)
	}
	stored := runprofile.DefaultGrant()
	if err := json.Unmarshal([]byte(`{"apps":["gmail"],"corpora":["cor_a"]}`), &stored); err != nil ||
		!slices.Equal(stored.Files, []string{"*"}) || !slices.Equal(stored.Knowledge, []string{"*"}) {
		t.Fatalf("a stored grant without files or knowledge = %v, %v (%v), want all", stored.Files, stored.Knowledge, err)
	}
	for in, want := range map[string][2][]string{
		`{"apps":["gmail"]}`:                                 {{"*"}, {"*"}},
		`{"apps":["gmail"],"files":null}`:                    {{"*"}, {"*"}},
		`{"apps":["gmail"],"files":[],"knowledge":[]}`:       {{}, {}},
		`{"apps":["gmail"],"files":["*"],"knowledge":["*"]}`: {{"*"}, {"*"}},
		`{"apps":["gmail"],"files":[" member ","org","team_abc","member"],"knowledge":["src_abc","src_abc","src_xyz2"]}`: {
			{"member", "org", "team_abc"}, {"src_abc", "src_xyz2"}},
	} {
		var grant runprofile.Grant
		if err := json.Unmarshal([]byte(in), &grant); err != nil {
			t.Fatal(err)
		}
		cleaned, err := cleanGrant(grant)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if !slices.Equal(cleaned.Files, want[0]) || cleaned.Files == nil {
			t.Errorf("%s: files = %#v, want %#v", in, cleaned.Files, want[0])
		}
		if !slices.Equal(cleaned.Knowledge, want[1]) || cleaned.Knowledge == nil {
			t.Errorf("%s: knowledge = %#v, want %#v", in, cleaned.Knowledge, want[1])
		}
	}
	for name, grant := range map[string]runprofile.Grant{
		"a scope files has not":        {Files: []string{"organization"}},
		"a team id of the wrong shape": {Files: []string{"team-abc"}},
		"a blank file scope":           {Files: []string{" "}},
		"all with a scope":             {Files: []string{"*", "org"}},
		"a corpus id as a source":      {Knowledge: []string{"cor_abc"}},
		"an uppercase source id":       {Knowledge: []string{"src_ABC"}},
		"all with a source":            {Knowledge: []string{"src_abc", "*"}},
		"too many sources":             {Knowledge: slices.Repeat([]string{"src_abc"}, maxGrantEntries+1)},
	} {
		if _, err := cleanGrant(grant); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: error = %v, want invalid", name, err)
		}
	}
}
