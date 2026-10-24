package assembly

import (
	"log/slog"
	"slices"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/subagents"
)

func subAgents(built agent.Agent) []string {
	names := []string{}
	for _, sub := range built.SubAgents() {
		names = append(names, sub.Name())
	}
	return names
}

// TestTheSingleAgentFlagChangesOnlyRoot: Settings' SingleAgent decides how
// Root is built and nothing else. Off, Root has its planner and researcher
// and declares spawn_helper; on, it has no sub-agents and declares
// web_search and plan in their place. The unattended, room and delivering
// builds offer the same tools either way.
func TestTheSingleAgentFlagChangesOnlyRoot(t *testing.T) {
	llm := &recordingModel{}
	off := assembledKrude(t, llm, "")
	on := assembledKrude(t, llm, "")
	on.singleAgent = true
	if err := on.buildAgents(); err != nil {
		t.Fatal(err)
	}

	if got := subAgents(off.Root); !slices.Equal(got, []string{"planner", "researcher"}) {
		t.Fatalf("flag off: Root's sub-agents = %v, want the planner and the researcher", got)
	}
	if got := subAgents(on.Root); len(got) != 0 {
		t.Fatalf("flag on: Root's sub-agents = %v, want none", got)
	}
	offTools, onTools := declared(llm.firstCall(t, off.Root)), declared(llm.firstCall(t, on.Root))
	for _, name := range []string{"spawn_helper", "create_subagent", "planner", "researcher"} {
		if !slices.Contains(offTools, name) || slices.Contains(onTools, name) {
			t.Fatalf("%q: declared with the flag off = %v, on = %v; want only off", name, slices.Contains(offTools, name), slices.Contains(onTools, name))
		}
	}
	for _, name := range []string{"web_search", "plan"} {
		if slices.Contains(offTools, name) || !slices.Contains(onTools, name) {
			t.Fatalf("%q: declared with the flag off = %v, on = %v; want only on", name, slices.Contains(offTools, name), slices.Contains(onTools, name))
		}
	}

	for name, pair := range map[string][2]agent.Agent{
		"unattended":       {off.Unattended, on.Unattended},
		"unattended quiet": {off.UnattendedQuiet, on.UnattendedQuiet},
		"room":             {off.Room, on.Room},
		"delivering":       {off.Delivering(DeliveryReads), on.Delivering(DeliveryReads)},
	} {
		before, after := declared(llm.firstCall(t, pair[0])), declared(llm.firstCall(t, pair[1]))
		slices.Sort(before)
		slices.Sort(after)
		if !slices.Equal(before, after) || len(pair[1].SubAgents()) != 0 {
			t.Fatalf("%s: tools with the flag off = %v, on = %v; want the same and no sub-agents", name, before, after)
		}
	}
}

// TestSubagentsComeWithTheFlagAndTheirStorage: single-agent Krude given
// storage for sub-agents has a manager for them and declares the four tools
// it manages them with; without the flag nothing has either, and the
// unattended, room and delivering builds offer the same tools whatever the
// flag and the storage.
func TestSubagentsComeWithTheFlagAndTheirStorage(t *testing.T) {
	llm := &recordingModel{}
	off := assembledKrude(t, llm, "")
	on := assembledKrude(t, llm, "")
	on.singleAgent = true
	knowledge := fakeToolbox(t, "knowledge", "knowledge_search")
	on.readOnlyKnowledge = &knowledge
	storage := SubagentStorage{Store: subagents.NewMemoryStore(), Sessions: session.InMemoryService()}
	if err := on.startSubagents(serviceconfig.Config{SubagentsPerRun: 2, SubagentsPerProcess: 8, SubagentTTL: time.Hour}, storage, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	if err := on.buildAgents(); err != nil {
		t.Fatal(err)
	}
	if off.Subagents != nil || on.Subagents == nil {
		t.Fatalf("Subagents: off = %v, on = %v", off.Subagents, on.Subagents)
	}
	if got := on.subagentFamilies(); !slices.Equal(got, subagents.Families) {
		t.Fatalf("a sub-agent here can have %v, want %v", got, subagents.Families)
	}
	offTools, onTools := declared(llm.firstCall(t, off.Root)), declared(llm.firstCall(t, on.Root))
	for _, name := range []string{subagents.SpawnToolName, subagents.MessageToolName, subagents.StatusToolName, subagents.StopToolName} {
		if slices.Contains(offTools, name) || !slices.Contains(onTools, name) {
			t.Fatalf("%q: declared with the flag off = %v, on = %v; want only on", name, slices.Contains(offTools, name), slices.Contains(onTools, name))
		}
	}
	for name, pair := range map[string][2]agent.Agent{
		"unattended":       {off.Unattended, on.Unattended},
		"unattended quiet": {off.UnattendedQuiet, on.UnattendedQuiet},
		"room":             {off.Room, on.Room},
		"delivering":       {off.Delivering(DeliveryReads), on.Delivering(DeliveryReads)},
	} {
		before, after := declared(llm.firstCall(t, pair[0])), declared(llm.firstCall(t, pair[1]))
		slices.Sort(before)
		slices.Sort(after)
		if !slices.Equal(before, after) || slices.Contains(after, subagents.SpawnToolName) {
			t.Fatalf("%s: tools with the flag off = %v, on = %v; want the same, and no spawn_agent", name, before, after)
		}
	}
}
