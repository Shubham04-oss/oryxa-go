package application

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	connectionseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/connections/v1"
	teamseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/providers"
)

// dbHarness is the harness on a PostgreSQL store in a throwaway schema, for
// the features whose rules live in SQL: renewal leases, holds, claims and
// bulk disconnects.
type dbHarness struct {
	*harness
	db       *postgres.Store
	database *pgx.Conn
}

func newDBHarness(t *testing.T) *dbHarness {
	t.Helper()
	store, database := openTestDatabase(t)
	return &dbHarness{harness: newHarness(t, func(dependencies *Dependencies) { dependencies.Store = store }), db: store, database: database}
}

// openTestDatabase migrates a throwaway schema and drops it afterwards, so the
// tests are safe against a shared development database.
func openTestDatabase(t *testing.T) (*postgres.Store, *pgx.Conn) {
	t.Helper()
	databaseURL := os.Getenv("CONNECTIONS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("CONNECTIONS_TEST_DATABASE_URL is required")
	}
	setup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	schemaName := "connections_app_" + hex.EncodeToString(suffix)
	schema := pgx.Identifier{schemaName}.Sanitize()
	database, err := pgx.Connect(setup, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(setup, "CREATE SCHEMA "+schema); err != nil {
		_ = database.Close(setup)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := database.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
		_ = database.Close(cleanup)
	})
	if _, err := database.Exec(setup, "SET search_path TO "+schema+", public"); err != nil {
		t.Fatal(err)
	}
	scoped, err := url.Parse(databaseURL)
	if err != nil || scoped.Scheme == "" {
		t.Fatal("CONNECTIONS_TEST_DATABASE_URL must be a postgres:// URL")
	}
	query := scoped.Query()
	query.Set("search_path", schemaName+",public")
	scoped.RawQuery = query.Encode()
	store, err := postgres.Open(setup, scoped.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.Migrate(setup); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	return store, database
}

func (h *dbHarness) stored(t *testing.T, connectionID string) domain.Connection {
	t.Helper()
	connection, err := h.db.Connection(ctx, organization, connectionID)
	if err != nil {
		t.Fatalf("load connection %s: %v", connectionID, err)
	}
	return connection
}

func (h *dbHarness) secretsOf(t *testing.T, connection domain.Connection) connectionSecrets {
	t.Helper()
	sealed, err := h.service.connectionSecrets(ctx, connection)
	if err != nil {
		t.Fatalf("open connection secrets: %v", err)
	}
	return sealed
}

// outbox counts the events written so far by detail type.
func (h *dbHarness) outbox(t *testing.T) map[string]int {
	t.Helper()
	rows, err := h.database.Query(ctx, `SELECT detail_type FROM connections_outbox`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var detailType string
		if err := rows.Scan(&detailType); err != nil {
			t.Fatal(err)
		}
		counts[detailType]++
	}
	return counts
}

func (h *dbHarness) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := h.database.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

// member is a member calling the internal API with only their own
// credentials, which it refuses.
func member(token string) Actor {
	return Actor{Credentials: as(token)}
}

// forwarded is a service carrying out a member's action: it forwards the
// member's credentials along with its own identity.
func forwarded(token string, purpose domain.Purpose) Actor {
	return Actor{Service: "integrations", Purpose: purpose, Credentials: as(token)}
}

func service(purpose domain.Purpose) Actor {
	return Actor{Service: "integrations", Purpose: purpose}
}

func tokenRequest(purpose domain.Purpose, actingMemberID string, scopes ...string) TokenRequest {
	return TokenRequest{OrganizationID: organization, Purpose: purpose, ActingMemberID: actingMemberID, Scopes: scopes}
}

func TestIssueTokenRenewsOnceForConcurrentCallers(t *testing.T) {
	h := newDBHarness(t)
	app := h.createApp(t, "google")
	h.oauth.set(func(oauth *fakeOAuth) {
		oauth.grant = providers.Grant{AccessToken: "access-old", RefreshToken: "refresh-1", Expiry: h.now.Add(time.Minute), Scopes: app.Scopes,
			Metadata: map[string]string{"instance_url": "https://acme.example.test"}}
		oauth.account = domain.ProviderAccount{ID: "sub-1", Name: "asha@example.com"}
		oauth.refreshGrant = providers.Grant{AccessToken: "access-new", RefreshToken: "refresh-2", Expiry: h.now.Add(time.Hour), Scopes: app.Scopes}
		oauth.refreshDelay = 300 * time.Millisecond
	})
	completion, err := h.connect(t, memberToken, "google", AuthorizeInput{AppID: app.ID})
	if err != nil {
		t.Fatal(err)
	}
	connectionID := completion.Connection.ID

	var wait sync.WaitGroup
	results := make(chan string, 8)
	failures := make(chan error, 8)
	for index := range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			actor, request := forwarded(memberToken, domain.PurposeAction), tokenRequest(domain.PurposeAction, "")
			if index%2 == 1 {
				actor, request = service(domain.PurposeAction), tokenRequest(domain.PurposeAction, "user-1")
			}
			token, err := h.service.IssueToken(ctx, actor, connectionID, request)
			if err != nil {
				failures <- err
				return
			}
			results <- token.AccessToken
		}()
	}
	wait.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Errorf("IssueToken() error = %v", err)
	}
	for token := range results {
		if token != "access-new" {
			t.Errorf("IssueToken() issued %q", token)
		}
	}
	if refreshes := h.oauth.refreshCount(); refreshes != 1 {
		t.Fatalf("the grant was refreshed %d times", refreshes)
	}
	renewed := h.stored(t, connectionID)
	sealed := h.secretsOf(t, renewed)
	if renewed.Version != 2 || sealed.Grant.AccessToken != "access-new" || sealed.Grant.RefreshToken != "refresh-2" || renewed.LastUsedAt == nil {
		t.Fatalf("connection after renewal = version %d, grant %+v, last used %v", renewed.Version, sealed.Grant, renewed.LastUsedAt)
	}
	token, err := h.service.IssueToken(ctx, forwarded(memberToken, domain.PurposeTrigger), connectionID, tokenRequest(domain.PurposeTrigger, "user-1", "email"))
	if err != nil || token.AccessToken != "access-new" || token.TokenType != "Bearer" || token.Placement != bearerPlacement ||
		token.ExpiresAt == nil || !token.ExpiresAt.Equal(h.now.Add(time.Hour)) || token.ProviderAccount.ID != "sub-1" || h.oauth.refreshCount() != 1 {
		t.Fatalf("IssueToken() after renewal = %+v %v", token, err)
	}
	// The provider's metadata goes into the refresh, survives it, and is
	// issued with the token.
	metadata := map[string]string{"instance_url": "https://acme.example.test"}
	if refresh := h.oauth.refreshRequests()[0]; !maps.Equal(refresh.Metadata, metadata) {
		t.Fatalf("refresh request metadata = %v, want %v", refresh.Metadata, metadata)
	}
	if !maps.Equal(sealed.Grant.Metadata, metadata) || !maps.Equal(token.Metadata, metadata) {
		t.Fatalf("metadata after renewal: stored %v, issued %v, want %v", sealed.Grant.Metadata, token.Metadata, metadata)
	}
}

