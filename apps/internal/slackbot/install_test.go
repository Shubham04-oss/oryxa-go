package slackbot

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/slackfake"
)

// startInstall asks for the Add to Slack link as an admin and answers the
// state it carries.
func (h *harness) startInstall(returnTo string) (InstallView, string) {
	h.t.Helper()
	view, err := h.service.Install(context.Background(), bearer("admin"), testOrg, returnTo)
	if err != nil {
		h.t.Fatalf("Install() = %v", err)
	}
	link, err := url.Parse(view.AddToSlackURL)
	if err != nil {
		h.t.Fatal(err)
	}
	return view, link.Query().Get("state")
}

// finishInstall is Slack's redirect: the browser comes back with a code.
func (h *harness) finishInstall(state, teamID string) (InstallCompletion, error) {
	h.t.Helper()
	return h.service.CompleteInstall(context.Background(), InstallCallback{State: state, Code: h.fake.IssueCode(teamID)})
}

func failureReason(t *testing.T, err error) string {
	t.Helper()
	var failed *InstallFailure
	if !errors.As(err, &failed) {
		t.Fatalf("%v is no install failure", err)
	}
	return failed.Reason
}

func TestTheAddToSlackLinkAsksForTheBotScopesAndBindsItsStateToTheAdmin(t *testing.T) {
	h := newHarness(t)
	view, state := h.startInstall("")

	if view.Status != StatusNotInstalled || view.Team != nil {
		t.Fatalf("view = %+v", view)
	}
	link, err := url.Parse(view.AddToSlackURL)
	if err != nil || link.Scheme != "https" || link.Host != "slack.com" || link.Path != "/oauth/v2/authorize" {
		t.Fatalf("link = %s", view.AddToSlackURL)
	}
	query := link.Query()
	if query.Get("client_id") != h.fake.ClientID() || query.Get("scope") != strings.Join(BotScopes, ",") ||
		query.Get("redirect_uri") != "https://api.oryxa.test/v1/apps/slack/install/callback" || state == "" {
		t.Fatalf("query = %v", query)
	}
	if query.Has("user_scope") {
		t.Fatal("the app asks for a user scope")
	}
	if !view.AddToSlackExpiresAt.Equal(testStart.Add(FlowLifetime)) {
		t.Fatalf("the link expires at %v", view.AddToSlackExpiresAt)
	}
	// Only the state's hash is kept, and it names the organization and the admin.
	if _, kept := h.store.flows[state]; kept || len(h.store.flows) != 1 {
		t.Fatal("the state itself was stored")
	}
	flow := h.store.flows[hashState(state)]
	if flow.OrganizationID != testOrg || flow.MemberID != "user-admin" || !flow.ExpiresAt.Equal(testStart.Add(FlowLifetime)) {
		t.Fatalf("flow = %+v", flow)
	}
	// Every link has a state of its own.
	if _, other := h.startInstall(""); other == state {
		t.Fatal("two links share a state")
	}
	h.assertLogsLack(state)
}

func TestOnlyAnAdminGetsTheLinkAndOnlyToAPageOfOurs(t *testing.T) {
	h := newHarness(t)
	for name, test := range map[string]struct {
		who  authclient.Credentials
		want error
	}{
		"a member":      {bearer("member"), domain.ErrForbidden},
		"an agent":      {bearer("agent"), domain.ErrForbidden},
		"a stranger":    {bearer("nobody"), domain.ErrForbidden},
		"no credential": {authclient.Credentials{}, domain.ErrUnauthorized},
	} {
		if _, err := h.service.Install(context.Background(), test.who, testOrg, ""); !errors.Is(err, test.want) {
			t.Errorf("%s = %v, want %v", name, err, test.want)
		}
		if _, err := h.service.Channels(context.Background(), test.who, testOrg); !errors.Is(err, test.want) {
			t.Errorf("%s asking for channels = %v, want %v", name, err, test.want)
		}
		if err := h.service.Uninstall(context.Background(), test.who, testOrg); !errors.Is(err, test.want) {
			t.Errorf("%s uninstalling = %v, want %v", name, err, test.want)
		}
	}
	if len(h.store.flows) != 0 {
		t.Fatal("a link was made for someone who may not have one")
	}
	if _, err := h.service.Install(context.Background(), bearer("owner"), testOrg, ""); err != nil {
		t.Fatalf("an owner = %v", err)
	}
	if _, err := h.service.Install(context.Background(), bearer("admin"), testOrg, "https://evil.example/settings"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a return page that is not ours = %v", err)
	}
	if _, err := h.service.Install(context.Background(), bearer("admin"), testOrg, testWebURL+"/settings"); err != nil {
		t.Fatalf("a return page that is = %v", err)
	}
}

