package projector

import (
	"slices"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
)

// World is every source of truth, as the reconcile read it. Read names the
// sources that answered; a slice whose sources did not is left alone, so a
// service that is down or not migrated never has its relationships deleted.
type World struct {
	Read        map[string]bool
	Memberships []Membership
	Teams       []Team
	TeamMembers []TeamMember
	Corpora     []PlacedRow
	Files       []PlacedRow
	Sources     []Source
	Connections []Connection
	Agents      []Agent
	Requests    []Request
	Charts      []Chart
}

type Membership struct{ Org, User, Role, Email string }
type Team struct{ ID, Org, Parent string }
type TeamMember struct{ Team, User, Role string }
type PlacedRow struct {
	ID string
	Placement
}
type Source struct{ ID, Org, Connection, CreatedBy string }
type Connection struct{ ID, Org, Member, Toolkit string }

// Agent is one member agent. Grant is a specialist's, nil for a primary and
// for a specialist on the default grant.
type Agent struct {
	ID, Org, User, Kind string
	Grant               *Grant
}

// Grant mirrors agents' runprofile.Grant, the parts SpiceDB holds. A nil
// list, or one holding "*", is everything, and nothing is written for it.
type Grant struct {
	Apps      []string `json:"apps"`
	WriteApps []string `json:"write_apps"`
	Corpora   []string `json:"corpora"`
	Files     []string `json:"files"`
	Knowledge []string `json:"knowledge"`
}

type Chart struct {
	Org   string
	Chart spec.OrgChart
}

func limited(list []string) bool { return list != nil && !slices.Contains(list, "*") }

// slice is part of SpiceDB one set of sources is the truth for.
type slice struct {
	resourceType string
	relations    []string
	needs        []string
}

// ownedSlices are every relationship the projector owns. Leases and standing
// summaries (step 6), system ownership and source#corpus are in none, so the
// reconcile never touches them.
var ownedSlices = []slice{
	{"org", []string{"owner", "admin", "member"}, []string{"auth"}},
	{"org", []string{"executive"}, []string{"auth", "workspace"}},
	{"member", []string{"chart", "manager", "exec_of"}, []string{"auth", "workspace"}},
	{"team", []string{"org", "parent", "lead", "member"}, []string{"teams"}},
	{"agent", []string{"acts_for", "org"}, []string{"agents"}},
	{"corpus", []string{"org", "owner", "team", "everyone", "created_by"}, []string{"rag"}},
	{"corpus", []string{"granted", "granted_write"}, []string{"agents"}},
	{"file", []string{"org", "owner", "team", "everyone", "admins_only", "created_by"}, []string{"files"}},
	{"file", []string{"granted"}, []string{"agents", "files"}},
	{"connection", []string{"org", "owner"}, []string{"apps"}},
	{"connection", []string{"granted", "granted_write"}, []string{"agents", "apps"}},
	{"source", []string{"org", "connection", "created_by"}, []string{"knowledge"}},
	{"source", []string{"granted"}, []string{"agents"}},
	{"request", []string{"org", "requester", "target", "target_agent"}, []string{"agents"}},
}

func (s slice) ready(world World) bool {
	for _, source := range s.needs {
		if !world.Read[source] {
			return false
		}
	}
	return true
}

// Expected is every relationship the sources call for, whether or not its
// slice can be reconciled.
func Expected(world World) []Rel {
	var rels []Rel
	for _, membership := range world.Memberships {
		if validOrgRole(membership.Role) {
			rels = append(rels, rel("org", membership.Org, membership.Role, "member", membership.User))
		}
	}
	for _, team := range world.Teams {
		rels = append(rels, rel("team", team.ID, "org", "org", team.Org))
		if team.Parent != "" {
			rels = append(rels, rel("team", team.ID, "parent", "team", team.Parent))
		}
	}
	for _, member := range world.TeamMembers {
		if relation, ok := teamRelation(member.Role); ok {
			rels = append(rels, rel("team", member.Team, relation, "member", member.User))
		}
	}
	for _, corpus := range world.Corpora {
		placedRels, _ := placed("corpus", corpus.ID, corpus.Placement)
		rels = append(rels, placedRels...)
	}
	for _, file := range world.Files {
		placedRels, _ := placed("file", file.ID, file.Placement)
		rels = append(rels, placedRels...)
	}
	for _, source := range world.Sources {
		rels = append(rels, rel("source", source.ID, "org", "org", source.Org))
		if source.Connection != "" {
			rels = append(rels, rel("source", source.ID, "connection", "connection", source.Connection))
		}
		if source.CreatedBy != "" {
			rels = append(rels, rel("source", source.ID, "created_by", "member", source.CreatedBy))
		}
	}
	for _, connection := range world.Connections {
		rels = append(rels, rel("connection", connection.ID, "org", "org", connection.Org),
			rel("connection", connection.ID, "owner", "member", connection.Member))
	}
	for _, agent := range world.Agents {
		rels = append(rels, rel("agent", agent.ID, "acts_for", "member", agent.User), rel("agent", agent.ID, "org", "org", agent.Org))
		rels = append(rels, grantRels(world, agent)...)
	}
	for _, request := range world.Requests {
		rels = append(rels, requestRels(request)...)
	}
	for _, chart := range world.Charts {
		rels = append(rels, chartRels(chart, world.Memberships)...)
	}
	return rels
}

