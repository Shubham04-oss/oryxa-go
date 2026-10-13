package slackbot

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/slackfake"
)

// startLink makes who's link to Sign in with Slack, and answers it and its
// state.
func (h *harness) startLink(who string) (LinkStart, string) {
	h.t.Helper()
	start, err := h.service.StartLink(context.Background(), bearer(who), testOrg, testWebURL+"/settings")
	if err != nil {
		h.t.Fatalf("StartLink(%s) = %v", who, err)
	}
	link, err := url.Parse(start.SignInURL)
	if err != nil {
		h.t.Fatal(err)
	}
	return start, link.Query().Get("state")
}

// signIn is Slack's redirect, to who's browser, once someone signed in as
// userID of teamID.
func (h *harness) signIn(who, state, teamID, userID string) (LinkCompletion, error) {
	h.t.Helper()
	return h.service.CompleteLink(context.Background(), bearer(who), InstallCallback{State: state, Code: h.fake.IssueSignInCode(teamID, userID)})
}

// linkedOf is who Slack users are linked to, as the internal profile read
// answers agents.
func (h *harness) linkedOf(userIDs ...string) map[string]string {
	h.t.Helper()
	view, err := h.service.Profiles(context.Background(), testOrg, userIDs)
	if err != nil {
		h.t.Fatal(err)
	}
	linked := map[string]string{}
	for _, profile := range view.Profiles {
		linked[profile.UserID] = profile.LinkedMemberID
	}
	return linked
}

// TestAMemberLinksTheirSlackAccountBySigningInAsThemself: the link sends the
// member to Sign in with Slack for their organization's workspace, with a
// state of its own that only its hash is kept of; Slack's sign-in comes back,
// and the account Slack says signed in is the member's from then on: their
// status says so, and the profile agents reads names them.
func TestAMemberLinksTheirSlackAccountBySigningInAsThemself(t *testing.T) {
	h := newHarness(t)
	install := h.installFor(testOrg)
	h.fake.AddUser(slackfake.User{ID: "U0MEMBER", Name: "member", Email: "member@acme.test"})

	start, state := h.startLink("member")
	link, _ := url.Parse(start.SignInURL)
	query := link.Query()
	if link.Host != "slack.com" || link.Path != "/openid/connect/authorize" || query.Get("response_type") != "code" ||
		query.Get("scope") != "openid" || query.Get("client_id") != h.fake.ClientID() || query.Get("team") != install.TeamID ||
		query.Get("redirect_uri") != testBaseURL+LinkCallbackPath || query.Get("nonce") == "" || state == "" {
		t.Fatalf("the sign-in link is %s", start.SignInURL)
	}
	if !start.ExpiresAt.Equal(testStart.Add(FlowLifetime)) {
		t.Fatalf("the link expires at %v", start.ExpiresAt)
	}
	if _, kept := h.store.linkFlows[state]; kept {
		t.Fatal("the state itself was stored")
	}
	if flow := h.store.linkFlows[hashState(state)]; flow.MemberID != "user-member" || flow.TeamID != install.TeamID || flow.OrganizationID != testOrg {
		t.Fatalf("flow = %+v", flow)
	}

	completion, err := h.signIn("member", state, install.TeamID, "U0MEMBER")
	if err != nil {
		t.Fatal(err)
	}
	if completion.MemberID != "user-member" || completion.SlackUserID != "U0MEMBER" || completion.ReturnTo != testWebURL+"/settings" {
		t.Fatalf("completion = %+v", completion)
	}
	status, err := h.service.LinkStatus(context.Background(), bearer("member"), testOrg)
	if err != nil || !status.Linked || status.SlackUserID != "U0MEMBER" || status.TeamID != install.TeamID {
		t.Fatalf("status = %+v, %v", status, err)
	}
	if linked := h.linkedOf("U0MEMBER"); linked["U0MEMBER"] != "user-member" {
		t.Fatalf("the profile names %q", linked["U0MEMBER"])
	}
	h.assertLogsLack(state)
}

