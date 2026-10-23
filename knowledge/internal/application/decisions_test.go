package application

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/access"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

// fakeRecorder keeps every decision it is handed, worth keeping or not:
// what the log keeps is the log's to decide.
type fakeRecorder struct {
	mu        sync.Mutex
	decisions []access.Decision
}

func (recorder *fakeRecorder) Record(_ context.Context, decision access.Decision) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.decisions = append(recorder.decisions, decision)
}

// recordingService serves one judged source, src_1 made by user-1, holding
// doc_1, to caller.
func recordingService(t *testing.T, caller domain.OrgMembership) (*Service, *fakeRecorder) {
	t.Helper()
	store := newFakeStore()
	source := activeSource(store, "gmail")
	store.documents[source.ID+"\x00msg_1"] = domain.Document{
		ID: "doc_1", OrganizationID: "org_1", SourceID: source.ID, ExternalID: "msg_1", Body: "hello",
	}
	recorder := &fakeRecorder{}
	return New(Dependencies{
		Store:     store,
		Directory: &fakeDirectory{membership: caller},
		Judge:     &fakeJudge{},
		Recorder:  recorder,
		Clock:     func() time.Time { return testClock },
	}, Config{}), recorder
}

// TestSingleDecisionsAreRecordedWithTheirRule: each single read or change of
// a source, and each read of a document, records one decision naming the
// action, the resource, its creator, the caller (and its agent) and the rule
// that let it through; a call refused records nothing.
func TestSingleDecisionsAreRecordedWithTheirRule(t *testing.T) {
	ctx := context.Background()
	credentials := authclient.Credentials{Authorization: "Bearer t"}
	admin := domain.OrgMembership{OrganizationID: "org_1", UserID: "user-2", Role: domain.OrgRoleAdmin}
	agent := func(role string, knowledge ...string) domain.OrgMembership {
		caller := member()
		caller.Agent, caller.AgentID, caller.AgentRole, caller.AgentKnowledge = true, "agt_"+role, role, knowledge
		return caller
	}

	// Each path, in an order that leaves the source to be removed last.
	type path struct {
		name, action, kind, id string
		call                   func(*Service) error
	}
	paths := []path{
		{"Source", access.ActionRead, "source", "src_1", func(s *Service) error {
			_, err := s.Source(ctx, credentials, "org_1", "src_1")
			return err
		}},
		{"Runs", access.ActionRead, "source", "src_1", func(s *Service) error {
			_, err := s.Runs(ctx, credentials, "org_1", "src_1", 10)
			return err
		}},
		{"Document", access.ActionRead, "document", "doc_1", func(s *Service) error {
			_, err := s.Document(ctx, credentials, "org_1", "doc_1")
			return err
		}},
		{"SyncSource", access.ActionWrite, "source", "src_1", func(s *Service) error {
			_, err := s.SyncSource(ctx, credentials, "org_1", "src_1")
			return err
		}},
		{"SetSourceKinds", access.ActionWrite, "source", "src_1", func(s *Service) error {
			_, err := s.SetSourceKinds(ctx, credentials, "org_1", "src_1", []string{"conversation"})
			return err
		}},
		{"RemoveSource", access.ActionDelete, "source", "src_1", func(s *Service) error {
			return s.RemoveSource(ctx, credentials, "org_1", "src_1")
		}},
	}
	// An agent may read and sync, never change what is indexed or remove it.
	agentPaths := []string{"Source", "Runs", "Document", "SyncSource"}

	for _, test := range []struct {
		name   string
		caller domain.OrgMembership
		reason string
		// allowed is the paths the caller may take; nil is every one.
		allowed []string
	}{
		{"its creator", member(), access.ReasonOwner, nil},
		{"an admin's own session", admin, access.ReasonOrgAdmin, nil},
		{"its creator's primary", agent(access.AgentPrimary), access.ReasonOwner, agentPaths},
		{"a specialist granted everything", agent(access.AgentSpecialist, access.All), access.ReasonOwner, agentPaths},
		{"a specialist whose grant names it", agent(access.AgentSpecialist, "src_1"), access.ReasonGrant, agentPaths},
		{"a specialist whose grant does not", agent(access.AgentSpecialist, "src_2"), "", []string{}},
		{"an admin's agent", func() domain.OrgMembership {
			caller := admin
			caller.Agent, caller.AgentID, caller.AgentRole = true, "agt_admin", access.AgentPrimary
			return caller
		}(), "", []string{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, recorder := recordingService(t, test.caller)
			var want []access.Decision
			for _, p := range paths {
				err := p.call(service)
				allowed := test.allowed == nil || slices.Contains(test.allowed, p.name)
				if allowed != (err == nil) {
					t.Fatalf("%s: error = %v, want allowed %t", p.name, err, allowed)
				}
				if !allowed {
					continue
				}
				decision := access.Decision{
					OrganizationID: "org_1", Service: "knowledge", Action: p.action,
					ResourceKind: p.kind, ResourceID: p.id, OwnerID: "user-1",
					ActorUserID: test.caller.UserID, Reason: test.reason,
				}
				if test.caller.Agent {
					decision.ActorAgentID, decision.ActorAgentRole = test.caller.AgentID, test.caller.AgentRole
				}
				want = append(want, decision)
			}
			if !slices.Equal(recorder.decisions, want) {
				t.Fatalf("recorded\n%+v\nwant\n%+v", recorder.decisions, want)
			}
		})
	}
}

// TestListingSourcesRecordsNothing: Sources decides each source it lists
// with the check a single read makes, but a listing is not a member reaching
// each one, so nothing is recorded for it.
func TestListingSourcesRecordsNothing(t *testing.T) {
	for _, caller := range []domain.OrgMembership{
		member(),
		{OrganizationID: "org_1", UserID: "user-2", Role: domain.OrgRoleAdmin},
		{OrganizationID: "org_1", UserID: "user-1", Role: domain.OrgRoleMember, Agent: true, AgentID: "agt_s",
			AgentRole: access.AgentSpecialist, AgentKnowledge: []string{"src_1"}},
	} {
		service, recorder := recordingService(t, caller)
		listed, err := service.Sources(context.Background(), authclient.Credentials{Authorization: "Bearer t"}, "org_1",
			domain.SourceQuery{Admin: true})
		if err != nil || len(listed) != 1 {
			t.Fatalf("%s: Sources() = %d, %v; want src_1", caller.UserID, len(listed), err)
		}
		if len(recorder.decisions) != 0 {
			t.Fatalf("%s: listing recorded %+v", caller.UserID, recorder.decisions)
		}
	}
}