func TestAnInstallExchangesTheCodeAndSealsTheBotTokenUnderItsWorkspace(t *testing.T) {
	h := newHarness(t)
	_, state := h.startInstall("")

	completion, err := h.finishInstall(state, slackfake.DefaultTeamID)
	if err != nil {
		t.Fatal(err)
	}
	if completion.OrganizationID != testOrg || completion.TeamID != slackfake.DefaultTeamID || completion.TeamName != "Acme" || completion.ReturnTo != "" {
		t.Fatalf("completion = %+v", completion)
	}
	install := h.store.install(slackfake.DefaultTeamID)
	if !install.Active() || install.OrganizationID != testOrg || install.BotUserID != slackfake.DefaultBotUserID ||
		install.InstalledBy != "user-admin" || install.Sealed == nil || !slices.Equal(install.Scopes, BotScopes) || !install.InstalledAt.Equal(testStart) {
		t.Fatalf("install = %+v", install)
	}
	// Sealed, not stored: the token is not in what was kept, and opens only
	// for its workspace.
	if strings.Contains(string(install.Sealed.Ciphertext), slackfake.DefaultBotToken) {
		t.Fatal("the bot token is in the sealed data as it is")
	}
	token, err := h.service.botToken(context.Background(), install)
	if err != nil || token.Reveal() != slackfake.DefaultBotToken {
		t.Fatalf("botToken = %q, %v", token, err)
	}
	if _, err := h.service.sealer.Open(context.Background(), tokenBinding("T0OTHER01"), *install.Sealed); err == nil {
		t.Fatal("the token opened for another workspace")
	}

	// What went to Slack: the code, with the client's credentials in Basic and the
	// redirect the flow named; then the token was tested as itself.
	exchange := h.fake.Calls("oauth.v2.access")
	if len(exchange) != 1 || exchange[0].BasicUser != h.fake.ClientID() || exchange[0].BasicPassword != h.fake.ClientSecret() ||
		exchange[0].Args["redirect_uri"] != "https://api.oryxa.test/v1/apps/slack/install/callback" {
		t.Fatalf("oauth.v2.access = %+v", exchange)
	}
	if tests := h.fake.Calls("auth.test"); len(tests) != 1 || tests[0].Token != slackfake.DefaultBotToken {
		t.Fatalf("auth.test = %+v", tests)
	}
	if revoked := h.fake.Calls("auth.revoke", "apps.uninstall"); len(revoked) != 0 {
		t.Fatalf("something was revoked: %+v", revoked)
	}

	events := h.store.publishedOf(appsv1.SlackInstallChangedDetailType)
	if len(events) != 1 {
		t.Fatalf("events = %+v", h.store.published())
	}
	data := dataOf(t, events[0].Data)
	if data["state"] != "active" || data["reason"] != "installed" || data["actor_id"] != "user-admin" || data["organization_id"] != testOrg {
		t.Fatalf("event = %v", data)
	}
	if strings.Contains(string(events[0].Data), "xoxb") {
		t.Fatal("the event carries the token")
	}
	h.assertLogsLack(slackfake.DefaultBotToken, h.fake.ClientSecret(), state)

	// The organization now sees an active install, and a link to install it again.
	view, _ := h.startInstall("")
	if view.Status != StatusActive || view.Team == nil || view.Team.ID != slackfake.DefaultTeamID || view.Team.Name != "Acme" ||
		view.BotUserID != slackfake.DefaultBotUserID || view.InstalledAt == nil || view.AddToSlackURL == "" {
		t.Fatalf("view = %+v", view)
	}
}

