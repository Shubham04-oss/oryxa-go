package spicedb

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"

	"github.com/Shubham04-oss/oryxa-go/internal/access"
)

// The replay proves schema.zed answers as today's code does. One small org is
// written into SpiceDB; then every actor (each member's own session and each
// kind of agent acting for them) tries every action on every resource, and
// Checks' answer from SpiceDB must equal today's, which the functions below
// state rule by rule from each service's code, with access.Actor deciding
// what it decides there.
//
// Acme: Olu owns it and Maya is an admin; Sam, Lia and Lee are members. Lia
// leads Eng, which Sam is on; Lee and Maya are on Ops.

var (
	roles = map[string]access.Role{"olu": access.Owner, "maya": access.Admin, "sam": access.Member, "lia": access.Member, "lee": access.Member}
	// team → members, and the leads (teams' admins) among them
	teamMembers = map[string][]string{"eng": {"sam", "lia"}, "ops": {"lee", "maya"}}
	teamLeads   = map[string][]string{"eng": {"lia"}}
)

type scope string

const (
	memberScope scope = "member"
	teamScope   scope = "team"
	orgScope    scope = "organization"
)

// placed is a corpus or file: where it hangs and who made it.
type placed struct {
	id, owner, team, createdBy string
	scope                      scope
	adminsOnly                 bool // files: access "admins"
}

var (
	corpora = []placed{
		{id: "cor_sam", scope: memberScope, owner: "sam", createdBy: "sam"},
		{id: "cor_eng", scope: teamScope, team: "eng", createdBy: "sam"},
		{id: "cor_eng_lee", scope: teamScope, team: "eng", createdBy: "lee"}, // its creator is not on the team
		{id: "cor_ops", scope: teamScope, team: "ops", createdBy: "lee"},
		{id: "cor_org", scope: orgScope, createdBy: "lee"},
	}
	files = []placed{
		{id: "f_sam", scope: memberScope, owner: "sam", createdBy: "sam"},
		{id: "f_sam_lee", scope: memberScope, owner: "sam", createdBy: "lee"},
		{id: "f_eng", scope: teamScope, team: "eng", createdBy: "sam"},
		{id: "f_eng_lee", scope: teamScope, team: "eng", createdBy: "lee"},
		{id: "f_org", scope: orgScope, createdBy: "lee"},
		{id: "f_board", scope: orgScope, adminsOnly: true, createdBy: "maya"},
	}
	sources     = map[string]string{"src_sam": "sam", "src_lee": "lee"} // → creator
	connections = map[string]string{"con_sam": "sam", "con_lee": "lee"} // → owner
)

// variant is one way of acting for a member.
type variant struct {
	name    string
	agent   bool
	role    string
	limited bool
	// granted and grantedWrite: the grant names every resource, for reads and
	// for app writes
	granted, grantedWrite bool
}

var variants = []variant{
	{name: "own"},
	{name: "primary", agent: true, role: access.AgentPrimary},
	{name: "norole", agent: true},
	{name: "spec", agent: true, role: access.AgentSpecialist},
	{name: "spec_in", agent: true, role: access.AgentSpecialist, limited: true, granted: true, grantedWrite: true},
	{name: "spec_ro", agent: true, role: access.AgentSpecialist, limited: true, granted: true},
	{name: "spec_out", agent: true, role: access.AgentSpecialist, limited: true},
	{name: "org", agent: true, role: access.AgentOrganization},
}

func agentID(user string, v variant) string { return "agt_" + user + "_" + v.name }

func onTeam(user, team string) bool { return slices.Contains(teamMembers[team], user) }
func leads(user, team string) bool  { return slices.Contains(teamLeads[team], user) }

// caller is one actor as the services see it.
type caller struct {
	user  string
	v     variant
	actor access.Actor
}

func (c caller) admin() bool { return c.actor.ActsAsAdmin() }

// reach is the grant: a limited specialist reaches only what it names.
func (c caller) reach() bool { return !c.v.limited || c.v.granted }

