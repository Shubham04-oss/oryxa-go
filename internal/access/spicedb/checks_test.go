package spicedb

import (
	"reflect"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/access"
)

func TestChecksAsksAnAgentForItsMembersReachOnly(t *testing.T) {
	corpus := Object{"corpus", "cor_1"}
	connection := Object{"connection", "con_1"}
	sam := Object{"member", "u1"}
	agent := Object{"agent", "agt_1"}
	admin := access.Actor{Role: access.Admin}
	primary := access.Actor{Role: access.Admin, Agent: true, AgentRole: access.AgentPrimary}
	specialist := access.Actor{Role: access.Admin, Agent: true, AgentRole: access.AgentSpecialist}
	for _, c := range []struct {
		name     string
		actor    access.Actor
		action   string
		resource Object
		limited  bool
		want     []Check
	}{
		{"own session asks the full permission", admin, access.ActionRead, corpus, false,
			[]Check{{corpus, "read", sam}}},
		{"search is read", admin, access.ActionSearch, corpus, false,
			[]Check{{corpus, "read", sam}}},
		{"an agent of an admin asks member reach", primary, access.ActionDelete, corpus, false,
			[]Check{{corpus, "delete_member_reach", sam}}},
		{"agent with no role is the primary", access.Actor{Role: access.Owner, Agent: true}, access.ActionRead, corpus, false,
			[]Check{{corpus, "read_member_reach", sam}}},
		{"an unlimited specialist asks no grant", specialist, access.ActionWrite, corpus, false,
			[]Check{{corpus, "write_member_reach", sam}}},
		{"a limited specialist also needs granted", specialist, access.ActionWrite, corpus, true,
			[]Check{{corpus, "write_member_reach", sam}, {corpus, "granted", agent}}},
		{"an app write needs granted_write", specialist, access.ActionWrite, connection, true,
			[]Check{{connection, "use", sam}, {connection, "granted_write", agent}}},
		{"an app read needs granted", specialist, access.ActionRead, connection, true,
			[]Check{{connection, "use", sam}, {connection, "granted", agent}}},
		{"acting through a connection needs granted", specialist, access.ActionUse, connection, true,
			[]Check{{connection, "use", sam}, {connection, "granted", agent}}},
		{"teams have no grant", specialist, ActionManage, Object{"team", "t1"}, true,
			[]Check{{Object{"team", "t1"}, "manage_member_reach", sam}}},
	} {
		got, ok := Checks(c.actor, "u1", "agt_1", c.action, c.resource, c.limited)
		if !ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: Checks = %v, %v; want %v", c.name, got, ok, c.want)
		}
	}
}

func TestChecksHasNoAnswerOutsideTheSchema(t *testing.T) {
	member := access.Actor{Role: access.Member}
	for _, c := range []struct {
		name     string
		actor    access.Actor
		action   string
		resource Object
	}{
		{"org agent", access.Actor{Role: access.Member, Agent: true, AgentRole: access.AgentOrganization}, access.ActionRead, Object{"corpus", "c"}},
		{"unknown agent role", access.Actor{Role: access.Member, Agent: true, AgentRole: "robot"}, access.ActionRead, Object{"corpus", "c"}},
		{"unknown kind", member, access.ActionRead, Object{"request", "r"}},
		{"unknown action", member, "fly", Object{"corpus", "c"}},
		{"files are not written in place", member, access.ActionWrite, Object{"file", "f"}},
		{"an agent removing a source", access.Actor{Role: access.Member, Agent: true}, access.ActionDelete, Object{"source", "s"}},
	} {
		if got, ok := Checks(c.actor, "u1", "agt_1", c.action, c.resource, false); ok {
			t.Errorf("%s: Checks = %v, want no answer", c.name, got)
		}
	}
	if _, ok := Checks(access.Actor{Role: access.Member, Agent: true, AgentRole: access.AgentSpecialist}, "u1", "", access.ActionRead, Object{"corpus", "c"}, true); ok {
		t.Error("a limited specialist with no agent id has an answer")
	}
}
