package slackrouter

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter/consentpolicy"
)

// identityFixture is a resolver over fakes, with Raya a member of org_1 whose
// Slack user is URAYA.
type identityFixture struct {
	resolver   *resolver
	members    *fakeMembers
	identities *fakeIdentities
	profiles   map[string]Profile
}

func newIdentityFixture() *identityFixture {
	fixture := &identityFixture{members: &fakeMembers{}, identities: &fakeIdentities{}, profiles: map[string]Profile{}}
	fixture.members.add(testOrg, Member{UserID: "usr_raya", Email: "raya@acme.com", DisplayName: "Raya Member", Role: "member"})
	fixture.profiles["URAYA"] = Profile{UserID: "URAYA", TeamID: testTeam, Email: "raya@acme.com", EmailConfirmed: true, DisplayName: "Raya"}
	fixture.resolver = &resolver{members: fixture.members, identities: fixture.identities, logger: nopLogger()}
	return fixture
}

func (fixture *identityFixture) resolve(slackID string) (resolved, error) {
	return fixture.resolver.resolve(context.Background(), testOrg, testTeam, slackID, fixture.profiles)
}

func (fixture *identityFixture) change(slackID string, change func(*Profile)) {
	profile := fixture.profiles[slackID]
	change(&profile)
	fixture.profiles[slackID] = profile
}

func TestAFullMemberWithAConfirmedAddressIsAMember(t *testing.T) {
	fixture := newIdentityFixture()
	got, err := fixture.resolve("URAYA")
	if err != nil || !got.Member {
		t.Fatalf("resolved = %+v, err = %v", got, err)
	}
	if want := (Person{UserID: "usr_raya", Email: "raya@acme.com", Name: "Raya", SlackUserID: "URAYA"}); got.Person != want || got.Name != "Raya" {
		t.Fatalf("person = %+v, want %+v", got.Person, want)
	}
	identity, ok := fixture.identities.get(testTeam, "URAYA")
	if !ok || identity.OrganizationID != testOrg || identity.MemberUserID != "usr_raya" || identity.Email != "raya@acme.com" || identity.SlackUserID != "URAYA" {
		t.Fatalf("kept match = %+v, %v", identity, ok)
	}
}

func TestWhoIsNotAMember(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Profile)
		why    string
		bot    bool
	}{
		{"a guest", func(p *Profile) { p.Guest = true }, "guest", false},
		{"a deactivated account", func(p *Profile) { p.Deleted = true }, "deactivated", false},
		{"a bot", func(p *Profile) { p.Bot = true }, "bot", true},
		{"a Slack Connect user", func(p *Profile) { p.Stranger = true }, "another workspace", false},
		{"a user of another workspace", func(p *Profile) { p.TeamID = "T2" }, "installed workspace", false},
		{"a user with no workspace named", func(p *Profile) { p.TeamID = "" }, "installed workspace", false},
		{"a user with no address", func(p *Profile) { p.Email = "  " }, "no email", false},
		{"a user whose address Slack has not confirmed", func(p *Profile) { p.EmailConfirmed = false }, "not confirmed", false},
		{"a user whose address no member uses", func(p *Profile) { p.Email = "raya@elsewhere.example" }, "no member", false},
		{"a user whose address only looks like a member's", func(p *Profile) { p.Email = "raya@acme.com.evil.example" }, "no member", false},
		{"a user with the local part of a member's address", func(p *Profile) { p.Email = "raya@acme.co" }, "no member", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newIdentityFixture()
			fixture.change("URAYA", test.change)
			got, err := fixture.resolve("URAYA")
			if err != nil {
				t.Fatal(err)
			}
			if got.Member || got.Person != (Person{}) {
				t.Fatalf("%s resolved as a member: %+v", test.name, got)
			}
			if !strings.Contains(got.Why, test.why) || got.Bot != test.bot {
				t.Fatalf("why = %q, bot = %v, want %q, %v", got.Why, got.Bot, test.why, test.bot)
			}
			if _, kept := fixture.identities.get(testTeam, "URAYA"); kept {
				t.Fatal("a match was kept for someone who is not a member")
			}
			// A person with no name of their own to give is still called
			// something.
			if got.Name == "" {
				t.Fatal("no name")
			}
		})
	}
	t.Run("a user Slack does not know", func(t *testing.T) {
		fixture := newIdentityFixture()
		got, err := fixture.resolve("UGHOST")
		if err != nil || got.Member || got.Name != unknownName || got.Bot {
			t.Fatalf("resolved = %+v, err = %v", got, err)
		}
	})
}

// TestAMemberOfAnotherOrganizationIsNotAMemberOfThisOne: the address is a
// member's somewhere, and that is not enough.
func TestAMemberOfAnotherOrganizationIsNotAMemberOfThisOne(t *testing.T) {
	fixture := newIdentityFixture()
	fixture.members.members = map[string]Member{}
	fixture.members.add("org_other", Member{UserID: "usr_raya", Email: "raya@acme.com"})
	got, err := fixture.resolve("URAYA")
	if err != nil || got.Member {
		t.Fatalf("resolved = %+v, err = %v", got, err)
	}
}

func TestAddressesMatchWithoutRegardToCaseOrSpace(t *testing.T) {
	fixture := newIdentityFixture()
	fixture.change("URAYA", func(p *Profile) { p.Email = "  Raya@ACME.com " })
	got, err := fixture.resolve("URAYA")
	if err != nil || !got.Member || got.Person.UserID != "usr_raya" {
		t.Fatalf("resolved = %+v, err = %v", got, err)
	}
	if identity, _ := fixture.identities.get(testTeam, "URAYA"); identity.Email != "raya@acme.com" {
		t.Fatalf("the match is kept on %q, want it lower-cased", identity.Email)
	}
	if got := fixture.members.asked[len(fixture.members.asked)-1]; got != "raya@acme.com" {
		t.Fatalf("auth was asked for %q", got)
	}
}

