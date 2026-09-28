package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"
	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/hooks"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/signin"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

func TestIntegrationsListWhatOryxaIsSwitchedOnFor(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "slack", "github")
	listed := h.service.Integrations()
	if len(listed) != 2 || listed[0].Toolkit != "github" || listed[1].Toolkit != "slack" {
		t.Fatalf("integrations = %+v", listed)
	}
	if strings.Join(listed[0].Kinds, ",") != "oauth2_code,api_token" || listed[0].Name != "GitHub" {
		t.Fatalf("github = %+v", listed[0])
	}
}

func TestManifestViewIsWhatAFormNeedsAndNothingSecret(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github", "slack", "notion", "gmail")

	github, err := h.service.Manifest("github")
	if err != nil {
		t.Fatal(err)
	}
	if github.Toolkit != "github" || github.Name != "GitHub" || github.RedirectURI != redirectURI || len(github.Methods) != 2 {
		t.Fatalf("github = %+v", github)
	}
	oauth, token := github.Methods[0], github.Methods[1]
	if oauth.Kind != integration.KindOAuth2Code || oauth.Client != "github" || strings.Join(oauth.Scopes, " ") != "repo read:org" || len(oauth.Fields) != 0 {
		t.Fatalf("oauth = %+v", oauth)
	}
	if token.Kind != integration.KindAPIToken || len(token.Fields) != 1 {
		t.Fatalf("token = %+v", token)
	}
	if field := token.Fields[0]; field.Name != "token" || !field.Secret || !field.Standard ||
		field.Level != integration.LevelConnection || !field.Validation.Required {
		t.Fatalf("token field = %+v", field)
	}
	// A connection's webhooks are made for each subscription: nothing to copy.
	if github.Webhook == nil || github.Webhook.Scope != integration.HookScopeConnection || github.Webhook.OrganizationEndpoint {
		t.Fatalf("github webhook = %+v", github.Webhook)
	}

	slack, err := h.service.Manifest("slack")
	if err != nil {
		t.Fatal(err)
	}
	if !slack.Webhook.OrganizationEndpoint || !slack.Webhook.OrganizationSecret || slack.Webhook.VerificationToken {
		t.Fatalf("slack webhook = %+v", slack.Webhook)
	}
	notion, err := h.service.Manifest("notion")
	if err != nil {
		t.Fatal(err)
	}
	if !notion.Webhook.OrganizationEndpoint || notion.Webhook.OrganizationSecret || !notion.Webhook.VerificationToken {
		t.Fatalf("notion webhook = %+v", notion.Webhook)
	}
	// Gmail's pushes are signed for the deployment: no organization's own.
	gmail, err := h.service.Manifest("gmail")
	if err != nil {
		t.Fatal(err)
	}
	if gmail.Webhook.OrganizationEndpoint || gmail.Webhook.OrganizationSecret {
		t.Fatalf("gmail webhook = %+v", gmail.Webhook)
	}

	// What goes where on a request, and every URL of the app's, stays inside.
	encoded, err := json.Marshal(github)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"token_url", "auth_url", "api_base", "\"use\"", "template", "Bearer", "client_secret", "access_token"} {
		if strings.Contains(string(encoded), leaked) {
			t.Errorf("the manifest view carries %s: %s", leaked, encoded)
		}
	}
	if _, err := h.service.Manifest("zendesk"); !errors.Is(err, domain.ErrNotConnectable) {
		t.Fatalf("an unshipped toolkit = %v", err)
	}
}

