package slackrouter

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter/consentpolicy"
)

// fakeOrgDirectory is Teams' directory as agents reads it: the tree as the
// holder of a token is allowed to see it. It answers whoever presents a token
// it issued, remembers who read, and shows only the people it was given.
type fakeOrgDirectory struct {
	agents.Directory // the searches are not what the router reads
	people           []agents.DirectoryPerson
	err              error
	loads            []chartLoad
}

func (directory *fakeOrgDirectory) Load(_ context.Context, organizationID, token string) (agents.OrganizationDirectory, error) {
	directory.loads = append(directory.loads, chartLoad{organizationID, token})
	if directory.err != nil {
		return agents.OrganizationDirectory{}, directory.err
	}
	return agents.OrganizationDirectory{Organization: agents.DirectoryOrganization{ID: organizationID}, People: directory.people}, nil
}

func directoryPerson(userID string, teams ...string) agents.DirectoryPerson {
	person := agents.DirectoryPerson{UserID: userID, Email: userID + "@acme.com", OrgRole: "member", Teams: []agents.DirectoryTeamRef{}}
	for _, name := range teams {
		person.Teams = append(person.Teams, agents.DirectoryTeamRef{ID: "team_" + name, Name: name, Path: "org.team_" + name, Role: "member"})
	}
	return person
}

// TestTheDirectoryIsReadAsTheAsker: the token is the asker's own, minted for
// them alone, the directory is the organization's, and what comes back is the
// teams of the people asked about and no one else.
func TestTheDirectoryIsReadAsTheAsker(t *testing.T) {
	directory := &fakeOrgDirectory{people: []agents.DirectoryPerson{
		directoryPerson("usr_ada", "Platform", "Infra"),
		directoryPerson("usr_raya", "Platform"),
		directoryPerson("usr_maya"),
		directoryPerson("usr_dave", "Sales"),
	}}
	tokens := &fakeTokens{granted: map[string]bool{"usr_ada": true, "usr_raya": true}}
	reader := NewAskerDirectory(directory, tokens)
	got, err := reader.Teams(context.Background(), testOrg, person("usr_ada", "ada@acme.com"), []string{"usr_ada", "usr_raya", "usr_maya", "usr_nobody"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"usr_ada": {"Platform", "Infra"}, "usr_raya": {"Platform"}, "usr_maya": {}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("teams = %v, want %v: someone who was not asked about is in it, or someone who has no teams is not", got, want)
	}
	if len(tokens.minted) != 1 || tokens.minted[0] != "usr_ada" {
		t.Fatalf("tokens minted for %v, want the asker's alone", tokens.minted)
	}
	if !reflect.DeepEqual(directory.loads, []chartLoad{{Organization: testOrg, Token: "token-for-usr_ada"}}) {
		t.Fatalf("directory loads = %+v, want one as the asker", directory.loads)
	}
}

// TestNoGrantIsAnErrorTheRouterKnows: an asker who has given agents no
// standing access has no token, so the directory is not read as them, and the
// error says why in the router's words, whatever wraps it.
func TestNoGrantIsAnErrorTheRouterKnows(t *testing.T) {
	directory := &fakeOrgDirectory{people: []agents.DirectoryPerson{directoryPerson("usr_ada", "Platform")}}
	reader := NewAskerDirectory(directory, &fakeTokens{})
	got, err := reader.Teams(context.Background(), testOrg, person("usr_ada", "ada@acme.com"), []string{"usr_ada"})
	if !errors.Is(err, ErrNoGrant) || got != nil {
		t.Fatalf("Teams = %v, %v, want ErrNoGrant", got, err)
	}
	if len(directory.loads) != 0 {
		t.Fatalf("the directory was read without a token: %+v", directory.loads)
	}
}

// TestADirectoryThatCannotBeReadIsAnErrorButNotNoGrant: a sign-in service that
// is down, a directory that is, and one that answers with nonsense are not the
// asker's to fix, so they are never reported as a missing grant.
func TestADirectoryThatCannotBeReadIsAnErrorButNotNoGrant(t *testing.T) {
	asker := person("usr_ada", "ada@acme.com")
	granted := map[string]bool{"usr_ada": true}
	for name, reader := range map[string]*AskerDirectory{
		"a sign-in service that is down": NewAskerDirectory(&fakeOrgDirectory{}, &fakeTokens{granted: granted, err: fmt.Errorf("%w: status 503", memberauth.ErrUnavailable)}),
		"a directory that is down":       NewAskerDirectory(&fakeOrgDirectory{err: errors.New("the organization directory did not answer: status 502")}, &fakeTokens{granted: granted}),
		"a directory that refuses the token": NewAskerDirectory(&fakeOrgDirectory{err: errors.New("the organization directory is not available to this member")},
			&fakeTokens{granted: granted}),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := reader.Teams(context.Background(), testOrg, asker, []string{"usr_ada"})
			if err == nil || errors.Is(err, ErrNoGrant) || got != nil {
				t.Fatalf("Teams = %v, %v, want an error that is not ErrNoGrant", got, err)
			}
		})
	}
}

