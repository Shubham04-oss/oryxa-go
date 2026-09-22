package agentteamrooms

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentteams"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

type memoryStore struct {
	messages []Message
	threads  map[string]string
}

func (store *memoryStore) Append(_ context.Context, _ agentteams.Scope, message Message) (Message, error) {
	message.Seq = int64(len(store.messages) + 1)
	message.ID = "message"
	message.CreatedAt = time.Now().UTC()
	store.messages = append(store.messages, message)
	return message, nil
}
func (store *memoryStore) List(_ context.Context, _ agentteams.Scope, _ string, after int64, _ int) ([]Message, error) {
	result := []Message{}
	for _, message := range store.messages {
		if message.Seq > after {
			result = append(result, message)
		}
	}
	return result, nil
}
func (store *memoryStore) Recent(_ context.Context, _ agentteams.Scope, _ string, limit int) ([]Message, error) {
	return tail(store.messages, limit), nil
}
func (store *memoryStore) Thread(_ context.Context, _ agentteams.Scope, _, agentID string) (string, error) {
	return store.threads[agentID], nil
}
func (store *memoryStore) SetThread(_ context.Context, _ agentteams.Scope, _, agentID, threadID string) error {
	store.threads[agentID] = threadID
	return nil
}

type teamRegistry struct {
	team   agentteams.Team
	scopes []agentteams.Scope
}

func (registry *teamRegistry) Get(_ context.Context, scope agentteams.Scope, id string) (agentteams.Team, error) {
	registry.scopes = append(registry.scopes, scope)
	if id != registry.team.ID || scope.OrganizationID != "org" || scope.UserID != "user" {
		return agentteams.Team{}, agentteams.ErrNotFound
	}
	return registry.team, nil
}

type agentCall struct {
	id, threadID, prompt string
	profile              managedagents.UnattendedProfile
}
type fakeAgents struct {
	calls   []agentCall
	outputs map[string]string
}

func (agents *fakeAgents) StartUnattendedRun(_ context.Context, _ threads.Member, id, threadID string, input threads.RunInput, profile managedagents.UnattendedProfile) (managedagents.StartedRun, error) {
	agents.calls = append(agents.calls, agentCall{id: id, threadID: threadID, prompt: input.Message, profile: profile})
	output := agents.outputs[id]
	if id == "primary" {
		primaryCalls := 0
		for _, call := range agents.calls {
			if call.id == "primary" {
				primaryCalls++
			}
		}
		if primaryCalls > 1 && agents.outputs["primary_final"] != "" {
			output = agents.outputs["primary_final"]
		}
	}
	return managedagents.StartedRun{ThreadID: "thread_" + id, Run: fakeRun{output: output}}, nil
}

type fakeRun struct{ output string }

func (run fakeRun) ID() string                      { return "run" }
func (run fakeRun) Discard(context.Context, string) {}
func (run fakeRun) Execute(_ context.Context, emit func(threads.Item) error) {
	_ = emit(threads.Item{Type: threads.ItemMessage, Author: "agent", Text: run.output})
}

func fixture(t *testing.T) (*Service, *memoryStore, *teamRegistry, *fakeAgents) {
	t.Helper()
	store := &memoryStore{threads: map[string]string{}}
	team := &teamRegistry{team: agentteams.Team{
		ID: "team", Name: "Launch", Description: "Review a launch together", Status: agentteams.StatusActive,
		CoordinatorAgentID: "primary", CoordinatorName: "Nia",
		Members: []agentteams.Member{
			{AgentID: "research", Name: "Research Specialist", Role: "Verify claims", Status: managedagents.StatusActive},
			{AgentID: "writing", Name: "Writing Specialist", Role: "Draft copy", Status: managedagents.StatusActive},
		},
	}}
	agents := &fakeAgents{outputs: map[string]string{}}
	service, err := New(store, team, agents, nil)
	if err != nil {
		t.Fatal(err)
	}
	return service, store, team, agents
}

func TestCoordinatorCanHandOffToNamedTeammate(t *testing.T) {
	service, store, registry, agents := fixture(t)
	agents.outputs["primary"] = `{"status":"completed","summary":"I will coordinate this.","handoffs":[{"target":"@research-specialist","request":"Check the launch claim"}]}`
	agents.outputs["primary_final"] = "The claim still needs a source, so we should not report it as verified."
	agents.outputs["research"] = `{"status":"completed","summary":"The claim needs a source.","handoffs":[]}`
	member := threads.Member{OrganizationID: "org", UserID: "user"}
	said, err := service.Post(context.Background(), member, "team", "Can we validate the launch claim?")
	if err != nil {
		t.Fatal(err)
	}
	if len(said) != 4 || len(agents.calls) != 3 || agents.calls[0].id != "primary" || agents.calls[1].id != "research" || agents.calls[2].id != "primary" {
		t.Fatalf("collaboration = messages %+v, calls %+v", said, agents.calls)
	}
	if !strings.Contains(said[1].Body, "@research-specialist") || said[2].InReplyToSeq != said[1].Seq {
		t.Fatalf("handoff is not visible and threaded: %+v", said)
	}
	if said[3].InReplyToSeq != said[2].Seq || len(store.messages) != 4 || registry.scopes[0].OrganizationID != "org" || registry.scopes[0].UserID != "user" {
		t.Fatalf("room scope or persistence lost: %+v %+v", store.messages, registry.scopes)
	}
	if agents.calls[1].profile != managedagents.UnattendedReads || !strings.Contains(agents.calls[1].prompt, "untrusted collaborator content") {
		t.Fatalf("specialist did not receive a scoped team turn: %+v", agents.calls[1])
	}
}

