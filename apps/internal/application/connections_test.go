package application

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

func TestConnectSetsUpTheAppAndRecordsItPending(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.composio.toolkits["github"] = composio.Toolkit{
		Slug: "github", AuthSchemes: []string{"OAUTH2"}, ComposioManagedAuthSchemes: []string{"OAUTH2"},
	}

	connection, redirect, err := h.service.Connect(context.Background(), memberToken, "org_1", ConnectInput{
		Toolkit: "GitHub", ReturnTo: "https://app.oryxa.test/settings/apps", Alias: "work",
	})
	if err != nil {
		t.Fatal(err)
	}
	if redirect != "https://connect.composio.dev/link/lt_1" {
		t.Fatalf("redirect = %q", redirect)
	}
	if connection.Toolkit != "github" || connection.Status != domain.ConnectionPending {
		t.Fatalf("connection = %+v", connection)
	}
	if connection.MemberID != "user-1" || connection.ComposioUserID != "oryxa.org_1.user-1" {
		t.Fatalf("connection is not the caller's: %+v", connection)
	}
	if connection.ComposioAccountID != "ca_new" || connection.ReturnTo != "https://app.oryxa.test/settings/apps" {
		t.Fatalf("connection = %+v", connection)
	}

	// Composio was asked for a managed auth config once, and the link
	// carries the member's Composio user and a callback with an opaque state.
	if len(h.composio.created) != 1 || h.composio.created[0] != "managed" {
		t.Fatalf("auth configs created = %v", h.composio.created)
	}
	if len(h.composio.links) != 1 {
		t.Fatalf("links = %d", len(h.composio.links))
	}
	link := h.composio.links[0]
	if link.UserID != "oryxa.org_1.user-1" || link.AuthConfigID != "ac_managed_github" || link.Alias != "work" {
		t.Fatalf("link request = %+v", link)
	}
	callback, err := url.Parse(link.CallbackURL)
	if err != nil {
		t.Fatal(err)
	}
	if callback.Scheme+"://"+callback.Host+callback.Path != "https://app.oryxa.test"+callbackPath {
		t.Fatalf("callback = %q", link.CallbackURL)
	}
	state := callback.Query().Get("state")
	if len(state) < 40 {
		t.Fatalf("state = %q", state)
	}

	// Only the hash of the state is kept, and it is what finds the
	// connection again.
	if _, held := h.store.states[state]; held {
		t.Fatal("the state itself was stored")
	}
	if _, err := h.store.PendingConnection(context.Background(), hashState(state)); err != nil {
		t.Fatalf("pending connection by state hash: %v", err)
	}
	// The saved auth config is reused by the next member who connects it.
	if config, err := h.store.AuthConfig(context.Background(), "github"); err != nil || !config.ComposioManaged {
		t.Fatalf("auth config = %+v, %v", config, err)
	}
}

func TestConnectReusesWhatTheProjectAlreadyHas(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.composio.toolkits["slack"] = composio.Toolkit{Slug: "slack", AuthSchemes: []string{"OAUTH2"}}
	// A config made in Composio's dashboard, and a disabled leftover.
	h.composio.authConfigs = []composio.AuthConfig{
		{ID: "ac_old", Toolkit: "slack", AuthScheme: "OAUTH2", Enabled: false},
		{ID: "ac_dashboard", Toolkit: "slack", AuthScheme: "OAUTH2", IsComposioManaged: true, Enabled: true},
	}

	if _, _, err := h.service.Connect(context.Background(), memberToken, "org_1", ConnectInput{Toolkit: "slack"}); err != nil {
		t.Fatal(err)
	}
	if len(h.composio.created) != 0 {
		t.Fatalf("created a config although one existed: %v", h.composio.created)
	}
	if h.composio.links[0].AuthConfigID != "ac_dashboard" {
		t.Fatalf("auth config used = %q", h.composio.links[0].AuthConfigID)
	}
	// A second member connecting the same app reuses the recorded config
	// without asking Composio again.
	if _, _, err := h.service.Connect(context.Background(), otherToken, "org_1", ConnectInput{Toolkit: "slack"}); err != nil {
		t.Fatal(err)
	}
	if h.composio.links[1].AuthConfigID != "ac_dashboard" {
		t.Fatalf("auth config reused = %q", h.composio.links[1].AuthConfigID)
	}
}

