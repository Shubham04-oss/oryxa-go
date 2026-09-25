package interactions

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/teamspaces"
)

// fakeTeamspaces answers RequestSeat and RequestStanding from a fixed
// seating plan, the way teamspaces.Service would after checking the live
// team directory, and records what it was asked.
type fakeTeamspaces struct {
	seats map[string]agents.TeamspaceSeat
	err   error
	calls []seatCall
	// lapsed, when set, is why every request no longer stands when read
	// again; standingErr is failing to read it at all.
	lapsed      string
	standingErr error
	standings   []standingCall
}

type standingCall struct {
	organizationID, teamspaceID string
	directory                   agents.OrganizationDirectory
	requester, target           agents.InteractionParty
}

func (spaces *fakeTeamspaces) RequestStanding(_ context.Context, organizationID string, directory agents.OrganizationDirectory, teamspaceID string, requester, target agents.InteractionParty) (agents.TeamspaceStanding, error) {
	spaces.standings = append(spaces.standings, standingCall{organizationID: organizationID, teamspaceID: teamspaceID, directory: directory, requester: requester, target: target})
	if spaces.standingErr != nil {
		return agents.TeamspaceStanding{}, spaces.standingErr
	}
	if spaces.lapsed != "" {
		return agents.TeamspaceStanding{Lapsed: spaces.lapsed}, nil
	}
	seat, ok := spaces.seats[target.AgentID]
	if !ok || seat.OwnerUserID != target.UserID {
		return agents.TeamspaceStanding{Lapsed: "the agent it asks no longer holds a seat in the teamspace"}, nil
	}
	return agents.TeamspaceStanding{Seat: seat}, nil
}

// fakeTokens mints every member an agent token, or fails as told: err
// memberauth.ErrGrantRequired is a member who never allowed agents to act
// for them while away.
type fakeTokens struct {
	err   error
	users []string
}

func (tokens *fakeTokens) AgentToken(_ context.Context, userID string) (string, time.Time, error) {
	tokens.users = append(tokens.users, userID)
	if tokens.err != nil {
		return "", time.Time{}, tokens.err
	}
	return "agent-token-" + userID, testNow.Add(time.Hour), nil
}

type seatCall struct {
	requester                  agents.TeamspaceActor
	directory                  agents.OrganizationDirectory
	teamspaceID, targetAgentID string
}

func (spaces *fakeTeamspaces) RequestSeat(_ context.Context, requester agents.TeamspaceActor, directory agents.OrganizationDirectory, teamspaceID, targetAgentID string) (agents.TeamspaceSeat, error) {
	spaces.calls = append(spaces.calls, seatCall{requester: requester, directory: directory, teamspaceID: teamspaceID, targetAgentID: targetAgentID})
	if spaces.err != nil {
		return agents.TeamspaceSeat{}, spaces.err
	}
	seat, ok := spaces.seats[targetAgentID]
	if !ok {
		return agents.TeamspaceSeat{}, teamspaces.ErrInvalid
	}
	return seat, nil
}

// teammateDirectory is what the requester sees: teammate B with a primary
// agent and three specialists, and member C.
var teammateDirectory = agents.OrganizationDirectory{People: []agents.DirectoryPerson{
	{UserID: "user_b", Email: "b@example.com", DisplayName: "B", Agents: []agents.DirectoryAgent{
		{ID: "agt_b", OwnerUserID: "user_b", OwnerEmail: "b@example.com", OwnerDisplayName: "B", Kind: "primary", Name: "Carl"},
		{ID: "agt_auto", OwnerUserID: "user_b", OwnerEmail: "b@example.com", OwnerDisplayName: "B", Kind: "specialist", Name: "Researcher"},
		{ID: "agt_ask", OwnerUserID: "user_b", OwnerEmail: "b@example.com", OwnerDisplayName: "B", Kind: "specialist", Name: "Counsel"},
		{ID: "agt_engineering", OwnerUserID: "user_b", OwnerEmail: "b@example.com", OwnerDisplayName: "B", Kind: "specialist", Name: "Engineering"},
	}},
	{UserID: "user_c", Email: "c@example.com", DisplayName: "C", Agents: []agents.DirectoryAgent{
		{ID: "agt_c", OwnerUserID: "user_c", OwnerEmail: "c@example.com", OwnerDisplayName: "C", Kind: "primary", Name: "Cleo"},
	}},
}}