// TestAnEmailChangeDoesNotMoveALink: an account that has the member's email
// address is not linked to them for it, however Slack has it confirmed; only
// the account the member signed in as is.
func TestAnEmailChangeDoesNotMoveALink(t *testing.T) {
	h := newHarness(t)
	install := h.installFor(testOrg)
	h.fake.AddUser(slackfake.User{ID: "U0MEMBER", Name: "member", Email: "member@acme.test"})
	h.fake.AddUser(slackfake.User{ID: "U0EVIL", Name: "admin's alt", Email: "member@acme.test"})
	_, state := h.startLink("member")
	if _, err := h.signIn("member", state, install.TeamID, "U0MEMBER"); err != nil {
		t.Fatal(err)
	}
	if linked := h.linkedOf("U0MEMBER", "U0EVIL"); linked["U0MEMBER"] != "user-member" || linked["U0EVIL"] != "" {
		t.Fatalf("linked = %v, want the member's own account alone", linked)
	}
}

// TestALinkIsRefusedForAnotherWorkspaceOrAnAccountAnotherMemberLinked: a
// member signed in to a workspace not their organization's is not linked; nor
// is one who signs in as an account another member linked. The browser goes
// back to the page with why, and nothing is kept.
func TestALinkIsRefusedForAnotherWorkspaceOrAnAccountAnotherMemberLinked(t *testing.T) {
	h := newHarness(t)
	install := h.installFor(testOrg)
	h.fake.AddUser(slackfake.User{ID: "U0OWNER", Name: "owner", Email: "owner@acme.test"})

	_, state := h.startLink("member")
	_, err := h.signIn("member", state, "T0OTHER", "U0OTHER")
	if reason := failureReason(t, err); reason != FailureWrongTeam {
		t.Fatalf("another workspace: %s", reason)
	}

	_, state = h.startLink("owner")
	if _, err := h.signIn("owner", state, install.TeamID, "U0OWNER"); err != nil {
		t.Fatal(err)
	}
	_, state = h.startLink("member")
	_, err = h.signIn("member", state, install.TeamID, "U0OWNER")
	var failed *InstallFailure
	if !errors.As(err, &failed) || failed.Reason != FailureTaken || failed.ReturnTo != testWebURL+"/settings" {
		t.Fatalf("an account another member linked: %v", err)
	}
	if linked := h.linkedOf("U0OWNER"); linked["U0OWNER"] != "user-owner" {
		t.Fatalf("the owner's link moved to %q", linked["U0OWNER"])
	}
	if status, _ := h.service.LinkStatus(context.Background(), bearer("member"), testOrg); status.Linked {
		t.Fatalf("the member is linked: %+v", status)
	}
}

// TestALinkFinishesOnlyInTheBrowserOfTheMemberWhoBeganIt: a sign-in URL sent
// to a coworker, who signs in to Slack as themself, links nothing: their
// browser is not signed in to Oryxa as the member who began it, or at all.
func TestALinkFinishesOnlyInTheBrowserOfTheMemberWhoBeganIt(t *testing.T) {
	h := newHarness(t)
	install := h.installFor(testOrg)
	_, state := h.startLink("member")
	if _, err := h.signIn("owner", state, install.TeamID, "U0OWNER"); failureReason(t, err) != FailureSession {
		t.Fatalf("another member's browser: %v", err)
	}
	_, state = h.startLink("member")
	if _, err := h.service.CompleteLink(context.Background(), authclient.Credentials{},
		InstallCallback{State: state, Code: h.fake.IssueSignInCode(install.TeamID, "U0OWNER")}); failureReason(t, err) != FailureSession {
		t.Fatalf("a browser signed in to nothing: %v", err)
	}
	if linked := h.linkedOf("U0OWNER"); linked["U0OWNER"] != "" {
		t.Fatalf("the owner's account was linked to %q", linked["U0OWNER"])
	}
}