func TestConnectKeepsOneCurrentConnectionPerMemberAndApp(t *testing.T) {
	t.Run("pending", func(t *testing.T) {
		h := newHarness(t)
		h.pending(t, "gmail")
		_, _, err := h.service.Connect(context.Background(), memberToken, "org_1", ConnectInput{Toolkit: "gmail"})
		if !errors.Is(err, domain.ErrConflict) || !strings.Contains(err.Error(), "already being connected") {
			t.Fatalf("second pending connection error=%v", err)
		}
		if len(h.composio.links) != 1 {
			t.Fatalf("connect links=%d, want no second provider flow", len(h.composio.links))
		}
	})

	t.Run("active", func(t *testing.T) {
		h := newHarness(t)
		h.activeConnection("apc_gmail", "user-1", "gmail", "ca_gmail")
		_, _, err := h.service.Connect(context.Background(), memberToken, "org_1", ConnectInput{Toolkit: "gmail"})
		if !errors.Is(err, domain.ErrConflict) || !strings.Contains(err.Error(), "already connected") {
			t.Fatalf("second active connection error=%v", err)
		}
		if len(h.composio.links) != 0 {
			t.Fatalf("connect links=%d, want no provider flow", len(h.composio.links))
		}
	})

	t.Run("expired can reconnect", func(t *testing.T) {
		h := newHarness(t)
		expired := h.activeConnection("apc_old", "user-1", "gmail", "ca_old")
		expired.Status = domain.ConnectionExpired
		h.store.connections[expired.ID] = expired
		h.composio.toolkits["gmail"] = composio.Toolkit{Slug: "gmail", AuthSchemes: []string{"OAUTH2"}, ComposioManagedAuthSchemes: []string{"OAUTH2"}}
		if _, _, err := h.service.Connect(context.Background(), memberToken, "org_1", ConnectInput{Toolkit: "gmail"}); err != nil {
			t.Fatalf("reconnect error=%v", err)
		}
	})
}

func TestConnectRevokesAProviderAccountThatLosesTheConnectionRace(t *testing.T) {
	h := newHarness(t)
	h.composio.toolkits["gmail"] = composio.Toolkit{Slug: "gmail", AuthSchemes: []string{"OAUTH2"}, ComposioManagedAuthSchemes: []string{"OAUTH2"}}
	h.store.createConnectionErr = domain.ErrConflict

	if _, _, err := h.service.Connect(context.Background(), memberToken, "org_1", ConnectInput{Toolkit: "gmail"}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("connect error=%v", err)
	}
	if len(h.composio.deleted) != 1 || h.composio.deleted[0] != "ca_new" || len(h.composio.revoked) != 1 || !h.composio.revoked[0] {
		t.Fatalf("deleted=%v revoked=%v", h.composio.deleted, h.composio.revoked)
	}
}

func TestConnectUsesASchemeTheMemberTypesInWhenComposioHasNoApp(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.composio.toolkits["stripe"] = composio.Toolkit{Slug: "stripe", AuthSchemes: []string{"OAUTH2", "API_KEY"}}

	if _, _, err := h.service.Connect(context.Background(), memberToken, "org_1", ConnectInput{Toolkit: "stripe"}); err != nil {
		t.Fatal(err)
	}
	if len(h.composio.created) != 1 || h.composio.created[0] != "API_KEY" {
		t.Fatalf("created = %v", h.composio.created)
	}
}

