package teamspaces

import (
	"context"
	"errors"
	"testing"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
)

// seatDirectory is Ada's view of the organization: Ada and Grace are on
// team_1, Hal is not. Ada is on it only when adaOnTeam; either way she may
// manage it, so the space stays visible to her.
func seatDirectory(adaOnTeam bool) primaryagents.OrganizationDirectory {
	ada := primaryagents.DirectoryPerson{
		UserID: "user_1", DisplayName: "Ada", OrgRole: "member", Teams: []primaryagents.DirectoryTeamRef{},
		Agents: []primaryagents.DirectoryAgent{{ID: "agt_ada", OwnerUserID: "user_1", Kind: "primary", Name: "Carl"}},
	}
	if adaOnTeam {
		ada.Teams = append(ada.Teams, primaryagents.DirectoryTeamRef{ID: "team_1", Name: "Engineering"})
	}
	return primaryagents.OrganizationDirectory{
		Organization: primaryagents.DirectoryOrganization{ID: "org_1", Name: "Oryxa"},
		Teams:        []primaryagents.DirectoryTeam{{ID: "team_1", Name: "Engineering", CanManage: true}},
		People: []primaryagents.DirectoryPerson{ada, {
			UserID: "user_2", DisplayName: "Grace", OrgRole: "member",
			Teams: []primaryagents.DirectoryTeamRef{{ID: "team_1", Name: "Engineering"}},
			Agents: []primaryagents.DirectoryAgent{
				{ID: "agt_grace", OwnerUserID: "user_2", Kind: "primary", Name: "Cleo"},
				{ID: "agt_auto", OwnerUserID: "user_2", Kind: "specialist", Name: "Researcher"},
				{ID: "agt_ask", OwnerUserID: "user_2", Kind: "specialist", Name: "Counsel"},
				{ID: "agt_private", OwnerUserID: "user_2", Kind: "specialist", Name: "Private Specialist"},
			},
		}, {
			UserID: "user_3", DisplayName: "Hal", OrgRole: "member", Teams: []primaryagents.DirectoryTeamRef{},
			Agents: []primaryagents.DirectoryAgent{
				{ID: "agt_hal", OwnerUserID: "user_3", Kind: "primary", Name: "Hank"},
				{ID: "agt_left", OwnerUserID: "user_3", Kind: "specialist", Name: "Leftover"},
			},
		}},
	}
}

// seatSpace is team_1's space. Grace contributed one specialist with each
// delegation mode and one that is now disabled; Hal contributed one before he
// left the team.
func seatSpace() *fakeRepository {
	return &fakeRepository{spaces: []Space{{
		ID: "tsp_1", OrganizationID: "org_1", TeamID: "team_1", Name: "Engineering", Status: StatusActive,
		OrganizationAgent: OrganizationAgent{ID: "oag_1", OrganizationID: "org_1", Name: "Oryxa Agent", Instructions: "Coordinate.", Status: StatusActive},
		Contributions: []Contribution{
			{AgentID: "agt_auto", OwnerUserID: "user_2", Name: "Researcher", Status: managedagents.StatusActive, Role: "Research", DelegationMode: DelegationAutomatic},
			{AgentID: "agt_ask", OwnerUserID: "user_2", Name: "Counsel", Status: managedagents.StatusActive, Role: "Legal review", DelegationMode: DelegationRequest},
			{AgentID: "agt_off", OwnerUserID: "user_2", Name: "Dormant", Status: managedagents.StatusDisabled, Role: "Idle", DelegationMode: DelegationAutomatic},
			{AgentID: "agt_left", OwnerUserID: "user_3", Name: "Leftover", Status: managedagents.StatusActive, Role: "Old work", DelegationMode: DelegationAutomatic},
		},
	}}}
}

var ada = primaryagents.TeamspaceActor{OrganizationID: "org_1", UserID: "user_1", AgentID: "agt_ada", AgentName: "Carl", AgentKind: "primary"}