func TestAnInstallStateWorksOnceAndOnlyWhileItIsFresh(t *testing.T) {
	h := newHarness(t)
	_, state := h.startInstall("")
	if _, err := h.finishInstall(state, slackfake.DefaultTeamID); err != nil {
		t.Fatal(err)
	}
	before := len(h.fake.Calls())

	for name, callback := range map[string]InstallCallback{
		"the same state again": {State: state, Code: h.fake.IssueCode(slackfake.DefaultTeamID)},
		"a state nobody made":  {State: "made-up-by-someone-0123456789abcdefghijk", Code: h.fake.IssueCode(slackfake.DefaultTeamID)},
		"no state":             {Code: h.fake.IssueCode(slackfake.DefaultTeamID)},
	} {
		_, err := h.service.CompleteInstall(context.Background(), callback)
		if !errors.Is(err, domain.ErrInvalid) || failureReason(t, err) != FailureState {
			t.Errorf("%s = %v", name, err)
		}
	}
	if len(h.fake.Calls()) != before {
		t.Fatal("a state that was refused reached Slack")
	}

	// A link that went unused for its ten minutes is as good as none.
	_, stale := h.startInstall("")
	h.clock.Advance(FlowLifetime + time.Second)
	if _, err := h.finishInstall(stale, slackfake.DefaultTeamID); failureReason(t, err) != FailureState {
		t.Fatalf("an expired state = %v", err)
	}
	// And it is spent by being tried.
	_, fresh := h.startInstall("")
	if _, err := h.finishInstall(fresh, slackfake.DefaultTeamID); err != nil {
		t.Fatal(err)
	}
}

func TestADeclinedInstallSpendsItsStateAndSendsTheBrowserBack(t *testing.T) {
	h := newHarness(t)
	_, state := h.startInstall(testWebURL + "/settings")

	_, err := h.service.CompleteInstall(context.Background(), InstallCallback{State: state, Error: "access_denied"})
	var failed *InstallFailure
	if !errors.As(err, &failed) || failed.Reason != FailureDenied || failed.ReturnTo != testWebURL+"/settings" || !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a declined install = %v", err)
	}
	if _, err := h.service.CompleteInstall(context.Background(), InstallCallback{State: state, Code: h.fake.IssueCode(slackfake.DefaultTeamID)}); failureReason(t, err) != FailureState {
		t.Fatalf("the declined state was used again: %v", err)
	}
	if len(h.fake.Calls()) != 0 || len(h.store.installs) != 0 {
		t.Fatal("a declined install did something")
	}
}

func TestAWorkspaceInstalledForAnotherOrganizationIsRefusedWith409(t *testing.T) {
	h := newHarness(t)
	h.installFor("org_2")
	before := h.store.install(slackfake.DefaultTeamID)
	_, state := h.startInstall(testWebURL + "/settings")

	_, err := h.finishInstall(state, slackfake.DefaultTeamID)
	if !errors.Is(err, ErrTeamBound) || !errors.Is(err, domain.ErrConflict) || failureReason(t, err) != FailureTeamBound {
		t.Fatalf("a workspace bound elsewhere = %v", err)
	}
	var failed *InstallFailure
	if !errors.As(err, &failed) || failed.ReturnTo != testWebURL+"/settings" {
		t.Fatalf("the browser is not sent back: %+v", failed)
	}
	after := h.store.install(slackfake.DefaultTeamID)
	if after.OrganizationID != "org_2" || string(after.Sealed.Ciphertext) != string(before.Sealed.Ciphertext) {
		t.Fatal("the other organization's install changed")
	}
	if len(h.store.published()) != 0 {
		t.Fatal("a refused install announced itself")
	}
	// The token Slack issued may be the one the other organization holds: it
	// is not revoked.
	if revoked := h.fake.Calls("auth.revoke", "apps.uninstall"); len(revoked) != 0 {
		t.Fatalf("something was revoked: %+v", revoked)
	}

	// Once that install ended, the workspace is free to be installed again.
	h.serve(h.fake.AppUninstalled("", slackfake.DefaultTeamID))
	_, state = h.startInstall("")
	if _, err := h.finishInstall(state, slackfake.DefaultTeamID); err != nil {
		t.Fatalf("a workspace that ended, installed for a new organization = %v", err)
	}
	if install := h.store.install(slackfake.DefaultTeamID); install.OrganizationID != testOrg || !install.Active() {
		t.Fatalf("install = %+v", install)
	}
}