// team is teamAccess in rag and files: teams answers the token as its member,
// then access.Actor.TeamReach narrows it for an agent.
func (c caller) team(team string) (visible, manage bool) {
	orgAdmin := roles[c.user].IsOrgAdmin()
	return c.actor.TeamReach(orgAdmin || onTeam(c.user, team), onTeam(c.user, team), leads(c.user, team), orgAdmin || leads(c.user, team))
}

// rag: reads.mayRead, then Service.mayWrite or mayDelete
// (rag/internal/application/reads.go, service.go); every write and delete
// first reads the corpus.
func (c caller) corpus(corpus placed, action string) bool {
	visible, manage := c.team(corpus.team)
	mine := corpus.scope == memberScope && corpus.owner == c.user
	read := c.reach() && (corpus.scope == orgScope || mine || c.admin() || (corpus.scope == teamScope && visible))
	switch action {
	case access.ActionRead, access.ActionSearch:
		return read
	case access.ActionWrite:
		return read && (mine || c.admin() || (corpus.scope == teamScope && visible))
	case access.ActionDelete:
		return read && (mine || c.admin() || (corpus.scope == teamScope && visible && (corpus.createdBy == c.user || manage)))
	}
	panic(action)
}

// files: Service.canSee, then canDelete (files/internal/application/service.go).
func (c caller) file(file placed, action string) bool {
	visible, manage := c.team(file.team)
	var see bool
	switch file.scope {
	case memberScope:
		see = file.owner == c.user || c.admin()
	case orgScope:
		see = !file.adminsOnly || c.admin()
	case teamScope:
		see = c.admin() || visible
	}
	see = see && c.reach()
	if action != access.ActionDelete {
		return see
	}
	switch {
	case c.admin():
		return see
	case file.scope == memberScope:
		return see && (file.owner == c.user || file.createdBy == c.user)
	case file.scope == teamScope:
		return see && (file.createdBy == c.user || manage)
	}
	return false
}

// knowledge: mayRead (knowledge/internal/application/sources.go). RemoveSource
// refuses every agent, which Checks leaves to the code.
func (c caller) source(creator string) bool {
	return c.reach() && (creator == c.user || c.admin())
}

// apps: a member's own connections, and MayReach/MayRun for a specialist
// (apps/internal/domain/domain.go).
func (c caller) connection(owner, action string) bool {
	if action == access.ActionWrite {
		return owner == c.user && (!c.v.limited || c.v.grantedWrite)
	}
	return owner == c.user && c.reach()
}

// teams: CanSeeTeam and CanManageTeam (teams/internal/domain/control.go),
// narrowed for an agent by TeamReach.
func (c caller) teamAction(team, action string) bool {
	visible, manage := c.team(team)
	if action == access.ActionRead {
		return visible
	}
	return manage
}

type replayCase struct {
	service  string
	caller   caller
	action   string
	resource Object
	want     bool
}

func replayCases() []replayCase {
	var cases []replayCase
	add := func(service string, c caller, action string, resource Object, want bool) {
		cases = append(cases, replayCase{service, c, action, resource, want})
	}
	for _, user := range []string{"olu", "maya", "sam", "lia", "lee"} {
		for _, v := range variants {
			c := caller{user: user, v: v, actor: access.Actor{Role: roles[user], Agent: v.agent, AgentRole: v.role}}
			for _, corpus := range corpora {
				for _, action := range []string{access.ActionRead, access.ActionSearch, access.ActionWrite, access.ActionDelete} {
					add("rag", c, action, Object{"corpus", corpus.id}, c.corpus(corpus, action))
				}
			}
			for _, file := range files {
				for _, action := range []string{access.ActionRead, access.ActionSearch, access.ActionDelete} {
					add("files", c, action, Object{"file", file.id}, c.file(file, action))
				}
			}
			for id, creator := range sources {
				for _, action := range []string{access.ActionRead, access.ActionDelete} {
					add("knowledge", c, action, Object{"source", id}, c.source(creator))
				}
			}
			for id, owner := range connections {
				for _, action := range []string{access.ActionRead, access.ActionWrite} {
					add("apps", c, action, Object{"connection", id}, c.connection(owner, action))
				}
			}
			for team := range teamMembers {
				for _, action := range []string{access.ActionRead, ActionManage} {
					add("teams", c, action, Object{"team", team}, c.teamAction(team, action))
				}
			}
		}
	}
	return cases
}