func TestConnectRefusesWhatItCannotConnect(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.composio.toolkits["oauthonly"] = composio.Toolkit{Slug: "oauthonly", AuthSchemes: []string{"OAUTH2"}}
	h.composio.toolkits["hackernews"] = composio.Toolkit{Slug: "hackernews", NoAuth: true}

	for name, testCase := range map[string]struct {
		input ConnectInput
		token string
		want  error
	}{
		"an app needing an OAuth app of ours": {input: ConnectInput{Toolkit: "oauthonly"}, want: domain.ErrNotConnectable},
		"an app needing no connection":        {input: ConnectInput{Toolkit: "hackernews"}, want: domain.ErrNotConnectable},
		"an app that does not exist":          {input: ConnectInput{Toolkit: "nosuchapp"}, want: domain.ErrNotFound},
		"a slug that is not one":              {input: ConnectInput{Toolkit: "not a slug"}, want: domain.ErrInvalid},
		"a return page elsewhere":             {input: ConnectInput{Toolkit: "oauthonly", ReturnTo: "https://evil.example/steal"}, want: domain.ErrInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := h.service.Connect(context.Background(), memberToken, "org_1", testCase.input); !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
		})
	}
	t.Run("an agent acting for the member", func(t *testing.T) {
		_, _, err := h.service.Connect(context.Background(), agentToken, "org_1", ConnectInput{Toolkit: "github"})
		if !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("error = %v, want forbidden", err)
		}
	})
	t.Run("another organization", func(t *testing.T) {
		_, _, err := h.service.Connect(context.Background(), memberToken, "org_2", ConnectInput{Toolkit: "github"})
		if !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("error = %v, want forbidden", err)
		}
	})
	t.Run("nothing recorded", func(t *testing.T) {
		if len(h.store.connections) != 0 {
			t.Fatalf("connections recorded = %d", len(h.store.connections))
		}
	})
}

func TestReturnAllowed(t *testing.T) {
	t.Parallel()
	service, err := New(Dependencies{Store: newFakeStore(), Directory: &fakeDirectory{}, Composio: newFakeComposio()},
		Config{PublicBaseURL: "https://apps.oryxa.test", ReturnOrigins: []string{"https://app.oryxa.test/settings"}})
	if err != nil {
		t.Fatal(err)
	}
	for target, want := range map[string]bool{
		"https://app.oryxa.test/settings":            true,
		"https://APP.oryxa.test/settings/apps?ok=1":  true,
		"https://app.oryxa.test/settingsomething":    false,
		"https://app.oryxa.test/other":               false,
		"https://app.oryxa.test/settings/../other":   false,
		"https://app.oryxa.test/settings/%2e%2e/out": false,
		"http://app.oryxa.test/settings":             false,
		"https://app.oryxa.test.evil.example/":       false,
		"https://user@app.oryxa.test/settings":       false,
		"//app.oryxa.test/settings":                  false,
		"javascript:alert(1)":                        false,
		"":                                           false,
	} {
		if got := service.returnAllowed(target); got != want {
			t.Errorf("returnAllowed(%q) = %v, want %v", target, got, want)
		}
	}
}

// pending starts a connection and answers the state its callback carries.
func (h harness) pending(t *testing.T, toolkit string) (domain.Connection, string) {
	t.Helper()
	h.composio.toolkits[toolkit] = composio.Toolkit{
		Slug: toolkit, AuthSchemes: []string{"OAUTH2"}, ComposioManagedAuthSchemes: []string{"OAUTH2"},
	}
	connection, _, err := h.service.Connect(context.Background(), memberToken, "org_1", ConnectInput{
		Toolkit: toolkit, ReturnTo: "https://app.oryxa.test/apps",
	})
	if err != nil {
		t.Fatal(err)
	}
	callback, err := url.Parse(h.composio.links[len(h.composio.links)-1].CallbackURL)
	if err != nil {
		t.Fatal(err)
	}
	return connection, callback.Query().Get("state")
}

