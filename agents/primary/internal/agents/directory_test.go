package agents

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
)

type fakeDirectory struct{ calls []string }

type fakeDirectoryContext struct{ calls []string }

func (directory *fakeDirectoryContext) Load(_ context.Context, organizationID, token string) (DirectoryContext, error) {
	directory.calls = append(directory.calls, organizationID+" "+token)
	return DirectoryContext{Ownership: []spec.OwnerHint{{Owner: "ben@example.com", System: "billing"}}}, nil
}

func (directory *fakeDirectory) Load(_ context.Context, organizationID, token string) (OrganizationDirectory, error) {
	directory.calls = append(directory.calls, "load "+organizationID+" "+token)
	return OrganizationDirectory{}, nil
}
func (directory *fakeDirectory) FindPeople(_ context.Context, organizationID, token, query string, limit int) ([]DirectoryPerson, error) {
	directory.calls = append(directory.calls, fmt.Sprintf("people %s %s %s %d", organizationID, token, query, limit))
	return []DirectoryPerson{{UserID: "user_2", Email: "ben@example.com", DisplayName: "Ben", Teams: []DirectoryTeamRef{}, Agents: []DirectoryAgent{{ID: "agt_2", Name: "Revenue", Kind: "specialist"}}}}, nil
}
func (directory *fakeDirectory) FindTeams(context.Context, string, string, string, int) ([]DirectoryTeam, error) {
	return []DirectoryTeam{}, nil
}
func (directory *fakeDirectory) FindAgents(context.Context, string, string, string, int) ([]DirectoryAgent, error) {
	return []DirectoryAgent{}, nil
}

func directoryLLM() model.LLM {
	return newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		switch {
		case functionResponse(request, "directory_find_people") != "":
			return genai.NewContentFromText("Found "+functionResponse(request, "directory_find_people"), genai.RoleModel)
		case functionResponse(request, "load_tools") != "":
			return functionCalls(&genai.FunctionCall{ID: "find_person", Name: "directory_find_people", Args: map[string]any{"query": "Ben", "limit": 5}})
		default:
			return functionCalls(&genai.FunctionCall{ID: "load_directory", Name: "load_tools", Args: map[string]any{"names": []any{"directory"}}})
		}
	})
}

func TestDirectoryToolboxDiscoversAsTheMember(t *testing.T) {
	directory := &fakeDirectory{}
	box, err := DirectoryToolbox(DirectoryConfig{Directory: directory})
	if err != nil {
		t.Fatal(err)
	}
	if box.Name != "directory" || box.Instructions == "" {
		t.Fatalf("box = %+v", box)
	}
	h := newHarness(t, directoryLLM(), box)
	ctx := memberauth.WithAccessToken(context.Background(), "member-token")
	answer := h.turnWith(ctx, "user_1", map[string]any{OrganizationIDKey: "org_1"}, "Who is Ben?")
	if !strings.Contains(answer, "ben@example.com") || len(directory.calls) != 1 || directory.calls[0] != "people org_1 member-token Ben 5" {
		t.Fatalf("answer=%q calls=%v", answer, directory.calls)
	}
}

func TestDirectoryToolboxNeedsADirectory(t *testing.T) {
	if _, err := DirectoryToolbox(DirectoryConfig{}); err == nil {
		t.Fatal("DirectoryToolbox() accepted no directory")
	}
}

func TestDirectoryToolboxUsesReviewedOwnershipAsMember(t *testing.T) {
	contextDirectory := &fakeDirectoryContext{}
	box, err := DirectoryToolbox(DirectoryConfig{Directory: &fakeDirectory{}, Context: contextDirectory})
	if err != nil {
		t.Fatal(err)
	}
	llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		switch {
		case functionResponse(request, "directory_find_owners") != "":
			return genai.NewContentFromText(functionResponse(request, "directory_find_owners"), genai.RoleModel)
		case functionResponse(request, "load_tools") != "":
			return functionCalls(&genai.FunctionCall{ID: "find_owner", Name: "directory_find_owners", Args: map[string]any{"query": "billing"}})
		default:
			return functionCalls(&genai.FunctionCall{ID: "load_directory", Name: "load_tools", Args: map[string]any{"names": []any{"directory"}}})
		}
	})
	h := newHarness(t, llm, box)
	ctx := memberauth.WithAccessToken(context.Background(), "member-token")
	answer := h.turnWith(ctx, "user_1", map[string]any{OrganizationIDKey: "org_1"}, "Who owns billing?")
	if !strings.Contains(answer, "ben@example.com") || len(contextDirectory.calls) != 1 || contextDirectory.calls[0] != "org_1 member-token" {
		t.Fatalf("answer=%q calls=%v", answer, contextDirectory.calls)
	}
}