// grantRels is a specialist's grant spelled out over what it names: corpora
// and sources by id, its member's connections by app, and files by scope.
func grantRels(world World, agent Agent) []Rel {
	grant := agent.Grant
	if agent.Kind != "specialist" || grant == nil {
		return nil
	}
	granted := func(resourceType, id, relation string) Rel { return rel(resourceType, id, relation, "agent", agent.ID) }
	var rels []Rel
	if limited(grant.Corpora) {
		for _, id := range grant.Corpora {
			rels = append(rels, granted("corpus", id, "granted"))
		}
	}
	if limited(grant.Knowledge) {
		for _, id := range grant.Knowledge {
			rels = append(rels, granted("source", id, "granted"))
		}
	}
	for _, connection := range world.Connections {
		if connection.Org != agent.Org || connection.Member != agent.User {
			continue
		}
		reads := !limited(grant.Apps) || slices.Contains(grant.Apps, connection.Toolkit)
		if limited(grant.Apps) && reads {
			rels = append(rels, granted("connection", connection.ID, "granted"))
		}
		// A write is still only in an app it reads.
		if reads && limited(grant.WriteApps) && slices.Contains(grant.WriteApps, connection.Toolkit) {
			rels = append(rels, granted("connection", connection.ID, "granted_write"))
		}
	}
	if limited(grant.Files) {
		for _, file := range world.Files {
			if file.Org != agent.Org {
				continue
			}
			if (file.Scope == "member" && file.Member == agent.User && slices.Contains(grant.Files, "member")) ||
				(file.Scope == "organization" && slices.Contains(grant.Files, "org")) ||
				(file.Scope == "team" && slices.Contains(grant.Files, file.Team)) {
				rels = append(rels, granted("file", file.ID, "granted"))
			}
		}
	}
	return rels
}

// chartRels is the org chart as runtime.Above reads it: everyone it names is
// on the chart, executives are marked, and an edge whose relation is in
// authority makes its from the to's manager. The chart names people by email;
// a person who is not a member of the org is left out.
func chartRels(chart Chart, memberships []Membership) []Rel {
	users := map[string]string{}
	for _, membership := range memberships {
		if membership.Org == chart.Org && membership.Email != "" {
			users[strings.ToLower(membership.Email)] = membership.User
		}
	}
	user := func(email string) string { return users[strings.ToLower(strings.TrimSpace(email))] }
	named := map[string]bool{}
	var rels []Rel
	onChart := func(email string) {
		if id := user(email); id != "" && !named[id] {
			named[id] = true
			rels = append(rels, rel("member", id, "chart", "org", chart.Org))
		}
	}
	for _, email := range chart.Chart.Executives {
		onChart(email)
		if id := user(email); id != "" {
			rels = append(rels, rel("member", id, "exec_of", "org", chart.Org), rel("org", chart.Org, "executive", "member", id))
		}
	}
	for _, relationship := range chart.Chart.Relationships {
		onChart(relationship.From)
		onChart(relationship.To)
		from, to := user(relationship.From), user(relationship.To)
		if from != "" && to != "" && from != to && slices.Contains(chart.Chart.Authority, relationship.Relation) {
			rels = append(rels, rel("member", to, "manager", "member", from))
		}
	}
	return rels
}

// Diff is what to delete and what to write so that actual becomes expected.
func Diff(expected, actual []Rel) (remove, add []Rel) {
	want := map[Rel]bool{}
	for _, rel := range expected {
		want[rel] = true
	}
	have := map[Rel]bool{}
	for _, rel := range actual {
		have[rel] = true
		if !want[rel] {
			remove = append(remove, rel)
		}
	}
	for rel := range want {
		if !have[rel] {
			add = append(add, rel)
		}
	}
	return remove, add
}
