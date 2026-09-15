package application

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	connectionseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/connections/v1"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/providers"
)

const (
	organization = "org_1"
	adminToken   = "Bearer admin"
	memberToken  = "Bearer member"
	otherToken   = "Bearer other"
	returnTo     = "https://app.example.test/connections?tab=apps"
)

var ctx = context.Background()

type harness struct {
	service *Service
	store   *fakeStore
	oauth   *fakeOAuth
	now     time.Time
}

// newHarness has an org admin, two members, and one team: the member can
// manage it and the other member can only see it. The member also owns
// org_2. GitHub connections come back as account 42 with read:user and repo.
func newHarness(t *testing.T, change ...func(*Dependencies)) *harness {
	t.Helper()
	h := &harness{
		store: newFakeStore(),
		oauth: &fakeOAuth{
			grant:   providers.Grant{AccessToken: "access-1", RefreshToken: "refresh-1", Scopes: []string{"read:user", "repo"}},
			account: domain.ProviderAccount{ID: "42", Name: "octocat"},
		},
		now: time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC),
	}
	dependencies := Dependencies{
		Store: h.store,
		Directory: fakeDirectory{
			adminToken: {{OrganizationID: organization, UserID: "user-admin", Role: domain.OrgRoleAdmin}},
			// The member also owns a second organization.
			memberToken: {
				{OrganizationID: organization, UserID: "user-1", Role: domain.OrgRoleMember},
				{OrganizationID: "org_2", UserID: "user-1", Role: domain.OrgRoleOwner},
			},
			otherToken: {{OrganizationID: organization, UserID: "user-2", Role: domain.OrgRoleMember}},
		},
		Teams: fakeTeams{
			memberToken + "/team_1": {TeamID: "team_1", CanManage: true, Member: true},
			otherToken + "/team_1":  {TeamID: "team_1", Member: true},
		},
		Sealer:         fakeSealer{},
		ProviderClient: h.oauth,
		Providers:      providers.Builtin(),
		Now:            func() time.Time { return h.now },
	}
	for _, apply := range change {
		apply(&dependencies)
	}
	h.service = New(dependencies, Config{
		PublicBaseURL:  "https://connect.example.test/",
		AllowedOrigins: []string{"https://app.example.test", "http://localhost:3000"},
	})
	return h
}

func as(token string) authclient.Credentials {
	return authclient.Credentials{Authorization: token}
}

func (h *harness) createApp(t *testing.T, provider string, scopes ...string) domain.App {
	t.Helper()
	app, err := h.service.CreateApp(ctx, as(adminToken), organization, CreateAppInput{
		Provider: provider, ClientID: "client-1", ClientSecret: "secret-1", Scopes: scopes,
	})
	if err != nil {
		t.Fatalf("CreateApp(%s) error = %v", provider, err)
	}
	return app
}

// callback plays the provider sending the browser back with a code, and
// returns the session ID the app completes with.
func (h *harness) callback(t *testing.T, provider string, authorization Authorization) string {
	t.Helper()
	target, err := h.service.Callback(ctx, provider, CallbackParams{State: stateOf(t, authorization), Code: "code-1"})
	if err != nil {
		t.Fatalf("Callback() error = %v", err)
	}
	parsed, err := url.Parse(target)
	if err != nil || parsed.Query().Get("session_id") == "" {
		t.Fatalf("Callback() target = %q", target)
	}
	return parsed.Query().Get("session_id")
}

// connect runs authorize, the callback and complete as one caller.
func (h *harness) connect(t *testing.T, token, provider string, input AuthorizeInput) (Completion, error) {
	t.Helper()
	if input.ReturnURI == "" {
		input.ReturnURI = returnTo
	}
	authorization, err := h.service.Authorize(ctx, as(token), organization, input)
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	return h.service.Complete(ctx, as(token), organization, h.callback(t, provider, authorization))
}

func stateOf(t *testing.T, authorization Authorization) string {
	t.Helper()
	parsed, err := url.Parse(authorization.URL)
	if err != nil || parsed.Query().Get("state") == "" {
		t.Fatalf("authorization URL = %q", authorization.URL)
	}
	return parsed.Query().Get("state")
}

func opened(t *testing.T, sealed *domain.SealedData, binding func() ([]byte, error)) string {
	t.Helper()
	if sealed == nil {
		t.Fatal("value was not sealed")
	}
	plaintext, err := binding()
	if err != nil {
		t.Fatalf("open sealed value: %v", err)
	}
	return string(plaintext)
}