func TestIssueTokenRenewalFailures(t *testing.T) {
	h := newDBHarness(t)
	app := h.createApp(t, "google")
	connect := func(account string) string {
		t.Helper()
		h.oauth.set(func(oauth *fakeOAuth) {
			oauth.grant = providers.Grant{AccessToken: "access-" + account, RefreshToken: "refresh-" + account, Expiry: h.now.Add(time.Minute), Scopes: app.Scopes}
			oauth.account = domain.ProviderAccount{ID: account}
		})
		completion, err := h.connect(t, memberToken, "google", AuthorizeInput{AppID: app.ID})
		if err != nil {
			t.Fatal(err)
		}
		return completion.Connection.ID
	}

	rejected := connect("sub-rejected")
	h.oauth.set(func(oauth *fakeOAuth) {
		oauth.refreshErr = fmt.Errorf("%w: invalid_grant", domain.ErrCredentialRejected)
	})
	if _, err := h.service.IssueToken(ctx, forwarded(memberToken, domain.PurposeAction), rejected, tokenRequest(domain.PurposeAction, "")); !errors.Is(err, domain.ErrNeedsReconnect) {
		t.Fatalf("IssueToken() with a rejected refresh error = %v", err)
	}
	if connection := h.stored(t, rejected); connection.Status != domain.StatusNeedsReconnect || connection.Reason != string(domain.ReconnectRefreshRejected) {
		t.Fatalf("connection after a rejected refresh = %s/%s", connection.Status, connection.Reason)
	}
	if h.outbox(t)[connectionseventsv1.ConnectionNeedsReconnectDetailType] != 1 {
		t.Fatalf("outbox = %v", h.outbox(t))
	}
	if _, err := h.service.IssueToken(ctx, forwarded(memberToken, domain.PurposeAction), rejected, tokenRequest(domain.PurposeAction, "")); !errors.Is(err, domain.ErrNeedsReconnect) {
		t.Fatalf("IssueToken() on a connection that needs reconnecting error = %v", err)
	}

	unavailable := connect("sub-unavailable")
	h.oauth.set(func(oauth *fakeOAuth) {
		oauth.refreshErr = fmt.Errorf("%w: token endpoint returned 503", domain.ErrProviderUnavailable)
	})
	token, err := h.service.IssueToken(ctx, forwarded(memberToken, domain.PurposeAction), unavailable, tokenRequest(domain.PurposeAction, ""))
	if err != nil || token.AccessToken != "access-sub-unavailable" {
		t.Fatalf("IssueToken() with an unexpired token and a failing refresh = %+v %v", token, err)
	}
	h.now = h.now.Add(2 * time.Minute)
	if _, err := h.service.IssueToken(ctx, forwarded(memberToken, domain.PurposeAction), unavailable, tokenRequest(domain.PurposeAction, "")); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("IssueToken() with an expired token and a failing refresh error = %v", err)
	}
	if connection := h.stored(t, unavailable); connection.Status != domain.StatusActive || connection.Version != 1 {
		t.Fatalf("connection after a failing refresh = %s version %d", connection.Status, connection.Version)
	}
	var leases int
	if err := h.database.QueryRow(ctx, `SELECT count(*) FROM connections WHERE refresh_lease_token IS NOT NULL`).Scan(&leases); err != nil || leases != 0 {
		t.Fatalf("leases left behind = %d %v", leases, err)
	}
}

func TestIssueTokenAccess(t *testing.T) {
	h := newDBHarness(t)
	app := h.createApp(t, "github", "read:user", "repo")
	connect := func(token string, input AuthorizeInput, account string) string {
		t.Helper()
		h.oauth.set(func(oauth *fakeOAuth) { oauth.account = domain.ProviderAccount{ID: account} })
		input.AppID = app.ID
		completion, err := h.connect(t, token, "github", input)
		if err != nil {
			t.Fatal(err)
		}
		return completion.Connection.ID
	}
	mine := connect(memberToken, AuthorizeInput{}, "42")
	adminsOnly := connect(adminToken, AuthorizeInput{Scope: domain.ScopeOrganization, Usage: domain.UsageAdmins}, "7")
	team := connect(memberToken, AuthorizeInput{Scope: domain.ScopeTeam, TeamID: "team_1"}, "42")

	token, err := h.service.IssueToken(ctx, forwarded(memberToken, domain.PurposeAction), mine, tokenRequest(domain.PurposeAction, "user-1", "repo"))
	if err != nil || token.AccessToken != "access-1" || token.ExpiresAt != nil || !slices.Equal(token.Scopes, []string{"read:user", "repo"}) {
		t.Fatalf("member IssueToken() = %+v %v", token, err)
	}
	for name, test := range map[string]struct {
		actor        Actor
		connectionID string
		request      TokenRequest
		want         error
	}{
		"another member":                   {forwarded(otherToken, domain.PurposeAction), mine, tokenRequest(domain.PurposeAction, ""), domain.ErrNotFound},
		"an admin cannot use it":           {forwarded(adminToken, domain.PurposeAction), mine, tokenRequest(domain.PurposeAction, ""), domain.ErrForbidden},
		"acting as someone else":           {forwarded(memberToken, domain.PurposeAction), mine, tokenRequest(domain.PurposeAction, "user-2"), domain.ErrForbidden},
		"members have no heartbeat":        {forwarded(memberToken, domain.PurposeHeartbeat), mine, tokenRequest(domain.PurposeHeartbeat, ""), domain.ErrForbidden},
		"unknown purpose":                  {forwarded(memberToken, domain.PurposeAction), mine, tokenRequest("export", ""), domain.ErrInvalid},
		"another organization":             {forwarded(memberToken, domain.PurposeAction), mine, TokenRequest{OrganizationID: "org_2", Purpose: domain.PurposeAction}, domain.ErrNotFound},
		"no organization":                  {forwarded(memberToken, domain.PurposeAction), mine, TokenRequest{Purpose: domain.PurposeAction}, domain.ErrInvalid},
		"scope the app does not allow":     {forwarded(memberToken, domain.PurposeAction), mine, tokenRequest(domain.PurposeAction, "", "workflow"), domain.ErrScopeRequired},
		"a token set it does not have":     {forwarded(memberToken, domain.PurposeAction), mine, TokenRequest{OrganizationID: organization, Purpose: domain.PurposeAction, Token: "user"}, domain.ErrInvalid},
		"service for the owner":            {service(domain.PurposeAction), mine, tokenRequest(domain.PurposeAction, "user-1"), nil},
		"service for another member":       {service(domain.PurposeAction), mine, tokenRequest(domain.PurposeAction, "user-2"), domain.ErrForbidden},
		"service acting for no one":        {service(domain.PurposeAction), mine, tokenRequest(domain.PurposeAction, ""), domain.ErrInvalid},
		"service token for another use":    {service(domain.PurposeTrigger), mine, tokenRequest(domain.PurposeAction, "user-1"), domain.ErrForbidden},
		"service in another organization":  {service(domain.PurposeAction), mine, TokenRequest{OrganizationID: "org_2", Purpose: domain.PurposeAction, ActingMemberID: "user-1"}, domain.ErrNotFound},
		"service on an admins-only action": {service(domain.PurposeAction), adminsOnly, tokenRequest(domain.PurposeAction, "user-admin"), domain.ErrForbidden},
		"service cleans admins-only":       {service(domain.PurposeCleanup), adminsOnly, tokenRequest(domain.PurposeCleanup, "user-admin"), nil},
		"member cannot use admins-only":    {forwarded(memberToken, domain.PurposeAction), adminsOnly, tokenRequest(domain.PurposeAction, ""), domain.ErrForbidden},
		"admin uses admins-only":           {forwarded(adminToken, domain.PurposeAction), adminsOnly, tokenRequest(domain.PurposeAction, ""), nil},
		"team member uses the team's":      {forwarded(otherToken, domain.PurposeAction), team, tokenRequest(domain.PurposeAction, ""), nil},
		"service for a team":               {service(domain.PurposeTrigger), team, tokenRequest(domain.PurposeTrigger, "user-2"), nil},
		"a member alone, their own":        {member(memberToken), mine, tokenRequest(domain.PurposeAction, ""), domain.ErrForbidden},
		"a member alone, a team's":         {member(otherToken), team, tokenRequest(domain.PurposeAction, ""), domain.ErrForbidden},
		"an admin alone, admins-only":      {member(adminToken), adminsOnly, tokenRequest(domain.PurposeAction, ""), domain.ErrForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := h.service.IssueToken(ctx, test.actor, test.connectionID, test.request); !errors.Is(err, test.want) {
				t.Fatalf("IssueToken() error = %v, want %v", err, test.want)
			}
		})
	}

	// Narrowing the app applies at once.
	if _, err := h.service.UpdateApp(ctx, as(adminToken), organization, app.ID, UpdateAppInput{Scopes: []string{"read:user"}}); err != nil {
		t.Fatal(err)
	}
	var missing domain.ScopeRequiredError
	if _, err := h.service.IssueToken(ctx, forwarded(memberToken, domain.PurposeAction), mine, tokenRequest(domain.PurposeAction, "", "repo")); !errors.As(err, &missing) ||
		!slices.Equal(missing.Missing, []string{"repo"}) {
		t.Fatalf("IssueToken() for a scope the app no longer allows error = %v", err)
	}

	// A disabled app blocks use but not cleanup.
	if _, err := h.service.SetAppStatus(ctx, as(adminToken), organization, app.ID, domain.AppStatusDisabled); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.IssueToken(ctx, forwarded(memberToken, domain.PurposeAction), mine, tokenRequest(domain.PurposeAction, "")); !errors.Is(err, domain.ErrAppDisabled) {
		t.Fatalf("IssueToken() on a disabled app error = %v", err)
	}
	if _, err := h.service.IssueToken(ctx, forwarded(memberToken, domain.PurposeCleanup), mine, tokenRequest(domain.PurposeCleanup, "")); err != nil {
		t.Fatalf("cleanup IssueToken() on a disabled app error = %v", err)
	}
	if _, err := h.service.SetAppStatus(ctx, as(adminToken), organization, app.ID, domain.AppStatusActive); err != nil {
		t.Fatal(err)
	}

	// After disconnect only cleanup works, and only while a hold is open.
	if err := h.service.Disconnect(ctx, as(memberToken), organization, mine); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.IssueToken(ctx, service(domain.PurposeAction), mine, tokenRequest(domain.PurposeAction, "user-1")); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("IssueToken() after disconnect error = %v", err)
	}
	if _, err := h.service.IssueToken(ctx, service(domain.PurposeCleanup), mine, tokenRequest(domain.PurposeCleanup, "user-1")); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cleanup IssueToken() without a hold error = %v", err)
	}
	if err := h.service.AddHold(ctx, service(domain.PurposeCleanup), organization, mine, "integrations:webhooks"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.IssueToken(ctx, service(domain.PurposeCleanup), mine, tokenRequest(domain.PurposeCleanup, "user-1")); err != nil {
		t.Fatalf("cleanup IssueToken() with a hold error = %v", err)
	}
}

