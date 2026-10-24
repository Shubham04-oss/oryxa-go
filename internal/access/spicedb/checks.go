package spicedb

import "github.com/Shubham04-oss/oryxa-go/internal/access"

// Object is one SpiceDB object, such as {"corpus", "cor_1"}, {"member", "u1"}
// or {"agent", "agt_1"}.
type Object struct{ Type, ID string }

// Check is one CheckPermission: may Subject have Permission on Resource.
type Check struct {
	Resource   Object
	Permission string
	Subject    Object
}

// ActionManage changes a team or a source, beside access's read, search,
// write and delete.
const ActionManage = "manage"

// permissions maps a resource type and an action to the permission a
// member's own session asks. An agent asks the same name with
// "_member_reach", which carries no admin arrow; a type in sameForAgents has
// no admin arrow to drop.
var permissions = map[string]map[string]string{
	"corpus":     {access.ActionRead: "read", access.ActionSearch: "read", access.ActionWrite: "write", access.ActionDelete: "delete"},
	"file":       {access.ActionRead: "read", access.ActionSearch: "read", access.ActionDelete: "delete"},
	"team":       {access.ActionRead: "see", access.ActionWrite: "manage", access.ActionDelete: "manage", ActionManage: "manage"},
	"source":     {access.ActionRead: "manage", access.ActionSearch: "manage", access.ActionWrite: "manage", access.ActionDelete: "manage", ActionManage: "manage"},
	"connection": {access.ActionRead: "read", access.ActionSearch: "read", access.ActionWrite: "use", access.ActionUse: "use", access.ActionDelete: "delete"},
}

var sameForAgents = map[string]bool{"connection#use": true}

// ungranted are what a specialist does without its grant: apps' grant gates
// acting through a connection, not seeing that its member has one.
var ungranted = map[string]bool{"connection#read": true, "connection#delete": true}

// grants is the relation a limited specialist needs on the resource, by
// type. Only apps grants writes apart from reads; rag, files and knowledge
// let a specialist write wherever its grant lets it read.
var grants = map[string]bool{"corpus": true, "file": true, "source": true, "connection": true}

// Checks returns the checks that must all pass for actor to do action on
// resource. ok is false when SpiceDB has no answer for this case (the org
// agent, an unknown kind), and the caller then skips the comparison.
// limited is access.Actor.Limited(grant) for the grant that applies.
func Checks(actor access.Actor, userID, agentID, action string, resource Object, limited bool) (checks []Check, ok bool) {
	permission, ok := permissions[resource.Type][action]
	if !ok || userID == "" {
		return nil, false
	}
	if actor.Agent {
		switch actor.AgentRole {
		case "", access.AgentPrimary, access.AgentSpecialist:
		default: // the org agent has its own rules, and an unknown role none
			return nil, false
		}
		// knowledge refuses an agent removing or reconfiguring a source by
		// its kind, not by any relation
		if resource.Type == "source" && action != access.ActionRead && action != access.ActionSearch {
			return nil, false
		}
	}
	key := resource.Type + "#" + permission
	if actor.Agent && !sameForAgents[key] {
		permission += "_member_reach"
	}
	checks = []Check{{Resource: resource, Permission: permission, Subject: Object{"member", userID}}}
	if limited && grants[resource.Type] && !ungranted[key] {
		if agentID == "" {
			return nil, false
		}
		grant := "granted"
		if resource.Type == "connection" && action == access.ActionWrite {
			grant = "granted_write"
		}
		checks = append(checks, Check{Resource: resource, Permission: grant, Subject: Object{"agent", agentID}})
	}
	return checks, true
}