func TestRequestSeatPlacesATeammatesPrimaryAndTheirContributions(t *testing.T) {
	service, err := New(seatSpace(), fakeDirectory{directory: seatDirectory(true)})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []primaryagents.TeamspaceSeat{
		{AgentID: "agt_grace", OwnerUserID: "user_2", Kind: "primary"},
		{AgentID: "agt_auto", OwnerUserID: "user_2", Kind: "specialist", DelegationMode: "automatic"},
		{AgentID: "agt_ask", OwnerUserID: "user_2", Kind: "specialist", DelegationMode: "request"},
	} {
		seat, err := service.RequestSeat(context.Background(), ada, "token", "tsp_1", " "+want.AgentID+" ")
		if err != nil || seat != want {
			t.Fatalf("RequestSeat(%s) = %+v, %v; want %+v", want.AgentID, seat, err, want)
		}
	}
}

// TestRequestSeatRefusesARequesterOffTheTeam: managing a team does not put
// Ada on it, and a space she cannot see at all stays hidden.
func TestRequestSeatRefusesARequesterOffTheTeam(t *testing.T) {
	service, _ := New(seatSpace(), fakeDirectory{directory: seatDirectory(false)})
	if _, err := service.RequestSeat(context.Background(), ada, "token", "tsp_1", "agt_grace"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a manager off the team: error=%v, want ErrForbidden", err)
	}
	hidden := seatDirectory(false)
	hidden.Teams[0].CanManage = false
	service, _ = New(seatSpace(), fakeDirectory{directory: hidden})
	if _, err := service.RequestSeat(context.Background(), ada, "token", "tsp_1", "agt_grace"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a stranger to the team: error=%v, want ErrNotFound", err)
	}
	service, _ = New(seatSpace(), fakeDirectory{directory: seatDirectory(true)})
	if _, err := service.RequestSeat(context.Background(), ada, "token", "tsp_missing", "agt_grace"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing space: error=%v, want ErrNotFound", err)
	}
}

// TestRequestSeatRefusesATargetWithoutASeat: someone off the team, a
// teammate's specialist they never contributed, a disabled contribution, and
// one whose owner has left the team hold no seat.
func TestRequestSeatRefusesATargetWithoutASeat(t *testing.T) {
	service, _ := New(seatSpace(), fakeDirectory{directory: seatDirectory(true)})
	for _, target := range []string{"agt_hal", "agt_private", "agt_off", "agt_left", "agt_unknown", ""} {
		if seat, err := service.RequestSeat(context.Background(), ada, "token", "tsp_1", target); !errors.Is(err, ErrInvalid) {
			t.Fatalf("RequestSeat(%q) = %+v, %v; want ErrInvalid", target, seat, err)
		}
	}
}

func TestRequestSeatRefusesADisabledSpace(t *testing.T) {
	for name, disable := range map[string]func(*Space){
		"disabled teamspace":          func(space *Space) { space.Status = StatusDisabled },
		"disabled organization agent": func(space *Space) { space.OrganizationAgent.Status = StatusDisabled },
	} {
		t.Run(name, func(t *testing.T) {
			repo := seatSpace()
			disable(&repo.spaces[0])
			service, _ := New(repo, fakeDirectory{directory: seatDirectory(true)})
			if _, err := service.RequestSeat(context.Background(), ada, "token", "tsp_1", "agt_grace"); !errors.Is(err, ErrDisabled) {
				t.Fatalf("error=%v, want ErrDisabled", err)
			}
		})
	}
}

// requestLedger keeps what interactions.Service creates. Create is all a new
// request reaches; the embedded nil Repository fails loudly on anything else.
type requestLedger struct {
	interactions.Repository
	created []primaryagents.Interaction
}

func (ledger *requestLedger) Create(_ context.Context, request primaryagents.Interaction, _ string) (primaryagents.Interaction, error) {
	ledger.created = append(ledger.created, request)
	return request, nil
}