func TestDirectMentionBypassesCoordinatorAndDoesNotLeaveTeam(t *testing.T) {
	service, _, _, agents := fixture(t)
	agents.outputs["research"] = `{"status":"completed","summary":"I checked it.","handoffs":[{"target":"Outside Agent","request":"Do work"}]}`
	said, err := service.Post(context.Background(), threads.Member{OrganizationID: "org", UserID: "user"}, "team", "@research-specialist please check this")
	if err != nil {
		t.Fatal(err)
	}
	if len(agents.calls) != 1 || agents.calls[0].id != "research" || len(said) != 2 {
		t.Fatalf("direct mention escaped or routed incorrectly: %+v %+v", agents.calls, said)
	}
}

func TestCoordinatorHandoffLineRoutesWithoutTreatingCasualMentionAsHandoff(t *testing.T) {
	service, _, _, agents := fixture(t)
	agents.outputs["primary"] = "Research Specialist should verify this.\n\n@research-specialist Check the claim's source."
	agents.outputs["primary_final"] = "The research specialist found that no source was supplied."
	agents.outputs["research"] = `{"status":"completed","summary":"No source was supplied.","handoffs":[]}`
	said, err := service.Post(context.Background(), threads.Member{OrganizationID: "org", UserID: "user"}, "team", "Coordinate a claim check")
	if err != nil || len(said) != 4 || len(agents.calls) != 3 || agents.calls[1].id != "research" {
		t.Fatalf("handoff line was not routed: %+v %+v %v", said, agents.calls, err)
	}
	if !strings.Contains(agents.calls[0].prompt, "end your answer with a separate line") {
		t.Fatalf("coordinator did not receive the handoff format: %q", agents.calls[0].prompt)
	}
	if !strings.Contains(agents.calls[2].prompt, "final coordinator turn") {
		t.Fatalf("coordinator did not receive a synthesis turn: %q", agents.calls[2].prompt)
	}

	service, _, _, agents = fixture(t)
	agents.outputs["primary"] = "I will talk with @research-specialist later; for now, here is the plan."
	said, err = service.Post(context.Background(), threads.Member{OrganizationID: "org", UserID: "user"}, "team", "Make a plan")
	if err != nil || len(said) != 2 || len(agents.calls) != 1 {
		t.Fatalf("casual mention woke a teammate: %+v %+v %v", said, agents.calls, err)
	}
}

func TestPausedTeamDoesNotRecordOrRun(t *testing.T) {
	service, store, registry, agents := fixture(t)
	registry.team.Status = agentteams.StatusDisabled
	_, err := service.Post(context.Background(), threads.Member{OrganizationID: "org", UserID: "user"}, "team", "hello")
	if !errors.Is(err, agentteams.ErrDisabled) || len(store.messages) != 0 || len(agents.calls) != 0 {
		t.Fatalf("paused team changed state: %v %+v %+v", err, store.messages, agents.calls)
	}
}

func TestTeamRoomReadIsOwnedAndValidationIsBounded(t *testing.T) {
	service, store, _, _ := fixture(t)
	store.messages = []Message{{Seq: 1, Body: "one"}, {Seq: 2, Body: "two"}}
	scope := agentteams.Scope{OrganizationID: "org", UserID: "user"}
	listed, err := service.Read(context.Background(), scope, "team", 1, 20)
	if err != nil || len(listed) != 1 || listed[0].Body != "two" {
		t.Fatalf("read = %+v, %v", listed, err)
	}
	_, err = service.Read(context.Background(), agentteams.Scope{OrganizationID: "org", UserID: "other"}, "team", 0, 20)
	if !errors.Is(err, agentteams.ErrNotFound) {
		t.Fatalf("another member read team: %v", err)
	}
	_, err = service.Post(context.Background(), threads.Member{OrganizationID: "org", UserID: "user"}, "team", strings.Repeat("x", maxBodyRunes+1))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized message = %v", err)
	}
}

func TestThreadPointersStayInsideTheMemberOwnedTeam(t *testing.T) {
	service, store, _, _ := fixture(t)
	store.threads["research"] = "thread_research"
	scope := agentteams.Scope{OrganizationID: "org", UserID: "user"}
	listed, err := service.Threads(context.Background(), scope, "team")
	if err != nil || len(listed) != 1 || listed["research"] != "thread_research" {
		t.Fatalf("team threads=%+v error=%v", listed, err)
	}
	_, err = service.Threads(context.Background(), agentteams.Scope{OrganizationID: "org", UserID: "other"}, "team")
	if !errors.Is(err, agentteams.ErrNotFound) {
		t.Fatalf("another member read team threads: %v", err)
	}
}