// TestWithoutADirectoryOrTokensNothingIsKnown: a deployment with no Teams URL,
// or no agent-token exchange, knows nobody's teams and asks for no grant.
func TestWithoutADirectoryOrTokensNothingIsKnown(t *testing.T) {
	asker := person("usr_ada", "ada@acme.com")
	directory := &fakeOrgDirectory{people: []agents.DirectoryPerson{directoryPerson("usr_ada", "Platform")}}
	for name, reader := range map[string]*AskerDirectory{
		"no reader":     nil,
		"no directory":  NewAskerDirectory(nil, &fakeTokens{granted: map[string]bool{"usr_ada": true}}),
		"no tokens":     NewAskerDirectory(directory, nil),
		"neither":       NewAskerDirectory(nil, nil),
		"no asker id":   NewAskerDirectory(directory, &fakeTokens{granted: map[string]bool{"": true}}),
		"a zero reader": {},
	} {
		person := asker
		if name == "no asker id" {
			person = Person{}
		}
		if got, err := reader.Teams(context.Background(), testOrg, person, []string{"usr_ada"}); err != nil || len(got) != 0 {
			t.Errorf("%s: Teams = %v, %v", name, got, err)
		}
	}
	if len(directory.loads) != 0 {
		t.Fatalf("the directory was read: %+v", directory.loads)
	}
}

// TestTheRealDirectoryDecidesADepartmentEndToEnd runs a mention through a router
// whose directory is the real AskerDirectory: what the asker's directory shows
// of the owners is what the screen judges, and an asker with no grant is
// refused without anything being asked.
func TestTheRealDirectoryDecidesADepartmentEndToEnd(t *testing.T) {
	for _, test := range []struct {
		name    string
		granted bool
		reason  consentpolicy.Reason // empty when the ask is made
		owners  int
	}{
		{"an asker who gave agents access", true, "", 1},
		{"an asker who never did", false, consentpolicy.NoGrant, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			h.member("URAYA", "Raya")
			h.member("UMAYA", "Maya")
			directory := &fakeOrgDirectory{people: []agents.DirectoryPerson{
				directoryPerson("usr_ada", "Platform"), directoryPerson("usr_raya", "Engineering"), directoryPerson("usr_maya", "Design"),
			}}
			h.router.deps.Directory = NewAskerDirectory(directory, &fakeTokens{granted: map[string]bool{"usr_ada": test.granted}})
			fake := &fakeJev{score: keywordScore}
			h.useJev(fake, 0)
			job := h.mention(toBot("ask the database deploy steps from <@URAYA> or <@UMAYA>"))
			h.mustRun(job)
			if test.reason != "" {
				h.refusedFor(job, test.reason, `{"reason":"no_grant"}`)
				if len(fake.calls()) != 0 || len(directory.loads) != 0 {
					t.Fatal("something was read for an asker with no grant")
				}
				return
			}
			ask := h.ask()
			if len(ask.Owners) != test.owners || ask.Owners[0].Person.UserID != "usr_raya" {
				t.Fatalf("owners = %+v, want Raya alone: Maya's department is Design", ask.Owners)
			}
			if len(directory.loads) != 1 || directory.loads[0].Token != "token-for-usr_ada" {
				t.Fatalf("directory loads = %+v, want one as the asker", directory.loads)
			}
			if got := stateOf(fake.calls()[0]); !reflect.DeepEqual(got.Asker.Teams, []string{"Platform"}) || len(got.Owners) != 2 {
				t.Fatalf("state = %+v", got)
			}
		})
	}
}