func TestOnlyAnOrganizationsAdminsManageItsOAuthClients(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github")
	ctx := context.Background()
	if _, err := h.service.OAuthClient(ctx, memberToken, "org_1", "github"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a member reading = %v", err)
	}
	if _, err := h.service.PutOAuthClient(ctx, memberToken, "org_1", "github", OAuthClientInput{ClientID: "Iv1.own"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a member saving = %v", err)
	}
	if err := h.service.RemoveOAuthClient(ctx, memberToken, "org_1", "github"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a member removing = %v", err)
	}
	if _, err := h.service.OAuthClient(ctx, adminToken, "org_2", "github"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("another organization's admin = %v", err)
	}
	if _, err := h.service.OAuthClient(ctx, adminToken, "org_1", "slack"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a registration nothing switched on signs in through = %v", err)
	}
	if len(h.signin.puts) != 0 || len(h.signin.removedCalls) != 0 {
		t.Fatal("signin was reached")
	}
}

func TestAnAdminSavesReadsAndRemovesTheirOwnOAuthClient(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github")
	h.store.clients["/github"] = signin.OAuthClient{ID: "aoc_default", Client: "github", ClientID: "Iv1.oryxa"}
	ctx := context.Background()

	view, err := h.service.OAuthClient(ctx, adminToken, "org_1", "github")
	if err != nil {
		t.Fatal(err)
	}
	if view.Saved || !view.OryxaDefault || view.RedirectURI != redirectURI || strings.Join(view.Toolkits, ",") != "github" {
		t.Fatalf("before = %+v", view)
	}

	view, err = h.service.PutOAuthClient(ctx, adminToken, "org_1", "github", OAuthClientInput{
		ClientID: "Iv1.own", ClientSecret: "own-client-secret-abcd", RefuseDefault: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	put := h.signin.puts[0]
	if put.OrganizationID != "org_1" || put.Client != "github" || put.ClientID != "Iv1.own" || !put.RefuseDefault ||
		put.ActorID != "user-3" || put.ClientSecret.Reveal() != "own-client-secret-abcd" {
		t.Fatalf("put = %+v", put)
	}
	if !view.Saved || view.ClientID != "Iv1.own" || !view.SecretSet || view.SecretHint != "abcd" || !view.RefuseDefault {
		t.Fatalf("view = %+v", view)
	}
	var saved appsv1.OAuthClientChanged
	decodeLast(t, h.store, appsv1.OAuthClientSavedDetailType, &saved)
	if saved.OrganizationID != "org_1" || saved.Client != "github" || saved.ClientID != "Iv1.own" || saved.ActorID != "user-3" {
		t.Fatalf("saved = %+v", saved)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "own-client-secret") || strings.Contains(h.allEvents(), "own-client-secret") {
		t.Fatalf("the secret leaked: %s", encoded)
	}

	// Saving the same thing again announces nothing.
	before := len(h.store.published(appsv1.OAuthClientSavedDetailType))
	if _, err := h.service.PutOAuthClient(ctx, adminToken, "org_1", "github", OAuthClientInput{ClientID: "Iv1.own", RefuseDefault: true}); err != nil {
		t.Fatal(err)
	}
	if after := len(h.store.published(appsv1.OAuthClientSavedDetailType)); after != before {
		t.Fatalf("an unchanged client was announced: %d → %d", before, after)
	}

	if err := h.service.RemoveOAuthClient(ctx, adminToken, "org_1", "github"); err != nil {
		t.Fatal(err)
	}
	var removed appsv1.OAuthClientChanged
	decodeLast(t, h.store, appsv1.OAuthClientRemovedDetailType, &removed)
	if removed.ClientID != "Iv1.own" || removed.ActorID != "user-3" {
		t.Fatalf("removed = %+v", removed)
	}
	// Removing what is not there is not an error, and announces nothing.
	if err := h.service.RemoveOAuthClient(ctx, adminToken, "org_1", "github"); err != nil {
		t.Fatal(err)
	}
	if count := len(h.store.published(appsv1.OAuthClientRemovedDetailType)); count != 1 {
		t.Fatalf("removals announced = %d", count)
	}
}

func TestAnOAuthClientFieldTheAppRefusesNamesTheField(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github")
	h.signin.putErr = &signin.Failure{Reason: signin.ReasonInvalidCredentials, Field: "client_secret", Rule: "is required for a new client"}
	_, err := h.service.PutOAuthClient(context.Background(), adminToken, "org_1", "github", OAuthClientInput{ClientID: "Iv1.own"})
	var invalid *domain.InvalidCredentials
	if !errors.As(err, &invalid) || invalid.Field != "client_secret" {
		t.Fatalf("error = %v", err)
	}
}

// An organization that brings its own Slack app has it deliver to an endpoint
// of the organization's own, signed with the app's signing secret, which the
// admin enters; entering another rotates it.
func TestAnOrganizationsOwnSlackAppGetsItsOwnEndpoint(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "slack")
	ctx := context.Background()

	view, err := h.service.OAuthClient(ctx, adminToken, "org_1", "slack")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Webhooks) != 1 || view.Webhooks[0].URL != "" || !view.Webhooks[0].SecretRequired {
		t.Fatalf("before = %+v", view.Webhooks)
	}

	view, err = h.service.PutOAuthClient(ctx, adminToken, "org_1", "slack", OAuthClientInput{
		ClientID: "12345.67890", ClientSecret: "slack-client-secret-1", WebhookSecrets: map[string]secrets.Secret{"slack": "signing-secret-0123456789"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.hooks.created) != 1 {
		t.Fatalf("created = %+v", h.hooks.created)
	}
	created := h.hooks.created[0]
	if created.Scope != integration.HookScopeOrganization || created.OrganizationID != "org_1" || created.Toolkit != "slack" ||
		created.Secret.Reveal() != "signing-secret-0123456789" {
		t.Fatalf("created = %+v", created)
	}
	webhook := view.Webhooks[0]
	if !strings.HasPrefix(webhook.URL, "https://api.oryxa.test/v1/apps/hooks/aph_") || !webhook.SecretSet {
		t.Fatalf("webhook = %+v", webhook)
	}
	encoded, _ := json.Marshal(view)
	if strings.Contains(string(encoded), "signing-secret") {
		t.Fatalf("the signing secret leaked: %s", encoded)
	}

	if _, err := h.service.PutOAuthClient(ctx, adminToken, "org_1", "slack", OAuthClientInput{
		ClientID: "12345.67890", WebhookSecrets: map[string]secrets.Secret{"slack": "signing-secret-9876543210"},
	}); err != nil {
		t.Fatal(err)
	}
	if len(h.hooks.rotated) != 1 || len(h.hooks.created) != 1 {
		t.Fatalf("rotated %v, created %d", h.hooks.rotated, len(h.hooks.created))
	}
	// Only a secret change is announced as a change too.
	if saved := h.store.published(appsv1.OAuthClientSavedDetailType); len(saved) != 2 {
		t.Fatalf("saves announced = %d", len(saved))
	}
}

func TestWebhookSecretsAreCheckedBeforeAnythingIsSaved(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "slack", "github")
	ctx := context.Background()
	for name, test := range map[string]struct {
		client string
		input  OAuthClientInput
		field  string
	}{
		"too short": {client: "slack", field: "webhook_secrets.slack",
			input: OAuthClientInput{ClientID: "1.2", ClientSecret: "x", WebhookSecrets: map[string]secrets.Secret{"slack": "short"}}},
		"an app whose secret Oryxa makes": {client: "github", field: "webhook_secrets",
			input: OAuthClientInput{ClientID: "Iv1.own", ClientSecret: "x", WebhookSecrets: map[string]secrets.Secret{"github": "a-long-enough-secret-value"}}},
		"with no client of its own": {client: "slack", field: "client_id",
			input: OAuthClientInput{RefuseDefault: true, WebhookSecrets: map[string]secrets.Secret{"slack": "a-long-enough-secret-value"}}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := h.service.PutOAuthClient(ctx, adminToken, "org_1", test.client, test.input)
			var invalid *domain.InvalidCredentials
			if !errors.As(err, &invalid) || invalid.Field != test.field {
				t.Fatalf("error = %v, want invalid %s", err, test.field)
			}
			if strings.Contains(err.Error(), "a-long-enough-secret-value") {
				t.Fatalf("the error quotes the secret: %v", err)
			}
		})
	}
	if len(h.signin.puts) != 0 || len(h.hooks.created) != 0 {
		t.Fatal("something was saved")
	}
}

// Notion sends its signing secret to the endpoint once; the organization's
// admin reveals it once, to paste back into Notion.
func TestAnOrganizationsOwnNotionAppsVerificationTokenIsRevealedOnceToItsAdmins(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "notion")
	ctx := context.Background()

	view, err := h.service.PutOAuthClient(ctx, adminToken, "org_1", "notion", OAuthClientInput{ClientID: "notion-own", ClientSecret: "notion-secret-value"})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.hooks.created) != 1 || h.hooks.created[0].Secret != "" {
		t.Fatalf("created = %+v", h.hooks.created)
	}
	if webhook := view.Webhooks[0]; webhook.URL == "" || webhook.SecretRequired || webhook.VerificationToken != VerificationAwaited {
		t.Fatalf("webhook = %+v", webhook)
	}

	if _, err := h.service.RevealVerificationToken(ctx, memberToken, "org_1", "notion"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a member = %v", err)
	}
	token, err := h.service.RevealVerificationToken(ctx, adminToken, "org_1", "notion")
	if err != nil {
		t.Fatal(err)
	}
	if token.Reveal() != "notion-verification-token-1234" || len(h.hooks.revealed) != 1 {
		t.Fatalf("token = %q, revealed %v", token.Reveal(), h.hooks.revealed)
	}
	h.hooks.revealErr = hooks.ErrAlreadyRevealed
	if _, err := h.service.RevealVerificationToken(ctx, adminToken, "org_1", "notion"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("a second reveal = %v, want conflict", err)
	}
}

