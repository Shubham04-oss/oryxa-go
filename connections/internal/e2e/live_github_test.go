package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/providers"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github/repos"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github/users"
)

// TestLiveGitHubPersonalToken connects a real GitHub personal access token
// through connections against the real GitHub, then calls GitHub through the
// generated GitHub clients with the credential connections issues. It runs
// only with CONNECTIONS_LIVE_GITHUB_TOKEN and CONNECTIONS_TEST_DATABASE_URL
// set, only reads from GitHub, and never shows the token in its output.
func TestLiveGitHubPersonalToken(t *testing.T) {
	token := strings.TrimSpace(os.Getenv("CONNECTIONS_LIVE_GITHUB_TOKEN"))
	if token == "" {
		t.Skip("set CONNECTIONS_LIVE_GITHUB_TOKEN to connect a real GitHub token")
	}
	redacted := func(body []byte) string { return strings.ReplaceAll(string(body), token, "[token]") }
	store, database := openStore(t)
	ctx := context.Background()
	identity := newServiceIdentity(t)
	// The provider client is the one connections runs with, which refuses
	// private and loopback addresses.
	stack := serveStack(t, store, fakeAuth(t), providers.Builtin(), providers.NewHTTPClient(), identity.verifier)

	// An admin enables GitHub personal access tokens, and a member connects
	// theirs. Connections asks GitHub whose token it is.
	status, _, body := stack.api.do(http.MethodPost, "/apps", adminToken, map[string]any{"provider": "github", "credential_type": "bearer"})
	var app domain.App
	if status != http.StatusCreated || json.Unmarshal(body, &app) != nil {
		t.Fatalf("POST apps = %d %s", status, body)
	}
	status, _, body = stack.api.do(http.MethodPost, "/connections", memberToken, map[string]any{
		"app_id": app.ID, "label": "Live GitHub token", "credential": map[string]string{"token": token},
	})
	var connection domain.Connection
	if status != http.StatusCreated || json.Unmarshal(body, &connection) != nil || connection.ProviderAccount.ID == "" {
		t.Fatalf("POST connections = %d %s", status, redacted(body))
	}
	if strings.Contains(string(body), token) {
		t.Fatal("the connection response carried the token")
	}
	t.Logf("GitHub accepted the token: connected account %q, id %s", connection.ProviderAccount.Name, connection.ProviderAccount.ID)

	// GitHub refuses a token it never issued, and connections says so.
	status, _, body = stack.api.do(http.MethodPost, "/connections", memberToken, map[string]any{
		"app_id": app.ID, "credential": map[string]string{"token": "ghp_" + strings.Repeat("0", 36)},
	})
	if status != http.StatusUnprocessableEntity || !strings.Contains(string(body), "credential_rejected") {
		t.Errorf("POST connections with a token GitHub never issued = %d %s", status, body)
	}

	// The token is sealed at rest and never reaches an event.
	var inRow, inOutbox bool
	if err := database.QueryRow(ctx, `SELECT strpos(encrypted_credentials::text, $1) > 0 FROM connections WHERE id = $2`,
		token, connection.ID).Scan(&inRow); err != nil || inRow {
		t.Errorf("the stored credential holds the token in the clear = %v %v", inRow, err)
	}
	if err := database.QueryRow(ctx, `SELECT coalesce(bool_or(strpos(detail::text, $1) > 0), false) FROM connections_outbox`,
		token).Scan(&inOutbox); err != nil || inOutbox {
		t.Errorf("an outbox event holds the token = %v %v", inOutbox, err)
	}

	// A service asks for the credential as itself, acting for the member.
	status, body = stack.internal.serviceCall(http.MethodPost, "/"+connection.ID+"/token", identity.token("action"),
		map[string]any{"organization_id": organization, "purpose": "action", "acting_member_id": "user-1"})
	var issued domain.IssuedToken
	if status != http.StatusOK || json.Unmarshal(body, &issued) != nil || issued.AccessToken != token || issued.Placement.In != "header" {
		t.Fatalf("token issuance = %d %s", status, redacted(body))
	}

	// The issued credential works with the generated GitHub clients, placed
	// where connections says it goes.
	place := func(_ context.Context, request *http.Request) error {
		request.Header.Set(issued.Placement.Name, issued.Placement.Prefix+issued.AccessToken)
		return nil
	}
	httpClient := &http.Client{Timeout: 30 * time.Second}
	calls, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()

	usersClient, err := users.NewClientWithResponses(github.Server,
		users.WithHTTPClient(httpClient), users.WithRequestEditorFn(github.Versioned), users.WithRequestEditorFn(place))
	if err != nil {
		t.Fatal(err)
	}
	me, err := usersClient.GetAuthenticatedWithResponse(calls)
	if err != nil {
		t.Fatalf("GetAuthenticated: %v", err)
	}
	if me.JSON200 == nil {
		t.Fatalf("GetAuthenticated answered %d: %s", me.StatusCode(), redacted(me.Body))
	}
	user, err := me.JSON200.AsPrivateUser()
	if err != nil || strconv.FormatInt(user.ID, 10) != connection.ProviderAccount.ID {
		t.Errorf("GetAuthenticated user = %s, id %d, %v; want the connection's account id %s", user.Login, user.ID, err, connection.ProviderAccount.ID)
	}
	scopes := me.HTTPResponse.Header.Get("X-OAuth-Scopes")
	if scopes == "" {
		scopes = "none listed, as for a fine-grained token"
	}
	t.Logf("GitHub user %s, token scopes: %s", user.Login, scopes)

	reposClient, err := repos.NewClientWithResponses(github.Server,
		repos.WithHTTPClient(httpClient), repos.WithRequestEditorFn(github.Versioned), repos.WithRequestEditorFn(place))
	if err != nil {
		t.Fatal(err)
	}
	perPage := 100
	list, err := reposClient.ListForAuthenticatedUserWithResponse(calls, &repos.ListForAuthenticatedUserParams{PerPage: &perPage})
	if err != nil {
		t.Fatalf("ListForAuthenticatedUser: %v", err)
	}
	if list.JSON200 == nil {
		t.Fatalf("ListForAuthenticatedUser answered %d: %s", list.StatusCode(), redacted(list.Body))
	}
	t.Logf("the token sees %d repositories on the first page", len(*list.JSON200))

	// Disconnecting deletes the credential, after which nothing is issued.
	if status, _, body := stack.api.do(http.MethodDelete, "/connections/"+connection.ID, memberToken, nil); status != http.StatusNoContent {
		t.Fatalf("DELETE connection = %d %s", status, body)
	}
	if claimed, err := stack.service.CleanUp(ctx); err != nil || claimed != 1 {
		t.Fatalf("CleanUp() = %d %v", claimed, err)
	}
	status, body = stack.internal.serviceCall(http.MethodPost, "/"+connection.ID+"/token", identity.token("cleanup"),
		map[string]any{"organization_id": organization, "purpose": "cleanup", "acting_member_id": "user-1"})
	if status != http.StatusNotFound {
		t.Fatalf("token issuance after deletion = %d %s", status, redacted(body))
	}
}
