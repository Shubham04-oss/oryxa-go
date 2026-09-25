package interactions

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
)

// tokenDirectory is teammateDirectory, recording the tokens it is loaded
// with.
type tokenDirectory struct {
	fakeDirectory
	tokens []string
}

func (directory *tokenDirectory) Load(ctx context.Context, organizationID, token string) (agents.OrganizationDirectory, error) {
	directory.tokens = append(directory.tokens, token)
	return directory.fakeDirectory.Load(ctx, organizationID, token)
}

// delegatedRequest is a request A made of B's specialist agt_auto in tsp_1
// while B let it decide for itself there.
func delegatedRequest(t *testing.T, service *Service) agents.Interaction {
	t.Helper()
	created := createInTeamspace(t, service, "agt_auto")
	if created.Approval != ApprovalAgent {
		t.Fatalf("created=%+v, want the specialist to decide it", created)
	}
	return created
}

// TestAnAgentDecidingReadsTheTeamspaceAgain: the seat a request was made
// from is read again, live and as the addressed member, before an agent
// decides it. Deciding stays the agent's only while its owner still leaves
// it so; a request its owner took back, or one that cannot be read again for
// want of a token of the member's, is held for them; one that no longer
// stands is closed by Oryxa, saying why; and failing to read it changes
// nothing.
func TestAnAgentDecidingReadsTheTeamspaceAgain(t *testing.T) {
	for _, testCase := range []struct {
		name string
		// change is what happened since the request was made; noSource is
		// a server that cannot mint agent tokens at all.
		change   func(*fakeTeamspaces, *fakeTokens)
		noSource bool
		want     func(*testing.T, error, agents.Interaction, *fakeRepository)
	}{
		{"still delegated", nil, false, func(t *testing.T, err error, stored agents.Interaction, _ *fakeRepository) {
			if err != nil || stored.Status != StatusApproved || stored.Decision.DecidedBy.Kind != DecidedByAgent {
				t.Fatalf("err=%v stored=%+v, want the specialist's approval recorded", err, stored)
			}
		}},
		{"its owner took deciding back", func(spaces *fakeTeamspaces, _ *fakeTokens) {
			spaces.seats["agt_auto"] = agents.TeamspaceSeat{AgentID: "agt_auto", OwnerUserID: "user_b", Kind: "specialist", DelegationMode: "request"}
		}, false, heldForTheOwner},
		{"the member never allowed agents to act for them", func(_ *fakeTeamspaces, tokens *fakeTokens) {
			tokens.err = memberauth.ErrGrantRequired
		}, false, heldForTheOwner},
		{"this server mints no tokens", nil, true, heldForTheOwner},
		{"the member who asked left the team", func(spaces *fakeTeamspaces, _ *fakeTokens) {
			spaces.lapsed = "the member who asked is no longer on the teamspace's team"
		}, false, closedBecauseOf("the member who asked is no longer on the teamspace's team")},
		{"the teamspace was disabled", func(spaces *fakeTeamspaces, _ *fakeTokens) {
			spaces.lapsed = "the teamspace it was made in, or its organization agent, is disabled"
		}, false, closedBecauseOf("the teamspace it was made in, or its organization agent, is disabled")},
		{"the specialist lost its seat", func(spaces *fakeTeamspaces, _ *fakeTokens) {
			delete(spaces.seats, "agt_auto")
		}, false, closedBecauseOf("the agent it asks no longer holds a seat in the teamspace")},
		{"auth did not answer", func(_ *fakeTeamspaces, tokens *fakeTokens) {
			tokens.err = memberauth.ErrUnavailable
		}, false, func(t *testing.T, err error, stored agents.Interaction, repo *fakeRepository) {
			if !errors.Is(err, memberauth.ErrUnavailable) || stored.Status != StatusPending || stored.Approval != ApprovalAgent || repo.holds != 0 {
				t.Fatalf("err=%v stored=%+v holds=%d, want the error and nothing changed", err, stored, repo.holds)
			}
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			spaces, tokens := teamspaceSeats(), &fakeTokens{}
			var source memberauth.TokenSource = tokens
			if testCase.noSource {
				source = nil
			}
			service, repo := teamspaceServiceWith(t, spaces, source)
			created := delegatedRequest(t, service)
			if testCase.change != nil {
				testCase.change(spaces, tokens)
			}
			_, err := service.Decide(context.Background(), autoSpecialist, created.ID, approve)
			testCase.want(t, err, repo.requests[created.ID], repo)
		})
	}
}

