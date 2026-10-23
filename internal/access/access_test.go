package access

import "testing"

func TestActsAsAdminOnlyFromAnAdminsOwnSession(t *testing.T) {
	for _, c := range []struct {
		actor Actor
		want  bool
	}{
		{Actor{Role: Owner}, true},
		{Actor{Role: Admin}, true},
		{Actor{Role: Member}, false},
		{Actor{Role: "guest"}, false},
		{Actor{Role: Owner, Agent: true}, false},
		{Actor{Role: Admin, Agent: true, AgentRole: AgentPrimary}, false},
		{Actor{Role: Admin, Agent: true, AgentRole: AgentOrganization}, false},
	} {
		if got := c.actor.ActsAsAdmin(); got != c.want {
			t.Errorf("%+v ActsAsAdmin = %v, want %v", c.actor, got, c.want)
		}
	}
	for role, valid := range map[Role]bool{Owner: true, Admin: true, Member: true, "": false, "guest": false} {
		if role.Valid() != valid {
			t.Errorf("%q Valid = %v", role, !valid)
		}
	}
}

func TestOnlyASpecialistIsNarrowedByItsGrant(t *testing.T) {
	specialist := Actor{Role: Admin, Agent: true, AgentRole: AgentSpecialist}
	for _, c := range []struct {
		name  string
		actor Actor
		grant []string
		id    string
		want  bool
	}{
		{"missing list is all", specialist, nil, "c1", true},
		{"star is all", specialist, []string{All}, "c1", true},
		{"named", specialist, []string{"c1"}, "c1", true},
		{"not named", specialist, []string{"c1"}, "c2", false},
		{"empty is none", specialist, []string{}, "c1", false},
		{"empty id never matches", specialist, []string{"c1"}, "", false},
		{"member", Actor{Role: Member}, []string{}, "c1", true},
		{"primary", Actor{Role: Member, Agent: true, AgentRole: AgentPrimary}, []string{}, "c1", true},
		{"agent with no role is the primary", Actor{Role: Member, Agent: true}, []string{}, "c1", true},
		{"organization agent", Actor{Role: Member, Agent: true, AgentRole: AgentOrganization}, []string{}, "c1", true},
	} {
		if got := c.actor.Reaches(c.grant, c.id); got != c.want {
			t.Errorf("%s: Reaches = %v, want %v", c.name, got, c.want)
		}
	}
	if specialist.Limited(nil) || specialist.Limited([]string{All, "c1"}) || !specialist.Limited([]string{}) {
		t.Error("Limited disagrees with Reaches")
	}
}

func TestTeamReachGivesAnAgentItsMembersOwnPlace(t *testing.T) {
	agent := Actor{Role: Admin, Agent: true}
	person := Actor{Role: Admin}
	for _, c := range []struct {
		name                                 string
		actor                                Actor
		canSee, member, teamAdmin, canManage bool
		visible, manage                      bool
	}{
		{"unseen is unseen", person, false, false, false, true, false, false},
		{"admin sees and manages any team", person, true, false, false, true, true, true},
		{"person keeps teams' answer", person, true, true, false, false, true, false},
		{"agent of an admin, member not on team", agent, true, false, false, true, false, false},
		{"agent of an admin, member on team", agent, true, true, false, true, true, false},
		{"agent, member is team admin", agent, true, true, true, true, true, true},
		{"agent never sees an unseen team", agent, false, true, true, true, false, false},
	} {
		visible, manage := c.actor.TeamReach(c.canSee, c.member, c.teamAdmin, c.canManage)
		if visible != c.visible || manage != c.manage {
			t.Errorf("%s: TeamReach = %v, %v; want %v, %v", c.name, visible, manage, c.visible, c.manage)
		}
	}
}