// account is what Composio answers for the account a pending connection made.
func account(connection domain.Connection, status string) composio.ConnectedAccount {
	return composio.ConnectedAccount{
		ID: connection.ComposioAccountID, UserID: connection.ComposioUserID, Toolkit: connection.Toolkit,
		AuthConfigID: connection.AuthConfigID, Status: status, WordID: "github_red-castle",
	}
}

func TestCompleteConnectionActivatesWhatComposioConfirms(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	connection, state := h.pending(t, "github")
	// Composio redirects a moment before the account is active.
	h.composio.accounts[connection.ComposioAccountID] = []composio.ConnectedAccount{
		account(connection, composio.StatusInitiated),
		account(connection, composio.StatusActive),
	}

	completion, err := h.service.CompleteConnection(context.Background(), ConnectCallback{State: state, CookieState: state, Status: "success", AccountID: connection.ComposioAccountID})
	if err != nil {
		t.Fatal(err)
	}
	if completion.Connection.Status != domain.ConnectionActive {
		t.Fatalf("status = %q", completion.Connection.Status)
	}
	if completion.Connection.Label != "github_red-castle" {
		t.Fatalf("label = %q", completion.Connection.Label)
	}
	if completion.ReturnTo != "https://app.oryxa.test/apps" {
		t.Fatalf("return to = %q", completion.ReturnTo)
	}
	events := h.store.published(appsv1.ConnectionConnectedDetailType)
	if len(events) != 1 {
		t.Fatalf("connected events = %d", len(events))
	}
	if !strings.Contains(string(events[0].Data), `"toolkit":"github"`) {
		t.Fatalf("event data = %s", events[0].Data)
	}

	// The state is spent: a replayed callback finds nothing.
	if _, err := h.service.CompleteConnection(context.Background(), ConnectCallback{State: state, CookieState: state, Status: "success"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("replayed callback = %v, want not found", err)
	}
	if len(h.store.published(appsv1.ConnectionConnectedDetailType)) != 1 {
		t.Fatal("the replay published a second event")
	}
}

func TestCompleteConnectionRefusesACallbackAboutSomeoneElse(t *testing.T) {
	t.Parallel()
	for name, prepare := range map[string]func(domain.Connection) (string, composio.ConnectedAccount){
		"an account the callback names instead": func(connection domain.Connection) (string, composio.ConnectedAccount) {
			return "ca_someone_else", account(connection, composio.StatusActive)
		},
		"an account of another member": func(connection domain.Connection) (string, composio.ConnectedAccount) {
			other := account(connection, composio.StatusActive)
			other.UserID = "oryxa.org_1.user-2"
			return "", other
		},
		"an account of another app": func(connection domain.Connection) (string, composio.ConnectedAccount) {
			other := account(connection, composio.StatusActive)
			other.Toolkit = "slack"
			return "", other
		},
		"an account on another auth config": func(connection domain.Connection) (string, composio.ConnectedAccount) {
			other := account(connection, composio.StatusActive)
			other.AuthConfigID = "ac_elsewhere"
			return "", other
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			connection, state := h.pending(t, "github")
			reported, answered := prepare(connection)
			h.composio.accounts[connection.ComposioAccountID] = []composio.ConnectedAccount{answered}

			_, err := h.service.CompleteConnection(context.Background(), ConnectCallback{State: state, CookieState: state, Status: "success", AccountID: reported})
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("error = %v, want forbidden", err)
			}
			// The connection is failed and its state spent, so the same trick
			// cannot be tried again.
			if got := h.store.connections[connection.ID]; got.Status != domain.ConnectionFailed {
				t.Fatalf("status = %q", got.Status)
			}
			if _, err := h.service.CompleteConnection(context.Background(), ConnectCallback{State: state, CookieState: state, Status: "success", AccountID: reported}); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("second attempt = %v, want not found", err)
			}
			if len(h.store.published(appsv1.ConnectionConnectedDetailType)) != 0 {
				t.Fatal("a refused callback published an event")
			}
		})
	}
}

