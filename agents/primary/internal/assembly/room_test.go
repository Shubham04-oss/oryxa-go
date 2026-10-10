package assembly

import (
	"context"
	"iter"
	"maps"
	"slices"
	"strings"
	"testing"

	"cloud.google.com/go/auth"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/serviceconfig"
)

// fixedToken is Google credentials that always hold the same token, so Build
// makes its models without looking for any.
type fixedToken string

func (token fixedToken) Token(context.Context) (*auth.Token, error) {
	return &auth.Token{Value: string(token), Type: "Bearer"}, nil
}

// builtKrude is Krude as Build assembles it for the service, with the
// member's apps, their knowledge and the request ledger. Nothing is
// contacted: every client connects on first use.
func builtKrude(t *testing.T) *Krude {
	t.Helper()
	krude, err := Build(context.Background(), Options{
		Settings: serviceconfig.Config{
			GoogleCloudProject: "oryxa-test", GoogleCloudLocation: "global",
			Model: "gemini-test", ModelLite: "gemini-test-lite",
			AppsURL: "http://127.0.0.1:1/mcp", RagURL: "http://127.0.0.1:1",
		},
		AWS:          validAWSConfig(),
		Google:       auth.NewCredentials(&auth.CredentialsOptions{TokenProvider: fixedToken("service-account-token")}),
		Interactions: noInteractions{},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	t.Cleanup(func() { _ = krude.Close(context.Background()) })
	return krude
}

// toolNames are the tools of a toolbox whose tools are fixed, which lists
// them without reaching anything.
func toolNames(t *testing.T, box agents.Toolbox) []string {
	t.Helper()
	tools, err := box.Toolset.Tools(nil)
	if err != nil {
		t.Fatalf("toolbox %s: %v", box.Name, err)
	}
	var names []string
	for _, one := range tools {
		names = append(names, one.Name())
	}
	return names
}

// stateOnly is a context holding a session's state and nothing else, which
// is all a delivery's interactions toolbox reads to say what it offers.
type stateOnly struct {
	agent.StrictContextMock
	state stateMap
}

func (c *stateOnly) ReadonlyState() session.ReadonlyState { return c.state }

func withState(state map[string]any) *stateOnly {
	return &stateOnly{StrictContextMock: agent.NewStrictContextMock(context.Background()), state: state}
}

type stateMap map[string]any

func (s stateMap) Get(key string) (any, error) {
	value, ok := s[key]
	if !ok {
		return nil, session.ErrStateKeyNotExist
	}
	return value, nil
}

func (s stateMap) All() iter.Seq2[string, any] { return maps.All(s) }

func toolboxNames(toolboxes []agents.Toolbox) []string {
	names := []string{}
	for _, box := range toolboxes {
		names = append(names, box.Name)
	}
	return names
}

// TestNoUnwatchedRunHoldsTheMembersRequests: what steers an app wake-up, a
// trigger, a workflow or a turn in a room is text somebody else wrote, so
// none of them starts with, or can load, a toolbox that lists, decides or
// answers the member's requests. A run delivering one request has that
// request's own toolbox instead, which reads it, and decides or answers it
// only as its delivery is for.
func TestNoUnwatchedRunHoldsTheMembersRequests(t *testing.T) {
	krude := builtKrude(t)
	for name, test := range map[string]struct {
		toolboxes []agents.Toolbox
		want      []string
	}{
		"an app wake-up, a trigger or a workflow": {krude.unattended, []string{"apps", "knowledge"}},
		"an unwatched run kept from the apps":     {krude.unattendedQuiet, []string{"knowledge"}},
		"a turn in a room":                        {krude.room, []string{agents.RoomToolboxName}},
	} {
		if got := toolboxNames(test.toolboxes); !slices.Equal(got, test.want) {
			t.Errorf("%s starts from %v, want exactly %v", name, got, test.want)
		}
	}
	if room := krude.room[0]; !slices.Equal(toolNames(t, room), []string{"room_ask_owner"}) {
		t.Errorf("a turn in a room is offered %v, want room_ask_owner alone", toolNames(t, room))
	}

	for reach, want := range map[DeliveryReach][]string{
		DeliveryReads:  {"apps", "knowledge", agents.InteractionsToolboxName},
		DeliveryApps:   {"apps", agents.InteractionsToolboxName},
		DeliveryQuiet:  {"knowledge", agents.InteractionsToolboxName},
		DeliveryReport: {agents.InteractionsToolboxName},
	} {
		toolboxes := krude.toolboxesFor(reach)
		if got := toolboxNames(toolboxes); !slices.Equal(got, want) {
			t.Errorf("reach %d starts from %v, want %v", reach, got, want)
			continue
		}
		interactions := toolboxes[len(toolboxes)-1]
		for _, purpose := range []string{agents.DeliveryDecides, agents.DeliveryWorks, agents.DeliveryReports} {
			offered, err := interactions.Toolset.Tools(withState(agents.DeliveryState(agents.Delivery{RequestID: "irq_1", Purpose: purpose})))
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, one := range offered {
				names = append(names, one.Name())
			}
			if !slices.Equal(names, agents.DeliveryTools(purpose)) {
				t.Errorf("reach %d delivering for %q offers %v, want %v", reach, purpose, names, agents.DeliveryTools(purpose))
			}
		}
	}
}

// TestOnlyAMembersOwnConversationCanDecideARequest: no toolset a run nobody
// is watching starts from offers interactions_decide, whatever it delivers
// and for whatever purpose; the toolboxes of a conversation the member
// started still do, and the ledger accepts a decision from that run alone.
func TestOnlyAMembersOwnConversationCanDecideARequest(t *testing.T) {
	krude := builtKrude(t)
	offersDecide := func(toolboxes []agents.Toolbox, state agent.ReadonlyContext) bool {
		for _, toolbox := range toolboxes {
			// Only the interactions toolbox holds the member's requests; the
			// others are the member's apps and knowledge, whose tools are
			// listed from their servers.
			if toolbox.Name != agents.InteractionsToolboxName && toolbox.Name != agents.RoomToolboxName {
				continue
			}
			offered, err := toolbox.Toolset.Tools(state)
			if err != nil {
				t.Fatal(err)
			}
			for _, one := range offered {
				if one.Name() == "interactions_decide" {
					return true
				}
			}
		}
		return false
	}
	for name, toolboxes := range map[string][]agents.Toolbox{
		"an app wake-up, a trigger or a workflow": krude.unattended,
		"an unwatched run kept from the apps":     krude.unattendedQuiet,
		"a turn in a room":                        krude.room,
	} {
		if offersDecide(toolboxes, withState(map[string]any{})) {
			t.Errorf("%s is offered interactions_decide", name)
		}
	}
	for _, reach := range []DeliveryReach{DeliveryReads, DeliveryApps, DeliveryQuiet, DeliveryReport} {
		for _, purpose := range []string{agents.DeliveryDecides, agents.DeliveryWorks, agents.DeliveryReports, ""} {
			state := withState(agents.DeliveryState(agents.Delivery{RequestID: "irq_1", Purpose: purpose}))
			if offersDecide(krude.toolboxesFor(reach), state) {
				t.Errorf("a delivery at reach %d for %q is offered interactions_decide", reach, purpose)
			}
		}
	}
	if !offersDecide(krude.toolboxes, withState(map[string]any{})) {
		t.Fatal("a member's own conversation is not offered interactions_decide")
	}
}

// TestATurnInARoomAnswersFromTheRoomAlone: Krude and a specialist taking a
// turn in a room are offered, from their first model call, the one tool
// that files what they cannot answer with their member, and nothing else:
// no toolbox to list or load, none of the member's apps, knowledge, memory,
// files or requests, and no research, helper or sub-agent. Their
// instruction says to answer from the room alone, and carries the tool's
// skill.
func TestATurnInARoomAnswersFromTheRoomAlone(t *testing.T) {
	llm := &recordingModel{}
	krude := assembledKrude(t, llm, "")
	specialist, err := krude.ManagedRoom(agents.ManagedSpec{ID: "agt_ledger", Name: "Ledger", Instructions: "Keep the pricing sheet."})
	if err != nil {
		t.Fatal(err)
	}
	for name, built := range map[string]agent.Agent{"Krude": krude.Room, "a specialist": specialist} {
		first := llm.firstCall(t, built)
		if offered := declared(first); !slices.Equal(offered, []string{"room_ask_owner"}) {
			t.Errorf("%s in a room was offered %v, want room_ask_owner alone", name, offered)
		}
		instruction := ""
		if first.Config.SystemInstruction != nil {
			for _, part := range first.Config.SystemInstruction.Parts {
				instruction += part.Text
			}
		}
		for _, want := range []string{"only from the room", "# Asking your member from a room"} {
			if !strings.Contains(instruction, want) {
				t.Errorf("%s's instruction in a room lacks %q:\n%s", name, want, instruction)
			}
		}
		for _, never := range []string{"list_tools", "load_tools", "get_user_details", "load_memory", "researcher"} {
			if strings.Contains(instruction, never) {
				t.Errorf("%s's instruction in a room sends it to %s:\n%s", name, never, instruction)
			}
		}
	}
}