func TestCreateAppSealsTheClientSecret(t *testing.T) {
	h := newHarness(t)
	app := h.createApp(t, "github", "repo", "read:user", "repo")
	if app.Provider != "github" || app.CredentialType != domain.CredentialOAuth2 || app.ClientID != "client-1" ||
		!slices.Equal(app.Scopes, []string{"read:user", "repo"}) || app.Status != domain.AppStatusActive || app.CreatedBy != "user-admin" {
		t.Fatalf("CreateApp() = %+v", app)
	}
	stored, err := h.store.App(ctx, organization, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	var sealedSecrets appSecrets
	if err := json.Unmarshal([]byte(opened(t, stored.EncryptedSecret, func() ([]byte, error) {
		return fakeSealer{}.Open(ctx, appSecretBinding(stored), *stored.EncryptedSecret)
	})), &sealedSecrets); err != nil || sealedSecrets != (appSecrets{ClientSecret: "secret-1"}) {
		t.Fatalf("sealed app secrets = %+v %v", sealedSecrets, err)
	}
	created := h.store.eventsOf(connectionseventsv1.AppCreatedDetailType)
	if len(created) != 1 || strings.Contains(string(created[0].Data), "secret-1") || !strings.Contains(string(created[0].Data), app.ID) {
		t.Fatalf("app created events = %s", created)
	}

	list, err := h.service.ListApps(ctx, as(memberToken), organization)
	if err != nil || list.Admin || len(list.Apps) != 1 {
		t.Fatalf("member ListApps() = %+v %v", list, err)
	}
	if list, err := h.service.ListApps(ctx, as(adminToken), organization); err != nil || !list.Admin {
		t.Fatalf("admin ListApps() = %+v %v", list, err)
	}

	for name, test := range map[string]struct {
		token, organization string
		input               CreateAppInput
		want                error
	}{
		"member":             {memberToken, organization, CreateAppInput{Provider: "github", ClientID: "c", ClientSecret: "s"}, domain.ErrForbidden},
		"other organization": {adminToken, "org_2", CreateAppInput{Provider: "github", ClientID: "c", ClientSecret: "s"}, domain.ErrNotFound},
		"unknown provider":   {adminToken, organization, CreateAppInput{Provider: "nope", ClientID: "c", ClientSecret: "s"}, domain.ErrInvalid},
		"unsupported type": {adminToken, organization, CreateAppInput{Provider: "github", CredentialType: domain.CredentialAPIKey,
			ClientID: "c", ClientSecret: "s"}, domain.ErrInvalid},
		"beyond the ceiling": {adminToken, organization, CreateAppInput{Provider: "github", ClientID: "c", ClientSecret: "s",
			Scopes: []string{"read:user", "delete_repo"}}, domain.ErrInvalid},
		"no secret":         {adminToken, organization, CreateAppInput{Provider: "github", ClientID: "c"}, domain.ErrInvalid},
		"multi-line client": {adminToken, organization, CreateAppInput{Provider: "github", ClientID: "c\nd", ClientSecret: "s"}, domain.ErrInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := h.service.CreateApp(ctx, as(test.token), test.organization, test.input); !errors.Is(err, test.want) {
				t.Fatalf("CreateApp() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestUpdateAppRotatesSecretsAndMovesConnectionsOnClientChange(t *testing.T) {
	h := newHarness(t)
	app := h.createApp(t, "github", "read:user", "repo")
	completion, err := h.connect(t, memberToken, "github", AuthorizeInput{AppID: app.ID})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := h.service.UpdateApp(ctx, as(memberToken), organization, app.ID, UpdateAppInput{ClientSecret: "secret-2"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member UpdateApp() error = %v", err)
	}
	unchanged, err := h.service.UpdateApp(ctx, as(adminToken), organization, app.ID, UpdateAppInput{Scopes: []string{"repo", "read:user"}})
	if err != nil || unchanged.Revision != 1 || len(h.store.eventsOf(connectionseventsv1.AppUpdatedDetailType)) != 0 {
		t.Fatalf("UpdateApp() without a change = %+v %v", unchanged, err)
	}

	rotated, err := h.service.UpdateApp(ctx, as(adminToken), organization, app.ID, UpdateAppInput{ClientSecret: "secret-2"})
	if err != nil || rotated.Revision != 2 || rotated.ClientID != "client-1" {
		t.Fatalf("rotate UpdateApp() = %+v %v", rotated, err)
	}
	if connection := h.store.connection(completion.Connection.ID); connection.Status != domain.StatusActive {
		t.Fatalf("connection after a secret rotation = %+v", connection)
	}

	if _, err := h.service.UpdateApp(ctx, as(adminToken), organization, app.ID, UpdateAppInput{ClientID: "client-2"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("client change without its secret error = %v", err)
	}
	moved, err := h.service.UpdateApp(ctx, as(adminToken), organization, app.ID, UpdateAppInput{ClientID: "client-2", ClientSecret: "secret-3"})
	if err != nil || moved.Revision != 3 || moved.ClientID != "client-2" {
		t.Fatalf("client change UpdateApp() = %+v %v", moved, err)
	}
	if connection := h.store.connection(completion.Connection.ID); connection.Status != domain.StatusNeedsReconnect ||
		connection.Reason != string(domain.ReconnectClientChanged) {
		t.Fatalf("connection after a client change = %+v", connection)
	}
	updates := h.store.eventsOf(connectionseventsv1.AppUpdatedDetailType)
	var last connectionseventsv1.AppUpdated
	if len(updates) != 2 || json.Unmarshal(updates[1].Data, &last) != nil ||
		!slices.Equal(last.Changed, []string{connectionseventsv1.AppChangedClient, connectionseventsv1.AppChangedSecret}) {
		t.Fatalf("app updated events = %s", updates)
	}
	if marked := h.store.eventsOf(connectionseventsv1.ConnectionNeedsReconnectDetailType); len(marked) != 1 {
		t.Fatalf("needs-reconnect events = %d", len(marked))
	}
}

func TestConnectFlow(t *testing.T) {
	h := newHarness(t)
	app := h.createApp(t, "github", "read:user", "repo")

	authorization, err := h.service.Authorize(ctx, as(memberToken), organization, AuthorizeInput{AppID: app.ID, ReturnURI: returnTo})
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	request := h.oauth.lastAuthorization()
	if request.ClientID != "client-1" || request.RedirectURI != "https://connect.example.test/v1/oauth/callback/github" ||
		!slices.Equal(request.Scopes, app.Scopes) || len(request.State) < 43 || request.PKCEChallenge != "" ||
		!authorization.ExpiresAt.Equal(h.now.Add(domain.SessionTTL)) {
		t.Fatalf("authorization request = %+v, authorization = %+v", request, authorization)
	}
	stateHash := hashValue(request.State)
	session := h.store.session(stateHash)
	if session.StateHash != stateHash || session.MemberID != "user-1" || session.Scope != domain.ScopeMember ||
		session.Intent != domain.IntentConnect || session.EncryptedVerifier != nil || session.ReturnURI != returnTo {
		t.Fatalf("session = %+v", session)
	}
	if _, ok := h.store.sessions[request.State]; ok {
		t.Fatal("the raw state was stored")
	}

	target, err := h.service.Callback(ctx, "github", CallbackParams{State: request.State, Code: "code-1"})
	if err != nil {
		t.Fatalf("Callback() error = %v", err)
	}
	parsed, _ := url.Parse(target)
	sessionID := parsed.Query().Get("session_id")
	if !strings.HasPrefix(target, "https://app.example.test/connections?") || parsed.Query().Get("tab") != "apps" || sessionID == "" {
		t.Fatalf("Callback() target = %q", target)
	}
	session = h.store.session(stateHash)
	var result callbackResult
	if session.HandleHash != hashValue(sessionID) || session.CallbackAt == nil || json.Unmarshal([]byte(opened(t, session.EncryptedResult, func() ([]byte, error) {
		return fakeSealer{}.Open(ctx, sessionBinding(session), *session.EncryptedResult)
	})), &result) != nil || result != (callbackResult{Code: "code-1"}) {
		t.Fatalf("session after callback = %+v, result = %+v", session, result)
	}

	if _, err := h.service.Complete(ctx, as(otherToken), organization, sessionID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("another member's Complete() error = %v", err)
	}
	// The member owns org_2, but that must not let them finish org_1's session
	// as an owner.
	if _, err := h.service.Complete(ctx, as(memberToken), "org_2", sessionID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Complete() through another organization error = %v", err)
	}
	if session := h.store.session(stateHash); session.ClaimedAt != nil || session.CompletedAt != nil {
		t.Fatalf("a refused completion touched the session: %+v", session)
	}

	completion, err := h.service.Complete(ctx, as(memberToken), organization, sessionID)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	connection := completion.Connection
	if completion.Reconnected || !strings.HasPrefix(connection.ID, "con_") || connection.AppID != app.ID || connection.Provider != "github" ||
		connection.Scope != domain.ScopeMember || connection.MemberID != "user-1" || connection.TeamID != "" ||
		connection.Usage != domain.UsageMembers || connection.ProviderAccount != (domain.ProviderAccount{ID: "42", Name: "octocat"}) ||
		!slices.Equal(connection.Scopes, []string{"read:user", "repo"}) || connection.Status != domain.StatusActive ||
		!connection.Default || connection.CreatedBy != "user-1" || connection.Version != 1 {
		t.Fatalf("Complete() = %+v", completion)
	}
	exchanges := h.oauth.exchangeRequests()
	if len(exchanges) != 1 || exchanges[0].ClientID != "client-1" || exchanges[0].ClientSecret != "secret-1" ||
		exchanges[0].RedirectURI != request.RedirectURI || exchanges[0].Code != "code-1" || exchanges[0].PKCEVerifier != "" ||
		!slices.Equal(exchanges[0].Scopes, app.Scopes) {
		t.Fatalf("exchange requests = %+v", exchanges)
	}
	stored := h.store.connection(connection.ID)
	var sealedConnection connectionSecrets
	if err := json.Unmarshal([]byte(opened(t, stored.EncryptedCredentials, func() ([]byte, error) {
		return fakeSealer{}.Open(ctx, connectionBinding(organization, connection.ID), *stored.EncryptedCredentials)
	})), &sealedConnection); err != nil || sealedConnection.Grant == nil || sealedConnection.Grant.AccessToken != "access-1" ||
		sealedConnection.Grant.RefreshToken != "refresh-1" || sealedConnection.Key != nil {
		t.Fatalf("sealed connection secrets = %+v %v", sealedConnection, err)
	}
	if next := h.store.nextChecks[connection.ID]; !next.Equal(h.now.Add(DefaultHeartbeatInterval)) {
		t.Fatalf("first health check at %v", next)
	}
	connected := h.store.eventsOf(connectionseventsv1.ConnectionConnectedDetailType)
	var payload connectionseventsv1.ConnectionConnected
	if len(connected) != 1 || json.Unmarshal(connected[0].Data, &payload) != nil || payload.ConnectionID != connection.ID ||
		payload.ConnectedBy != "user-1" || payload.ProviderAccount.ID != "42" || strings.Contains(string(connected[0].Data), "access-1") {
		t.Fatalf("connected events = %s", connected)
	}

	if _, err := h.service.Complete(ctx, as(memberToken), organization, sessionID); !errors.Is(err, domain.ErrSessionExpired) {
		t.Fatalf("second Complete() error = %v", err)
	}
	if target, err := h.service.Callback(ctx, "github", CallbackParams{State: request.State, Code: "code-2"}); err != nil || !strings.Contains(target, "error=session_expired") {
		t.Fatalf("second Callback() = %q %v", target, err)
	}

	// The same account cannot be connected twice in one scope, but a second
	// account can, and it is not the default.
	if _, err := h.connect(t, memberToken, "github", AuthorizeInput{AppID: app.ID}); !errors.Is(err, domain.ErrAlreadyConnected) {
		t.Fatalf("connecting the same account again error = %v", err)
	}
	h.oauth.set(func(oauth *fakeOAuth) { oauth.account = domain.ProviderAccount{ID: "43", Name: "hubot"} })
	second, err := h.connect(t, memberToken, "github", AuthorizeInput{AppID: app.ID})
	if err != nil || second.Connection.Default {
		t.Fatalf("second account Complete() = %+v %v", second, err)
	}
}

func TestConnectUsesPKCEWhenTheProviderDoes(t *testing.T) {
	h := newHarness(t)
	app := h.createApp(t, "google")
	h.oauth.set(func(oauth *fakeOAuth) {
		oauth.grant = providers.Grant{AccessToken: "access-g", RefreshToken: "refresh-g", Scopes: []string{"email", "openid", "profile"}}
		oauth.account = domain.ProviderAccount{ID: "sub-1", Name: "asha@example.com"}
	})
	authorization, err := h.service.Authorize(ctx, as(memberToken), organization, AuthorizeInput{AppID: app.ID, ReturnURI: "http://localhost:3000/done"})
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	request := h.oauth.lastAuthorization()
	session := h.store.session(hashValue(request.State))
	verifier := opened(t, session.EncryptedVerifier, func() ([]byte, error) {
		return fakeSealer{}.Open(ctx, sessionBinding(session), *session.EncryptedVerifier)
	})
	sum := sha256.Sum256([]byte(verifier))
	if request.PKCEChallenge == "" || request.PKCEChallenge != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Fatalf("PKCE challenge %q does not match the sealed verifier", request.PKCEChallenge)
	}
	completion, err := h.service.Complete(ctx, as(memberToken), organization, h.callback(t, "google", authorization))
	if err != nil || completion.Connection.ProviderAccount.ID != "sub-1" {
		t.Fatalf("Complete() = %+v %v", completion, err)
	}
	if exchanges := h.oauth.exchangeRequests(); len(exchanges) != 1 || exchanges[0].PKCEVerifier != verifier {
		t.Fatalf("exchange requests = %+v", exchanges)
	}
}

func TestCallbackOutcomes(t *testing.T) {
	h := newHarness(t)
	app := h.createApp(t, "github", "read:user", "repo")
	authorize := func() string {
		t.Helper()
		authorization, err := h.service.Authorize(ctx, as(memberToken), organization, AuthorizeInput{AppID: app.ID, ReturnURI: returnTo})
		if err != nil {
			t.Fatal(err)
		}
		return stateOf(t, authorization)
	}

	for name, state := range map[string]string{"unknown state": "not-a-state", "no state": "", "oversized state": strings.Repeat("s", 5000)} {
		if _, err := h.service.Callback(ctx, "github", CallbackParams{State: state, Code: "code-1"}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("%s: Callback() error = %v", name, err)
		}
	}
	if _, err := h.service.Callback(ctx, "google", CallbackParams{State: authorize(), Code: "code-1"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Callback() for another provider error = %v", err)
	}

	denied := authorize()
	target, err := h.service.Callback(ctx, "github", CallbackParams{State: denied, Code: "", Error: "access_denied"})
	if err != nil || !strings.Contains(target, "error=provider_denied") || strings.Contains(target, "access_denied") {
		t.Fatalf("denied Callback() = %q %v", target, err)
	}
	if session := h.store.session(hashValue(denied)); session.ProviderError != CallbackProviderDenied || session.HandleHash != "" {
		t.Fatalf("denied session = %+v", session)
	}
	if target, err := h.service.Callback(ctx, "github", CallbackParams{State: authorize(), Code: ""}); err != nil || !strings.Contains(target, "error=provider_denied") {
		t.Fatalf("Callback() without a code = %q %v", target, err)
	}

	late := authorize()
	h.now = h.now.Add(domain.SessionTTL + time.Second)
	if target, err := h.service.Callback(ctx, "github", CallbackParams{State: late, Code: "code-1"}); err != nil || !strings.Contains(target, "error=session_expired") {
		t.Fatalf("late Callback() = %q %v", target, err)
	}

	// A member who waits too long after the callback cannot complete.
	h.now = h.now.Add(-domain.SessionTTL)
	authorization, err := h.service.Authorize(ctx, as(memberToken), organization, AuthorizeInput{AppID: app.ID, ReturnURI: returnTo})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := h.callback(t, "github", authorization)
	h.now = h.now.Add(domain.SessionTTL)
	if _, err := h.service.Complete(ctx, as(memberToken), organization, sessionID); !errors.Is(err, domain.ErrSessionExpired) {
		t.Fatalf("late Complete() error = %v", err)
	}
	if exchanges := h.oauth.exchangeRequests(); len(exchanges) != 0 {
		t.Fatalf("expired sessions exchanged codes: %+v", exchanges)
	}
}

func TestCompleteChecksGrantedScopesAndUsesTheSessionUp(t *testing.T) {
	h := newHarness(t)
	app := h.createApp(t, "github", "public_repo", "read:user")
	h.oauth.set(func(oauth *fakeOAuth) { oauth.grant.Scopes = []string{"read:user"} })
	authorization, err := h.service.Authorize(ctx, as(memberToken), organization, AuthorizeInput{AppID: app.ID, ReturnURI: returnTo})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := h.callback(t, "github", authorization)
	_, err = h.service.Complete(ctx, as(memberToken), organization, sessionID)
	var missing domain.ScopeRequiredError
	if !errors.Is(err, domain.ErrScopeRequired) || !errors.As(err, &missing) || !slices.Equal(missing.Missing, []string{"public_repo"}) {
		t.Fatalf("Complete() with a missing scope error = %v", err)
	}
	if session := h.store.session(hashValue(stateOf(t, authorization))); session.CompletedAt == nil {
		t.Fatalf("failed completion left the session usable: %+v", session)
	}
	if _, err := h.service.Complete(ctx, as(memberToken), organization, sessionID); !errors.Is(err, domain.ErrSessionExpired) {
		t.Fatalf("retrying a failed Complete() error = %v", err)
	}
	if len(h.store.connections) != 0 {
		t.Fatalf("connections = %+v", h.store.connections)
	}

	// A broader scope satisfies a narrower one.
	h.oauth.set(func(oauth *fakeOAuth) { oauth.grant.Scopes = []string{"read:user", "repo"} })
	if completion, err := h.connect(t, memberToken, "github", AuthorizeInput{AppID: app.ID}); err != nil ||
		!slices.Equal(completion.Connection.Scopes, []string{"read:user", "repo"}) {
		t.Fatalf("Complete() with an implied scope = %+v %v", completion, err)
	}

	for name, failure := range map[string]error{
		"rejected code":        domain.ErrCredentialRejected,
		"provider unavailable": domain.ErrProviderUnavailable,
	} {
		h.oauth.set(func(oauth *fakeOAuth) { oauth.exchangeErr = failure })
		if _, err := h.connect(t, otherToken, "github", AuthorizeInput{AppID: app.ID}); !errors.Is(err, failure) {
			t.Fatalf("%s: Complete() error = %v", name, err)
		}
	}
}

func TestCompleteExchangesACodeOnce(t *testing.T) {
	h := newHarness(t)
	app := h.createApp(t, "github", "read:user", "repo")
	authorization, err := h.service.Authorize(ctx, as(memberToken), organization, AuthorizeInput{AppID: app.ID, ReturnURI: returnTo})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := h.callback(t, "github", authorization)
	var repeated error
	h.oauth.set(func(oauth *fakeOAuth) {
		oauth.onExchange = func() {
			_, repeated = h.service.Complete(ctx, as(memberToken), organization, sessionID)
		}
	})
	if _, err := h.service.Complete(ctx, as(memberToken), organization, sessionID); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if !errors.Is(repeated, domain.ErrSessionExpired) {
		t.Fatalf("Complete() repeated during the exchange error = %v", repeated)
	}
	if exchanges := h.oauth.exchangeRequests(); len(exchanges) != 1 {
		t.Fatalf("the code was exchanged %d times", len(exchanges))
	}

	// Two requests that both read the session before either claimed it: the
	// claim stops the second, and its failure does not use up the session
	// the first is still completing.
	h.oauth.set(func(oauth *fakeOAuth) {
		oauth.account = domain.ProviderAccount{ID: "43"}
		oauth.onExchange = nil
	})
	authorization, err = h.service.Authorize(ctx, as(memberToken), organization, AuthorizeInput{AppID: app.ID, ReturnURI: returnTo})
	if err != nil {
		t.Fatal(err)
	}
	sessionID = h.callback(t, "github", authorization)
	h.store.staleHandles = true
	h.oauth.set(func(oauth *fakeOAuth) {
		oauth.onExchange = func() {
			_, repeated = h.service.Complete(ctx, as(memberToken), organization, sessionID)
		}
	})
	if completion, err := h.service.Complete(ctx, as(memberToken), organization, sessionID); err != nil || completion.Connection.ProviderAccount.ID != "43" {
		t.Fatalf("Complete() racing a stale read = %+v %v", completion, err)
	}
	if !errors.Is(repeated, domain.ErrSessionExpired) {
		t.Fatalf("Complete() from a stale read error = %v", repeated)
	}
	if exchanges := h.oauth.exchangeRequests(); len(exchanges) != 2 {
		t.Fatalf("codes were exchanged %d times for two sessions", len(exchanges))
	}
}

func TestAuthorizeChecksPlacementAppAndReturnURI(t *testing.T) {
	h := newHarness(t)
	app := h.createApp(t, "github", "read:user", "repo")
	authorize := func(token string, input AuthorizeInput) error {
		if input.AppID == "" {
			input.AppID = app.ID
		}
		if input.ReturnURI == "" {
			input.ReturnURI = returnTo
		}
		_, err := h.service.Authorize(ctx, as(token), organization, input)
		return err
	}
	for name, test := range map[string]struct {
		token string
		input AuthorizeInput
		want  error
	}{
		"member connection":                 {memberToken, AuthorizeInput{}, nil},
		"team the member manages":           {memberToken, AuthorizeInput{Scope: domain.ScopeTeam, TeamID: "team_1"}, nil},
		"team the member only sees":         {otherToken, AuthorizeInput{Scope: domain.ScopeTeam, TeamID: "team_1"}, domain.ErrForbidden},
		"team the member cannot see":        {memberToken, AuthorizeInput{Scope: domain.ScopeTeam, TeamID: "team_2"}, domain.ErrNotFound},
		"team without its ID":               {memberToken, AuthorizeInput{Scope: domain.ScopeTeam}, domain.ErrInvalid},
		"member connection naming a team":   {memberToken, AuthorizeInput{TeamID: "team_1"}, domain.ErrInvalid},
		"organization as a member":          {memberToken, AuthorizeInput{Scope: domain.ScopeOrganization}, domain.ErrForbidden},
		"organization as an admin":          {adminToken, AuthorizeInput{Scope: domain.ScopeOrganization, Usage: domain.UsageAdmins}, nil},
		"admins usage off the organization": {memberToken, AuthorizeInput{Usage: domain.UsageAdmins}, domain.ErrInvalid},
		"unknown scope":                     {memberToken, AuthorizeInput{Scope: "everyone"}, domain.ErrInvalid},
		"unknown app":                       {memberToken, AuthorizeInput{AppID: "app_missing"}, domain.ErrNotFound},
		"another origin":                    {memberToken, AuthorizeInput{ReturnURI: "https://evil.example.test/done"}, domain.ErrInvalid},
		"plain http":                        {memberToken, AuthorizeInput{ReturnURI: "http://app.example.test/done"}, domain.ErrInvalid},
		"relative return":                   {memberToken, AuthorizeInput{ReturnURI: "/done"}, domain.ErrInvalid},
		"return with a fragment":            {memberToken, AuthorizeInput{ReturnURI: "https://app.example.test/done#x"}, domain.ErrInvalid},
		"return with user info":             {memberToken, AuthorizeInput{ReturnURI: "https://user@app.example.test/done"}, domain.ErrInvalid},
		"localhost over http":               {memberToken, AuthorizeInput{ReturnURI: "http://localhost:3000/done"}, nil},
		"outsider":                          {"Bearer stranger", AuthorizeInput{}, domain.ErrUnauthorized},
	} {
		t.Run(name, func(t *testing.T) {
			if err := authorize(test.token, test.input); !errors.Is(err, test.want) {
				t.Fatalf("Authorize() error = %v, want %v", err, test.want)
			}
		})
	}

	if _, err := h.service.SetAppStatus(ctx, as(adminToken), organization, app.ID, domain.AppStatusDisabled); err != nil {
		t.Fatal(err)
	}
	if err := authorize(memberToken, AuthorizeInput{}); !errors.Is(err, domain.ErrAppDisabled) {
		t.Fatalf("Authorize() on a disabled app error = %v", err)
	}

	withoutTeams := newHarness(t, func(dependencies *Dependencies) { dependencies.Teams = nil })
	other := withoutTeams.createApp(t, "github", "read:user")
	if _, err := withoutTeams.service.Authorize(ctx, as(memberToken), organization, AuthorizeInput{
		AppID: other.ID, Scope: domain.ScopeTeam, TeamID: "team_1", ReturnURI: returnTo,
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("team Authorize() without teams error = %v", err)
	}
}

func TestCompleteRechecksAccessAndKeepsPlacement(t *testing.T) {
	h := newHarness(t)
	app := h.createApp(t, "github", "read:user", "repo")

	team, err := h.connect(t, memberToken, "github", AuthorizeInput{AppID: app.ID, Scope: domain.ScopeTeam, TeamID: "team_1"})
	if err != nil || team.Connection.Scope != domain.ScopeTeam || team.Connection.TeamID != "team_1" || team.Connection.MemberID != "" {
		t.Fatalf("team Complete() = %+v %v", team, err)
	}
	organizationWide, err := h.connect(t, adminToken, "github", AuthorizeInput{AppID: app.ID, Scope: domain.ScopeOrganization, Usage: domain.UsageAdmins})
	if err != nil || organizationWide.Connection.Usage != domain.UsageAdmins || organizationWide.Connection.MemberID != "" {
		t.Fatalf("organization Complete() = %+v %v", organizationWide, err)
	}

	// The member loses the right to manage the team between authorize and
	// complete.
	authorization, err := h.service.Authorize(ctx, as(memberToken), organization, AuthorizeInput{
		AppID: app.ID, Scope: domain.ScopeTeam, TeamID: "team_1", ReturnURI: returnTo,
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := h.callback(t, "github", authorization)
	h.service.teams = fakeTeams{memberToken + "/team_1": {TeamID: "team_1", Member: true}}
	if _, err := h.service.Complete(ctx, as(memberToken), organization, sessionID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("Complete() after losing team access error = %v", err)
	}
	if len(h.oauth.exchangeRequests()) != 2 {
		t.Fatalf("a refused completion exchanged its code")
	}
}

func TestReconnectKeepsTheAccount(t *testing.T) {
	h := newHarness(t)
	app := h.createApp(t, "github", "read:user", "repo")
	completion, err := h.connect(t, memberToken, "github", AuthorizeInput{AppID: app.ID})
	if err != nil {
		t.Fatal(err)
	}
	connectionID := completion.Connection.ID
	if _, err := h.service.UpdateApp(ctx, as(adminToken), organization, app.ID, UpdateAppInput{ClientID: "client-2", ClientSecret: "secret-2"}); err != nil {
		t.Fatal(err)
	}

	if _, err := h.service.Reconnect(ctx, as(otherToken), organization, connectionID, returnTo); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("another member's Reconnect() error = %v", err)
	}
	if _, err := h.service.Reconnect(ctx, as(adminToken), organization, connectionID, returnTo); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("admin Reconnect() of a member connection error = %v", err)
	}

	reconnect := func() (Completion, error) {
		t.Helper()
		authorization, err := h.service.Reconnect(ctx, as(memberToken), organization, connectionID, returnTo)
		if err != nil {
			t.Fatalf("Reconnect() error = %v", err)
		}
		if session := h.store.session(hashValue(stateOf(t, authorization))); session.Intent != domain.IntentReconnect ||
			session.ConnectionID != connectionID {
			t.Fatalf("reconnect session = %+v", session)
		}
		return h.service.Complete(ctx, as(memberToken), organization, h.callback(t, "github", authorization))
	}

	h.oauth.set(func(oauth *fakeOAuth) { oauth.account = domain.ProviderAccount{ID: "43", Name: "hubot"} })
	if _, err := reconnect(); !errors.Is(err, domain.ErrAccountChanged) {
		t.Fatalf("Reconnect() to another account error = %v", err)
	}
	if connection := h.store.connection(connectionID); connection.Status != domain.StatusNeedsReconnect || connection.ProviderAccount.ID != "42" {
		t.Fatalf("connection after a refused reconnect = %+v", connection)
	}

	h.oauth.set(func(oauth *fakeOAuth) {
		oauth.account = domain.ProviderAccount{ID: "42", Name: "octocat-renamed"}
		oauth.grant = providers.Grant{AccessToken: "access-2", RefreshToken: "refresh-2", Scopes: []string{"read:user", "repo"}}
	})
	reconnected, err := reconnect()
	connection := reconnected.Connection
	if err != nil || !reconnected.Reconnected || connection.ID != connectionID || connection.Status != domain.StatusActive ||
		connection.Reason != "" || connection.ProviderAccount.Name != "octocat-renamed" || connection.Version != 3 {
		t.Fatalf("Reconnect() completion = %+v %v", reconnected, err)
	}
	exchanges := h.oauth.exchangeRequests()
	if last := exchanges[len(exchanges)-1]; last.ClientID != "client-2" || last.ClientSecret != "secret-2" {
		t.Fatalf("reconnect exchanged with %+v", last)
	}
	stored := h.store.connection(connectionID)
	if grant := opened(t, stored.EncryptedCredentials, func() ([]byte, error) {
		return fakeSealer{}.Open(ctx, connectionBinding(organization, connectionID), *stored.EncryptedCredentials)
	}); !strings.Contains(grant, "access-2") {
		t.Fatalf("sealed grant after reconnect = %s", grant)
	}
	if events := h.store.eventsOf(connectionseventsv1.ConnectionReconnectedDetailType); len(events) != 1 {
		t.Fatalf("reconnected events = %d", len(events))
	}
}

func TestListGetAndDisconnect(t *testing.T) {
	h := newHarness(t)
	app := h.createApp(t, "github", "read:user", "repo")
	connect := func(token string, input AuthorizeInput, account string) domain.Connection {
		t.Helper()
		h.oauth.set(func(oauth *fakeOAuth) { oauth.account = domain.ProviderAccount{ID: account} })
		input.AppID = app.ID
		completion, err := h.connect(t, token, "github", input)
		if err != nil {
			t.Fatalf("connect %s error = %v", account, err)
		}
		return completion.Connection
	}
	mine := connect(memberToken, AuthorizeInput{}, "42")
	shared := connect(adminToken, AuthorizeInput{Scope: domain.ScopeOrganization}, "7")
	team := connect(memberToken, AuthorizeInput{Scope: domain.ScopeTeam, TeamID: "team_1"}, "42")

	ids := func(connections []domain.Connection) []string {
		result := []string{}
		for _, connection := range connections {
			result = append(result, connection.ID)
		}
		slices.Sort(result)
		return result
	}
	sorted := func(values ...string) []string {
		slices.Sort(values)
		return values
	}
	for name, test := range map[string]struct {
		token  string
		filter ConnectionFilter
		want   []string
		err    error
	}{
		"member":                    {memberToken, ConnectionFilter{}, sorted(mine.ID, shared.ID), nil},
		"other member":              {otherToken, ConnectionFilter{}, []string{shared.ID}, nil},
		"admin":                     {adminToken, ConnectionFilter{}, sorted(mine.ID, shared.ID), nil},
		"admin member scope":        {adminToken, ConnectionFilter{Scope: domain.ScopeMember}, []string{mine.ID}, nil},
		"team":                      {otherToken, ConnectionFilter{TeamID: "team_1"}, []string{team.ID}, nil},
		"team by scope":             {memberToken, ConnectionFilter{Scope: domain.ScopeTeam, TeamID: "team_1"}, []string{team.ID}, nil},
		"team the admin cannot see": {adminToken, ConnectionFilter{TeamID: "team_1"}, nil, domain.ErrNotFound},
		"team without its ID":       {memberToken, ConnectionFilter{Scope: domain.ScopeTeam}, nil, domain.ErrInvalid},
		"team ID off a team scope":  {memberToken, ConnectionFilter{Scope: domain.ScopeMember, TeamID: "team_1"}, nil, domain.ErrInvalid},
		"another app":               {memberToken, ConnectionFilter{AppID: "app_other"}, []string{}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			connections, err := h.service.ListConnections(ctx, as(test.token), organization, test.filter)
			if !errors.Is(err, test.err) || (err == nil && !slices.Equal(ids(connections), test.want)) {
				t.Fatalf("ListConnections() = %v %v, want %v %v", ids(connections), err, test.want, test.err)
			}
		})
	}

	if _, err := h.service.GetConnection(ctx, as(otherToken), organization, mine.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetConnection() of another member's connection error = %v", err)
	}
	if got, err := h.service.GetConnection(ctx, as(adminToken), organization, mine.ID); err != nil || got.ID != mine.ID || got.EncryptedCredentials == nil {
		t.Fatalf("admin GetConnection() = %+v %v", got, err)
	}
	if err := h.service.Disconnect(ctx, as(otherToken), organization, shared.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member Disconnect() of an organization connection error = %v", err)
	}
	if err := h.service.Disconnect(ctx, as(otherToken), organization, team.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("Disconnect() of a team connection by a member who only sees it error = %v", err)
	}
	for attempt := range 2 {
		if err := h.service.Disconnect(ctx, as(adminToken), organization, mine.ID); err != nil {
			t.Fatalf("admin Disconnect() attempt %d error = %v", attempt+1, err)
		}
	}
	disconnected, err := h.service.GetConnection(ctx, as(memberToken), organization, mine.ID)
	if err != nil || disconnected.Status != domain.StatusDisconnected || disconnected.Reason != string(domain.DisconnectRequested) || disconnected.Default {
		t.Fatalf("GetConnection() after disconnect = %+v %v", disconnected, err)
	}
	events := h.store.eventsOf(connectionseventsv1.ConnectionDisconnectedDetailType)
	var payload connectionseventsv1.ConnectionDisconnected
	if len(events) != 1 || json.Unmarshal(events[0].Data, &payload) != nil || payload.DisconnectedBy != "user-admin" ||
		payload.Reason != string(domain.DisconnectRequested) || payload.MemberID != "user-1" {
		t.Fatalf("disconnected events = %s", events)
	}
	if _, err := h.service.Reconnect(ctx, as(memberToken), organization, mine.ID, returnTo); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Reconnect() of a disconnected connection error = %v", err)
	}

	if err := h.service.DeleteApp(ctx, as(memberToken), organization, app.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member DeleteApp() error = %v", err)
	}
	if err := h.service.DeleteApp(ctx, as(adminToken), organization, app.ID); err != nil {
		t.Fatalf("DeleteApp() error = %v", err)
	}
	for _, id := range []string{shared.ID, team.ID} {
		if connection := h.store.connection(id); connection.Status != domain.StatusDisconnected || connection.Reason != string(domain.DisconnectAppDeleted) {
			t.Fatalf("connection after the app was deleted = %+v", connection)
		}
	}
	if events := h.store.eventsOf(connectionseventsv1.ConnectionDisconnectedDetailType); len(events) != 3 {
		t.Fatalf("disconnected events after deleting the app = %d", len(events))
	}
}

// TestInternalAPIServesOnlyServices pins down who gets raw provider tokens:
// only a service. A member holding their own credentials gets nothing from the
// internal API, not even a connection they may use through tools, and a service
// forwarding a member has that member's access checked.
func TestInternalAPIServesOnlyServices(t *testing.T) {
	h := newHarness(t)
	app := h.createApp(t, "github", "read:user", "repo")
	mine, err := h.connect(t, memberToken, "github", AuthorizeInput{AppID: app.ID})
	if err != nil {
		t.Fatal(err)
	}
	h.oauth.set(func(oauth *fakeOAuth) { oauth.account = domain.ProviderAccount{ID: "7"} })
	shared, err := h.connect(t, adminToken, "github", AuthorizeInput{AppID: app.ID, Scope: domain.ScopeOrganization})
	if err != nil {
		t.Fatal(err)
	}
	action := TokenRequest{OrganizationID: organization, Purpose: domain.PurposeAction}
	actingAs := func(memberID string) TokenRequest {
		request := action
		request.ActingMemberID = memberID
		return request
	}
	forward := func(token string) Actor {
		return Actor{Service: "integrations", Purpose: domain.PurposeAction, Credentials: as(token)}
	}
	onItsOwn := Actor{Service: "integrations", Purpose: domain.PurposeAction}

	alone := Actor{Credentials: as(memberToken)}
	for _, connectionID := range []string{mine.Connection.ID, shared.Connection.ID} {
		if _, err := h.service.IssueToken(ctx, alone, connectionID, action); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("IssueToken() for a member alone error = %v", err)
		}
		if _, err := h.service.InternalConnection(ctx, alone, organization, connectionID); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("InternalConnection() for a member alone error = %v", err)
		}
		if err := h.service.Report(ctx, alone, organization, connectionID); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("Report() for a member alone error = %v", err)
		}
		if err := h.service.AddHold(ctx, alone, organization, connectionID, "integrations"); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("AddHold() for a member alone error = %v", err)
		}
	}

	// A service carrying out the member's action gets the token once the
	// member's own access is checked.
	for _, connectionID := range []string{mine.Connection.ID, shared.Connection.ID} {
		if token, err := h.service.IssueToken(ctx, forward(memberToken), connectionID, action); err != nil || token.AccessToken != "access-1" {
			t.Fatalf("IssueToken() for a forwarded member = %+v %v", token, err)
		}
	}
	for name, test := range map[string]struct {
		actor        Actor
		connectionID string
		request      TokenRequest
		want         error
	}{
		"another member's connection":         {forward(otherToken), mine.Connection.ID, action, domain.ErrNotFound},
		"a member who is not signed in":       {forward("Bearer stranger"), shared.Connection.ID, action, domain.ErrUnauthorized},
		"acting as someone else":              {forward(memberToken), mine.Connection.ID, actingAs("user-2"), domain.ErrForbidden},
		"a purpose the token does not name":   {forward(memberToken), mine.Connection.ID, TokenRequest{OrganizationID: organization, Purpose: domain.PurposeTrigger}, domain.ErrForbidden},
		"on its own, for another member":      {onItsOwn, mine.Connection.ID, actingAs("user-2"), domain.ErrForbidden},
		"on its own, for the owner":           {onItsOwn, mine.Connection.ID, actingAs("user-1"), nil},
		"on its own, naming no member":        {onItsOwn, shared.Connection.ID, action, domain.ErrInvalid},
		"on its own, for an organization one": {onItsOwn, shared.Connection.ID, actingAs("user-2"), nil},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := h.service.IssueToken(ctx, test.actor, test.connectionID, test.request); !errors.Is(err, test.want) {
				t.Fatalf("IssueToken() error = %v, want %v", err, test.want)
			}
		})
	}

	// Metadata follows the same rule: a forwarded member sees only what they
	// can see.
	if _, err := h.service.InternalConnection(ctx, forward(otherToken), organization, mine.Connection.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("InternalConnection() for a forwarded member who cannot see it error = %v", err)
	}
	if connection, err := h.service.InternalConnection(ctx, onItsOwn, organization, mine.Connection.ID); err != nil || connection.ID != mine.Connection.ID {
		t.Fatalf("InternalConnection() for a service = %+v %v", connection, err)
	}
}
