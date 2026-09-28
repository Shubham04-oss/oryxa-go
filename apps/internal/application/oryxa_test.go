package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/signin"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

// decodeLast decodes the last event of a detail type.
func decodeLast(t *testing.T, store *fakeOryxaStore, detailType string, into any) {
	t.Helper()
	events := store.published(detailType)
	if len(events) == 0 {
		t.Fatalf("no %s was published", detailType)
	}
	if err := json.Unmarshal(events[len(events)-1].Data, into); err != nil {
		t.Fatal(err)
	}
}

func TestOryxaOAuthConnectStartsTheSigninAndRecordsAPendingConnection(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github")

	connected, err := h.service.ConnectOryxa(context.Background(), memberToken, "org_1", ConnectInput{
		Toolkit: "GitHub", ReturnTo: "https://app.oryxa.test/home?view=tools", Host: "app.oryxa.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	connection := connected.Connection
	if connection.Status != domain.ConnectionPending || !connection.Oryxa() || connection.Toolkit != "github" ||
		connection.MemberID != "user-1" || connection.ComposioAccountID != "" {
		t.Fatalf("connection = %+v", connection)
	}
	if connected.RedirectURL == "" || connected.State == "" || !connected.StateExpiresAt.Equal(testClock.Add(signin.FlowLifetime)) {
		t.Fatalf("connect = %+v", connected)
	}
	if len(h.signin.starts) != 1 {
		t.Fatalf("starts = %d", len(h.signin.starts))
	}
	start := h.signin.starts[0]
	if start.OrganizationID != "org_1" || start.MemberID != "user-1" || start.ConnectionID != connection.ID ||
		start.Toolkit != "github" || start.Kind != integration.KindOAuth2Code || start.ReturnTo != "https://app.oryxa.test/home?view=tools" {
		t.Fatalf("start = %+v", start)
	}
	// The state lives in signin's flow, never in the connection.
	if len(h.store.states) != 0 {
		t.Fatalf("a state hash was recorded: %v", h.store.states)
	}
	var initiated appsv1.ConnectionInitiated
	decodeLast(t, h.store, appsv1.ConnectionInitiatedDetailType, &initiated)
	if initiated.Broker != appsv1.BrokerOryxa || initiated.ConnectionID != connection.ID {
		t.Fatalf("initiated = %+v", initiated)
	}
	// Composio was never asked for anything.
	if len(h.composio.links) != 0 || len(h.composio.created) != 0 {
		t.Fatalf("composio was used: links %v, configs %v", h.composio.links, h.composio.created)
	}
}

// The state cookie is set on the connect request's answer and comes back only
// to that host, so an OAuth sign-in starts only on the redirect URI's host.
func TestOryxaOAuthConnectStartsOnlyWhereItsCallbackCanReadTheCookie(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github", "slack")

	_, err := h.service.ConnectOryxa(context.Background(), memberToken, "org_1", ConnectInput{Toolkit: "github", Host: "api.oryxa.test"})
	if !errors.Is(err, domain.ErrNotConnectable) {
		t.Fatalf("error = %v, want not connectable", err)
	}
	if len(h.store.connections) != 0 || len(h.signin.starts) != 0 {
		t.Fatalf("recorded %d connections, %d starts", len(h.store.connections), len(h.signin.starts))
	}
	// A cookie is a host's, whatever the port.
	if _, err := h.service.ConnectOryxa(context.Background(), memberToken, "org_1", ConnectInput{Toolkit: "github", Host: "APP.oryxa.test:8443"}); err != nil {
		t.Fatalf("same host, another port = %v", err)
	}
	// A key sets no cookie, so it may be given from anywhere.
	if _, err := h.service.ConnectOryxa(context.Background(), otherToken, "org_1", ConnectInput{
		Toolkit: "github", Kind: integration.KindAPIToken, Fields: map[string]string{"token": "ghp_secretvalue"}, Host: "api.oryxa.test",
	}); err != nil {
		t.Fatalf("a key from another host = %v", err)
	}
}

func TestOryxaKeyConnectIsActiveAtOnce(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github")

	connected, err := h.service.ConnectOryxa(context.Background(), memberToken, "org_1", ConnectInput{
		Toolkit: "github", Kind: integration.KindAPIToken, Fields: map[string]string{"token": "ghp_secretvalue"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if connected.RedirectURL != "" || connected.State != "" {
		t.Fatalf("a key answered a sign-in: %+v", connected)
	}
	connection := connected.Connection
	if connection.Status != domain.ConnectionActive || connection.Label != "octocat" || !connection.Oryxa() ||
		connection.CredentialKind != integration.KindAPIToken {
		t.Fatalf("connection = %+v", connection)
	}
	save := h.signin.saves[0]
	if save.ConnectionID != connection.ID || save.Kind != integration.KindAPIToken || save.Fields["token"] != "ghp_secretvalue" ||
		save.OrganizationID != "org_1" || save.Toolkit != "github" {
		t.Fatalf("save = %+v", save)
	}
	if types := h.eventTypes(); !containsAll(types, appsv1.ConnectionInitiatedDetailType, appsv1.ConnectionConnectedDetailType) {
		t.Fatalf("events = %v", types)
	}
	var connectedEvent appsv1.ConnectionConnected
	decodeLast(t, h.store, appsv1.ConnectionConnectedDetailType, &connectedEvent)
	if connectedEvent.Broker != appsv1.BrokerOryxa || connectedEvent.ConnectionID != connection.ID {
		t.Fatalf("connected = %+v", connectedEvent)
	}
	if strings.Contains(h.allEvents(), "ghp_secretvalue") {
		t.Fatal("an event carries the key")
	}
	if len(h.signin.starts) != 0 {
		t.Fatal("a key started an OAuth sign-in")
	}
}

func TestOryxaKeyTheAppRefusesFailsTheConnectionNamingTheFieldNotTheValue(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github")
	h.signin.saveErr = &signin.Failure{Reason: signin.ReasonInvalidCredentials, Field: "token", Rule: "was refused by the app"}

	_, err := h.service.ConnectOryxa(context.Background(), memberToken, "org_1", ConnectInput{
		Toolkit: "github", Kind: integration.KindAPIToken, Fields: map[string]string{"token": "ghp_wrongvalue"},
	})
	var invalid *domain.InvalidCredentials
	if !errors.As(err, &invalid) || !errors.Is(err, domain.ErrInvalidCredentials) || invalid.Field != "token" {
		t.Fatalf("error = %v, want invalid credentials naming token", err)
	}
	if strings.Contains(err.Error(), "ghp_wrongvalue") {
		t.Fatalf("the error quotes the key: %v", err)
	}
	var connection domain.Connection
	for _, recorded := range h.store.connections {
		connection = recorded
	}
	if connection.Status != domain.ConnectionFailed || connection.StatusReason != "token was refused by the app" {
		t.Fatalf("connection = %+v", connection)
	}
	var failed appsv1.ConnectionFailed
	decodeLast(t, h.store, appsv1.ConnectionFailedDetailType, &failed)
	if failed.Reason != appsv1.FailedInvalidCredentials || failed.Broker != appsv1.BrokerOryxa {
		t.Fatalf("failed = %+v", failed)
	}
	// The slot is free again: a failed connection is history.
	h.signin.saveErr = nil
	if _, err := h.service.ConnectOryxa(context.Background(), memberToken, "org_1", ConnectInput{
		Toolkit: "github", Kind: integration.KindAPIToken, Fields: map[string]string{"token": "ghp_rightvalue"},
	}); err != nil {
		t.Fatalf("connecting again = %v", err)
	}
}

func TestOryxaConnectFailuresAreRecordedWithTheirReason(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		kind       string
		startErr   error
		saveErr    error
		want       error
		wantReason string
	}{
		"an app that does not answer a key's test call": {
			kind: integration.KindAPIToken, saveErr: &signin.Failure{Reason: signin.ReasonUnavailable},
			want: domain.ErrUnavailable, wantReason: appsv1.FailedUnavailable,
		},
		"no OAuth client to sign in through": {
			startErr: signin.ErrNoClient, want: domain.ErrNotConnectable, wantReason: appsv1.FailedNotConnectable,
		},
		"a field an OAuth sign-in takes, broken": {
			startErr: &signin.Failure{Reason: signin.ReasonInvalidCredentials, Field: "subdomain", Rule: "is required"},
			want:     domain.ErrInvalidCredentials, wantReason: appsv1.FailedInvalidCredentials,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newOryxaHarness(t, "github")
			h.signin.startErr, h.signin.saveErr = test.startErr, test.saveErr
			input := ConnectInput{Toolkit: "github", Kind: test.kind}
			if test.kind == integration.KindAPIToken {
				input.Fields = map[string]string{"token": "ghp_value"}
			}
			_, err := h.service.ConnectOryxa(context.Background(), memberToken, "org_1", input)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			var failed appsv1.ConnectionFailed
			decodeLast(t, h.store, appsv1.ConnectionFailedDetailType, &failed)
			if failed.Reason != test.wantReason || failed.Broker != appsv1.BrokerOryxa {
				t.Fatalf("failed = %+v", failed)
			}
			for _, connection := range h.store.connections {
				if connection.Status != domain.ConnectionFailed {
					t.Fatalf("connection = %+v", connection)
				}
			}
		})
	}
}

func TestOryxaIsRefusedWhileItIsOff(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, service := range map[string]*Service{
		"configured, no toolkit switched on": newOryxaHarness(t).service,
		"not configured at all":              newHarness(t).service,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := service.ConnectOryxa(ctx, memberToken, "org_1", ConnectInput{Toolkit: "github"})
			if !errors.Is(err, domain.ErrNotConnectable) {
				t.Fatalf("connect = %v", err)
			}
			if _, err := service.Manifest("github"); !errors.Is(err, domain.ErrNotConnectable) {
				t.Fatalf("manifest = %v", err)
			}
			if _, err := service.OAuthClient(ctx, adminToken, "org_1", "github"); !errors.Is(err, domain.ErrNotConnectable) {
				t.Fatalf("oauth client = %v", err)
			}
			if _, err := service.ReplaceCredential(ctx, memberToken, "org_1", "apc_1", ReplaceCredentialInput{}); !errors.Is(err, domain.ErrNotConnectable) {
				t.Fatalf("replace = %v", err)
			}
			if integrations := service.Integrations(); len(integrations) != 0 {
				t.Fatalf("integrations = %v", integrations)
			}
		})
	}
}

func TestOryxaNeedsItsToolkitSwitchedOnAndItsKind(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github")
	for name, test := range map[string]struct {
		input ConnectInput
		want  error
	}{
		"a toolkit not switched on":  {input: ConnectInput{Toolkit: "slack"}, want: domain.ErrNotConnectable},
		"a toolkit nothing ships":    {input: ConnectInput{Toolkit: "zendesk"}, want: domain.ErrNotConnectable},
		"a kind the app does not do": {input: ConnectInput{Toolkit: "github", Kind: integration.KindBasic}, want: domain.ErrInvalid},
		"a return page elsewhere":    {input: ConnectInput{Toolkit: "github", ReturnTo: "https://evil.example/"}, want: domain.ErrInvalid},
		"a slug that is not one":     {input: ConnectInput{Toolkit: "not a slug"}, want: domain.ErrInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := h.service.ConnectOryxa(context.Background(), memberToken, "org_1", test.input); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
	if len(h.store.connections) != 0 || len(h.signin.starts) != 0 || len(h.signin.saves) != 0 {
		t.Fatalf("something was recorded: %d connections", len(h.store.connections))
	}
}

func TestOryxaNewRefusesAToolkitNothingShips(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t)
	_, err := New(Dependencies{
		Store: h.store, Directory: h.directory, Composio: h.composio,
		Oryxa: &Oryxa{Store: h.store, Signin: h.signin, Hooks: h.hooks, Integrations: h.hooks.registry,
			Toolkits: []string{"zendesk"}, RedirectURI: redirectURI},
	}, Config{PublicBaseURL: "https://api.oryxa.test"})
	if err == nil {
		t.Fatal("New() switched on a toolkit no integration registers")
	}
}

func TestAgentsCannotConnectReplaceKeysOrManageOAuthClients(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github", "notion")
	h.oryxaConnection("apc_key", "user-1", "github", domain.ConnectionActive, integration.KindAPIToken)
	ctx := context.Background()

	checks := map[string]error{}
	_, checks["connect"] = h.service.ConnectOryxa(ctx, agentToken, "org_1", ConnectInput{Toolkit: "github"})
	_, checks["replace a key"] = h.service.ReplaceCredential(ctx, agentToken, "org_1", "apc_key",
		ReplaceCredentialInput{Fields: map[string]string{"token": "ghp_new"}})
	_, checks["read a client"] = h.service.OAuthClient(ctx, adminAgentToken, "org_1", "github")
	_, checks["save a client"] = h.service.PutOAuthClient(ctx, adminAgentToken, "org_1", "github",
		OAuthClientInput{ClientID: "Iv1.own", ClientSecret: "own-secret-value-1234"})
	checks["remove a client"] = h.service.RemoveOAuthClient(ctx, adminAgentToken, "org_1", "github")
	_, checks["reveal a token"] = h.service.RevealVerificationToken(ctx, adminAgentToken, "org_1", "notion")
	for name, err := range checks {
		if !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s = %v, want forbidden", name, err)
		}
	}
	if len(h.signin.starts)+len(h.signin.saves)+len(h.signin.puts)+len(h.signin.removedCalls) != 0 || len(h.hooks.revealed) != 0 {
		t.Fatal("an agent reached signin or hooks")
	}
}

func TestOneCurrentConnectionHoldsAcrossBrokers(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github")
	h.composioConnection("apc_composio", "user-1", "github")

	if _, err := h.service.ConnectOryxa(context.Background(), memberToken, "org_1", ConnectInput{Toolkit: "github"}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("Oryxa beside Composio = %v, want conflict", err)
	}
	h.oryxaConnection("apc_oryxa", "user-2", "github", domain.ConnectionPending, "")
	h.composio.toolkits["github"] = composioToolkit("github")
	if _, _, err := h.service.Connect(context.Background(), otherToken, "org_1", ConnectInput{Toolkit: "github"}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("Composio beside Oryxa = %v, want conflict", err)
	}
}

// startSignin connects github through Oryxa's OAuth and answers the pending
// connection.
func (h oryxaHarness) startSignin(t *testing.T) domain.Connection {
	t.Helper()
	connected, err := h.service.ConnectOryxa(context.Background(), memberToken, "org_1", ConnectInput{
		Toolkit: "github", ReturnTo: "https://app.oryxa.test/home", Host: "app.oryxa.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	h.signin.completion = signin.Completion{
		ConnectionID: connected.Connection.ID, OrganizationID: "org_1", MemberID: "user-1", Toolkit: "github",
		Kind: integration.KindOAuth2Code, ReturnTo: "https://app.oryxa.test/home",
		Account: signin.Account{Label: "octocat", Ref: "583231"},
	}
	return connected.Connection
}

func TestOryxaCallbackActivatesTheConnection(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github")
	pending := h.startSignin(t)

	completion, err := h.service.CompleteSignin(context.Background(), SigninCallback{
		State: "state-" + pending.ID, CookieState: "state-" + pending.ID, Code: "code-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if completion.Connection.ID != pending.ID || completion.Connection.Status != domain.ConnectionActive ||
		completion.Connection.Label != "octocat" || completion.ReturnTo != "https://app.oryxa.test/home" {
		t.Fatalf("completion = %+v", completion)
	}
	callback := h.signin.callbacks[0]
	if callback.State != "state-"+pending.ID || callback.CookieState != "state-"+pending.ID || callback.Code != "code-1" {
		t.Fatalf("callback = %+v", callback)
	}
	var connected appsv1.ConnectionConnected
	decodeLast(t, h.store, appsv1.ConnectionConnectedDetailType, &connected)
	if connected.Broker != appsv1.BrokerOryxa || connected.ConnectionID != pending.ID {
		t.Fatalf("connected = %+v", connected)
	}
}

func TestOryxaCallbackTheAppRefusedFailsTheConnection(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github")
	pending := h.startSignin(t)
	h.signin.completeErr = &signin.Failure{Reason: signin.ReasonDenied, Rule: "the app sent back no code"}

	completion, err := h.service.CompleteSignin(context.Background(), SigninCallback{
		State: "state-" + pending.ID, CookieState: "state-" + pending.ID, Error: "access_denied",
	})
	if err != nil {
		t.Fatal(err)
	}
	if completion.Connection.Status != domain.ConnectionFailed || completion.ReturnTo != "https://app.oryxa.test/home" {
		t.Fatalf("completion = %+v", completion)
	}
	if got := h.store.connection(pending.ID); got.Status != domain.ConnectionFailed || got.StatusReason != "the app did not grant access" {
		t.Fatalf("connection = %+v", got)
	}
	var failed appsv1.ConnectionFailed
	decodeLast(t, h.store, appsv1.ConnectionFailedDetailType, &failed)
	if failed.Reason != appsv1.FailedDeniedAtProvider || failed.Broker != appsv1.BrokerOryxa {
		t.Fatalf("failed = %+v", failed)
	}
}

func TestOryxaCallbackThatNamesNoSigninIsAnError(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github")
	pending := h.startSignin(t)
	h.signin.completion, h.signin.completeErr = signin.Completion{}, signin.ErrBadState

	if _, err := h.service.CompleteSignin(context.Background(), SigninCallback{State: "forged", CookieState: "other"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("error = %v, want forbidden", err)
	}
	if got := h.store.connection(pending.ID); got.Status != domain.ConnectionPending {
		t.Fatalf("connection = %+v", got)
	}
}

func TestOryxaCallbackForAConnectionRemovedMeanwhileLeavesItRemoved(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github")
	pending := h.startSignin(t)
	if err := h.service.RemoveConnection(context.Background(), memberToken, "org_1", pending.ID); err != nil {
		t.Fatal(err)
	}
	h.signin.completeErr = signin.ErrNotPending

	completion, err := h.service.CompleteSignin(context.Background(), SigninCallback{State: "s", CookieState: "s", Code: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if completion.Connection.Status != domain.ConnectionRemoved {
		t.Fatalf("completion = %+v", completion)
	}
	if failed := h.store.published(appsv1.ConnectionFailedDetailType); len(failed) != 0 {
		t.Fatalf("a removed connection was failed too: %d", len(failed))
	}
}

// When the browser coming back is signed in to Oryxa at all, it must be as
// the member who began the sign-in, in the organization they began it in.
func TestOryxaCallbackChecksTheAccessCookieAgainstTheFlow(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		cookie      string
		unavailable bool
		wantStatus  domain.ConnectionStatus
		wantReason  string
	}{
		"no cookie":                      {wantStatus: domain.ConnectionActive},
		"the member's own":               {cookie: "member-session", wantStatus: domain.ConnectionActive},
		"a cookie that is no session":    {cookie: "expired-session", wantStatus: domain.ConnectionActive},
		"another member's":               {cookie: "other-session", wantStatus: domain.ConnectionFailed, wantReason: appsv1.FailedWrongAccount},
		"the member in another org":      {cookie: "elsewhere-session", wantStatus: domain.ConnectionFailed, wantReason: appsv1.FailedWrongAccount},
		"auth not answering, fails shut": {cookie: "member-session", unavailable: true, wantStatus: domain.ConnectionFailed, wantReason: appsv1.FailedUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newOryxaHarness(t, "github")
			pending := h.startSignin(t)
			h.directory.unavailable = test.unavailable

			completion, err := h.service.CompleteSignin(context.Background(), SigninCallback{
				State: "s", CookieState: "s", Code: "c", AccessCookie: test.cookie,
			})
			if err != nil {
				t.Fatal(err)
			}
			if completion.Connection.ID != pending.ID || completion.Connection.Status != test.wantStatus {
				t.Fatalf("completion = %+v, want %s", completion.Connection, test.wantStatus)
			}
			if test.wantReason != "" {
				var failed appsv1.ConnectionFailed
				decodeLast(t, h.store, appsv1.ConnectionFailedDetailType, &failed)
				if failed.Reason != test.wantReason {
					t.Fatalf("reason = %q, want %q", failed.Reason, test.wantReason)
				}
			}
		})
	}
}

func TestOryxaReplaceCredentialKeepsTheNewKeyAndAnnouncesIt(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github")
	h.oryxaConnection("apc_key", "user-1", "github", domain.ConnectionActive, integration.KindAPIToken)
	h.signin.account = signin.Account{Label: "hubot"}

	connection, err := h.service.ReplaceCredential(context.Background(), memberToken, "org_1", "apc_key",
		ReplaceCredentialInput{Fields: map[string]string{"token": "ghp_newvalue"}})
	if err != nil {
		t.Fatal(err)
	}
	if connection.Label != "hubot" || connection.Status != domain.ConnectionActive {
		t.Fatalf("connection = %+v", connection)
	}
	save := h.signin.saves[0]
	if save.ConnectionID != "apc_key" || save.Kind != integration.KindAPIToken || save.Fields["token"] != "ghp_newvalue" {
		t.Fatalf("save = %+v", save)
	}
	var replaced appsv1.CredentialReplaced
	decodeLast(t, h.store, appsv1.CredentialReplacedDetailType, &replaced)
	if replaced.ConnectionID != "apc_key" || replaced.Kind != integration.KindAPIToken || replaced.MemberID != "user-1" ||
		!replaced.ReplacedAt.Equal(testClock) {
		t.Fatalf("replaced = %+v", replaced)
	}
	if strings.Contains(h.allEvents(), "ghp_newvalue") {
		t.Fatal("an event carries the key")
	}
}

func TestOryxaReplaceCredentialRefusals(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github")
	h.oryxaConnection("apc_key", "user-1", "github", domain.ConnectionActive, integration.KindAPIToken)
	h.oryxaConnection("apc_oauth", "user-2", "github", domain.ConnectionActive, integration.KindOAuth2Code)
	h.oryxaConnection("apc_expired", "user-1", "github", domain.ConnectionExpired, integration.KindAPIToken)
	h.composioConnection("apc_composio", "user-3", "github")
	ctx := context.Background()
	fields := ReplaceCredentialInput{Fields: map[string]string{"token": "ghp_x"}}

	for name, test := range map[string]struct {
		caller       authclient.Credentials
		connectionID string
		input        ReplaceCredentialInput
		want         error
	}{
		"another member's connection":      {caller: otherToken, connectionID: "apc_key", input: fields, want: domain.ErrNotFound},
		"an admin, on a member's":          {caller: adminToken, connectionID: "apc_key", input: fields, want: domain.ErrNotFound},
		"a connection that is not there":   {caller: memberToken, connectionID: "apc_none", input: fields, want: domain.ErrNotFound},
		"an OAuth sign-in":                 {caller: otherToken, connectionID: "apc_oauth", input: fields, want: domain.ErrInvalid},
		"a Composio connection":            {caller: adminToken, connectionID: "apc_composio", input: fields, want: domain.ErrInvalid},
		"an expired connection":            {caller: memberToken, connectionID: "apc_expired", input: fields, want: domain.ErrNeedsReconnect},
		"another kind than the one it has": {caller: memberToken, connectionID: "apc_key", input: ReplaceCredentialInput{Kind: integration.KindBasic}, want: domain.ErrInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := h.service.ReplaceCredential(ctx, test.caller, "org_1", test.connectionID, test.input); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
	if len(h.signin.saves) != 0 {
		t.Fatalf("signin was asked %d times", len(h.signin.saves))
	}
}

// A new key the app refuses is not kept: the connection goes on with its old
// one, and nothing is announced.
func TestOryxaReplaceCredentialTheAppRefusesKeepsTheOldKey(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github")
	h.oryxaConnection("apc_key", "user-1", "github", domain.ConnectionActive, integration.KindAPIToken)
	h.signin.saveErr = &signin.Failure{Reason: signin.ReasonInvalidCredentials, Rule: "was refused by the app"}

	_, err := h.service.ReplaceCredential(context.Background(), memberToken, "org_1", "apc_key",
		ReplaceCredentialInput{Fields: map[string]string{"token": "ghp_badvalue"}})
	if !errors.Is(err, domain.ErrInvalidCredentials) || strings.Contains(err.Error(), "ghp_badvalue") {
		t.Fatalf("error = %v", err)
	}
	if got := h.store.connection("apc_key"); got.Status != domain.ConnectionActive || got.Label != "octocat" {
		t.Fatalf("connection = %+v", got)
	}
	if replaced := h.store.published(appsv1.CredentialReplacedDetailType); len(replaced) != 0 {
		t.Fatal("a refused key was announced as replaced")
	}
}

func composioToolkit(slug string) composio.Toolkit {
	return composio.Toolkit{Slug: slug, AuthSchemes: []string{"OAUTH2"}, ComposioManagedAuthSchemes: []string{"OAUTH2"}}
}