// TestALinkStateWorksOnceAndNotForLong: a sign-in comes back once; again, or
// after the flow's ten minutes, it links nothing.
func TestALinkStateWorksOnceAndNotForLong(t *testing.T) {
	h := newHarness(t)
	install := h.installFor(testOrg)
	_, state := h.startLink("member")
	if _, err := h.signIn("member", state, install.TeamID, "U0MEMBER"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.signIn("member", state, install.TeamID, "U0MEMBER"); failureReason(t, err) != FailureState {
		t.Fatalf("a state used twice: %v", err)
	}
	_, late := h.startLink("owner")
	h.clock.Advance(FlowLifetime + 1)
	if _, err := h.signIn("owner", late, install.TeamID, "U0OWNER"); failureReason(t, err) != FailureState {
		t.Fatalf("an expired state: %v", err)
	}
	if _, err := h.service.CompleteLink(context.Background(), bearer("member"), InstallCallback{}); failureReason(t, err) != FailureState {
		t.Fatalf("no state: %v", err)
	}
}

// TestOnlyAMemberInPersonLinksAndOnlyWhereSlackIsInstalled: an agent, someone
// of no organization and no one at all make no link; and without an install
// there is nothing to link to.
func TestOnlyAMemberInPersonLinksAndOnlyWhereSlackIsInstalled(t *testing.T) {
	h := newHarness(t)
	if _, err := h.service.StartLink(context.Background(), bearer("member"), testOrg, ""); !errors.Is(err, domain.ErrNotConnected) {
		t.Fatalf("with no install: %v", err)
	}
	h.installFor(testOrg)
	for name, test := range map[string]struct {
		who  authclient.Credentials
		want error
	}{
		"an agent":      {bearer("agent"), domain.ErrForbidden},
		"a stranger":    {bearer("nobody"), domain.ErrForbidden},
		"no credential": {authclient.Credentials{}, domain.ErrUnauthorized},
	} {
		if _, err := h.service.StartLink(context.Background(), test.who, testOrg, ""); !errors.Is(err, test.want) {
			t.Errorf("%s starting a link = %v, want %v", name, err, test.want)
		}
		if err := h.service.Unlink(context.Background(), test.who, testOrg); !errors.Is(err, test.want) {
			t.Errorf("%s unlinking = %v, want %v", name, err, test.want)
		}
	}
	if _, err := h.service.StartLink(context.Background(), bearer("member"), testOrg, "https://evil.test/"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a return page of nobody's: %v", err)
	}
	if len(h.store.linkFlows) != 0 {
		t.Fatal("a link was started for someone who may not make one")
	}
}

// TestUnlinkingAndRelinking: a member's unlink forgets their account at once,
// and linking another account replaces the one they had.
func TestUnlinkingAndRelinking(t *testing.T) {
	h := newHarness(t)
	install := h.installFor(testOrg)
	h.fake.AddUser(slackfake.User{ID: "U0MEMBER", Name: "member", Email: "member@acme.test"})
	h.fake.AddUser(slackfake.User{ID: "U0MEMBER2", Name: "member's new account", Email: "member@acme.test"})
	_, state := h.startLink("member")
	if _, err := h.signIn("member", state, install.TeamID, "U0MEMBER"); err != nil {
		t.Fatal(err)
	}
	_, state = h.startLink("member")
	if _, err := h.signIn("member", state, install.TeamID, "U0MEMBER2"); err != nil {
		t.Fatal(err)
	}
	if linked := h.linkedOf("U0MEMBER", "U0MEMBER2"); linked["U0MEMBER"] != "" || linked["U0MEMBER2"] != "user-member" {
		t.Fatalf("after relinking: %v", linked)
	}
	if err := h.service.Unlink(context.Background(), bearer("member"), testOrg); err != nil {
		t.Fatal(err)
	}
	if linked := h.linkedOf("U0MEMBER2"); linked["U0MEMBER2"] != "" {
		t.Fatalf("after unlinking: %v", linked)
	}
	if status, _ := h.service.LinkStatus(context.Background(), bearer("member"), testOrg); status.Linked {
		t.Fatalf("status = %+v", status)
	}
	if err := h.service.Unlink(context.Background(), bearer("member"), testOrg); err != nil {
		t.Fatalf("unlinking again: %v", err)
	}
}