func TestAnOrganizationServesOneWorkspaceAtATime(t *testing.T) {
	h := newHarness(t)
	h.installFor(testOrg)
	h.fake.AddWorkspace(slackfake.Workspace{TeamID: "T0SECOND1", Name: "Second"})
	_, state := h.startInstall("")
	_, err := h.finishInstall(state, "T0SECOND1")
	if !errors.Is(err, ErrOrganizationBound) || !errors.Is(err, domain.ErrConflict) || failureReason(t, err) != FailureOrgBound {
		t.Fatalf("a second workspace = %v", err)
	}
	if _, ok := h.store.installs["T0SECOND1"]; ok {
		t.Fatal("a second workspace was installed")
	}
}

func TestInstallingOverAnInstallReplacesItsTokenAndSaysSo(t *testing.T) {
	h := newHarness(t)
	_, state := h.startInstall("")
	if _, err := h.finishInstall(state, slackfake.DefaultTeamID); err != nil {
		t.Fatal(err)
	}
	first := h.store.install(slackfake.DefaultTeamID)
	h.clock.Advance(48 * time.Hour)
	_, state = h.startInstall("")
	if _, err := h.finishInstall(state, slackfake.DefaultTeamID); err != nil {
		t.Fatal(err)
	}
	second := h.store.install(slackfake.DefaultTeamID)
	if !second.InstalledAt.Equal(first.InstalledAt) || !second.UpdatedAt.After(first.UpdatedAt) {
		t.Fatalf("installed %v then %v, updated %v then %v", first.InstalledAt, second.InstalledAt, first.UpdatedAt, second.UpdatedAt)
	}
	events := h.store.publishedOf(appsv1.SlackInstallChangedDetailType)
	if len(events) != 2 || dataOf(t, events[0].Data)["reason"] != "installed" || dataOf(t, events[1].Data)["reason"] != "reinstalled" {
		t.Fatalf("events = %+v", events)
	}
}