func TestOnlyAnAppThatSendsATokenHasOneToReveal(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "slack")
	if _, err := h.service.RevealVerificationToken(context.Background(), adminToken, "org_1", "slack"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("slack = %v", err)
	}
	if len(h.hooks.revealed) != 0 {
		t.Fatal("hooks was asked")
	}
}

func TestTheDeploymentsOwnEndpointIsForServicesToSetUp(t *testing.T) {
	t.Parallel()
	// Registered is enough: an operator sets Oryxa's own app up before the
	// toolkit is switched on.
	h := newOryxaHarness(t)
	ctx := context.Background()

	view, err := h.service.DeploymentHook(ctx, "slack")
	if err != nil {
		t.Fatal(err)
	}
	if view.Toolkit != "slack" || !strings.HasPrefix(view.URL, "https://api.oryxa.test/v1/apps/hooks/aph_") || view.VerificationToken != "" {
		t.Fatalf("view = %+v", view)
	}
	again, err := h.service.DeploymentHook(ctx, "slack")
	if err != nil || again.URL != view.URL {
		t.Fatalf("again = %+v, %v", again, err)
	}
	notion, err := h.service.DeploymentHook(ctx, "notion")
	if err != nil || notion.VerificationToken != VerificationAwaited {
		t.Fatalf("notion = %+v, %v", notion, err)
	}
	token, err := h.service.RevealDeploymentVerificationToken(ctx, "notion")
	if err != nil || token.Reveal() != "notion-verification-token-1234" {
		t.Fatalf("token = %q, %v", token.Reveal(), err)
	}
	if _, err := h.service.DeploymentHook(ctx, "github"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("github's are per connection = %v", err)
	}
	if _, err := h.service.RevealDeploymentVerificationToken(ctx, "slack"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("slack sends none = %v", err)
	}
	if _, err := newHarness(t).service.DeploymentHook(ctx, "slack"); !errors.Is(err, domain.ErrNotConnectable) {
		t.Fatalf("where Oryxa's sign-in is not configured = %v", err)
	}
}

