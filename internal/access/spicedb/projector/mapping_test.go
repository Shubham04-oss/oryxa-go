package projector

import (
	"encoding/json"
	"slices"
	"sort"
	"testing"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	filesv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/files/v1"
	teamsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
)

func strs[T interface{ String() string }](items []T) []string {
	var out []string
	for _, item := range items {
		out = append(out, item.String())
	}
	sort.Strings(out)
	return out
}

func (filter Filter) String() string { return Rel(filter).String() }

func mapEvent(t *testing.T, source, detailType string, event any) Change {
	t.Helper()
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	change, err := mappers[source][detailType](data)
	if err != nil {
		t.Fatalf("%s: %v", detailType, err)
	}
	return change
}

func TestEventsMapToRelationships(t *testing.T) {
	cases := []struct {
		name             string
		change           Change
		deletes, touches []string
	}{
		{
			name:    "auth member joined",
			change:  mapEvent(t, authv1.Source, authv1.MemberJoinedDetailType, authv1.MemberJoined{OrganizationID: "acme", UserID: "sam", Role: "admin"}),
			touches: []string{"org:acme#admin@member:sam"},
		},
		{
			name:    "auth role changed: the other roles go, the new one is written",
			change:  mapEvent(t, authv1.Source, authv1.MemberRoleChangedDetailType, authv1.MemberRoleChanged{OrganizationID: "acme", UserID: "sam", PreviousRole: "admin", Role: "member"}),
			deletes: []string{"org:acme#admin@member:sam", "org:acme#owner@member:sam"},
			touches: []string{"org:acme#member@member:sam"},
		},
		{
			name:    "auth member removed",
			change:  mapEvent(t, authv1.Source, authv1.MemberRemovedDetailType, authv1.MemberRemoved{OrganizationID: "acme", UserID: "sam", PreviousRole: "member"}),
			deletes: []string{"org:acme#admin@member:sam", "org:acme#executive@member:sam", "org:acme#member@member:sam", "org:acme#owner@member:sam"},
		},
		{
			name:   "organization deleted",
			change: mapEvent(t, authv1.Source, authv1.OrganizationDeletedDetailType, authv1.OrganizationDeleted{OrganizationID: "acme"}),
			deletes: []string{"agent:#@org:acme", "chart_person:#@org:acme", "corpus:#@org:acme", "file:#@org:acme", "member:#@org:acme", "org:acme#@:",
				"request:#@org:acme", "source:#@org:acme", "team:#@org:acme"},
		},
		{
			name:    "team created",
			change:  mapEvent(t, teamsv1.Source, teamsv1.TeamCreatedDetailType, teamsv1.TeamCreated{OrganizationID: "acme", TeamID: "eng"}),
			touches: []string{"team:eng#org@org:acme"},
		},
		{
			name:    "team admin added is its lead",
			change:  mapEvent(t, teamsv1.Source, teamsv1.MemberAddedDetailType, teamsv1.MemberAdded{OrganizationID: "acme", TeamID: "eng", UserID: "maya", Role: "admin"}),
			deletes: []string{"team:eng#member@member:maya"},
			touches: []string{"team:eng#lead@member:maya"},
		},
		{
			name:    "team role changed to member",
			change:  mapEvent(t, teamsv1.Source, teamsv1.MemberRoleChangedDetailType, teamsv1.MemberRoleChanged{OrganizationID: "acme", TeamID: "eng", UserID: "maya", Role: "member"}),
			deletes: []string{"team:eng#lead@member:maya"},
			touches: []string{"team:eng#member@member:maya"},
		},
		{
			name:    "team member removed",
			change:  mapEvent(t, teamsv1.Source, teamsv1.MemberRemovedDetailType, teamsv1.MemberRemoved{OrganizationID: "acme", TeamID: "eng", UserID: "maya"}),
			deletes: []string{"team:eng#lead@member:maya", "team:eng#member@member:maya"},
		},
		{
			name:    "team deleted",
			change:  mapEvent(t, teamsv1.Source, teamsv1.TeamDeletedDetailType, teamsv1.TeamDeleted{OrganizationID: "acme", TeamID: "eng"}),
			deletes: []string{"team:eng#@:"},
		},
		{
			name: "admins-only org file",
			change: mapEvent(t, filesv1.Source, filesv1.ObjectCreatedDetailType, filesv1.ObjectCreated{FileID: "f_board", OrganizationID: "acme",
				Scope: "organization", Access: "admins", CreatedBy: "olu"}),
			touches: []string{"file:f_board#admins_only@org:acme", "file:f_board#created_by@member:olu", "file:f_board#org@org:acme"},
		},
		{
			name: "member file",
			change: mapEvent(t, filesv1.Source, filesv1.ObjectCreatedDetailType, filesv1.ObjectCreated{FileID: "f_lee", OrganizationID: "acme",
				Scope: "member", MemberID: "lee", CreatedBy: "lee"}),
			touches: []string{"file:f_lee#created_by@member:lee", "file:f_lee#org@org:acme", "file:f_lee#owner@member:lee"},
		},
		{
			name:    "file deleted",
			change:  mapEvent(t, filesv1.Source, filesv1.ObjectDeletedDetailType, filesv1.ObjectDeleted{FileID: "f_lee", OrganizationID: "acme"}),
			deletes: []string{"file:f_lee#@:"},
		},
		{
			name:    "agent created",
			change:  mapEvent(t, agentsv1.Source, agentsv1.AgentCreatedDetailType, agentsv1.AgentChanged{AgentID: "agt_sam", UserID: "sam", OrganizationID: "acme", Kind: "primary"}),
			touches: []string{"agent:agt_sam#acts_for@member:sam", "agent:agt_sam#org@org:acme"},
		},
		{
			name:    "agent deleted takes its grants",
			change:  mapEvent(t, agentsv1.Source, agentsv1.AgentDeletedDetailType, agentsv1.AgentChanged{AgentID: "agt_r", OrganizationID: "acme"}),
			deletes: []string{"agent:agt_r#@:", "connection:#@agent:agt_r", "corpus:#@agent:agt_r", "file:#@agent:agt_r", "source:#@agent:agt_r"},
		},
		{
			name: "request made",
			change: mapEvent(t, agentsv1.Source, agentsv1.InteractionRequestedDetailType, agentsv1.InteractionChanged{RequestID: "int_1", OrganizationID: "acme",
				RequesterUserID: "lee", RequesterAgentID: "agt_lee", TargetUserID: "sam", TargetAgentID: "agt_sam"}),
			touches: []string{"request:int_1#org@org:acme", "request:int_1#requester@agent:agt_lee", "request:int_1#requester@member:lee",
				"request:int_1#target@member:sam", "request:int_1#target_agent@agent:agt_sam"},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if test.change.Organization != "acme" {
				t.Errorf("organization = %q", test.change.Organization)
			}
			if got := strs(test.change.Delete); !slices.Equal(got, test.deletes) {
				t.Errorf("deletes = %v, want %v", got, test.deletes)
			}
			if got := strs(test.change.Touch); !slices.Equal(got, test.touches) {
				t.Errorf("touches = %v, want %v", got, test.touches)
			}
		})
	}
}