func heldForTheOwner(t *testing.T, err error, stored agents.Interaction, repo *fakeRepository) {
	t.Helper()
	if !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "your member's own decision") {
		t.Fatalf("err=%v, want the agent told the request is held for its member", err)
	}
	if stored.Status != StatusPending || stored.Approval != ApprovalOwner || stored.Decision != nil || repo.holds != 1 {
		t.Fatalf("stored=%+v holds=%d, want it pending, held for its owner", stored, repo.holds)
	}
}

func closedBecauseOf(reason string) func(*testing.T, error, agents.Interaction, *fakeRepository) {
	return func(t *testing.T, err error, stored agents.Interaction, _ *fakeRepository) {
		t.Helper()
		if !errors.Is(err, ErrClosed) || !strings.Contains(err.Error(), reason) {
			t.Fatalf("err=%v, want ErrClosed saying %q", err, reason)
		}
		if stored.Status != StatusRejected || stored.Decision == nil || stored.Decision.DecidedBy == nil ||
			stored.Decision.DecidedBy.Kind != DecidedBySystem || !strings.Contains(stored.Decision.Message, reason) {
			t.Fatalf("stored=%+v, want it rejected by Oryxa, saying %q", stored, reason)
		}
	}
}

// TestAnAgentDecidingReadsTheTeamspaceAsItsMember: the teamspace is read as
// the addressed member sees it — with their own token when they are here,
// watching the run, and otherwise with an agent token minted for them —
// and for the request's own parties.
func TestAnAgentDecidingReadsTheTeamspaceAsItsMember(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"agent-token-user_b": context.Background(),
		"own-token":          memberauth.WithAccessToken(context.Background(), "own-token"),
	} {
		spaces, tokens := teamspaceSeats(), &fakeTokens{}
		directory := &tokenDirectory{fakeDirectory: fakeDirectory{directory: teammateDirectory}}
		repo := newFakeRepository()
		service, err := New(Config{Repository: repo, Directory: directory, Teamspaces: spaces, Tokens: tokens, Now: func() time.Time { return testNow }})
		if err != nil {
			t.Fatal(err)
		}
		created := delegatedRequest(t, service)
		if _, err := service.Decide(ctx, autoSpecialist, created.ID, approve); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if last := directory.tokens[len(directory.tokens)-1]; last != name {
			t.Fatalf("the teamspace was read with %q, want %q", last, name)
		}
		read := spaces.standings[len(spaces.standings)-1]
		if read.organizationID != "org_1" || read.teamspaceID != "tsp_1" || read.requester.UserID != "user_a" || read.requester.AgentID != "agt_a" ||
			read.target.UserID != "user_b" || read.target.AgentID != "agt_auto" {
			t.Fatalf("read %+v, want the request's own teamspace and parties", read)
		}
	}
}