func TestAnInstallThatIsNotWhatTheSurfaceServesIsRefusedAndKeepsNothing(t *testing.T) {
	for name, test := range map[string]struct {
		prepare func(h *harness)
		reason  string
		want    error
		says    string
	}{
		"an Enterprise Grid install": {
			prepare: func(h *harness) {
				h.fake.ModifyOAuth(func(answer map[string]any) {
					answer["is_enterprise_install"] = true
					answer["enterprise"] = map[string]any{"id": "E0ORG", "name": "Big Co"}
				})
			},
			reason: FailureEnterprise, want: domain.ErrNotConnectable, says: "Enterprise",
		},
		"a token that rotates": {
			prepare: func(h *harness) {
				h.fake.ModifyOAuth(func(answer map[string]any) {
					answer["refresh_token"] = "xoxe-1-refresh"
					answer["expires_in"] = 43200
				})
			},
			reason: FailureRotation, want: domain.ErrNotConnectable, says: "rotation",
		},
		"an install missing a scope": {
			prepare: func(h *harness) {
				h.fake.AddWorkspace(slackfake.Workspace{
					TeamID: slackfake.DefaultTeamID, Name: "Acme", BotUserID: slackfake.DefaultBotUserID, BotToken: slackfake.DefaultBotToken,
					Scopes: []string{"app_mentions:read", "chat:write"},
				})
			},
			reason: FailureScopes, want: domain.ErrNotConnectable, says: "groups:read",
		},
		"a token that belongs to another bot": {
			prepare: func(h *harness) {
				h.fake.ModifyOAuth(func(answer map[string]any) { answer["bot_user_id"] = "U0SOMEONEELSE" })
			},
			reason: FailureSlackRefused, want: domain.ErrInvalid, says: "another workspace or bot",
		},
		"a user token where a bot's was expected": {
			prepare: func(h *harness) {
				h.fake.ModifyOAuth(func(answer map[string]any) { answer["token_type"] = "user" })
			},
			reason: FailureSlackRefused, want: domain.ErrInvalid, says: "bot token",
		},
		"Slack down": {
			prepare: func(h *harness) { h.fake.Down(true) },
			reason:  FailureUnavailable, want: domain.ErrUnavailable, says: "did not answer",
		},
		"Slack refusing the code": {
			prepare: func(h *harness) { h.fake.Fail("oauth.v2.access", "invalid_code", 1) },
			reason:  FailureSlackRefused, want: domain.ErrInvalid, says: "invalid_code",
		},
		"KMS down": {
			prepare: func(h *harness) {
				h.keys.mu.Lock()
				h.keys.broken = true
				h.keys.mu.Unlock()
			},
			reason: FailureUnavailable, want: domain.ErrUnavailable, says: "seal",
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			test.prepare(h)
			_, state := h.startInstall("")
			_, err := h.finishInstall(state, slackfake.DefaultTeamID)
			if err == nil || !errors.Is(err, test.want) || failureReason(t, err) != test.reason || !strings.Contains(err.Error(), test.says) {
				t.Fatalf("err = %v, want %s (%v) saying %q", err, test.reason, test.want, test.says)
			}
			if len(h.store.installs) != 0 || len(h.store.published()) != 0 {
				t.Fatal("a refused install left something behind")
			}
			h.assertLogsLack(slackfake.DefaultBotToken, "xoxe-1-refresh", h.fake.ClientSecret())
		})
	}
}

func TestDisconnectingUninstallsTheAppAndEndsTheInstall(t *testing.T) {
	h := newHarness(t)
	h.installFor(testOrg)

	if err := h.service.Uninstall(context.Background(), bearer("admin"), testOrg); err != nil {
		t.Fatal(err)
	}
	calls := h.fake.Calls("apps.uninstall")
	if len(calls) != 1 || calls[0].Token != slackfake.DefaultBotToken || calls[0].Args["client_id"] != h.fake.ClientID() {
		t.Fatalf("apps.uninstall = %+v", calls)
	}
	install := h.store.install(slackfake.DefaultTeamID)
	if install.Active() || install.Sealed != nil || install.EndReason != appsv1.SlackRemovedByAdmin {
		t.Fatalf("install = %+v", install)
	}
	events := h.store.publishedOf(appsv1.SlackInstallChangedDetailType)
	if len(events) != 1 {
		t.Fatalf("events = %+v", h.store.published())
	}
	if data := dataOf(t, events[0].Data); data["state"] != "ended" || data["reason"] != "removed_by_admin" || data["actor_id"] != "user-admin" {
		t.Fatalf("event = %v", data)
	}
	view, _ := h.startInstall("")
	if view.Status != StatusEnded || view.EndReason != appsv1.SlackRemovedByAdmin || view.EndedAt == nil {
		t.Fatalf("view = %+v", view)
	}
	// There is nothing left to disconnect.
	if err := h.service.Uninstall(context.Background(), bearer("admin"), testOrg); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("disconnecting twice = %v", err)
	}

	// Slack refusing to uninstall does not keep the install alive.
	other := newHarness(t)
	other.installFor(testOrg)
	other.fake.Fail("apps.uninstall", "not_allowed_token_type", 1)
	if err := other.service.Uninstall(context.Background(), bearer("admin"), testOrg); err != nil {
		t.Fatal(err)
	}
	if other.store.install(slackfake.DefaultTeamID).Active() {
		t.Fatal("the install outlived a refused uninstall")
	}
}