func TestMappingRefusesWhatItCannotPlace(t *testing.T) {
	for name, err := range map[string]error{
		"unknown org role":    second(MemberJoined("acme", "sam", "superuser")),
		"unknown team role":   second(TeamMemberSet("acme", "eng", "sam", "owner")),
		"team scope, no team": second(Placed("corpus", "cor_1", Placement{Org: "acme", Scope: "team"})),
		"no org":              second(AgentChanged("", "agt_1", "sam")),
	} {
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func second(_ Change, err error) error { return err }

func TestEverySubscriptionHasAMapper(t *testing.T) {
	for source, types := range Subscriptions() {
		for _, detailType := range types {
			if mappers[source][detailType] == nil {
				t.Errorf("%s %s has no mapper", source, detailType)
			}
		}
	}
}

func TestGrantsSpellOutWhatIsLimited(t *testing.T) {
	world := World{
		Connections: []Connection{
			{ID: "con_gmail", Org: "acme", Member: "sam", Toolkit: "gmail"},
			{ID: "con_github", Org: "acme", Member: "sam", Toolkit: "github"},
			{ID: "con_lee", Org: "acme", Member: "lee", Toolkit: "gmail"},
		},
		Files: []PlacedRow{
			{ID: "f_sam", Placement: Placement{Org: "acme", Scope: "member", Member: "sam"}},
			{ID: "f_lee", Placement: Placement{Org: "acme", Scope: "member", Member: "lee"}},
			{ID: "f_eng", Placement: Placement{Org: "acme", Scope: "team", Team: "eng"}},
			{ID: "f_ops", Placement: Placement{Org: "acme", Scope: "team", Team: "ops"}},
			{ID: "f_org", Placement: Placement{Org: "acme", Scope: "organization"}},
		},
	}
	specialist := Agent{ID: "agt_r", Org: "acme", User: "sam", Kind: "specialist", Grant: &Grant{
		Apps: []string{"gmail"}, WriteApps: []string{"gmail", "github"},
		Corpora: []string{"cor_sam"}, Files: []string{"member", "eng"}, Knowledge: []string{},
	}}
	got := strs(grantRels(world, specialist))
	want := []string{
		"connection:con_gmail#granted@agent:agt_r",
		"connection:con_gmail#granted_write@agent:agt_r",
		"corpus:cor_sam#granted@agent:agt_r",
		"file:f_eng#granted@agent:agt_r",
		"file:f_sam#granted@agent:agt_r",
	}
	if !slices.Equal(got, want) {
		t.Errorf("grants = %v, want %v", got, want)
	}

	all := Agent{ID: "agt_all", Org: "acme", User: "sam", Kind: "specialist", Grant: &Grant{
		Apps: []string{"*"}, Corpora: nil, Files: []string{"*"}, Knowledge: []string{"*"},
	}}
	if got := grantRels(world, all); len(got) != 0 {
		t.Errorf("an all-reaching grant wrote %v", strs(got))
	}
	if got := grantRels(world, Agent{ID: "agt_sam", Org: "acme", User: "sam", Kind: "primary", Grant: specialist.Grant}); len(got) != 0 {
		t.Errorf("a primary got grants %v", strs(got))
	}
}

func TestChartFollowsAbove(t *testing.T) {
	memberships := []Membership{
		{Org: "acme", User: "maya", Email: "maya@acme.test"},
		{Org: "acme", User: "sam", Email: "Sam@acme.test"},
		{Org: "acme", User: "lee", Email: "lee@acme.test"},
		{Org: "other", User: "zed", Email: "zed@acme.test"},
	}
	chart := Chart{Org: "acme", Chart: spec.OrgChart{
		Executives: []string{"maya@acme.test"},
		Authority:  []string{"manages"},
		Relationships: []spec.Relationship{
			{From: "maya@acme.test", Relation: "manages", To: "sam@acme.test"},
			{From: "sam@acme.test", Relation: "mentors", To: "lee@acme.test"},
			{From: "sam@acme.test", Relation: "manages", To: "zed@acme.test"},
		},
	}}
	got := strs(chartRels(chart, memberships))
	want := []string{
		"chart_person:acme/lee#chart@org:acme",
		"chart_person:acme/lee#user@member:lee",
		"chart_person:acme/maya#chart@org:acme",
		"chart_person:acme/maya#exec_of@org:acme",
		"chart_person:acme/maya#user@member:maya",
		"chart_person:acme/sam#chart@org:acme",
		"chart_person:acme/sam#manager@chart_person:acme/maya",
		"chart_person:acme/sam#user@member:sam",
		"org:acme#executive@member:maya",
	}
	if !slices.Equal(got, want) {
		t.Errorf("chart = %v, want %v", got, want)
	}
}

func TestExpectedWritesTeamParentsAndSkipsInternalSourceConnections(t *testing.T) {
	world := World{
		Teams:   []Team{{ID: "eng", Org: "acme"}, {ID: "payments", Org: "acme", Parent: "eng"}},
		Sources: []Source{{ID: "src_files", Org: "acme", CreatedBy: "sam"}},
	}
	got := strs(Expected(world))
	want := []string{
		"source:src_files#created_by@member:sam",
		"source:src_files#org@org:acme",
		"team:eng#org@org:acme",
		"team:payments#org@org:acme",
		"team:payments#parent@team:eng",
	}
	if !slices.Equal(got, want) {
		t.Errorf("expected = %v, want %v", got, want)
	}
}

func TestDiff(t *testing.T) {
	keep := rel("org", "acme", "member", "member", "sam")
	stale := rel("org", "acme", "admin", "member", "sam")
	missing := rel("org", "acme", "member", "member", "lee")
	remove, add := Diff([]Rel{keep, missing, missing}, []Rel{keep, stale})
	if !slices.Equal(strs(remove), []string{stale.String()}) || !slices.Equal(strs(add), []string{missing.String()}) {
		t.Errorf("remove = %v, add = %v", strs(remove), strs(add))
	}
}

func TestSlicesSkipWhatWasNotRead(t *testing.T) {
	world := World{Read: map[string]bool{"agents": true}}
	for _, owned := range ownedSlices {
		ready := owned.ready(world)
		wantReady := slices.Equal(owned.needs, []string{"agents"})
		if ready != wantReady {
			t.Errorf("%s %v ready = %v", owned.resourceType, owned.relations, ready)
		}
	}
}