// TestTheOwnerApprovingReadsTheTeamspaceAgain: the member's own decision
// works whatever standing they gave their agent, as long as the request
// still stands in its teamspace, read with their own token; approving one
// that no longer stands closes it instead, while rejecting it is always
// theirs to do.
func TestTheOwnerApprovingReadsTheTeamspaceAgain(t *testing.T) {
	spaces := teamspaceSeats()
	directory := &tokenDirectory{fakeDirectory: fakeDirectory{directory: teammateDirectory}}
	repo := newFakeRepository()
	service, err := New(Config{Repository: repo, Directory: directory, Teamspaces: spaces, Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	owner := inOwnSession(targetPrimary)
	for _, target := range []string{"agt_b", "agt_ask", "agt_auto"} {
		created := createInTeamspace(t, service, target)
		if decided, err := service.DecideAsOwner(context.Background(), owner, "member-token", created.ID, approve); err != nil || decided.Status != StatusApproved {
			t.Fatalf("approving the request to %s = %+v, %v", target, decided, err)
		}
		if last := directory.tokens[len(directory.tokens)-1]; last != "member-token" {
			t.Fatalf("the teamspace was read with %q, want the member's own token", last)
		}
	}

	spaces.lapsed = "the member who asked is no longer on the teamspace's team"
	lapsed := recordedBefore(repo, "agt_b")
	if _, err := service.DecideAsOwner(context.Background(), owner, "", lapsed.ID, approve); !errors.Is(err, ErrInvalid) {
		t.Fatalf("approving without the member's token: error=%v, want ErrInvalid", err)
	}
	_, err = service.DecideAsOwner(context.Background(), owner, "member-token", lapsed.ID, approve)
	closedBecauseOf("the member who asked is no longer on the teamspace's team")(t, err, repo.requests[lapsed.ID], repo)

	declined := recordedBefore(repo, "agt_ask")
	reads := len(spaces.standings)
	rejected, err := service.DecideAsOwner(context.Background(), owner, "member-token", declined.ID, agents.InteractionDecisionInput{Decision: DecisionRejected, Message: "No."})
	if err != nil || rejected.Status != StatusRejected || rejected.Decision.DecidedBy.Kind != DecidedByMember || len(spaces.standings) != reads {
		t.Fatalf("rejecting = %+v, %v after %d reads, want the member's own rejection, read nowhere", rejected, err, len(spaces.standings)-reads)
	}
}

// recordedBefore records a held request to target in tsp_1 straight into
// the ledger, as if made before the teamspace changed.
func recordedBefore(repo *fakeRepository, target string) agents.Interaction {
	request := between("irq_"+target+"_before", "user_a", "agt_a", "user_b", target, "tsp_1")
	request.Revision = 1
	repo.requests[request.ID] = request
	return request
}

// TestTheWakerReadsARequestAgainBeforeWakingItsAgent: what Recheck finds for
// each request the waker may wake an agent for.
func TestTheWakerReadsARequestAgainBeforeWakingItsAgent(t *testing.T) {
	approvedBy := func(request agents.Interaction, decider *agents.InteractionDecider) agents.Interaction {
		request.Status = StatusApproved
		request.Decision = &agents.InteractionDecision{Decision: DecisionApproved, Message: "Go.", DecidedAt: testNow, DecidedBy: decider}
		return request
	}
	byMember := &agents.InteractionDecider{Kind: DecidedByMember, UserID: "user_b"}
	byAgent := &agents.InteractionDecider{Kind: DecidedByAgent, UserID: "user_b", AgentID: "agt_auto"}
	delegated := between("irq_auto", "user_a", "agt_a", "user_b", "agt_auto", "tsp_1")
	delegated.Approval = ApprovalAgent
	held := between("irq_held", "user_a", "agt_a", "user_b", "agt_b", "tsp_1")
	outside := between("irq_outside", "user_a", "agt_a", "user_b", "agt_b", "")
	outside.Approval = ApprovalAgent
	takenBack := func(spaces *fakeTeamspaces, _ *fakeTokens) {
		spaces.seats["agt_auto"] = agents.TeamspaceSeat{AgentID: "agt_auto", OwnerUserID: "user_b", Kind: "specialist", DelegationMode: "request"}
	}
	noGrant := func(_ *fakeTeamspaces, tokens *fakeTokens) { tokens.err = memberauth.ErrGrantRequired }
	lapsed := func(spaces *fakeTeamspaces, _ *fakeTokens) {
		spaces.lapsed = "the teamspace it was made in no longer exists"
	}
	for _, testCase := range []struct {
		name     string
		request  agents.Interaction
		change   func(*fakeTeamspaces, *fakeTokens)
		verdict  Verdict
		status   string
		approval string
		read     bool
	}{
		{"pending, still delegated", delegated, nil, VerdictStands, StatusPending, ApprovalAgent, true},
		{"pending, deciding taken back", delegated, takenBack, VerdictHeld, StatusPending, ApprovalOwner, true},
		{"pending, no grant to read it with", delegated, noGrant, VerdictHeld, StatusPending, ApprovalOwner, false},
		{"pending, held already", held, nil, VerdictHeld, StatusPending, ApprovalOwner, true},
		{"pending, held, no grant", held, noGrant, VerdictHeld, StatusPending, ApprovalOwner, false},
		{"pending, no longer standing", held, lapsed, VerdictClosed, StatusRejected, ApprovalOwner, true},
		{"approved by its member", approvedBy(held, byMember), nil, VerdictStands, StatusApproved, ApprovalOwner, true},
		{"approved by its member, no grant", approvedBy(held, byMember), noGrant, VerdictStands, StatusApproved, ApprovalOwner, false},
		{"approved by the delegated agent", approvedBy(delegated, byAgent), nil, VerdictStands, StatusApproved, ApprovalAgent, true},
		{"approved by the agent, deciding taken back", approvedBy(delegated, byAgent), takenBack, VerdictHeld, StatusPending, ApprovalOwner, true},
		{"approved by the agent, no grant", approvedBy(delegated, byAgent), noGrant, VerdictStands, StatusApproved, ApprovalAgent, false},
		{"held, approved by no recorded decider", approvedBy(held, nil), nil, VerdictHeld, StatusPending, ApprovalOwner, true},
		{"held, approved by an agent", approvedBy(held, byAgent), noGrant, VerdictHeld, StatusPending, ApprovalOwner, false},
		{"approved, no longer standing", approvedBy(held, byMember), lapsed, VerdictClosed, StatusFailed, ApprovalOwner, true},
		{"outside any teamspace", outside, lapsed, VerdictStands, StatusPending, ApprovalAgent, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			spaces, tokens := teamspaceSeats(), &fakeTokens{}
			if testCase.change != nil {
				testCase.change(spaces, tokens)
			}
			service, repo := teamspaceServiceWith(t, spaces, tokens)
			repo.requests[testCase.request.ID] = testCase.request
			rechecked, verdict, err := service.Recheck(context.Background(), testCase.request)
			if err != nil || verdict != testCase.verdict {
				t.Fatalf("Recheck = %s, %v; want %s", verdict, err, testCase.verdict)
			}
			stored := repo.requests[testCase.request.ID]
			if stored.Status != testCase.status || stored.Approval != testCase.approval || rechecked.Status != stored.Status {
				t.Fatalf("stored=%+v returned=%+v, want %s held by %s", stored, rechecked, testCase.status, testCase.approval)
			}
			if stored.Status == StatusPending && stored.Decision != nil {
				t.Fatalf("a pending request kept a decision: %+v", stored.Decision)
			}
			if read := len(spaces.standings) == 1; read != testCase.read {
				t.Fatalf("read the teamspace %d times, want read=%t", len(spaces.standings), testCase.read)
			}
		})
	}
}

// TestAWakerRecheckThatCannotReadChangesNothing: failing to read the
// teamspace, or to be told by auth whether there is a grant, is an error to
// retry, not a verdict.
func TestAWakerRecheckThatCannotReadChangesNothing(t *testing.T) {
	for name, change := range map[string]func(*fakeTeamspaces, *fakeTokens){
		"auth did not answer":        func(_ *fakeTeamspaces, tokens *fakeTokens) { tokens.err = memberauth.ErrUnavailable },
		"the teamspace did not read": func(spaces *fakeTeamspaces, _ *fakeTokens) { spaces.standingErr = errors.New("database is down") },
	} {
		spaces, tokens := teamspaceSeats(), &fakeTokens{}
		change(spaces, tokens)
		service, repo := teamspaceServiceWith(t, spaces, tokens)
		request := between("irq_auto", "user_a", "agt_a", "user_b", "agt_auto", "tsp_1")
		request.Approval = ApprovalAgent
		repo.requests[request.ID] = request
		if _, verdict, err := service.Recheck(context.Background(), request); err == nil || verdict != "" {
			t.Fatalf("%s: Recheck = %q, %v; want an error", name, verdict, err)
		}
		if stored := repo.requests[request.ID]; stored.Approval != ApprovalAgent || stored.Status != StatusPending || repo.holds != 0 {
			t.Fatalf("%s: stored=%+v, want nothing changed", name, stored)
		}
	}
}