func TestANameIsWhatSlackCallsThemThenWhatAuthDoes(t *testing.T) {
	fixture := newIdentityFixture()
	fixture.change("URAYA", func(p *Profile) { p.DisplayName, p.RealName = "", "Raya R." })
	if got, _ := fixture.resolve("URAYA"); got.Name != "Raya R." {
		t.Fatalf("name = %q, want the real name when there is no display name", got.Name)
	}
	fixture.change("URAYA", func(p *Profile) { p.RealName = "" })
	if got, _ := fixture.resolve("URAYA"); got.Name != "Raya Member" || got.Person.Name != "Raya Member" {
		t.Fatalf("name = %q, want the member's own when Slack has none", got.Name)
	}
}

// TestAMatchIsDroppedTheMomentItStopsHolding: nothing is authorized from what
// was kept, and what was kept does not outlive what it was made from.
func TestAMatchIsDroppedTheMomentItStopsHolding(t *testing.T) {
	fixture := newIdentityFixture()
	if got, _ := fixture.resolve("URAYA"); !got.Member {
		t.Fatal("not a member to begin with")
	}
	if _, ok := fixture.identities.get(testTeam, "URAYA"); !ok {
		t.Fatal("no match kept")
	}
	fixture.change("URAYA", func(p *Profile) { p.Guest = true })
	if got, _ := fixture.resolve("URAYA"); got.Member {
		t.Fatal("still a member as a guest")
	}
	if _, ok := fixture.identities.get(testTeam, "URAYA"); ok {
		t.Fatal("the match outlived the membership")
	}
}

// TestAMatchFollowsTheAddress: a person whose Slack address changes to another
// member's, confirmed by Slack, is that member from then on, and is nobody
// the address was before. The kept match is the one of the last resolution.
func TestAMatchFollowsTheAddress(t *testing.T) {
	fixture := newIdentityFixture()
	fixture.members.add(testOrg, Member{UserID: "usr_maya", Email: "maya@acme.com", DisplayName: "Maya"})
	if got, _ := fixture.resolve("URAYA"); got.Person.UserID != "usr_raya" {
		t.Fatalf("resolved = %+v", got)
	}
	fixture.change("URAYA", func(p *Profile) { p.Email = "maya@acme.com" })
	got, err := fixture.resolve("URAYA")
	if err != nil || got.Person.UserID != "usr_maya" {
		t.Fatalf("resolved = %+v, err = %v, want Maya on Maya's address", got, err)
	}
	if identity, _ := fixture.identities.get(testTeam, "URAYA"); identity.MemberUserID != "usr_maya" || identity.Email != "maya@acme.com" {
		t.Fatalf("match = %+v, want it to follow the address", identity)
	}
}

func TestAFailureToDropAMatchDoesNotStopWhatThePersonAsked(t *testing.T) {
	fixture := newIdentityFixture()
	fixture.identities.delErr = errors.New("database down")
	fixture.change("URAYA", func(p *Profile) { p.Guest = true })
	got, err := fixture.resolve("URAYA")
	if err != nil || got.Member {
		t.Fatalf("resolved = %+v, err = %v", got, err)
	}
}

func TestAFailureToReadOrKeepAMatchIsAFailure(t *testing.T) {
	fixture := newIdentityFixture()
	fixture.members.err = errors.New("database down")
	if _, err := fixture.resolve("URAYA"); err == nil || IsPermanent(err) {
		t.Fatalf("err = %v, want a failure to retry, not a non-member", err)
	}
	fixture = newIdentityFixture()
	fixture.identities.saveErr = errors.New("database down")
	if _, err := fixture.resolve("URAYA"); err == nil || IsPermanent(err) {
		t.Fatalf("err = %v, want a failure to retry", err)
	}
}

// TestASlackUserAtTheAskersAddressButNotConfirmedIsRefused is the router's
// view of the same rule: the asker is told they are not a member.
func TestAnAskerWhoseAddressIsNotConfirmedIsRefused(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	h.profile(testAsker, func(p *Profile) { p.EmailConfirmed = false })
	job := h.mention(toBot("ask <@URAYA>"))
	h.mustRun(job)
	if _, params := h.refusal(job); params.Reason != consentpolicy.NotMember {
		t.Fatalf("reason = %q", params.Reason)
	}
}

func TestAnOwnerWhoIsAGuestIsNotAsked(t *testing.T) {
	for name, change := range map[string]func(*Profile){
		"a guest":               func(p *Profile) { p.Guest = true },
		"a deactivated account": func(p *Profile) { p.Deleted = true },
		"unconfirmed":           func(p *Profile) { p.EmailConfirmed = false },
		"a bot":                 func(p *Profile) { p.Bot = true },
		"a mismatch":            func(p *Profile) { p.Email = "raya@other.example" },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.member("URAYA", "Raya")
			h.profile("URAYA", change)
			job := h.mention(toBot("ask <@URAYA>"))
			h.mustRun(job)
			_, params := h.refusal(job)
			if params.Reason != consentpolicy.OwnerNotMember || len(params.Names) != 1 || params.Names[0] != "Raya" {
				t.Fatalf("refusal = %+v", params)
			}
		})
	}
}