// Oryxa's own OAuth apps come from the deployment's configuration, and are
// saved once the upkeep runs, until every one has been.
func TestTheUpkeepSavesOryxasOwnOAuthApps(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github")
	h.service.oryxa.DefaultClients = []DefaultClient{{Client: "github", ClientID: "Iv1.oryxa", ClientSecret: "oryxa-github-client-secret"}}
	h.signin.putErr = errBoom

	h.service.maintainOryxa(context.Background())
	if len(h.signin.puts) != 1 || h.service.defaultsSaved {
		t.Fatalf("puts %d, saved %v", len(h.signin.puts), h.service.defaultsSaved)
	}
	h.signin.putErr = nil
	h.service.maintainOryxa(context.Background())
	put := h.signin.puts[1]
	if put.OrganizationID != "" || put.Client != "github" || put.ClientID != "Iv1.oryxa" ||
		put.ClientSecret.Reveal() != "oryxa-github-client-secret" || put.ActorID != defaultClientActor || put.RefuseDefault {
		t.Fatalf("put = %+v", put)
	}
	var saved appsv1.OAuthClientChanged
	decodeLast(t, h.store, appsv1.OAuthClientSavedDetailType, &saved)
	if saved.OrganizationID != "" || saved.ClientID != "Iv1.oryxa" || saved.ActorID != defaultClientActor {
		t.Fatalf("saved = %+v", saved)
	}
	h.service.maintainOryxa(context.Background())
	if len(h.signin.puts) != 2 {
		t.Fatalf("saved again after it had been: %d", len(h.signin.puts))
	}
	if strings.Contains(h.allEvents(), "oryxa-github-client-secret") {
		t.Fatal("an event carries the secret")
	}
}

