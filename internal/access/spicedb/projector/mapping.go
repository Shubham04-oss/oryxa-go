// Package projector keeps SpiceDB's relationships in step with the services
// that own the data (docs/design/permissions, §6 step 4). It runs in shadow:
// the code in internal/access still decides, so nothing here changes what
// any service answers.
//
// Two paths write. A consumer turns each source event into a Change (the
// pure functions in this file) and applies it; a nightly reconcile reads
// every source of truth, compares it with what SpiceDB holds and repairs the
// difference. Removals always go first, so a failure part way leaves access
// missing, never left over.
package projector

import (
	"fmt"
	"strings"
)

// Rel is one relationship: resource#relation@subject. Ids are the services'
// own; no relationship this package writes has a caveat or a subject relation.
type Rel struct {
	ResourceType, ResourceID, Relation, SubjectType, SubjectID string
}

func (rel Rel) String() string {
	return rel.ResourceType + ":" + rel.ResourceID + "#" + rel.Relation + "@" + rel.SubjectType + ":" + rel.SubjectID
}

// Filter matches relationships to delete. ResourceType is required; an empty
// field matches anything. With every field set it names one relationship.
type Filter Rel

func (filter Filter) exact() bool {
	return filter.ResourceID != "" && filter.Relation != "" && filter.SubjectType != "" && filter.SubjectID != ""
}

// Change is what one event does to SpiceDB: Delete first, then Touch.
// Organization is whose ZedToken the write moves; empty for none.
type Change struct {
	Organization string
	Delete       []Filter
	Touch        []Rel
}

func (change Change) Empty() bool { return len(change.Delete) == 0 && len(change.Touch) == 0 }

func rel(resourceType, resourceID, relation, subjectType, subjectID string) Rel {
	return Rel{resourceType, resourceID, relation, subjectType, subjectID}
}

// orgRoles are auth's roles, each its own relation on org.
var orgRoles = []string{"owner", "admin", "member"}

func validOrgRole(role string) bool { return role == "owner" || role == "admin" || role == "member" }

// MemberJoined: auth adds a member with a role.
func MemberJoined(org, user, role string) (Change, error) {
	if org == "" || user == "" || !validOrgRole(role) {
		return Change{}, fmt.Errorf("member joined: organization %q, user %q, role %q", org, user, role)
	}
	return Change{Organization: org, Touch: []Rel{rel("org", org, role, "member", user)}}, nil
}

// MemberRoleChanged: the old role goes before the new one is written. The
// write is atomic, so the member is never seen with both or neither.
func MemberRoleChanged(org, user, role string) (Change, error) {
	change, err := MemberJoined(org, user, role)
	if err != nil {
		return Change{}, err
	}
	for _, old := range orgRoles {
		if old != role {
			change.Delete = append(change.Delete, Filter(rel("org", org, old, "member", user)))
		}
	}
	return change, nil
}

// MemberRemoved: every role goes, whatever the event says was the last one.
// The member's team places, chart edges and agents leave by their own
// services' events, or by the reconcile.
func MemberRemoved(org, user string) (Change, error) {
	if org == "" || user == "" {
		return Change{}, fmt.Errorf("member removed: organization %q, user %q", org, user)
	}
	change := Change{Organization: org}
	for _, role := range orgRoles {
		change.Delete = append(change.Delete, Filter(rel("org", org, role, "member", user)))
	}
	change.Delete = append(change.Delete, Filter(rel("org", org, "executive", "member", user)))
	return change, nil
}

// orgSubjects are the types that point at an organization.
var orgSubjects = []string{"member", "chart_person", "team", "agent", "corpus", "file", "source", "request"}

// OrganizationDeleted drops the org and every arrow into it. What hangs on
// the org's teams, corpora and files goes when their own deletions arrive.
func OrganizationDeleted(org string) (Change, error) {
	if org == "" {
		return Change{}, fmt.Errorf("organization deleted: no organization")
	}
	change := Change{Organization: org, Delete: []Filter{{ResourceType: "org", ResourceID: org}}}
	for _, resourceType := range orgSubjects {
		change.Delete = append(change.Delete, Filter{ResourceType: resourceType, SubjectType: "org", SubjectID: org})
	}
	return change, nil
}