func TestCompleteConnectionFailsWhatDidNotFinish(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		status         string
		reportedStatus string
		want           domain.ConnectionStatus
	}{
		"Composio says it failed":       {status: composio.StatusFailed, reportedStatus: "success", want: domain.ConnectionFailed},
		"Composio says it was revoked":  {status: composio.StatusRevoked, reportedStatus: "success", want: domain.ConnectionFailed},
		"the member cancelled":          {status: composio.StatusInitiated, reportedStatus: "failed", want: domain.ConnectionFailed},
		"the member has not finished":   {status: composio.StatusInitiated, reportedStatus: "success", want: domain.ConnectionPending},
		"the member closed the browser": {status: composio.StatusInitiated, reportedStatus: "", want: domain.ConnectionPending},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			connection, state := h.pending(t, "github")
			h.composio.accounts[connection.ComposioAccountID] = []composio.ConnectedAccount{account(connection, testCase.status)}

			completion, err := h.service.CompleteConnection(context.Background(), ConnectCallback{State: state, CookieState: state, Status: testCase.reportedStatus})
			if err != nil {
				t.Fatal(err)
			}
			if completion.Connection.Status != testCase.want {
				t.Fatalf("status = %q, want %q", completion.Connection.Status, testCase.want)
			}
			if completion.ReturnTo != "https://app.oryxa.test/apps" {
				t.Fatalf("return to = %q", completion.ReturnTo)
			}
			if len(h.store.published(appsv1.ConnectionConnectedDetailType)) != 0 {
				t.Fatal("published a connected event")
			}
			// A connection still pending can be finished later; a failed one
			// cannot be resumed.
			_, err = h.service.CompleteConnection(context.Background(), ConnectCallback{State: state, CookieState: state, Status: testCase.reportedStatus})
			if testCase.want == domain.ConnectionPending {
				if err != nil {
					t.Fatalf("second callback = %v", err)
				}
			} else if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("second callback = %v, want not found", err)
			}
		})
	}
}