func TestNewRefusesADefaultOAuthAppNothingSignsInThrough(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t)
	for name, client := range map[string]DefaultClient{
		"an unknown registration": {Client: "zendesk", ClientID: "x", ClientSecret: "y"},
		"no secret":               {Client: "github", ClientID: "x"},
	} {
		_, err := New(Dependencies{
			Store: h.store, Directory: h.directory, Composio: h.composio,
			Oryxa: &Oryxa{Store: h.store, Signin: h.signin, Hooks: h.hooks, Integrations: h.hooks.registry,
				DefaultClients: []DefaultClient{client}, RedirectURI: redirectURI},
		}, Config{PublicBaseURL: "https://api.oryxa.test"})
		if err == nil {
			t.Errorf("%s: New() accepted it", name)
		}
	}
}

// A deleted organization keeps no OAuth client anyone could sign in through,
// and no endpoint its own apps could still deliver to.
func TestADeletedOrganizationsOwnClientsAndEndpointsAreRetired(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "slack", "github")
	ctx := context.Background()
	if _, err := h.service.PutOAuthClient(ctx, adminToken, "org_1", "slack", OAuthClientInput{
		ClientID: "1.2", ClientSecret: "slack-client-secret", WebhookSecrets: map[string]secrets.Secret{"slack": "signing-secret-0123456789"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.PutOAuthClient(ctx, adminToken, "org_1", "github", OAuthClientInput{ClientID: "Iv1.own", ClientSecret: "github-secret"}); err != nil {
		t.Fatal(err)
	}
	h.oryxaConnection("apc_1", "user-1", "github", domain.ConnectionActive, integration.KindOAuth2Code)

	if err := h.service.OrganizationDeleted(ctx, authv1.OrganizationDeleted{OrganizationID: "org_1"}); err != nil {
		t.Fatal(err)
	}
	if len(h.signin.clients) != 0 {
		t.Fatalf("clients left = %v", h.signin.clients)
	}
	if len(h.hooks.deletedEndpoints) != 1 || len(h.hooks.endpoints) != 0 {
		t.Fatalf("endpoints deleted %v, left %v", h.hooks.deletedEndpoints, h.hooks.endpoints)
	}
	if got := h.store.connection("apc_1"); got.Status != domain.ConnectionRemoved {
		t.Fatalf("connection = %+v", got)
	}
}
