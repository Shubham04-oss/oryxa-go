package interactions

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
)

// List reads the fake ledger as the store selects: the member's inbox, their
// sent requests, or both. It leaves the teamspace and the other member to
// the service, which restates every narrowing itself.
func (repo *fakeRepository) List(ctx context.Context, organizationID, userID string, query Query) ([]agents.Interaction, error) {
	var found []agents.Interaction
	if query.Box != BoxSent {
		inbox, err := repo.Inbox(ctx, organizationID, userID, query.Limit)
		if err != nil {
			return nil, err
		}
		found = append(found, inbox...)
	}
	if query.Box != BoxInbox {
		sent, err := repo.Sent(ctx, organizationID, userID, query.Limit)
		if err != nil {
			return nil, err
		}
		found = append(found, sent...)
	}
	return found, nil
}

func between(id, requesterUserID, requesterAgentID, targetUserID, targetAgentID, teamspaceID string) agents.Interaction {
	return agents.Interaction{
		ID: id, OrganizationID: "org_1", TeamspaceID: teamspaceID, Approval: ApprovalOwner,
		Requester: agents.InteractionParty{UserID: requesterUserID, AgentID: requesterAgentID},
		Target:    agents.InteractionParty{UserID: targetUserID, AgentID: targetAgentID},
		Status:    StatusPending, ExpiresAt: testNow.Add(time.Hour),
	}
}

// sideConversations seeds what A, B and C asked each other: A and B both
// ways in teamspace tsp_1, A of B's specialist there too, A of B outside any
// teamspace and in tsp_2, and A and B each of C in tsp_1.
func sideConversations(t *testing.T) *Service {
	t.Helper()
	service, repo := testService(t)
	for _, request := range []agents.Interaction{
		between("irq_ab_room", "user_a", "agt_a", "user_b", "agt_b", "tsp_1"),
		between("irq_ba_room", "user_b", "agt_b", "user_a", "agt_a", "tsp_1"),
		between("irq_a_ask_room", "user_a", "agt_a", "user_b", "agt_ask", "tsp_1"),
		between("irq_ab_outside", "user_a", "agt_a", "user_b", "agt_b", ""),
		between("irq_ab_other_room", "user_a", "agt_a", "user_b", "agt_b", "tsp_2"),
		between("irq_ac_room", "user_a", "agt_a", "user_c", "agt_c", "tsp_1"),
		between("irq_bc_room", "user_b", "agt_b", "user_c", "agt_c", "tsp_1"),
	} {
		repo.requests[request.ID] = request
	}
	return service
}

func listedIDs(t *testing.T, service *Service, actor agents.InteractionActor, query Query) []string {
	t.Helper()
	listed, err := service.List(context.Background(), actor, query)
	if err != nil {
		t.Fatalf("List(%+v) as %s: %v", query, actor.AgentID, err)
	}
	ids := make([]string, 0, len(listed))
	for _, request := range listed {
		ids = append(ids, request.ID)
	}
	slices.Sort(ids)
	return ids
}

// TestEitherTeammateReadsTheRequestsBetweenThemInATeamspace: asked as A or
// as B, with the other as the member and the teamspace named, the listing is
// the same requests in both directions — and nobody else's.
func TestEitherTeammateReadsTheRequestsBetweenThemInATeamspace(t *testing.T) {
	service := sideConversations(t)
	want := []string{"irq_a_ask_room", "irq_ab_room", "irq_ba_room"}
	if got := listedIDs(t, service, requester, Query{Box: BoxAll, TeamspaceID: "tsp_1", WithUserID: "user_b"}); !slices.Equal(got, want) {
		t.Fatalf("as A = %v, want %v", got, want)
	}
	if got := listedIDs(t, service, targetPrimary, Query{Box: BoxAll, TeamspaceID: " tsp_1 ", WithUserID: " user_a "}); !slices.Equal(got, want) {
		t.Fatalf("as B = %v, want %v", got, want)
	}
}

// TestAListingKeepsToTheRequestsTheActorMayRead: C, a third member, sees only
// their own request from A there; B's specialist sees only the one addressed
// to it; and A's inbox and sent requests keep to their own side.
func TestAListingKeepsToTheRequestsTheActorMayRead(t *testing.T) {
	service := sideConversations(t)
	for _, testCase := range []struct {
		name  string
		actor agents.InteractionActor
		query Query
		want  []string
	}{
		{"C, with A in tsp_1", otherMember, Query{Box: BoxAll, TeamspaceID: "tsp_1", WithUserID: "user_a"}, []string{"irq_ac_room"}},
		{"B's specialist, with A in tsp_1", askSpecialist, Query{Box: BoxAll, TeamspaceID: "tsp_1", WithUserID: "user_a"}, []string{"irq_a_ask_room"}},
		{"A's inbox in tsp_1", requester, Query{Box: BoxInbox, TeamspaceID: "tsp_1"}, []string{"irq_ba_room"}},
		{"A's sent requests in tsp_1", requester, Query{Box: BoxSent, TeamspaceID: "tsp_1"}, []string{"irq_a_ask_room", "irq_ab_room", "irq_ac_room"}},
		{"A's sent requests to B anywhere", requester, Query{Box: BoxSent, WithUserID: "user_b"}, []string{"irq_a_ask_room", "irq_ab_other_room", "irq_ab_outside", "irq_ab_room"}},
		{"A's inbox anywhere", requester, Query{Box: BoxInbox}, []string{"irq_ba_room"}},
	} {
		if got := listedIDs(t, service, testCase.actor, testCase.query); !slices.Equal(got, testCase.want) {
			t.Errorf("%s = %v, want %v", testCase.name, got, testCase.want)
		}
	}
}

func TestAListingNamesAnotherMemberAndIDsItCanHold(t *testing.T) {
	service := sideConversations(t)
	for _, query := range []Query{
		{Box: BoxAll, WithUserID: "user_a"},
		{Box: BoxAll, TeamspaceID: strings.Repeat("t", 129)},
		{Box: BoxAll, WithUserID: "user_\x00"},
		{Box: "everything"},
	} {
		if _, err := service.List(context.Background(), requester, query); !errors.Is(err, ErrInvalid) {
			t.Errorf("List(%+v) error=%v, want ErrInvalid", query, err)
		}
	}
}