// world is the org as relationships, the way the projector writes them.
func world() []string {
	var rels []string
	rel := func(format string, args ...any) { rels = append(rels, fmt.Sprintf(format, args...)) }
	for user, role := range roles {
		rel("org:acme#%s@member:%s", role, user)
		for _, v := range variants {
			if v.agent {
				rel("agent:%s#acts_for@member:%s", agentID(user, v), user)
				rel("agent:%s#org@org:acme", agentID(user, v))
			}
		}
	}
	for team, members := range teamMembers {
		rel("team:%s#org@org:acme", team)
		for _, member := range members {
			rel("team:%s#member@member:%s", team, member)
		}
		for _, lead := range teamLeads[team] {
			rel("team:%s#lead@member:%s", team, lead)
		}
	}
	place := func(kind string, p placed) {
		rel("%s:%s#org@org:acme", kind, p.id)
		rel("%s:%s#created_by@member:%s", kind, p.id, p.createdBy)
		switch {
		case p.scope == memberScope:
			rel("%s:%s#owner@member:%s", kind, p.id, p.owner)
		case p.scope == teamScope:
			rel("%s:%s#team@team:%s", kind, p.id, p.team)
		case p.adminsOnly:
			rel("%s:%s#admins_only@org:acme", kind, p.id)
		default:
			rel("%s:%s#everyone@org:acme", kind, p.id)
		}
	}
	for _, corpus := range corpora {
		place("corpus", corpus)
	}
	for _, file := range files {
		place("file", file)
	}
	for id, creator := range sources {
		rel("source:%s#org@org:acme", id)
		rel("source:%s#created_by@member:%s", id, creator)
	}
	for id, owner := range connections {
		rel("connection:%s#owner@member:%s", id, owner)
	}
	// grants name every resource for the variants that have them
	var resources []string
	for _, corpus := range corpora {
		resources = append(resources, "corpus:"+corpus.id)
	}
	for _, file := range files {
		resources = append(resources, "file:"+file.id)
	}
	for id := range sources {
		resources = append(resources, "source:"+id)
	}
	for id := range connections {
		resources = append(resources, "connection:"+id)
	}
	for user := range roles {
		for _, v := range variants {
			for _, resource := range resources {
				if v.granted {
					rel("%s#granted@agent:%s", resource, agentID(user, v))
				}
				if v.grantedWrite && strings.HasPrefix(resource, "connection:") {
					rel("%s#granted_write@agent:%s", resource, agentID(user, v))
				}
			}
		}
	}
	return rels
}

// relationship parses "type:id#relation@type:id", with every id prefixed so
// runs against one SpiceDB never share state.
func relationship(prefix, rel string) *v1.Relationship {
	resource, subject, _ := strings.Cut(rel, "@")
	resource, relation, _ := strings.Cut(resource, "#")
	return &v1.Relationship{
		Resource: object(prefix, resource),
		Relation: relation,
		Subject:  &v1.SubjectReference{Object: object(prefix, subject)},
	}
}

func object(prefix, typed string) *v1.ObjectReference {
	kind, id, _ := strings.Cut(typed, ":")
	return &v1.ObjectReference{ObjectType: kind, ObjectId: prefix + id}
}