// TeamCreated hangs the team on its org. Its parent is not on the event, so
// the reconcile writes it.
func TeamCreated(org, team string) (Change, error) {
	if org == "" || team == "" {
		return Change{}, fmt.Errorf("team created: organization %q, team %q", org, team)
	}
	return Change{Organization: org, Touch: []Rel{rel("team", team, "org", "org", org)}}, nil
}

// TeamPlaced is a team created, with its parent when it has one. Teams knows
// the parent as it creates the team; the event doesn't carry it.
func TeamPlaced(org, team, parent string) (Change, error) {
	change, err := TeamCreated(org, team)
	if err != nil || parent == "" {
		return change, err
	}
	change.Touch = append(change.Touch, rel("team", team, "parent", "team", parent))
	return change, nil
}

// TeamDeleted drops the team, its members and its place under a parent. A
// subteam's arrow to it goes with the subteam's own deletion.
func TeamDeleted(org, team string) (Change, error) {
	if org == "" || team == "" {
		return Change{}, fmt.Errorf("team deleted: organization %q, team %q", org, team)
	}
	return Change{Organization: org, Delete: []Filter{{ResourceType: "team", ResourceID: team}}}, nil
}

// teamRelation is a team role's relation: a team admin is its lead.
func teamRelation(role string) (string, bool) {
	switch role {
	case "admin":
		return "lead", true
	case "member":
		return "member", true
	}
	return "", false
}

// TeamMemberSet is a team member added or their role changed: the other
// relation goes, then theirs is written.
func TeamMemberSet(org, team, user, role string) (Change, error) {
	relation, ok := teamRelation(role)
	if org == "" || team == "" || user == "" || !ok {
		return Change{}, fmt.Errorf("team member: organization %q, team %q, user %q, role %q", org, team, user, role)
	}
	other := "member"
	if relation == "member" {
		other = "lead"
	}
	return Change{
		Organization: org,
		Delete:       []Filter{Filter(rel("team", team, other, "member", user))},
		Touch:        []Rel{rel("team", team, relation, "member", user)},
	}, nil
}

// TeamMemberRemoved drops both of the member's relations on the team.
func TeamMemberRemoved(org, team, user string) (Change, error) {
	if org == "" || team == "" || user == "" {
		return Change{}, fmt.Errorf("team member removed: organization %q, team %q, user %q", org, team, user)
	}
	return Change{Organization: org, Delete: []Filter{
		Filter(rel("team", team, "lead", "member", user)),
		Filter(rel("team", team, "member", "member", user)),
	}}, nil
}

// Placement is where a corpus or file hangs, as rag and files keep it.
type Placement struct {
	Org, Scope, Team, Member string
	// Access is a file's: "admins" makes an org file admins only.
	Access    string
	CreatedBy string
}

// placed is the org, scope and creator relationships of a corpus or file.
func placed(resourceType, id string, placement Placement) ([]Rel, error) {
	rels := []Rel{rel(resourceType, id, "org", "org", placement.Org)}
	switch {
	case placement.Scope == "member" && placement.Member != "":
		rels = append(rels, rel(resourceType, id, "owner", "member", placement.Member))
	case placement.Scope == "team" && placement.Team != "":
		rels = append(rels, rel(resourceType, id, "team", "team", placement.Team))
	case placement.Scope == "organization" && resourceType == "file" && placement.Access == "admins":
		rels = append(rels, rel(resourceType, id, "admins_only", "org", placement.Org))
	case placement.Scope == "organization":
		rels = append(rels, rel(resourceType, id, "everyone", "org", placement.Org))
	default:
		return nil, fmt.Errorf("%s %s: scope %q with team %q and member %q", resourceType, id, placement.Scope, placement.Team, placement.Member)
	}
	if placement.CreatedBy != "" {
		rels = append(rels, rel(resourceType, id, "created_by", "member", placement.CreatedBy))
	}
	return rels, nil
}