// organizationDirectory is fakeDirectory as the request ledger reads it: a
// new request loads the directory and looks nothing up.
type organizationDirectory struct{ fakeDirectory }

func (organizationDirectory) FindPeople(context.Context, string, string, string, int) ([]primaryagents.DirectoryPerson, error) {
	return nil, errors.New("not used")
}

func (organizationDirectory) FindTeams(context.Context, string, string, string, int) ([]primaryagents.DirectoryTeam, error) {
	return nil, errors.New("not used")
}

func (organizationDirectory) FindAgents(context.Context, string, string, string, int) ([]primaryagents.DirectoryAgent, error) {
	return nil, errors.New("not used")
}

// TestATeamspaceRequestIsApprovedAsTheTargetsOwnerChose runs the request
// ledger over this package's seat lookup, as the service wires them: who
// approves follows what the target's owner chose in the teamspace, and a
// requester off the team or a target without a seat is refused.
func TestATeamspaceRequestIsApprovedAsTheTargetsOwnerChose(t *testing.T) {
	requestsFor := func(t *testing.T, directory primaryagents.OrganizationDirectory) (*interactions.Service, *requestLedger) {
		t.Helper()
		spaces, err := New(seatSpace(), fakeDirectory{directory: directory})
		if err != nil {
			t.Fatal(err)
		}
		ledger := &requestLedger{}
		requests, err := interactions.New(interactions.Config{
			Repository: ledger, Directory: organizationDirectory{fakeDirectory{directory: directory}}, Teamspaces: spaces,
		})
		if err != nil {
			t.Fatal(err)
		}
		return requests, ledger
	}
	requester := primaryagents.InteractionActor{OrganizationID: "org_1", UserID: "user_1", AgentID: "agt_ada", AgentName: "Carl", AgentKind: "primary"}
	ask := func(target, teamspaceID string) primaryagents.InteractionCreateInput {
		return primaryagents.InteractionCreateInput{TargetAgentID: target, TeamspaceID: teamspaceID, Purpose: "Launch", Task: "Review the launch plan."}
	}

	requests, ledger := requestsFor(t, seatDirectory(true))
	for _, testCase := range []struct{ target, teamspaceID, approval string }{
		{"agt_grace", "tsp_1", interactions.ApprovalOwner},
		{"agt_auto", "tsp_1", interactions.ApprovalAgent},
		{"agt_ask", "tsp_1", interactions.ApprovalOwner},
		{"agt_grace", "", interactions.ApprovalAgent},
	} {
		created, err := requests.Create(context.Background(), requester, "token", testCase.target+testCase.teamspaceID, ask(testCase.target, testCase.teamspaceID))
		if err != nil || created.Approval != testCase.approval || created.TeamspaceID != testCase.teamspaceID {
			t.Fatalf("asking %s in %q = %+v, %v; want approval %q", testCase.target, testCase.teamspaceID, created, err, testCase.approval)
		}
	}
	if len(ledger.created) != 4 {
		t.Fatalf("recorded %d requests, want 4", len(ledger.created))
	}

	for _, target := range []string{"agt_hal", "agt_private", "agt_left"} {
		if _, err := requests.Create(context.Background(), requester, "token", "refused-"+target, ask(target, "tsp_1")); !errors.Is(err, ErrInvalid) {
			t.Fatalf("asking %s, who has no seat, error=%v, want ErrInvalid", target, err)
		}
	}
	offTeam, offTeamLedger := requestsFor(t, seatDirectory(false))
	if _, err := offTeam.Create(context.Background(), requester, "token", "off-team", ask("agt_grace", "tsp_1")); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a requester off the team: error=%v, want ErrForbidden", err)
	}
	if len(ledger.created) != 4 || len(offTeamLedger.created) != 0 {
		t.Fatalf("refused requests were recorded: %d and %d", len(ledger.created)-4, len(offTeamLedger.created))
	}
}