func TestKeyBasedConnections(t *testing.T) {
	h := newDBHarness(t)
	bearerApp, err := h.service.CreateApp(ctx, as(adminToken), organization, CreateAppInput{Provider: "github", CredentialType: domain.CredentialBearer})
	if err != nil || bearerApp.ClientID != "" || bearerApp.EncryptedSecret != nil || len(bearerApp.Scopes) != 0 {
		t.Fatalf("CreateApp(bearer) = %+v %v", bearerApp, err)
	}
	if _, err := h.service.CreateApp(ctx, as(adminToken), organization, CreateAppInput{Provider: "github", CredentialType: domain.CredentialBearer, ClientID: "c"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CreateApp(bearer) with a client error = %v", err)
	}
	h.oauth.set(func(oauth *fakeOAuth) { oauth.keyAccount = domain.ProviderAccount{ID: "42", Name: "octocat"} })

	created, err := h.service.CreateConnection(ctx, as(memberToken), organization, CreateConnectionInput{
		AppID: bearerApp.ID, Label: " Personal token ", Credential: domain.CredentialInput{Token: " ghp_first "},
	})
	if err != nil || created.CredentialType != domain.CredentialBearer || created.MemberID != "user-1" || created.Label != "Personal token" ||
		!created.Default || created.ProviderAccount.ID != "42" || created.Status != domain.StatusActive || len(created.Scopes) != 0 {
		t.Fatalf("CreateConnection() = %+v %v", created, err)
	}
	if sealed := h.secretsOf(t, h.stored(t, created.ID)); sealed.Key == nil || sealed.Key.Token != "ghp_first" || sealed.Grant != nil {
		t.Fatalf("sealed key = %+v", sealed)
	}
	token, err := h.service.IssueToken(ctx, forwarded(memberToken, domain.PurposeAction), created.ID, tokenRequest(domain.PurposeAction, "", "repo"))
	if err != nil || token.TokenType != "bearer" || token.AccessToken != "ghp_first" || token.ExpiresAt != nil ||
		token.Placement != (domain.TokenPlacement{In: "header", Name: "Authorization", Prefix: "Bearer "}) {
		t.Fatalf("IssueToken() for a key = %+v %v", token, err)
	}

	for name, test := range map[string]struct {
		input CreateConnectionInput
		want  error
	}{
		"same account again":   {CreateConnectionInput{AppID: bearerApp.ID, Credential: domain.CredentialInput{Token: "ghp_again"}}, domain.ErrAlreadyConnected},
		"wrong credential":     {CreateConnectionInput{AppID: bearerApp.ID, Credential: domain.CredentialInput{APIKey: "key"}}, domain.ErrInvalid},
		"no credential":        {CreateConnectionInput{AppID: bearerApp.ID}, domain.ErrInvalid},
		"unknown app":          {CreateConnectionInput{AppID: "app_missing", Credential: domain.CredentialInput{Token: "t"}}, domain.ErrNotFound},
		"organization scope":   {CreateConnectionInput{AppID: bearerApp.ID, Scope: domain.ScopeOrganization, Credential: domain.CredentialInput{Token: "t"}}, domain.ErrForbidden},
		"label over the limit": {CreateConnectionInput{AppID: bearerApp.ID, Label: strings.Repeat("l", 121), Credential: domain.CredentialInput{Token: "t"}}, domain.ErrInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := h.service.CreateConnection(ctx, as(memberToken), organization, test.input); !errors.Is(err, test.want) {
				t.Fatalf("CreateConnection() error = %v, want %v", err, test.want)
			}
		})
	}
	h.oauth.set(func(oauth *fakeOAuth) {
		oauth.keyErr = fmt.Errorf("%w: provider refused the credential", domain.ErrCredentialRejected)
	})
	if _, err := h.service.CreateConnection(ctx, as(otherToken), organization, CreateConnectionInput{
		AppID: bearerApp.ID, Credential: domain.CredentialInput{Token: "ghp_bad"},
	}); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("CreateConnection() with a rejected key error = %v", err)
	}

	h.oauth.set(func(oauth *fakeOAuth) { oauth.keyErr, oauth.keyAccount = nil, domain.ProviderAccount{ID: "43"} })
	if _, err := h.service.ReplaceCredential(ctx, as(memberToken), organization, created.ID, domain.CredentialInput{Token: "ghp_other"}); !errors.Is(err, domain.ErrAccountChanged) {
		t.Fatalf("ReplaceCredential() for another account error = %v", err)
	}
	if _, err := h.service.ReplaceCredential(ctx, as(otherToken), organization, created.ID, domain.CredentialInput{Token: "ghp_second"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("another member's ReplaceCredential() error = %v", err)
	}
	h.oauth.set(func(oauth *fakeOAuth) { oauth.keyAccount = domain.ProviderAccount{ID: "42", Name: "octocat-renamed"} })
	replaced, err := h.service.ReplaceCredential(ctx, as(memberToken), organization, created.ID, domain.CredentialInput{Token: "ghp_second"})
	if err != nil || replaced.Version != 2 || replaced.ProviderAccount.Name != "octocat-renamed" {
		t.Fatalf("ReplaceCredential() = %+v %v", replaced, err)
	}
	if token, err := h.service.IssueToken(ctx, forwarded(memberToken, domain.PurposeAction), created.ID, tokenRequest(domain.PurposeAction, "")); err != nil || token.AccessToken != "ghp_second" {
		t.Fatalf("IssueToken() after replacement = %+v %v", token, err)
	}
	if h.outbox(t)[connectionseventsv1.ConnectionReconnectedDetailType] != 1 {
		t.Fatalf("outbox = %v", h.outbox(t))
	}

	oauthApp := h.createApp(t, "github", "read:user")
	if _, err := h.service.CreateConnection(ctx, as(memberToken), organization, CreateConnectionInput{
		AppID: oauthApp.ID, Credential: domain.CredentialInput{Token: "t"},
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CreateConnection() on an oauth2 app error = %v", err)
	}
}

func TestKeyConnectionOnAnAppsDomain(t *testing.T) {
	h := newDBHarness(t)
	app, err := h.service.CreateApp(ctx, as(adminToken), organization, CreateAppInput{
		Provider: "atlassian", CredentialType: domain.CredentialBasic, Domain: "acme.atlassian.net",
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored, err := h.service.store.App(ctx, organization, app.ID); err != nil || stored.Settings.Domain != "acme.atlassian.net" {
		t.Fatalf("stored app = %+v %v", stored, err)
	}
	h.oauth.set(func(oauth *fakeOAuth) {
		oauth.keyAccount = domain.ProviderAccount{ID: "557058:f58131cb", Name: "ada@example.test"}
	})

	created, err := h.service.CreateConnection(ctx, as(memberToken), organization, CreateConnectionInput{
		AppID: app.ID, Credential: domain.CredentialInput{Username: "ada@example.test", Password: "api-token"},
	})
	if err != nil || created.ProviderAccount.ID != "557058:f58131cb" {
		t.Fatalf("CreateConnection() = %+v %v", created, err)
	}
	// The identity check ran on the app's site.
	h.oauth.mu.Lock()
	checked := slices.Clone(h.oauth.keyAPIURLs)
	h.oauth.mu.Unlock()
	if !slices.Equal(checked, []string{"https://acme.atlassian.net"}) {
		t.Fatalf("identity checks ran against %v", checked)
	}
	// The token names the site it works on.
	token, err := h.service.IssueToken(ctx, forwarded(memberToken, domain.PurposeAction), created.ID, tokenRequest(domain.PurposeAction, ""))
	if err != nil || token.Domain != "acme.atlassian.net" || token.TokenType != "basic" ||
		token.AccessToken != base64.StdEncoding.EncodeToString([]byte("ada@example.test:api-token")) ||
		token.Placement != (domain.TokenPlacement{In: "header", Name: "Authorization", Prefix: "Basic "}) {
		t.Fatalf("IssueToken() = %+v %v", token, err)
	}
}

func TestClientCredentialsConnections(t *testing.T) {
	h := newDBHarness(t)
	app, err := h.service.CreateApp(ctx, as(adminToken), organization, CreateAppInput{
		Provider: "microsoft", CredentialType: domain.CredentialOAuth2ClientCredentials, ClientID: "client-cc", ClientSecret: "secret-cc",
	})
	if err != nil || !slices.Equal(app.Scopes, []string{"https://graph.microsoft.com/.default"}) {
		t.Fatalf("CreateApp(client credentials) = %+v %v", app, err)
	}
	h.oauth.set(func(oauth *fakeOAuth) {
		oauth.mintGrant = providers.Grant{AccessToken: "app-token-1", TokenType: "Bearer", Expiry: h.now.Add(time.Hour), Scopes: app.Scopes}
	})
	if _, err := h.service.CreateConnection(ctx, as(adminToken), organization, CreateConnectionInput{
		AppID: app.ID, Credential: domain.CredentialInput{Tenant: "contoso.onmicrosoft.com"},
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("member-scope client-credentials CreateConnection() error = %v", err)
	}
	created, err := h.service.CreateConnection(ctx, as(adminToken), organization, CreateConnectionInput{
		AppID: app.ID, Scope: domain.ScopeOrganization, Credential: domain.CredentialInput{Tenant: "contoso.onmicrosoft.com"},
	})
	if err != nil || created.ProviderAccount.ID != "contoso.onmicrosoft.com" || created.Scope != domain.ScopeOrganization {
		t.Fatalf("CreateConnection(client credentials) = %+v %v", created, err)
	}
	if mints := h.oauth.mints; len(mints) != 1 || mints[0] != (providers.MintRequest{ClientID: "client-cc", ClientSecret: "secret-cc", Tenant: "contoso.onmicrosoft.com"}) {
		t.Fatalf("mints = %+v", mints)
	}

	token, err := h.service.IssueToken(ctx, service(domain.PurposeTrigger), created.ID, tokenRequest(domain.PurposeTrigger, "user-1", "Mail.Send"))
	if err != nil || token.AccessToken != "app-token-1" || len(h.oauth.mints) != 1 {
		t.Fatalf("IssueToken() with a fresh minted token = %+v %v (mints %d)", token, err, len(h.oauth.mints))
	}
	h.now = h.now.Add(59 * time.Minute)
	h.oauth.set(func(oauth *fakeOAuth) {
		oauth.mintGrant = providers.Grant{AccessToken: "app-token-2", Expiry: h.now.Add(time.Hour)}
	})
	if token, err := h.service.IssueToken(ctx, service(domain.PurposeTrigger), created.ID, tokenRequest(domain.PurposeTrigger, "user-1")); err != nil ||
		token.AccessToken != "app-token-2" || len(h.oauth.mints) != 2 || h.oauth.mints[1].Tenant != "contoso.onmicrosoft.com" {
		t.Fatalf("IssueToken() with an expiring minted token = %+v %v", token, err)
	}

	// An admin can also consent through authorize.
	authorization, err := h.service.Authorize(ctx, as(adminToken), organization, AuthorizeInput{AppID: app.ID, Scope: domain.ScopeOrganization, ReturnURI: returnTo})
	if err != nil || !strings.HasPrefix(authorization.URL, "https://login.example.test/adminconsent?") {
		t.Fatalf("Authorize(client credentials) = %+v %v", authorization, err)
	}
	target, err := h.service.Callback(ctx, "microsoft", CallbackParams{State: stateOf(t, authorization), AdminConsent: "True", Tenant: "fabrikam.onmicrosoft.com"})
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(target)
	completion, err := h.service.Complete(ctx, as(adminToken), organization, parsed.Query().Get("session_id"))
	if err != nil || completion.Connection.ProviderAccount.ID != "fabrikam.onmicrosoft.com" || h.oauth.mints[2].Tenant != "fabrikam.onmicrosoft.com" {
		t.Fatalf("admin consent Complete() = %+v %v", completion, err)
	}
	refused, err := h.service.Authorize(ctx, as(adminToken), organization, AuthorizeInput{AppID: app.ID, Scope: domain.ScopeOrganization, ReturnURI: returnTo})
	if err != nil {
		t.Fatal(err)
	}
	if target, err := h.service.Callback(ctx, "microsoft", CallbackParams{State: stateOf(t, refused), Error: "access_denied"}); err != nil ||
		!strings.Contains(target, "error=provider_denied") {
		t.Fatalf("refused admin consent Callback() = %q %v", target, err)
	}
}

func TestInstallationConnections(t *testing.T) {
	h := newDBHarness(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	if _, err := h.service.CreateApp(ctx, as(adminToken), organization, CreateAppInput{
		Provider: "github", CredentialType: domain.CredentialInstallation, ClientID: "Iv1.client", ClientSecret: "secret-app",
		GitHubAppID: "123", GitHubAppSlug: "oryxa-test", PrivateKey: "not a key",
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CreateApp(installation) with a bad key error = %v", err)
	}
	app, err := h.service.CreateApp(ctx, as(adminToken), organization, CreateAppInput{
		Provider: "github", CredentialType: domain.CredentialInstallation, ClientID: "Iv1.client", ClientSecret: "secret-app",
		GitHubAppID: "123", GitHubAppSlug: "oryxa-test", PrivateKey: privateKeyPEM,
	})
	if err != nil || app.Settings != (domain.AppSettings{GitHubAppID: "123", GitHubAppSlug: "oryxa-test"}) || len(app.Scopes) != 0 {
		t.Fatalf("CreateApp(installation) = %+v %v", app, err)
	}
	h.oauth.set(func(oauth *fakeOAuth) {
		oauth.grant = providers.Grant{AccessToken: "ghu_user"}
		oauth.userInstallations = []string{"555", "999"}
		oauth.installationAccount = domain.ProviderAccount{ID: "999", Name: "acme"}
		oauth.installationToken = providers.Grant{AccessToken: "ghs_installation", Expiry: h.now.Add(time.Hour)}
	})
	install := func(installationID, setupAction string) (string, error) {
		t.Helper()
		authorization, err := h.service.Authorize(ctx, as(memberToken), organization, AuthorizeInput{AppID: app.ID, ReturnURI: returnTo})
		if err != nil || !strings.HasPrefix(authorization.URL, "https://github.example.test/apps/oryxa-test/installations/new?") {
			t.Fatalf("Authorize(installation) = %+v %v", authorization, err)
		}
		return h.service.Callback(ctx, "github", CallbackParams{
			State: stateOf(t, authorization), Code: "code-install", InstallationID: installationID, SetupAction: setupAction,
		})
	}

	target, err := install("999", "install")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(target)
	completion, err := h.service.Complete(ctx, as(memberToken), organization, parsed.Query().Get("session_id"))
	if err != nil || completion.Connection.ProviderAccount != (domain.ProviderAccount{ID: "999", Name: "acme"}) ||
		completion.Connection.CredentialType != domain.CredentialInstallation {
		t.Fatalf("Complete(installation) = %+v %v", completion, err)
	}
	if sealed := h.secretsOf(t, h.stored(t, completion.Connection.ID)); sealed.InstallationID != "999" || sealed.Grant.AccessToken != "ghs_installation" {
		t.Fatalf("sealed installation = %+v", sealed)
	}
	if token, err := h.service.IssueToken(ctx, forwarded(memberToken, domain.PurposeAction), completion.Connection.ID, tokenRequest(domain.PurposeAction, "")); err != nil ||
		token.AccessToken != "ghs_installation" || h.oauth.installationTokens != 1 {
		t.Fatalf("IssueToken(installation) = %+v %v", token, err)
	}

	forged, err := install("31337", "install")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ = url.Parse(forged)
	if _, err := h.service.Complete(ctx, as(memberToken), organization, parsed.Query().Get("session_id")); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("Complete() with an installation the user cannot reach error = %v", err)
	}
	if target, err := install("", "request"); err != nil || !strings.Contains(target, "error=installation_pending") {
		t.Fatalf("Callback() for an installation awaiting approval = %q %v", target, err)
	}
}

func TestUpdateConnection(t *testing.T) {
	h := newDBHarness(t)
	app := h.createApp(t, "github", "read:user", "repo")
	first, err := h.connect(t, memberToken, "github", AuthorizeInput{AppID: app.ID})
	if err != nil {
		t.Fatal(err)
	}
	h.oauth.set(func(oauth *fakeOAuth) { oauth.account = domain.ProviderAccount{ID: "43"} })
	second, err := h.connect(t, memberToken, "github", AuthorizeInput{AppID: app.ID})
	if err != nil || !first.Connection.Default || second.Connection.Default {
		t.Fatalf("connections = %+v %+v %v", first, second, err)
	}
	label, yes, no := "Work account", true, false
	updated, err := h.service.UpdateConnection(ctx, as(memberToken), organization, second.Connection.ID, UpdateConnectionInput{Label: &label, Default: &yes})
	if err != nil || updated.Label != "Work account" || !updated.Default || updated.Version != second.Connection.Version {
		t.Fatalf("UpdateConnection() = %+v %v", updated, err)
	}
	if h.stored(t, first.Connection.ID).Default {
		t.Fatal("the previous default is still the default")
	}
	for name, test := range map[string]struct {
		token string
		input UpdateConnectionInput
		want  error
	}{
		"nothing to change":  {memberToken, UpdateConnectionInput{}, domain.ErrInvalid},
		"default false":      {memberToken, UpdateConnectionInput{Default: &no}, domain.ErrInvalid},
		"another member":     {otherToken, UpdateConnectionInput{Label: &label}, domain.ErrNotFound},
		"admin cannot":       {adminToken, UpdateConnectionInput{Label: &label}, domain.ErrForbidden},
		"empty label":        {memberToken, UpdateConnectionInput{Label: new(string)}, nil},
		"control characters": {memberToken, UpdateConnectionInput{Label: func() *string { value := "a\nb"; return &value }()}, domain.ErrInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := h.service.UpdateConnection(ctx, as(test.token), organization, first.Connection.ID, test.input); !errors.Is(err, test.want) {
				t.Fatalf("UpdateConnection() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestHoldsAndCredentialDeletion(t *testing.T) {
	h := newDBHarness(t)
	app := h.createApp(t, "github", "read:user", "repo")
	personal, err := h.connect(t, memberToken, "github", AuthorizeInput{AppID: app.ID})
	if err != nil {
		t.Fatal(err)
	}
	team, err := h.connect(t, memberToken, "github", AuthorizeInput{AppID: app.ID, Scope: domain.ScopeTeam, TeamID: "team_1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.service.AddHold(ctx, member(memberToken), organization, personal.Connection.ID, "integrations"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member AddHold() error = %v", err)
	}
	if err := h.service.AddHold(ctx, service(domain.PurposeCleanup), organization, personal.Connection.ID, "Not A Holder"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("AddHold() with a bad holder error = %v", err)
	}
	for range 2 {
		if err := h.service.AddHold(ctx, service(domain.PurposeCleanup), organization, personal.Connection.ID, "integrations:webhooks"); err != nil {
			t.Fatalf("AddHold() error = %v", err)
		}
	}
	if err := h.service.Disconnect(ctx, as(memberToken), organization, personal.Connection.ID); err != nil {
		t.Fatal(err)
	}
	if claimed, err := h.service.CleanUp(ctx); err != nil || claimed != 0 || h.stored(t, personal.Connection.ID).EncryptedCredentials == nil {
		t.Fatalf("CleanUp() with a hold open = %d %v", claimed, err)
	}
	if err := h.service.ReleaseHold(ctx, service(domain.PurposeCleanup), organization, personal.Connection.ID, "integrations:webhooks"); err != nil {
		t.Fatal(err)
	}
	// The team connection still holds a grant for the same account, so the
	// provider grant is not revoked.
	if claimed, err := h.service.CleanUp(ctx); err != nil || claimed != 1 {
		t.Fatalf("CleanUp() after release = %d %v", claimed, err)
	}
	if deleted := h.stored(t, personal.Connection.ID); deleted.EncryptedCredentials != nil || deleted.CredentialsDeletedAt == nil || len(h.oauth.revokeRequests()) != 0 {
		t.Fatalf("connection after cleanup = %+v, revocations %d", deleted, len(h.oauth.revokeRequests()))
	}
	if err := h.service.AddHold(ctx, service(domain.PurposeCleanup), organization, personal.Connection.ID, "integrations"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("AddHold() on deleted credentials error = %v", err)
	}

	if err := h.service.Disconnect(ctx, as(memberToken), organization, team.Connection.ID); err != nil {
		t.Fatal(err)
	}
	if claimed, err := h.service.CleanUp(ctx); err != nil || claimed != 1 {
		t.Fatalf("CleanUp() for the last holder of the grant = %d %v", claimed, err)
	}
	if revocations := h.oauth.revokeRequests(); len(revocations) != 1 || revocations[0].Grant.AccessToken != "access-1" || revocations[0].ClientSecret != "secret-1" {
		t.Fatalf("revocations = %+v", revocations)
	}
	revoked := map[string]bool{}
	for _, detail := range h.outboxEvents(t, connectionseventsv1.ConnectionCredentialsDeletedDetailType) {
		var payload connectionseventsv1.ConnectionCredentialsDeleted
		if err := json.Unmarshal([]byte(detail), &payload); err != nil {
			t.Fatal(err)
		}
		revoked[payload.ConnectionID] = payload.Revoked
	}
	if len(revoked) != 2 || revoked[personal.Connection.ID] || !revoked[team.Connection.ID] {
		t.Fatalf("credentials deleted events revoked = %v, want the shared grant kept and the last one revoked", revoked)
	}

	// A hold stops mattering once MaxHold has passed.
	h.oauth.set(func(oauth *fakeOAuth) { oauth.account = domain.ProviderAccount{ID: "77"} })
	held, err := h.connect(t, otherToken, "github", AuthorizeInput{AppID: app.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.service.AddHold(ctx, service(domain.PurposeCleanup), organization, held.Connection.ID, "integrations"); err != nil {
		t.Fatal(err)
	}
	if err := h.service.Disconnect(ctx, as(otherToken), organization, held.Connection.ID); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(domain.MaxHold + time.Minute)
	if claimed, err := h.service.CleanUp(ctx); err != nil || claimed != 1 || h.stored(t, held.Connection.ID).CredentialsDeletedAt == nil {
		t.Fatalf("CleanUp() past MaxHold = %d %v", claimed, err)
	}
	var sessions int
	if err := h.database.QueryRow(ctx, `SELECT count(*) FROM connections_sessions`).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("sessions left after the sweep = %d %v", sessions, err)
	}
}

// outboxEvents returns the data of the events of one detail type.
func (h *dbHarness) outboxEvents(t *testing.T, detailType string) []string {
	t.Helper()
	rows, err := h.database.Query(ctx, `SELECT detail::text FROM connections_outbox WHERE detail_type = $1 ORDER BY occurred_at, id`, detailType)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var detail string
		if err := rows.Scan(&detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	return details
}

func TestHeartbeat(t *testing.T) {
	h := newDBHarness(t)
	app := h.createApp(t, "github", "read:user", "repo")
	connect := func(token, account string) string {
		t.Helper()
		h.oauth.set(func(oauth *fakeOAuth) { oauth.account = domain.ProviderAccount{ID: account} })
		completion, err := h.connect(t, token, "github", AuthorizeInput{AppID: app.ID})
		if err != nil {
			t.Fatal(err)
		}
		return completion.Connection.ID
	}
	health := func(connectionID string) (domain.Health, int, time.Time) {
		t.Helper()
		var state domain.Health
		var failures int
		var next time.Time
		if err := h.database.QueryRow(ctx, `SELECT state, consecutive_failures, next_check_at FROM connections_health WHERE connection_id = $1`,
			connectionID).Scan(&state, &failures, &next); err != nil {
			t.Fatal(err)
		}
		return state, failures, next
	}
	checked := func(want int) {
		t.Helper()
		if claimed, err := h.service.CheckHealth(ctx); err != nil || claimed != want {
			t.Fatalf("CheckHealth() = %d %v, want %d", claimed, err, want)
		}
	}

	connectionID := connect(memberToken, "42")
	checked(0)
	if err := h.service.Report(ctx, member(memberToken), organization, connectionID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("Report() by a member alone error = %v", err)
	}
	if err := h.service.Report(ctx, service(domain.PurposeAction), organization, connectionID); err != nil {
		t.Fatal(err)
	}
	h.oauth.set(func(oauth *fakeOAuth) { oauth.account = domain.ProviderAccount{ID: "42"} })
	checked(1)
	if state, failures, next := health(connectionID); state != domain.HealthHealthy || failures != 0 || next.Before(h.now.Add(20*time.Hour)) {
		t.Fatalf("health after a passing check = %s %d %v", state, failures, next)
	}

	h.exec(t, `UPDATE connections_health SET next_check_at = $1`, h.now)
	h.oauth.set(func(oauth *fakeOAuth) {
		oauth.identityErr = fmt.Errorf("%w: provider returned 502", domain.ErrProviderUnavailable)
	})
	checked(1)
	if state, failures, next := health(connectionID); state != domain.HealthDegraded || failures != 1 || !next.Equal(h.now.Add(domain.MinHealthBackoff)) {
		t.Fatalf("health after a failing check = %s %d %v", state, failures, next)
	}
	h.now = h.now.Add(domain.MinHealthBackoff)
	checked(1)
	if state, failures, next := health(connectionID); state != domain.HealthDegraded || failures != 2 || !next.Equal(h.now.Add(2*domain.MinHealthBackoff)) {
		t.Fatalf("health after a second failing check = %s %d %v", state, failures, next)
	}
	if events := h.outbox(t)[connectionseventsv1.ConnectionHealthChangedDetailType]; events != 2 {
		t.Fatalf("health changed events = %d, want unknown→healthy and healthy→degraded only", events)
	}

	// A different account, or a refused credential, needs a reconnect.
	h.oauth.set(func(oauth *fakeOAuth) { oauth.identityErr, oauth.account = nil, domain.ProviderAccount{ID: "43"} })
	h.exec(t, `UPDATE connections_health SET next_check_at = $1`, h.now)
	checked(1)
	if connection := h.stored(t, connectionID); connection.Status != domain.StatusNeedsReconnect || connection.Reason != string(domain.ReconnectAccountChanged) {
		t.Fatalf("connection after an account change = %s/%s", connection.Status, connection.Reason)
	}
	refused := connect(otherToken, "44")
	h.oauth.set(func(oauth *fakeOAuth) {
		oauth.identityErr = fmt.Errorf("%w: provider refused the credential", domain.ErrCredentialRejected)
	})
	h.exec(t, `UPDATE connections_health SET next_check_at = $1`, h.now)
	checked(1)
	if connection := h.stored(t, refused); connection.Status != domain.StatusNeedsReconnect || connection.Reason != string(domain.ReconnectUnauthorized) {
		t.Fatalf("connection after a refused credential = %s/%s", connection.Status, connection.Reason)
	}

	// Recent use is the heartbeat, until a failure is reported.
	h.oauth.set(func(oauth *fakeOAuth) { oauth.identityErr = nil; oauth.account = domain.ProviderAccount{ID: "45"} })
	used := connect(adminToken, "45")
	if _, err := h.service.IssueToken(ctx, forwarded(adminToken, domain.PurposeAction), used, tokenRequest(domain.PurposeAction, "")); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(time.Minute)
	h.exec(t, `UPDATE connections_health SET next_check_at = $1 WHERE connection_id = $2`, h.now, used)
	identities := h.oauth.identityCount()
	checked(1)
	if h.oauth.identityCount() != identities {
		t.Fatal("a recently used connection was checked")
	}
	if err := h.service.Report(ctx, service(domain.PurposeAction), organization, used); err != nil {
		t.Fatal(err)
	}
	checked(1)
	if state, _, _ := health(used); state != domain.HealthHealthy || h.oauth.identityCount() != identities+1 {
		t.Fatalf("health of a reported connection = %s", state)
	}
}

func TestLifecycleEvents(t *testing.T) {
	h := newDBHarness(t)
	app := h.createApp(t, "github", "read:user", "repo")
	connect := func(token string, input AuthorizeInput, account string) string {
		t.Helper()
		h.oauth.set(func(oauth *fakeOAuth) { oauth.account = domain.ProviderAccount{ID: account} })
		input.AppID = app.ID
		completion, err := h.connect(t, token, "github", input)
		if err != nil {
			t.Fatal(err)
		}
		return completion.Connection.ID
	}
	personal := connect(memberToken, AuthorizeInput{}, "42")
	team := connect(memberToken, AuthorizeInput{Scope: domain.ScopeTeam, TeamID: "team_1"}, "43")
	shared := connect(adminToken, AuthorizeInput{Scope: domain.ScopeOrganization}, "44")
	status := func(connectionID string) (domain.Status, string) {
		connection := h.stored(t, connectionID)
		return connection.Status, connection.Reason
	}

	if err := h.service.MemberRemoved(ctx, autheventsv1.MemberRemoved{OrganizationID: organization, UserID: "user-1",
		Reason: autheventsv1.MemberRemovedReasonOrganizationDeleted}); err != nil {
		t.Fatal(err)
	}
	if got, _ := status(personal); got != domain.StatusActive {
		t.Fatal("a removal because the organization was deleted disconnected a connection")
	}
	for range 2 {
		if err := h.service.MemberRemoved(ctx, autheventsv1.MemberRemoved{OrganizationID: organization, UserID: "user-1",
			Reason: autheventsv1.MemberRemovedReasonLeft}); err != nil {
			t.Fatal(err)
		}
	}
	if got, reason := status(personal); got != domain.StatusDisconnected || reason != string(domain.DisconnectMemberLeft) {
		t.Fatalf("member connection after the member left = %s/%s", got, reason)
	}
	if got, _ := status(team); got != domain.StatusActive {
		t.Fatal("the member's team connection was disconnected")
	}
	if err := h.service.TeamDeleted(ctx, teamseventsv1.TeamDeleted{OrganizationID: organization, TeamID: "team_1"}); err != nil {
		t.Fatal(err)
	}
	if got, reason := status(team); got != domain.StatusDisconnected || reason != string(domain.DisconnectTeamDeleted) {
		t.Fatalf("team connection after the team was deleted = %s/%s", got, reason)
	}
	for range 2 {
		if err := h.service.OrganizationDeleted(ctx, autheventsv1.OrganizationDeleted{OrganizationID: organization}); err != nil {
			t.Fatal(err)
		}
	}
	if got, reason := status(shared); got != domain.StatusDisconnected || reason != string(domain.DisconnectOrganizationDeleted) {
		t.Fatalf("organization connection after the organization was deleted = %s/%s", got, reason)
	}
	if apps, err := h.db.Apps(ctx, organization); err != nil || len(apps) != 0 {
		t.Fatalf("apps after the organization was deleted = %+v %v", apps, err)
	}
	counts := h.outbox(t)
	if counts[connectionseventsv1.ConnectionDisconnectedDetailType] != 3 || counts[connectionseventsv1.AppDeletedDetailType] != 1 {
		t.Fatalf("outbox = %v", counts)
	}
	if deleted := h.outboxEvents(t, connectionseventsv1.AppDeletedDetailType); !strings.Contains(deleted[0], `"reason": "organization_deleted"`) &&
		!strings.Contains(deleted[0], `"reason":"organization_deleted"`) {
		t.Fatalf("app deleted event = %s", deleted[0])
	}
	for _, invalid := range []error{
		h.service.OrganizationDeleted(ctx, autheventsv1.OrganizationDeleted{}),
		h.service.MemberRemoved(ctx, autheventsv1.MemberRemoved{OrganizationID: organization}),
		h.service.TeamDeleted(ctx, teamseventsv1.TeamDeleted{TeamID: "team_1"}),
	} {
		if invalid == nil {
			t.Fatal("an event naming nothing was accepted")
		}
	}
}

func TestAuthorizeLimitsOpenSessions(t *testing.T) {
	h := newDBHarness(t)
	app := h.createApp(t, "github", "read:user")
	for index := range domain.MaxOpenSessions {
		if _, err := h.service.Authorize(ctx, as(memberToken), organization, AuthorizeInput{AppID: app.ID, ReturnURI: returnTo}); err != nil {
			t.Fatalf("Authorize() %d error = %v", index+1, err)
		}
	}
	if _, err := h.service.Authorize(ctx, as(memberToken), organization, AuthorizeInput{AppID: app.ID, ReturnURI: returnTo}); !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("Authorize() past the limit error = %v", err)
	}
	if _, err := h.service.Authorize(ctx, as(otherToken), organization, AuthorizeInput{AppID: app.ID, ReturnURI: returnTo}); err != nil {
		t.Fatalf("another member's Authorize() error = %v", err)
	}
	h.now = h.now.Add(domain.SessionTTL)
	if _, err := h.service.Authorize(ctx, as(memberToken), organization, AuthorizeInput{AppID: app.ID, ReturnURI: returnTo}); err != nil {
		t.Fatalf("Authorize() after the sessions expired error = %v", err)
	}
}

// TestReconnectDuringRenewalWins replays a reconnect landing while a renewal
// is still waiting on the provider. Whether the late refresh succeeds or is
// refused, the reconnect's credentials must stand, the connection must stay
// active, and the waiting caller must get the reconnected token.
func TestReconnectDuringRenewalWins(t *testing.T) {
	h := newDBHarness(t)
	app := h.createApp(t, "google")
	for name, refreshErr := range map[string]error{
		"late refresh succeeds": nil,
		"late refresh refused":  fmt.Errorf("%w: invalid_grant", domain.ErrCredentialRejected),
	} {
		t.Run(name, func(t *testing.T) {
			account := "sub-" + strings.ReplaceAll(name, " ", "-")
			h.oauth.set(func(oauth *fakeOAuth) {
				oauth.grant = providers.Grant{AccessToken: "access-old", RefreshToken: "refresh-old", Expiry: h.now.Add(time.Minute), Scopes: app.Scopes}
				oauth.account = domain.ProviderAccount{ID: account}
				oauth.refreshes, oauth.refreshErr, oauth.refreshDelay = nil, refreshErr, 1500*time.Millisecond
				oauth.refreshGrant = providers.Grant{AccessToken: "access-stale", RefreshToken: "refresh-stale", Expiry: h.now.Add(time.Hour), Scopes: app.Scopes}
			})
			completion, err := h.connect(t, memberToken, "google", AuthorizeInput{AppID: app.ID})
			if err != nil {
				t.Fatal(err)
			}
			connectionID := completion.Connection.ID

			type issued struct {
				token domain.IssuedToken
				err   error
			}
			result := make(chan issued, 1)
			go func() {
				token, err := h.service.IssueToken(ctx, forwarded(memberToken, domain.PurposeAction), connectionID, tokenRequest(domain.PurposeAction, ""))
				result <- issued{token, err}
			}()
			for deadline := time.Now().Add(5 * time.Second); h.oauth.refreshCount() == 0; {
				if time.Now().After(deadline) {
					t.Fatal("the renewal never reached the provider")
				}
				time.Sleep(20 * time.Millisecond)
			}

			h.oauth.set(func(oauth *fakeOAuth) {
				oauth.grant = providers.Grant{AccessToken: "access-reconnected", RefreshToken: "refresh-reconnected", Expiry: h.now.Add(time.Hour), Scopes: app.Scopes}
			})
			authorization, err := h.service.Reconnect(ctx, as(memberToken), organization, connectionID, returnTo)
			if err != nil {
				t.Fatal(err)
			}
			if reconnected, err := h.service.Complete(ctx, as(memberToken), organization, h.callback(t, "google", authorization)); err != nil || !reconnected.Reconnected {
				t.Fatalf("Complete() during a renewal = %+v %v", reconnected, err)
			}

			got := <-result
			if got.err != nil || got.token.AccessToken != "access-reconnected" {
				t.Fatalf("IssueToken() racing the reconnect = %+v %v", got.token, got.err)
			}
			connection := h.stored(t, connectionID)
			if sealed := h.secretsOf(t, connection); connection.Status != domain.StatusActive || sealed.Grant.AccessToken != "access-reconnected" ||
				sealed.Grant.RefreshToken != "refresh-reconnected" {
				t.Fatalf("connection after the race = %s, grant %+v", connection.Status, sealed.Grant)
			}
			if refreshes := h.oauth.refreshCount(); refreshes != 1 {
				t.Fatalf("the provider was asked to refresh %d times", refreshes)
			}
		})
	}
	if events := h.outbox(t)[connectionseventsv1.ConnectionNeedsReconnectDetailType]; events != 0 {
		t.Fatalf("needs-reconnect events = %d, want none", events)
	}
}

// TestHeartbeatNonOAuth2Credentials exercises probe's other three branches:
// TestHeartbeat only ever claims an OAuth2 connection. Each section here
// forces a passing check, then a permanent failure, and checks the
// connection is marked needs_reconnect with the right reason. The
// client-credentials failure runs through freshGrant exactly like an OAuth2
// refresh would, which also proves CheckHealth's ErrNeedsReconnect skip
// branch: the renewal inside probe already recorded the failure, so the
// check itself is recorded as skipped rather than failed.
func TestHeartbeatNonOAuth2Credentials(t *testing.T) {
	h := newDBHarness(t)
	forceDue := func(connectionID string) {
		t.Helper()
		h.exec(t, `UPDATE connections_health SET next_check_at = $1 WHERE connection_id = $2`, h.now, connectionID)
	}
	health := func(connectionID string) (domain.Health, int) {
		t.Helper()
		var state domain.Health
		var failures int
		if err := h.database.QueryRow(ctx, `SELECT state, consecutive_failures FROM connections_health WHERE connection_id = $1`,
			connectionID).Scan(&state, &failures); err != nil {
			t.Fatal(err)
		}
		return state, failures
	}
	checked := func(want int) {
		t.Helper()
		if claimed, err := h.service.CheckHealth(ctx); err != nil || claimed != want {
			t.Fatalf("CheckHealth() = %d %v, want %d", claimed, err, want)
		}
	}

	// Client credentials: minting in the tenant is the check.
	ccApp, err := h.service.CreateApp(ctx, as(adminToken), organization, CreateAppInput{
		Provider: "microsoft", CredentialType: domain.CredentialOAuth2ClientCredentials, ClientID: "client-cc", ClientSecret: "secret-cc",
	})
	if err != nil {
		t.Fatal(err)
	}
	h.oauth.set(func(oauth *fakeOAuth) {
		oauth.mintGrant = providers.Grant{AccessToken: "app-token-1", Expiry: h.now.Add(time.Hour), Scopes: ccApp.Scopes}
	})
	ccConnection, err := h.service.CreateConnection(ctx, as(adminToken), organization, CreateConnectionInput{
		AppID: ccApp.ID, Scope: domain.ScopeOrganization, Credential: domain.CredentialInput{Tenant: "contoso.onmicrosoft.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	forceDue(ccConnection.ID)
	checked(1)
	if state, failures := health(ccConnection.ID); state != domain.HealthHealthy || failures != 0 {
		t.Fatalf("client-credentials health after a passing check = %s %d", state, failures)
	}

	h.now = h.now.Add(59 * time.Minute)
	h.oauth.set(func(oauth *fakeOAuth) {
		oauth.mintErr = fmt.Errorf("%w: unauthorized_client", domain.ErrCredentialRejected)
	})
	forceDue(ccConnection.ID)
	checked(1)
	if connection := h.stored(t, ccConnection.ID); connection.Status != domain.StatusNeedsReconnect || connection.Reason != string(domain.ReconnectCredentialRejected) {
		t.Fatalf("client-credentials connection after a rejected mint = %s/%s", connection.Status, connection.Reason)
	}
	if state, failures := health(ccConnection.ID); state != domain.HealthHealthy || failures != 0 {
		t.Fatalf("client-credentials health after a skipped check = %s %d, want it unchanged by the freshGrant failure", state, failures)
	}

	// Installation: identity comes from the installation itself, not a grant.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	installApp, err := h.service.CreateApp(ctx, as(adminToken), organization, CreateAppInput{
		Provider: "github", CredentialType: domain.CredentialInstallation, ClientID: "Iv1.client", ClientSecret: "secret-app",
		GitHubAppID: "123", GitHubAppSlug: "oryxa-test", PrivateKey: privateKeyPEM,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.oauth.set(func(oauth *fakeOAuth) {
		oauth.grant = providers.Grant{AccessToken: "ghu_user"}
		oauth.userInstallations = []string{"999"}
		oauth.installationAccount = domain.ProviderAccount{ID: "999", Name: "acme"}
		oauth.installationToken = providers.Grant{AccessToken: "ghs_installation", Expiry: h.now.Add(time.Hour)}
	})
	authorization, err := h.service.Authorize(ctx, as(memberToken), organization, AuthorizeInput{AppID: installApp.ID, ReturnURI: returnTo})
	if err != nil {
		t.Fatal(err)
	}
	target, err := h.service.Callback(ctx, "github", CallbackParams{
		State: stateOf(t, authorization), Code: "code-install", InstallationID: "999", SetupAction: "install",
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(target)
	installCompletion, err := h.service.Complete(ctx, as(memberToken), organization, parsed.Query().Get("session_id"))
	if err != nil {
		t.Fatal(err)
	}
	installConnection := installCompletion.Connection.ID
	forceDue(installConnection)
	checked(1)
	if state, failures := health(installConnection); state != domain.HealthHealthy || failures != 0 {
		t.Fatalf("installation health after a passing check = %s %d", state, failures)
	}

	h.oauth.set(func(oauth *fakeOAuth) {
		oauth.installationErr = fmt.Errorf("%w: %w", domain.ErrCredentialRejected, providers.ErrInstallationNotFound)
	})
	forceDue(installConnection)
	checked(1)
	if connection := h.stored(t, installConnection); connection.Status != domain.StatusNeedsReconnect || connection.Reason != string(domain.ReconnectInstallationRemoved) {
		t.Fatalf("installation connection after a missing installation = %s/%s", connection.Status, connection.Reason)
	}

	// Key-based: identity is checked directly against the key, with no grant
	// to renew.
	bearerApp, err := h.service.CreateApp(ctx, as(adminToken), organization, CreateAppInput{Provider: "github", CredentialType: domain.CredentialBearer})
	if err != nil {
		t.Fatal(err)
	}
	h.oauth.set(func(oauth *fakeOAuth) { oauth.keyAccount = domain.ProviderAccount{ID: "42", Name: "octocat"} })
	keyConnection, err := h.service.CreateConnection(ctx, as(memberToken), organization, CreateConnectionInput{
		AppID: bearerApp.ID, Credential: domain.CredentialInput{Token: "ghp_first"},
	})
	if err != nil {
		t.Fatal(err)
	}
	forceDue(keyConnection.ID)
	checked(1)
	if state, failures := health(keyConnection.ID); state != domain.HealthHealthy || failures != 0 {
		t.Fatalf("key-based health after a passing check = %s %d", state, failures)
	}

	h.oauth.set(func(oauth *fakeOAuth) {
		oauth.keyErr = fmt.Errorf("%w: provider refused the credential", domain.ErrCredentialRejected)
	})
	forceDue(keyConnection.ID)
	checked(1)
	if connection := h.stored(t, keyConnection.ID); connection.Status != domain.StatusNeedsReconnect || connection.Reason != string(domain.ReconnectCredentialRejected) {
		t.Fatalf("key-based connection after a refused key = %s/%s", connection.Status, connection.Reason)
	}
}

// TestInstallationTokenRenewalFailure covers token.go's installation renewal
// branch, the other path that reads fakeOAuth.installationTokErr (the
// heartbeat instead reads installationErr, covered above): IssueToken renews
// an installation token through InstallationToken, not Installation, so a
// removed installation surfaces there when the cached token nears expiry.
func TestInstallationTokenRenewalFailure(t *testing.T) {
	h := newDBHarness(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	app, err := h.service.CreateApp(ctx, as(adminToken), organization, CreateAppInput{
		Provider: "github", CredentialType: domain.CredentialInstallation, ClientID: "Iv1.client", ClientSecret: "secret-app",
		GitHubAppID: "123", GitHubAppSlug: "oryxa-test", PrivateKey: privateKeyPEM,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.oauth.set(func(oauth *fakeOAuth) {
		oauth.grant = providers.Grant{AccessToken: "ghu_user"}
		oauth.userInstallations = []string{"999"}
		oauth.installationAccount = domain.ProviderAccount{ID: "999", Name: "acme"}
		oauth.installationToken = providers.Grant{AccessToken: "ghs_installation", Expiry: h.now.Add(time.Hour)}
	})
	authorization, err := h.service.Authorize(ctx, as(memberToken), organization, AuthorizeInput{AppID: app.ID, ReturnURI: returnTo})
	if err != nil {
		t.Fatal(err)
	}
	target, err := h.service.Callback(ctx, "github", CallbackParams{
		State: stateOf(t, authorization), Code: "code-install", InstallationID: "999", SetupAction: "install",
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(target)
	completion, err := h.service.Complete(ctx, as(memberToken), organization, parsed.Query().Get("session_id"))
	if err != nil {
		t.Fatal(err)
	}
	connectionID := completion.Connection.ID
	if token, err := h.service.IssueToken(ctx, forwarded(memberToken, domain.PurposeAction), connectionID, tokenRequest(domain.PurposeAction, "")); err != nil ||
		token.AccessToken != "ghs_installation" {
		t.Fatalf("initial IssueToken(installation) = %+v %v", token, err)
	}

	// The installation token nears its expiry; the provider says the
	// installation is gone.
	h.now = h.now.Add(59 * time.Minute)
	h.oauth.set(func(oauth *fakeOAuth) {
		oauth.installationTokErr = fmt.Errorf("%w: %w", domain.ErrCredentialRejected, providers.ErrInstallationNotFound)
	})
	if _, err := h.service.IssueToken(ctx, forwarded(memberToken, domain.PurposeAction), connectionID, tokenRequest(domain.PurposeAction, "")); !errors.Is(err, domain.ErrNeedsReconnect) {
		t.Fatalf("IssueToken() with a removed installation error = %v", err)
	}
	if connection := h.stored(t, connectionID); connection.Status != domain.StatusNeedsReconnect || connection.Reason != string(domain.ReconnectInstallationRemoved) {
		t.Fatalf("connection after a removed installation = %s/%s", connection.Status, connection.Reason)
	}
}

// TestClientCredentialsMintFailures covers Mint's two callers: a rejected
// mint during CreateConnection leaves nothing behind, and one during a later
// renewal, driven through IssueToken exactly as TestIssueTokenRenewalFailures
// drives an OAuth2 refresh, marks the connection needs_reconnect. A
// provider-unavailable mint in between shows the same still-usable-token
// fallback renew gives an OAuth2 grant.
func TestClientCredentialsMintFailures(t *testing.T) {
	h := newDBHarness(t)
	app, err := h.service.CreateApp(ctx, as(adminToken), organization, CreateAppInput{
		Provider: "microsoft", CredentialType: domain.CredentialOAuth2ClientCredentials, ClientID: "client-cc", ClientSecret: "secret-cc",
	})
	if err != nil {
		t.Fatal(err)
	}

	h.oauth.set(func(oauth *fakeOAuth) {
		oauth.mintErr = fmt.Errorf("%w: unauthorized_client", domain.ErrCredentialRejected)
	})
	if _, err := h.service.CreateConnection(ctx, as(adminToken), organization, CreateConnectionInput{
		AppID: app.ID, Scope: domain.ScopeOrganization, Credential: domain.CredentialInput{Tenant: "contoso.onmicrosoft.com"},
	}); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("CreateConnection() with a rejected mint error = %v", err)
	}
	if connections, err := h.service.ListConnections(ctx, as(adminToken), organization, ConnectionFilter{}); err != nil || len(connections) != 0 {
		t.Fatalf("connections after a refused connect = %+v %v", connections, err)
	}

	h.oauth.set(func(oauth *fakeOAuth) {
		oauth.mintErr = nil
		oauth.mintGrant = providers.Grant{AccessToken: "app-token-1", Expiry: h.now.Add(time.Hour), Scopes: app.Scopes}
	})
	created, err := h.service.CreateConnection(ctx, as(adminToken), organization, CreateConnectionInput{
		AppID: app.ID, Scope: domain.ScopeOrganization, Credential: domain.CredentialInput{Tenant: "contoso.onmicrosoft.com"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The token has not actually expired yet, so a failing renewal still hands
	// out the old one.
	h.now = h.now.Add(59 * time.Minute)
	h.oauth.set(func(oauth *fakeOAuth) {
		oauth.mintErr = fmt.Errorf("%w: token endpoint returned 503", domain.ErrProviderUnavailable)
	})
	token, err := h.service.IssueToken(ctx, service(domain.PurposeTrigger), created.ID, tokenRequest(domain.PurposeTrigger, "user-1"))
	if err != nil || token.AccessToken != "app-token-1" {
		t.Fatalf("IssueToken() with an unexpired token and a failing mint = %+v %v", token, err)
	}

	// Now it really has expired, so the failure surfaces.
	h.now = h.now.Add(2 * time.Minute)
	if _, err := h.service.IssueToken(ctx, service(domain.PurposeTrigger), created.ID, tokenRequest(domain.PurposeTrigger, "user-1")); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("IssueToken() with an expired token and a failing mint error = %v", err)
	}
	if connection := h.stored(t, created.ID); connection.Status != domain.StatusActive {
		t.Fatalf("connection after a transient mint failure = %s, want it to stay active", connection.Status)
	}

	// A permanent refusal marks the connection needs_reconnect instead.
	h.oauth.set(func(oauth *fakeOAuth) {
		oauth.mintErr = fmt.Errorf("%w: unauthorized_client", domain.ErrCredentialRejected)
	})
	if _, err := h.service.IssueToken(ctx, service(domain.PurposeTrigger), created.ID, tokenRequest(domain.PurposeTrigger, "user-1")); !errors.Is(err, domain.ErrNeedsReconnect) {
		t.Fatalf("IssueToken() with a permanently rejected mint error = %v", err)
	}
	if connection := h.stored(t, created.ID); connection.Status != domain.StatusNeedsReconnect || connection.Reason != string(domain.ReconnectCredentialRejected) {
		t.Fatalf("connection after a rejected mint = %s/%s", connection.Status, connection.Reason)
	}
}