// Placed is a corpus (rag) or file (files) created.
func Placed(resourceType, id string, placement Placement) (Change, error) {
	if placement.Org == "" || id == "" {
		return Change{}, fmt.Errorf("%s created: organization %q, id %q", resourceType, placement.Org, id)
	}
	rels, err := placed(resourceType, id, placement)
	if err != nil {
		return Change{}, err
	}
	return Change{Organization: placement.Org, Touch: rels}, nil
}

// SourceRegistered is a knowledge source added. Knowledge publishes no event
// for it; the reconcile writes the same from the table (Expected).
func SourceRegistered(org, id, connection, createdBy string) (Change, error) {
	if org == "" || id == "" {
		return Change{}, fmt.Errorf("source registered: organization %q, id %q", org, id)
	}
	rels := []Rel{rel("source", id, "org", "org", org)}
	if connection != "" {
		rels = append(rels, rel("source", id, "connection", "connection", connection))
	}
	if createdBy != "" {
		rels = append(rels, rel("source", id, "created_by", "member", createdBy))
	}
	return Change{Organization: org, Touch: rels}, nil
}

// Deleted drops a resource and everything on it, grants included.
func Deleted(resourceType, org, id string) (Change, error) {
	if id == "" {
		return Change{}, fmt.Errorf("%s deleted: no id", resourceType)
	}
	return Change{Organization: org, Delete: []Filter{{ResourceType: resourceType, ResourceID: id}}}, nil
}

// ConnectionOwned is an apps connection started or connected.
func ConnectionOwned(org, connection, member string) (Change, error) {
	if org == "" || connection == "" || member == "" {
		return Change{}, fmt.Errorf("connection: organization %q, id %q, member %q", org, connection, member)
	}
	return Change{Organization: org, Touch: []Rel{
		rel("connection", connection, "org", "org", org),
		rel("connection", connection, "owner", "member", member),
	}}, nil
}

// AgentChanged is a primary or specialist created or updated. Its grant is
// not on the event; the consumer reads it from agents (see grantSlice).
func AgentChanged(org, agent, user string) (Change, error) {
	if org == "" || agent == "" || user == "" {
		return Change{}, fmt.Errorf("agent: organization %q, agent %q, user %q", org, agent, user)
	}
	return Change{Organization: org, Touch: []Rel{
		rel("agent", agent, "acts_for", "member", user),
		rel("agent", agent, "org", "org", org),
	}}, nil
}

// grantedTypes are what a specialist's grant names.
var grantedTypes = []string{"corpus", "connection", "file", "source"}

// AgentDeleted drops the agent and every grant naming it. Requests keep it,
// as the ledger does.
func AgentDeleted(org, agent string) (Change, error) {
	if agent == "" {
		return Change{}, fmt.Errorf("agent deleted: no id")
	}
	change := Change{Organization: org, Delete: []Filter{{ResourceType: "agent", ResourceID: agent}}}
	for _, resourceType := range grantedTypes {
		change.Delete = append(change.Delete, Filter{ResourceType: resourceType, SubjectType: "agent", SubjectID: agent})
	}
	return change, nil
}

// Request is one interaction's parties.
type Request struct {
	ID, Org, RequesterUser, RequesterAgent, TargetUser, TargetAgent string
}

func requestRels(request Request) []Rel {
	rels := []Rel{rel("request", request.ID, "org", "org", request.Org)}
	add := func(relation, subjectType, id string) {
		if strings.TrimSpace(id) != "" {
			rels = append(rels, rel("request", request.ID, relation, subjectType, id))
		}
	}
	add("requester", "member", request.RequesterUser)
	add("requester", "agent", request.RequesterAgent)
	add("target", "member", request.TargetUser)
	add("target_agent", "agent", request.TargetAgent)
	return rels
}

// RequestMade is an interaction requested. Its parties never change.
func RequestMade(request Request) (Change, error) {
	if request.ID == "" || request.Org == "" {
		return Change{}, fmt.Errorf("request: id %q, organization %q", request.ID, request.Org)
	}
	return Change{Organization: request.Org, Touch: requestRels(request)}, nil
}
