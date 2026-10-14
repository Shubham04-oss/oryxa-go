package runprofile

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

// golden is the whole table. A change to what any run may do is a change
// here, in review, on purpose.
const golden = `
organization synthesis        tools=none            knowledge=none      memory=-/-  guarded=true  decides=false model=lite
primary      delivery_apps    tools=delivery_apps   knowledge=none      memory=-/-  guarded=true  decides=false model=main
primary      delivery_quiet   tools=delivery_quiet  knowledge=read_only memory=-/-  guarded=true  decides=false model=main
primary      delivery_reads   tools=delivery_reads  knowledge=read_only memory=-/-  guarded=true  decides=false model=main
primary      delivery_report  tools=delivery_report knowledge=none      memory=-/-  guarded=true  decides=false model=main
primary      room_turn        tools=room            knowledge=none      memory=-/-  guarded=true  decides=false model=main
primary      team_turn        tools=reads           knowledge=read_only memory=-/-  guarded=true  decides=false model=main
primary      wakeup           tools=reads           knowledge=read_only memory=-/-  guarded=true  decides=false model=main
primary      wakeup_quiet     tools=quiet           knowledge=read_only memory=-/-  guarded=true  decides=false model=main
primary      watched          tools=full            knowledge=member    memory=r/w  guarded=true  decides=true  model=main
primary      workflow         tools=reads           knowledge=read_only memory=-/-  guarded=true  decides=false model=main
specialist   delivery_apps    tools=delivery_apps   knowledge=none      memory=-/-  guarded=true  decides=false model=lite
specialist   delivery_quiet   tools=delivery_quiet  knowledge=read_only memory=-/-  guarded=true  decides=false model=lite
specialist   delivery_reads   tools=delivery_reads  knowledge=read_only memory=-/-  guarded=true  decides=false model=lite
specialist   delivery_report  tools=delivery_report knowledge=none      memory=-/-  guarded=true  decides=false model=lite
specialist   room_turn        tools=room            knowledge=none      memory=-/-  guarded=true  decides=false model=lite
specialist   team_turn        tools=reads           knowledge=read_only memory=-/-  guarded=true  decides=false model=lite
specialist   wakeup           tools=reads           knowledge=read_only memory=-/-  guarded=true  decides=false model=lite
specialist   wakeup_quiet     tools=quiet           knowledge=read_only memory=-/-  guarded=true  decides=false model=lite
specialist   watched          tools=full            knowledge=member    memory=r/w  guarded=true  decides=true  model=lite
specialist   workflow         tools=reads           knowledge=read_only memory=-/-  guarded=true  decides=false model=lite
`

func TestTheTableIsPinned(t *testing.T) {
	var lines []string
	for role, modes := range table {
		for mode, profile := range modes {
			memory := map[bool]string{true: "r", false: "-"}
			lines = append(lines, fmt.Sprintf("%-12s %-16s tools=%-15s knowledge=%-9s memory=%s/%s  guarded=%-5t decides=%-5t model=%s",
				role, mode, profile.Tools, profile.Knowledge, memory[profile.ReadsMemory],
				map[bool]string{true: "w", false: "-"}[profile.WritesMemory], profile.Guarded, profile.Decides, profile.Model))
		}
	}
	slices.Sort(lines)
	if got := "\n" + strings.Join(lines, "\n") + "\n"; got != golden {
		t.Fatalf("the run-profile table changed; if on purpose, update golden to:\n%s", got)
	}
}

// TestOnlyAMembersOwnTurnIsFull: everything a run nobody is watching does is
// steered by somebody else's words, so only the watched mode answers on the
// full agent, touches memory or decides. interactions relies on this to tell
// from what started a run alone whether it decides.
func TestOnlyAMembersOwnTurnIsFull(t *testing.T) {
	for role, modes := range table {
		for mode, profile := range modes {
			watched := mode == Watched
			if profile.Reduced() == watched || profile.ReadsMemory != watched || profile.WritesMemory != watched || profile.Decides != watched {
				t.Errorf("%s %s: %+v", role, mode, profile)
			}
		}
	}
}

func TestAPairTheTableDoesNotHoldGetsNothing(t *testing.T) {
	for _, pair := range [][2]string{{"organization", "watched"}, {"primary", "synthesis"}, {"someone", "watched"}} {
		if got := Lookup(Role(pair[0]), Mode(pair[1])); got != least || !got.Reduced() || got.Decides {
			t.Errorf("%v got %+v", pair, got)
		}
	}
	if RoleOf("") != Primary || RoleOf("specialist") != Specialist || RoleOf("organization") != Organization {
		t.Error("RoleOf does not name the roles threads and interactions carry")
	}
}

func TestModeOf(t *testing.T) {
	cases := []struct {
		startedBy string
		tools     Toolset
		want      Mode
	}{
		{agentsv1.StartedByMember, "", Watched},
		{agentsv1.StartedByMember, ToolsReads, Watched},
		{agentsv1.StartedByMember, ToolsDeliveryReport, Watched},
		{agentsv1.StartedByMember, ToolsRoom, RoomTurn},
		{agentsv1.StartedByTrigger, "", Wakeup},
		{agentsv1.StartedByTrigger, ToolsQuiet, WakeupQuiet},
		{agentsv1.StartedByTrigger, ToolsDeliveryReads, DeliveryReads},
		{agentsv1.StartedByTrigger, ToolsDeliveryQuiet, DeliveryQuiet},
		{agentsv1.StartedByTrigger, ToolsDeliveryApps, DeliveryApps},
		{agentsv1.StartedByTrigger, ToolsDeliveryReport, DeliveryReport},
		{agentsv1.StartedByTrigger, ToolsRoom, RoomTurn},
		{agentsv1.StartedByTeam, ToolsReads, TeamTurn},
		{agentsv1.StartedByTeam, ToolsRoom, RoomTurn},
		{agentsv1.StartedByWorkflow, ToolsReads, Workflow},
		{agentsv1.StartedByOrganization, "", Synthesis},
	}
	for _, c := range cases {
		if got, err := ModeOf(c.startedBy, c.tools); err != nil || got != c.want {
			t.Errorf("ModeOf(%q, %q) = %q, %v; want %q", c.startedBy, c.tools, got, err, c.want)
		}
	}
	for _, bad := range [][2]string{{"", ""}, {"stranger", ""}, {agentsv1.StartedByTrigger, "everything"}} {
		if _, err := ModeOf(bad[0], Toolset(bad[1])); err == nil {
			t.Errorf("ModeOf(%q, %q) accepted", bad[0], bad[1])
		}
	}
}

// TestADefaultGrantReadsEveryAppAndWritesNothing: a specialist nobody has
// granted anything reaches the apps it did before grants, read-only (I3).
func TestADefaultGrantReadsEveryAppAndWritesNothing(t *testing.T) {
	if grant := DefaultGrant(); !grant.ReachesApps() || grant.Writes() || grant.Apps[0] != AllApps {
		t.Fatalf("the default grant is %+v", grant)
	}
	if (Grant{}).ReachesApps() || !(Grant{WriteTools: []string{"GMAIL_SEND_EMAIL"}}).Writes() {
		t.Fatal("an empty grant reaches an app, or a granted write tool does not write")
	}
}
