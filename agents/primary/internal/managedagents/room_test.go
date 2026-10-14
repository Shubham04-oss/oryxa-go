package managedagents

import (
	"context"
	"iter"
	"strings"
	"sync"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/adkmemory"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/pricing"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runprofile"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

// saying answers every call with the same text.
type saying string

func (saying) Name() string { return "saying" }

func (said saying) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{Content: genai.NewContentFromText(string(said), genai.RoleModel), TurnComplete: true}, nil)
	}
}

// recallingMemory counts what is remembered and looked up.
type recallingMemory struct {
	mu                sync.Mutex
	written, searched int
}

func (bank *recallingMemory) Write(context.Context, adkmemory.Scope, session.Session) error {
	bank.mu.Lock()
	defer bank.mu.Unlock()
	bank.written++
	return nil
}

func (bank *recallingMemory) Search(context.Context, adkmemory.Scope, string) ([]memory.Entry, error) {
	bank.mu.Lock()
	defer bank.mu.Unlock()
	bank.searched++
	return nil, nil
}

func (*recallingMemory) Forget(context.Context, string, string) error { return nil }

func (bank *recallingMemory) touched() (int, int) {
	bank.mu.Lock()
	defer bank.mu.Unlock()
	return bank.written, bank.searched
}

// TestATurnInARoomAnswersFromTheRoomEvenBesideItsOwner: a seat's turn in a
// room runs on the room's reduced agent, with no memory at all, whether its
// owner is reading the room (a team turn) or away (an unwatched one): the
// owner being there does not make the room's words their own conversation.
// The same team turn on any other profile is a team turn (runprofile.TeamTurn):
// another agent's words steer it, so it answers on the reduced agent and
// remembers nothing either (L3). Without the reduced agent, a turn in a room
// does not run at all.
func TestATurnInARoomAnswersFromTheRoomEvenBesideItsOwner(t *testing.T) {
	repo := newFakeRepository()
	prices, err := pricing.NewTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	bank := &recallingMemory{}
	remembering, err := adkmemory.New(bank)
	if err != nil {
		t.Fatal(err)
	}
	answering := func(said string) agent.Agent {
		built, err := llmagent.New(llmagent.Config{Name: "managed_agt_primary", Model: saying(said), Instruction: "Answer."})
		if err != nil {
			t.Fatal(err)
		}
		return built
	}
	service := &Service{
		repository: repo,
		factory:    func(Definition) (agent.Agent, error) { return answering("the member's own agent"), nil },
		unattended: func(_ Definition, profile UnattendedProfile) (agent.Agent, error) {
			if profile == RoomSeat {
				return answering("from the room"), nil
			}
			return answering("the reduced agent"), nil
		},
		sessions: session.InMemoryService(), runs: noRuns{}, prices: prices, memory: remembering,
	}
	member := threads.Member{OrganizationID: "org_1", UserID: "user_1"}
	turn := func(origin string, profile UnattendedProfile) string {
		t.Helper()
		started, err := service.StartUnattendedRun(context.Background(), member, repo.primary.ID, "",
			threads.RunInput{Message: "@carl, what does Ada's CRM say about Northwind?", StartedBy: origin}, profile)
		if err != nil {
			t.Fatalf("a %s turn on %q: %v", origin, profile, err)
		}
		var said strings.Builder
		started.Run.Execute(context.Background(), func(item threads.Item) error {
			if item.Type == threads.ItemMessage && item.Author != threads.AuthorMember {
				said.WriteString(item.Text)
			}
			return nil
		})
		return said.String()
	}

	for _, origin := range []string{agentsv1.StartedByTeam, agentsv1.StartedByTrigger} {
		if said := turn(origin, RoomSeat); said != "from the room" {
			t.Fatalf("a %s turn in a room was answered by %q, want the room's agent", origin, said)
		}
	}
	if written, searched := bank.touched(); written != 0 || searched != 0 {
		t.Fatalf("turns in a room wrote %d and looked up %d memories, want none", written, searched)
	}
	if said := turn(agentsv1.StartedByTeam, UnattendedReads); said != "the reduced agent" {
		t.Fatalf("a team turn elsewhere was answered by %q, want the reduced agent", said)
	}
	if written, searched := bank.touched(); written != 0 || searched != 0 {
		t.Fatalf("a team turn elsewhere wrote %d and looked up %d memories, want none", written, searched)
	}
	if said := turn(agentsv1.StartedByMember, UnattendedReads); said != "the member's own agent" {
		t.Fatalf("the member's own turn was answered by %q", said)
	}
	if written, _ := bank.touched(); written != 1 {
		t.Fatalf("the member's own turn wrote %d memories, want it remembered", written)
	}

	service.unattended = nil
	if _, err := service.StartUnattendedRun(context.Background(), member, repo.primary.ID, "",
		threads.RunInput{Message: "@carl?", StartedBy: agentsv1.StartedByTeam}, RoomSeat); err == nil {
		t.Fatal("a turn in a room ran with no reduced agent to answer it")
	}
}

// TestOnlyATurnInARoomIsConfined: a room's profile answers on its reduced
// agent whoever started the run, reads nothing of the member's and delivers
// nothing; every other profile leaves a watched run to the member's own
// agent.
func TestOnlyATurnInARoomIsConfined(t *testing.T) {
	for _, profile := range []UnattendedProfile{UnattendedReads, UnattendedQuiet, DeliveryReads, DeliveryApps, DeliveryQuiet, DeliveryReport, RoomSeat, "everything"} {
		mode, err := runprofile.ModeOf(agentsv1.StartedByMember, runprofile.Toolset(profile))
		if confined := err == nil && runprofile.Lookup(runprofile.Primary, mode).Reduced(); confined != (profile == RoomSeat) {
			t.Errorf("%q: a member's own turn is reduced = %t (%v)", profile, confined, err)
		}
	}
	if RoomSeat.ReadsApps() || RoomSeat.Delivers() {
		t.Errorf("a turn in a room reads apps %t, delivers %t; want neither", RoomSeat.ReadsApps(), RoomSeat.Delivers())
	}
}
