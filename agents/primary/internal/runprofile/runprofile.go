// Package runprofile is the one place that decides what a run may do: which
// tools it answers with, how far into the knowledge it reaches, whether it
// reads or writes the member's memory, whether the Bedrock guardrail screens
// its model, whether it may decide a request, and which model tier it runs
// on. Each is looked up by the agent's role and the run's mode, in [table].
//
// The code that starts and runs agents asks this table instead of testing the
// kind of run itself: threads (which agent answers, memory), interactions
// (who may decide). What a named tool set holds is built in assembly; this
// package only names which set a run gets.
package runprofile

import (
	"fmt"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/modelrouter"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

// Role is which agent runs.
type Role string

const (
	// Primary is the member's own agent, Krude.
	Primary Role = "primary"
	// Specialist is an agent the member made.
	Specialist Role = "specialist"
	// Organization is the organization's synthesis agent.
	Organization Role = "organization"
)

// RoleOf is the role of an agent of kind, as threads and interactions carry
// it. Krude's own engine carries no kind, so an empty one is the primary. A
// kind this does not know is its own role, which the table does not hold, so
// it gets the least ([Lookup]).
func RoleOf(kind string) Role {
	if kind == "" {
		return Primary
	}
	return Role(kind)
}

// Toolset names a set of tools assembly builds. Every set but [ToolsFull] is a
// reduced one; their names are the reaches managedagents passes for a run
// nobody is watching (managedagents.UnattendedProfile).
type Toolset string

const (
	// Full is everything the role has: for the primary, the apps with their
	// writes, knowledge, sandbox, browser, directory, requests, teamspaces,
	// the agent system, sub-agents, helpers and saved files.
	ToolsFull Toolset = "full"
	// Reads is the apps read-only and the knowledge read-only, plus
	// load_artifacts (agents.UnattendedRoot).
	ToolsReads Toolset = "reads"
	// Quiet is Reads without the apps.
	ToolsQuiet Toolset = "quiet"
	// DeliveryReads, DeliveryQuiet, DeliveryApps and DeliveryReport are a
	// run delivering one request: that request's own tools, plus the apps and
	// knowledge read-only, the knowledge alone, the apps alone, or nothing.
	ToolsDeliveryReads  Toolset = "delivery_reads"
	ToolsDeliveryQuiet  Toolset = "delivery_quiet"
	ToolsDeliveryApps   Toolset = "delivery_apps"
	ToolsDeliveryReport Toolset = "delivery_report"
	// Room is filing a question with the seat's member, and nothing else
	// (agents.RoomRoot).
	ToolsRoom Toolset = "room"
	// None is no tools at all.
	ToolsNone Toolset = "none"
)

// Knowledge is how far a run reaches into the organization's knowledge.
type Knowledge string

const (
	KnowledgeMember   Knowledge = "member"    // the member's reach, writes included
	KnowledgeReadOnly Knowledge = "read_only" // the member's reach, reads only
	KnowledgeNone     Knowledge = "none"
)

// Mode is what kind of run it is: who started it, and with which tools.
type Mode string

const (
	// Watched is a member's own turn: they started it and read it.
	Watched Mode = "watched"
	// Wakeup is a run an app event, a schedule's batch or the edge (a call)
	// started, with the member not there.
	Wakeup Mode = "wakeup"
	// WakeupQuiet is a wake-up that may not read the member's apps.
	WakeupQuiet Mode = "wakeup_quiet"
	// DeliveryReads, DeliveryQuiet, DeliveryApps and DeliveryReport deliver
	// one request to the agent it addresses, reaching what was approved.
	DeliveryReads  Mode = "delivery_reads"
	DeliveryQuiet  Mode = "delivery_quiet"
	DeliveryApps   Mode = "delivery_apps"
	DeliveryReport Mode = "delivery_report"
	// RoomTurn is a seat's turn in a room, its member reading or away.
	RoomTurn Mode = "room_turn"
	// TeamTurn is a turn a team coordinator gave an agent: a team room's
	// turn or a delegation.
	TeamTurn Mode = "team_turn"
	// Workflow is a run a member's durable workflow dispatched.
	Workflow Mode = "workflow"
	// Synthesis is the organization agent reading what members published.
	Synthesis Mode = "synthesis"
)

// Profile is what a run may do.
type Profile struct {
	Tools     Toolset
	Knowledge Knowledge
	// ReadsMemory and WritesMemory: load_memory on the runner, and the run
	// handed to Memory Bank once it ends.
	ReadsMemory, WritesMemory bool
	// Guarded is whether the Bedrock guardrail screens the answering agent's
	// model calls in the run, main or lite, when one is configured
	// (modelrouter.Router.Answer, WithGuard).
	Guarded bool
	// Decides is whether the agent may decide a request addressed to it.
	Decides bool
	// Model is the tier the run's own agent answers on.
	Model modelrouter.Tier
}

// Reduced is whether the run answers on a reduced agent rather than the
// role's full one.
func (profile Profile) Reduced() bool { return profile.Tools != ToolsFull }

// least is what a (role, mode) the table does not hold gets: nothing, and
// screened.
var least = Profile{Tools: ToolsNone, Knowledge: KnowledgeNone, Guarded: true, Model: modelrouter.Lite}

// table is every (role, mode) a run can have. A run nobody is watching is
// steered by text somebody else wrote, so it answers on a reduced agent, with
// no memory, and decides nothing; only the member's own turn gets the full
// agent. Every run is guarded: the guardrail, when configured, screens every
// agent's model, the lite one included (L5).
var table = map[Role]map[Mode]Profile{
	Primary: {
		// The member's own conversation with Krude: everything.
		Watched: {Tools: ToolsFull, Knowledge: KnowledgeMember, ReadsMemory: true, WritesMemory: true, Guarded: true, Decides: true, Model: modelrouter.Main},
		// App events and the edge steer it: reads only, nothing that reaches out.
		Wakeup: {Tools: ToolsReads, Knowledge: KnowledgeReadOnly, Guarded: true, Model: modelrouter.Main},
		// As a wake-up, kept out of the member's apps.
		WakeupQuiet: {Tools: ToolsQuiet, Knowledge: KnowledgeReadOnly, Guarded: true, Model: modelrouter.Main},
		// Approved work allowed the apps and the knowledge.
		DeliveryReads: {Tools: ToolsDeliveryReads, Knowledge: KnowledgeReadOnly, Guarded: true, Model: modelrouter.Main},
		// Approved work allowed the knowledge alone.
		DeliveryQuiet: {Tools: ToolsDeliveryQuiet, Knowledge: KnowledgeReadOnly, Guarded: true, Model: modelrouter.Main},
		// Approved work allowed the apps alone.
		DeliveryApps: {Tools: ToolsDeliveryApps, Knowledge: KnowledgeNone, Guarded: true, Model: modelrouter.Main},
		// A new request, an outcome report, or approved work allowed nothing.
		DeliveryReport: {Tools: ToolsDeliveryReport, Knowledge: KnowledgeNone, Guarded: true, Model: modelrouter.Main},
		// Everyone in the room steers it and reads it: the room alone.
		RoomTurn: {Tools: ToolsRoom, Knowledge: KnowledgeNone, Guarded: true, Model: modelrouter.Main},
		// Another agent's output steers it (L3): as unwatched as a wake-up.
		TeamTurn: {Tools: ToolsReads, Knowledge: KnowledgeReadOnly, Guarded: true, Model: modelrouter.Main},
		// A schedule fired it: as unwatched as a wake-up.
		Workflow: {Tools: ToolsReads, Knowledge: KnowledgeReadOnly, Guarded: true, Model: modelrouter.Main},
	},
	Specialist: {
		// The member's own conversation with their specialist: the primary's toolboxes and memory (L2, L4).
		Watched: {Tools: ToolsFull, Knowledge: KnowledgeMember, ReadsMemory: true, WritesMemory: true, Decides: true, Guarded: true, Model: modelrouter.Lite},
		// Each unwatched mode is the primary's, on the lite model.
		Wakeup:         {Tools: ToolsReads, Knowledge: KnowledgeReadOnly, Guarded: true, Model: modelrouter.Lite},
		WakeupQuiet:    {Tools: ToolsQuiet, Knowledge: KnowledgeReadOnly, Guarded: true, Model: modelrouter.Lite},
		DeliveryReads:  {Tools: ToolsDeliveryReads, Knowledge: KnowledgeReadOnly, Guarded: true, Model: modelrouter.Lite},
		DeliveryQuiet:  {Tools: ToolsDeliveryQuiet, Knowledge: KnowledgeReadOnly, Guarded: true, Model: modelrouter.Lite},
		DeliveryApps:   {Tools: ToolsDeliveryApps, Knowledge: KnowledgeNone, Guarded: true, Model: modelrouter.Lite},
		DeliveryReport: {Tools: ToolsDeliveryReport, Knowledge: KnowledgeNone, Guarded: true, Model: modelrouter.Lite},
		RoomTurn:       {Tools: ToolsRoom, Knowledge: KnowledgeNone, Guarded: true, Model: modelrouter.Lite},
		// A delegation or a team room's turn (L3).
		TeamTurn: {Tools: ToolsReads, Knowledge: KnowledgeReadOnly, Guarded: true, Model: modelrouter.Lite},
		Workflow: {Tools: ToolsReads, Knowledge: KnowledgeReadOnly, Guarded: true, Model: modelrouter.Lite},
	},
	Organization: {
		// Reading published posts into a synthesis admins read: no tools, no memory, guarded.
		Synthesis: {Tools: ToolsNone, Knowledge: KnowledgeNone, Guarded: true, Model: modelrouter.Lite},
	},
}

// reducedModes are the modes of a run nobody is watching that its reduced
// tools, rather than what started it, name.
var reducedModes = map[Toolset]Mode{
	ToolsQuiet: WakeupQuiet, ToolsDeliveryReads: DeliveryReads, ToolsDeliveryQuiet: DeliveryQuiet,
	ToolsDeliveryApps: DeliveryApps, ToolsDeliveryReport: DeliveryReport,
}

// Lookup is the profile of a role's run in mode, or nothing at all for a
// pair the table does not hold.
func Lookup(role Role, mode Mode) Profile {
	if profile, ok := table[role][mode]; ok {
		return profile
	}
	return least
}

// ModeOf is the mode of a run startedBy (agentsv1.StartedBy*) on an engine
// whose reduced agent was built with tools ("" is [ToolsReads], Krude's own). A
// turn in a room is a room turn whoever started it; otherwise a member's own
// turn is watched, and any other run is what its reduced tools say, or, on
// the reads, what started it.
func ModeOf(startedBy string, tools Toolset) (Mode, error) {
	switch startedBy {
	case agentsv1.StartedByMember, agentsv1.StartedByTrigger, agentsv1.StartedByTeam, agentsv1.StartedByWorkflow, agentsv1.StartedByOrganization:
	default:
		return "", fmt.Errorf("a run is started by a member, trigger, team coordinator, workflow or organization agent, not %q", startedBy)
	}
	switch tools {
	case ToolsRoom:
		return RoomTurn, nil
	case "", ToolsReads:
	case ToolsQuiet, ToolsDeliveryReads, ToolsDeliveryQuiet, ToolsDeliveryApps, ToolsDeliveryReport:
		if startedBy == agentsv1.StartedByMember {
			return Watched, nil
		}
		return reducedModes[tools], nil
	default:
		return "", fmt.Errorf("no run answers with the tool set %q", tools)
	}
	switch startedBy {
	case agentsv1.StartedByMember:
		return Watched, nil
	case agentsv1.StartedByTeam:
		return TeamTurn, nil
	case agentsv1.StartedByWorkflow:
		return Workflow, nil
	case agentsv1.StartedByOrganization:
		return Synthesis, nil
	default:
		return Wakeup, nil
	}
}