// teamspaceSeats seats B's primary agent in its whisperer seat and two of
// B's specialists as contributions, one with each delegation mode.
func teamspaceSeats() *fakeTeamspaces {
	return &fakeTeamspaces{seats: map[string]agents.TeamspaceSeat{
		"agt_b":    {AgentID: "agt_b", OwnerUserID: "user_b", Kind: "primary"},
		"agt_auto": {AgentID: "agt_auto", OwnerUserID: "user_b", Kind: "specialist", DelegationMode: "automatic"},
		"agt_ask":  {AgentID: "agt_ask", OwnerUserID: "user_b", Kind: "specialist", DelegationMode: "request"},
	}}
}

// teamspaceService is the ledger over spaces, able to mint every member's
// agent token to read a request's teamspace again.
func teamspaceService(t *testing.T, spaces Teamspaces) (*Service, *fakeRepository) {
	t.Helper()
	return teamspaceServiceWith(t, spaces, &fakeTokens{})
}

func teamspaceServiceWith(t *testing.T, spaces Teamspaces, tokens memberauth.TokenSource) (*Service, *fakeRepository) {
	t.Helper()
	repo := newFakeRepository()
	service, err := New(Config{
		Repository: repo, Directory: fakeDirectory{directory: teammateDirectory}, Teamspaces: spaces, Tokens: tokens,
		Now: func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, repo
}

func askInTeamspace(targetAgentID string) agents.InteractionCreateInput {
	return agents.InteractionCreateInput{
		TargetAgentID: targetAgentID, TeamspaceID: " tsp_1 ", Purpose: "Launch review", Task: "Check the launch checklist.",
	}
}

// TestARequestToATeammatesPrimaryWaitsForItsOwner: B's primary agent sits in
// B's own seat, so only B decides what is asked of it in the teamspace.
func TestARequestToATeammatesPrimaryWaitsForItsOwner(t *testing.T) {
	spaces := teamspaceSeats()
	service, repo := teamspaceService(t, spaces)
	created, err := service.Create(context.Background(), requester, "member-token", "key", askInTeamspace("agt_b"))
	if err != nil {
		t.Fatal(err)
	}
	if created.Approval != ApprovalOwner || created.TeamspaceID != "tsp_1" || created.Status != StatusPending || created.Target.UserID != "user_b" {
		t.Fatalf("created=%+v, want a pending request in tsp_1 held for its owner", created)
	}
	if stored := repo.requests[created.ID]; stored.Approval != ApprovalOwner || stored.TeamspaceID != "tsp_1" {
		t.Fatalf("stored=%+v", stored)
	}
	if len(spaces.calls) != 1 {
		t.Fatalf("seat lookups=%+v, want one", spaces.calls)
	}
	call := spaces.calls[0]
	if call.requester.OrganizationID != "org_1" || call.requester.UserID != "user_a" || call.requester.AgentID != "agt_a" ||
		len(call.directory.People) != len(teammateDirectory.People) || call.teamspaceID != "tsp_1" || call.targetAgentID != "agt_b" {
		t.Fatalf("seat lookup=%+v, want the requester's own identity and directory for tsp_1 and agt_b", call)
	}
}

// TestAContributionsDelegationModeDecidesWhoApproves: the specialist's owner
// chose, when contributing it, whether its requests wait for them.
func TestAContributionsDelegationModeDecidesWhoApproves(t *testing.T) {
	for _, testCase := range []struct {
		target, approval string
	}{
		{"agt_auto", ApprovalAgent},
		{"agt_ask", ApprovalOwner},
	} {
		t.Run(testCase.target, func(t *testing.T) {
			service, _ := teamspaceService(t, teamspaceSeats())
			created, err := service.Create(context.Background(), requester, "member-token", "key", askInTeamspace(testCase.target))
			if err != nil {
				t.Fatal(err)
			}
			if created.Approval != testCase.approval || created.TeamspaceID != "tsp_1" {
				t.Fatalf("created=%+v, want approval %q in tsp_1", created, testCase.approval)
			}
		})
	}
}

// TestTeamspaceRefusalsStopTheRequest: a requester who is not on the team, a
// target without a seat there, or a space that is gone or disabled refuses
// the request as the teamspace said, and nothing is recorded.
func TestTeamspaceRefusalsStopTheRequest(t *testing.T) {
	for _, refusal := range []error{
		teamspaces.ErrForbidden, // the requester is not on the team
		teamspaces.ErrInvalid,   // the target has no seat there
		teamspaces.ErrNotFound,  // the space is gone or hidden from the requester
		teamspaces.ErrDisabled,  // the space or its organization agent is off
	} {
		t.Run(refusal.Error(), func(t *testing.T) {
			spaces := teamspaceSeats()
			spaces.err = refusal
			service, repo := teamspaceService(t, spaces)
			if _, err := service.Create(context.Background(), requester, "member-token", "key", askInTeamspace("agt_b")); !errors.Is(err, refusal) {
				t.Fatalf("Create error=%v, want %v", err, refusal)
			}
			if len(repo.requests) != 0 {
				t.Fatalf("a refused request was recorded: %+v", repo.requests)
			}
		})
	}
}

// TestATargetWithoutASeatIsRefused: the seating plan has no place for C's
// agent or B's uncontributed specialist, and a seat that names another agent
// or owner than the directory's is not the target's.
func TestATargetWithoutASeatIsRefused(t *testing.T) {
	for _, target := range []string{"agt_c", "agt_engineering"} {
		service, repo := teamspaceService(t, teamspaceSeats())
		if _, err := service.Create(context.Background(), requester, "member-token", "key", askInTeamspace(target)); !errors.Is(err, teamspaces.ErrInvalid) {
			t.Fatalf("%s: error=%v, want the teamspace's refusal", target, err)
		}
		if len(repo.requests) != 0 {
			t.Fatalf("%s: a refused request was recorded", target)
		}
	}

	spaces := teamspaceSeats()
	spaces.seats["agt_b"] = agents.TeamspaceSeat{AgentID: "agt_b", OwnerUserID: "user_c", Kind: "primary"}
	service, repo := teamspaceService(t, spaces)
	if _, err := service.Create(context.Background(), requester, "member-token", "key", askInTeamspace("agt_b")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatched seat error=%v, want ErrInvalid", err)
	}
	if len(repo.requests) != 0 {
		t.Fatal("a request with a mismatched seat was recorded")
	}
}

// TestARequestOutsideATeamspaceIsDecidedByItsAgentAsBefore: without a
// teamspace nothing changes — the target's agent decides, and no teamspace is
// consulted, whether or not the service can reach one.
func TestARequestOutsideATeamspaceIsDecidedByItsAgentAsBefore(t *testing.T) {
	spaces := teamspaceSeats()
	withSpaces, _ := teamspaceService(t, spaces)
	withoutSpaces, _ := teamspaceService(t, nil)
	for name, service := range map[string]*Service{"with teamspaces": withSpaces, "without teamspaces": withoutSpaces} {
		created, err := service.Create(context.Background(), requester, "member-token", "key", agents.InteractionCreateInput{
			TargetAgentID: "agt_b", Purpose: "Question", Task: "Answer.",
		})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if created.Approval != ApprovalAgent || created.TeamspaceID != "" {
			t.Fatalf("%s: created=%+v, want an agent-approved request outside any teamspace", name, created)
		}
	}
	if len(spaces.calls) != 0 {
		t.Fatalf("a request outside a teamspace looked up a seat: %+v", spaces.calls)
	}
}

func TestATeamspaceRequestNeedsTeamspacesAndABoundedID(t *testing.T) {
	withoutSpaces, _ := teamspaceService(t, nil)
	if _, err := withoutSpaces.Create(context.Background(), requester, "member-token", "key", askInTeamspace("agt_b")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("teamspace request without teamspaces error=%v, want ErrInvalid", err)
	}
	service, _ := teamspaceService(t, teamspaceSeats())
	for _, id := range []string{strings.Repeat("t", 129), "tsp_\x00"} {
		input := askInTeamspace("agt_b")
		input.TeamspaceID = id
		if _, err := service.Create(context.Background(), requester, "member-token", "key", input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("teamspace_id %q error=%v, want ErrInvalid", id, err)
		}
	}
}