func TestReplayTodaysAnswersAgainstTheSchema(t *testing.T) {
	config := FromEnv()
	if !config.Enabled() {
		t.Skip("SPICEDB_ENDPOINT is not set")
	}
	client, err := Dial(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	schema, err := os.ReadFile("schema.zed")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.WriteSchema(ctx, &v1.WriteSchemaRequest{Schema: string(schema)}); err != nil {
		t.Fatalf("write schema: %v", err)
	}

	prefix := fmt.Sprintf("r%d_", time.Now().UnixNano())
	var updates []*v1.RelationshipUpdate
	types := map[string]bool{}
	for _, rel := range world() {
		r := relationship(prefix, rel)
		types[r.Resource.ObjectType] = true
		updates = append(updates, &v1.RelationshipUpdate{Operation: v1.RelationshipUpdate_OPERATION_CREATE, Relationship: r})
	}
	if _, err := client.WriteRelationships(ctx, &v1.WriteRelationshipsRequest{Updates: updates}); err != nil {
		t.Fatalf("write relationships: %v", err)
	}
	t.Cleanup(func() {
		for kind := range types {
			_, err := client.DeleteRelationships(context.Background(), &v1.DeleteRelationshipsRequest{
				RelationshipFilter: &v1.RelationshipFilter{ResourceType: kind, OptionalResourceIdPrefix: prefix},
			})
			if err != nil {
				t.Errorf("clean up %s: %v", kind, err)
			}
		}
	})

	full := &v1.Consistency{Requirement: &v1.Consistency_FullyConsistent{FullyConsistent: true}}
	type asked struct {
		replayCase
		name   string
		checks []Check
	}
	var cases []asked
	var items []*v1.CheckBulkPermissionsRequestItem
	counts, skipped := map[string]int{}, map[string]int{}
	for _, c := range replayCases() {
		name := fmt.Sprintf("%s: %s/%s %s %s:%s", c.service, c.caller.user, c.caller.v.name, c.action, c.resource.Type, c.resource.ID)
		checks, ok := Checks(c.caller.actor, c.caller.user, agentID(c.caller.user, c.caller.v), c.action, c.resource, c.caller.v.limited)
		if !ok {
			// SpiceDB answers nothing for the org agent, nor for an agent
			// changing a source, which knowledge refuses by kind
			if c.caller.v.name != "org" && !(c.resource.Type == "source" && c.action == access.ActionDelete && c.caller.v.agent) {
				t.Errorf("%s: Checks has no answer", name)
			}
			skipped[c.service]++
			continue
		}
		cases = append(cases, asked{c, name, checks})
		for _, check := range checks {
			items = append(items, &v1.CheckBulkPermissionsRequestItem{
				Resource:   &v1.ObjectReference{ObjectType: check.Resource.Type, ObjectId: prefix + check.Resource.ID},
				Permission: check.Permission,
				Subject:    &v1.SubjectReference{Object: &v1.ObjectReference{ObjectType: check.Subject.Type, ObjectId: prefix + check.Subject.ID}},
			})
		}
	}
	var answers []bool
	for len(items) > 0 {
		batch := items[:min(len(items), 500)]
		items = items[len(batch):]
		response, err := client.CheckBulkPermissions(ctx, &v1.CheckBulkPermissionsRequest{Consistency: full, Items: batch})
		if err != nil {
			t.Fatalf("check: %v", err)
		}
		for _, pair := range response.Pairs {
			if pair.GetError() != nil {
				t.Fatalf("check %v: %v", pair.Request, pair.GetError())
			}
			answers = append(answers, pair.GetItem().Permissionship == v1.CheckPermissionResponse_PERMISSIONSHIP_HAS_PERMISSION)
		}
	}
	for _, c := range cases {
		got := !slices.Contains(answers[:len(c.checks)], false)
		answers = answers[len(c.checks):]
		if got != c.want {
			t.Errorf("%s: SpiceDB says %v, today's code %v (checks %v)", c.name, got, c.want, c.checks)
		}
		counts[c.service]++
	}
	t.Logf("compared %v; skipped %v", counts, skipped)
}