func TestCompleteConnectionNeedsAState(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	if _, err := h.service.CompleteConnection(context.Background(), ConnectCallback{Status: "success"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("no state = %v, want invalid", err)
	}
	if _, err := h.service.CompleteConnection(context.Background(), ConnectCallback{State: "made-up", CookieState: "made-up", Status: "success"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown state = %v, want not found", err)
	}
}

func TestConnectionsListWhatTheCallerMaySee(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.activeConnection("apc_1", "user-1", "github", "ca_1")
	theirs := h.activeConnection("apc_2", "user-2", "slack", "ca_2")

	for name, testCase := range map[string]struct {
		credentials  authclient.Credentials
		organization bool
		want         []string
	}{
		"a member sees their own":            {credentials: memberToken, want: []string{mine.ID}},
		"a member cannot widen the listing":  {credentials: memberToken, organization: true, want: []string{mine.ID}},
		"an admin sees their own by default": {credentials: adminToken, want: nil},
		"an admin can see the organization":  {credentials: adminToken, organization: true, want: []string{mine.ID, theirs.ID}},
		"an agent sees its member's":         {credentials: agentToken, organization: true, want: []string{mine.ID}},
	} {
		t.Run(name, func(t *testing.T) {
			listed, err := h.service.Connections(context.Background(), testCase.credentials, "org_1", "", testCase.organization)
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, connection := range listed {
				ids = append(ids, connection.ID)
			}
			if strings.Join(ids, ",") != strings.Join(testCase.want, ",") {
				t.Fatalf("listed = %v, want %v", ids, testCase.want)
			}
		})
	}

	t.Run("by app", func(t *testing.T) {
		listed, err := h.service.Connections(context.Background(), adminToken, "org_1", "SLACK", true)
		if err != nil {
			t.Fatal(err)
		}
		if len(listed) != 1 || listed[0].ID != theirs.ID {
			t.Fatalf("listed = %+v", listed)
		}
	})
	t.Run("one connection", func(t *testing.T) {
		if _, err := h.service.Connection(context.Background(), memberToken, "org_1", mine.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := h.service.Connection(context.Background(), memberToken, "org_1", theirs.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("another member's = %v, want not found", err)
		}
		if _, err := h.service.Connection(context.Background(), adminToken, "org_1", theirs.ID); err != nil {
			t.Fatalf("an admin's read = %v", err)
		}
		if _, err := h.service.Connection(context.Background(), agentToken, "org_1", theirs.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("an agent reading another member's = %v, want not found", err)
		}
	})
}

func TestRemoveConnectionDeletesItAtComposio(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	connection := h.activeConnection("apc_1", "user-1", "github", "ca_1")
	h.store.triggers["apt_1"] = domain.Trigger{
		ID: "apt_1", OrganizationID: "org_1", MemberID: "user-1", ConnectionID: connection.ID,
		Toolkit: "github", Slug: "GITHUB_COMMIT_EVENT", ComposioTriggerID: "ti_1", Status: domain.TriggerActive,
	}

	if err := h.service.RemoveConnection(context.Background(), memberToken, "org_1", connection.ID); err != nil {
		t.Fatal(err)
	}
	if got := h.store.connections[connection.ID]; got.Status != domain.ConnectionRemoved || got.StatusReason != appsv1.RemovedByMember {
		t.Fatalf("connection = %+v", got)
	}
	if got := h.store.triggers["apt_1"]; got.Status != domain.TriggerRemoved {
		t.Fatalf("trigger = %+v", got)
	}
	if len(h.composio.deletedTriggers) != 1 || h.composio.deletedTriggers[0] != "ti_1" {
		t.Fatalf("deleted triggers = %v", h.composio.deletedTriggers)
	}
	if len(h.composio.deleted) != 1 || h.composio.deleted[0] != "ca_1" || !h.composio.revoked[0] {
		t.Fatalf("deleted accounts = %v, revoked = %v", h.composio.deleted, h.composio.revoked)
	}
	events := h.store.published(appsv1.ConnectionRemovedDetailType)
	if len(events) != 1 || !strings.Contains(string(events[0].Data), `"reason":"`+appsv1.RemovedByMember+`"`) {
		t.Fatalf("removed events = %+v", events)
	}

	// Removing again is quiet, and publishes nothing twice.
	if err := h.service.RemoveConnection(context.Background(), memberToken, "org_1", connection.ID); err != nil {
		t.Fatal(err)
	}
	if len(h.store.published(appsv1.ConnectionRemovedDetailType)) != 1 {
		t.Fatal("a second removal published an event")
	}
}

func TestRemoveConnectionToleratesWhatComposioNoLongerHas(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.composio.deleteErr = composio.ErrNotFound
	connection := h.activeConnection("apc_1", "user-1", "github", "ca_1")

	if err := h.service.RemoveConnection(context.Background(), memberToken, "org_1", connection.ID); err != nil {
		t.Fatal(err)
	}
	if got := h.store.connections[connection.ID]; got.Status != domain.ConnectionRemoved {
		t.Fatalf("status = %q", got.Status)
	}
}

func TestRemoveConnectionKeepsItWhenComposioCannotDeleteIt(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.composio.deleteErr = composio.ErrUnavailable
	connection := h.activeConnection("apc_1", "user-1", "github", "ca_1")

	err := h.service.RemoveConnection(context.Background(), memberToken, "org_1", connection.ID)
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("error = %v, want unavailable", err)
	}
	// It is still there to remove, so a retry can finish the job rather than
	// leaving a live credential behind a removed row.
	if got := h.store.connections[connection.ID]; got.Status != domain.ConnectionActive {
		t.Fatalf("status = %q, want active", got.Status)
	}
}

func TestRemoveConnectionChecksWhoIsAsking(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	theirs := h.activeConnection("apc_2", "user-2", "slack", "ca_2")

	if err := h.service.RemoveConnection(context.Background(), memberToken, "org_1", theirs.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("another member's = %v, want not found", err)
	}
	if err := h.service.RemoveConnection(context.Background(), agentToken, "org_1", theirs.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("an agent's = %v, want forbidden", err)
	}
	if got := h.store.connections[theirs.ID]; got.Status != domain.ConnectionActive {
		t.Fatalf("status = %q, want active", got.Status)
	}
	// An org admin may remove a member's connection, and it says who did.
	if err := h.service.RemoveConnection(context.Background(), adminToken, "org_1", theirs.ID); err != nil {
		t.Fatal(err)
	}
	if got := h.store.connections[theirs.ID]; got.StatusReason != appsv1.RemovedByAdmin {
		t.Fatalf("reason = %q", got.StatusReason)
	}
}

// A connect link finished in a browser that did not start it, as a forwarded
// link is, is refused, and the connection and the account Composio made
// for it are removed (#476). The member's own browser finishes it with the
// cookie, or, started from the CLI, by being signed in as them.
func TestCompleteConnectionOnlyFromTheStartingBrowser(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		cookie, session string
		want            error
	}{
		"no cookie, no session":           {want: domain.ErrForbidden},
		"another cookie":                  {cookie: "forged", want: domain.ErrForbidden},
		"the cookie, another member":      {cookie: "same", session: "other", want: domain.ErrForbidden},
		"no cookie, another member":       {session: "other", want: domain.ErrForbidden},
		"no cookie, an agent's session":   {session: "agent", want: domain.ErrForbidden},
		"the cookie, a dead session":      {cookie: "same", session: "expired"},
		"the cookie alone":                {cookie: "same"},
		"no cookie, the member signed in": {session: "member"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			connection, state := h.pending(t, "github")
			h.composio.accounts[connection.ComposioAccountID] = []composio.ConnectedAccount{account(connection, composio.StatusActive)}
			cookie := test.cookie
			if cookie == "same" {
				cookie = state
			}
			completion, err := h.service.CompleteConnection(context.Background(), ConnectCallback{
				State: state, CookieState: cookie, AccessCookie: test.session, Status: "success",
			})
			if test.want == nil {
				if err != nil || completion.Connection.Status != domain.ConnectionActive {
					t.Fatalf("CompleteConnection = %+v, %v", completion.Connection, err)
				}
				return
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("CompleteConnection = %v, want %v", err, test.want)
			}
			stored := h.store.connections[connection.ID]
			if stored.Status != domain.ConnectionRemoved || stored.StatusReason != RemovedWrongBrowser {
				t.Fatalf("connection = %s %q", stored.Status, stored.StatusReason)
			}
			if !slices.Contains(h.composio.deleted, connection.ComposioAccountID) {
				t.Fatalf("Composio deleted %v, want the account", h.composio.deleted)
			}
			// The state is spent: the starting browser cannot finish it now.
			if _, err := h.service.CompleteConnection(context.Background(), ConnectCallback{State: state, CookieState: state}); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("after refusal = %v, want not found", err)
			}
		})
	}
}

// The state is the browser's nonce, and the callback goes back to the host
// the connect was sent to, which is where that cookie lives.
func TestConnectUsesTheBrowsersNonceAndHost(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.composio.toolkits["github"] = composio.Toolkit{Slug: "github", AuthSchemes: []string{"OAUTH2"}, ComposioManagedAuthSchemes: []string{"OAUTH2"}}
	connection, _, err := h.service.Connect(context.Background(), memberToken, "org_1", ConnectInput{
		Toolkit: "github", BrowserNonce: "nonce-1", Host: "APP.oryxa.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := h.composio.links[0].CallbackURL; got != "https://app.oryxa.test"+callbackPath+"?state=nonce-1" {
		t.Fatalf("callback = %q", got)
	}
	if pending, err := h.store.PendingConnection(context.Background(), hashState("nonce-1")); err != nil || pending.ID != connection.ID {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
}
