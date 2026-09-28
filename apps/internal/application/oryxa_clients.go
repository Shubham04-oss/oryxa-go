package application

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/hooks"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/signin"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// An organization may bring its own OAuth app for a registration (github,
// google, …) instead of signing its members in through Oryxa's, or refuse
// Oryxa's outright. Only its admins manage that, never an agent, and the
// client's secret is write-only: what comes back is whether one is set and,
// for a long one, its last four characters.
//
// An organization's own app also delivers its own webhooks, signed with its
// own secret, so apps whose deliveries are shared by the whole app (Slack,
// Notion) get an endpoint of the organization's own: its URL is shown here
// for the admin to copy into the app, and the app's signing secret is entered
// here, or for Notion revealed here once.

// OAuthClientInput is what an organization admin saves for a registration.
type OAuthClientInput struct {
	ClientID string
	// ClientSecret is write-only. A new client needs one; saving the live
	// client again without one keeps its secret.
	ClientSecret secrets.Secret
	// Fields are the registration's client-level fields. One left out keeps
	// its value; an empty one clears it.
	Fields map[string]string
	// RefuseDefault stops the organization's members signing in through
	// Oryxa's own app for this registration.
	RefuseDefault bool
	// WebhookSecrets are the organization's own app's signing secrets, by
	// toolkit, for the apps whose deliveries are signed with a secret an
	// admin enters (Slack). Write-only.
	WebhookSecrets map[string]secrets.Secret
}

// OAuthClientView is an organization's OAuth client for a registration, as
// its admins see it: never a secret, and only a hint of one.
type OAuthClientView struct {
	Client string `json:"client"`
	// Saved says the organization has saved something for the registration:
	// its own client, or only a refusal of Oryxa's.
	Saved    bool   `json:"saved"`
	ClientID string `json:"client_id,omitempty"`
	// SecretSet says a client secret is saved, and SecretHint is its last
	// four characters when it is 16 characters or longer.
	SecretSet  bool   `json:"secret_set"`
	SecretHint string `json:"secret_hint,omitempty"`
	// Fields are the client-level fields that are not secret, and
	// SecretFields the secret ones that are set, each with its hint.
	Fields       map[string]string `json:"fields"`
	SecretFields map[string]string `json:"secret_fields"`
	// RefuseDefault is the "don't use Oryxa's app" switch.
	RefuseDefault bool `json:"refuse_default"`
	// OryxaDefault says Oryxa has an app of its own for the registration,
	// which members sign in through unless the organization brings its own
	// or refuses it.
	OryxaDefault bool `json:"oryxa_default"`
	// RedirectURI is the callback to register with the organization's own
	// app.
	RedirectURI string `json:"redirect_uri,omitempty"`
	// Toolkits are the apps switched on here that sign in through the
	// registration.
	Toolkits []string `json:"toolkits"`
	// Webhooks are the endpoints the organization's own app delivers to, one
	// per app whose deliveries need one.
	Webhooks  []OrganizationWebhookView `json:"webhooks"`
	CreatedBy string                    `json:"created_by,omitempty"`
	CreatedAt *time.Time                `json:"created_at,omitempty"`
	UpdatedAt *time.Time                `json:"updated_at,omitempty"`
}

// Verification token states, for an app that sends its endpoint a token to
// paste back (Notion).
const (
	// VerificationAwaited: the app has not sent it yet.
	VerificationAwaited = "awaited"
	// VerificationReady: it arrived, and an admin may reveal it once.
	VerificationReady = "ready"
	// VerificationRevealed: it was revealed, and is never shown again.
	VerificationRevealed = "revealed"
)

// OrganizationWebhookView is one endpoint an organization's own app delivers
// to.
type OrganizationWebhookView struct {
	Toolkit string `json:"toolkit"`
	// URL is where the app delivers, to copy into its settings; empty until
	// the endpoint exists, which for an app signed with a secret the admin
	// enters is once they enter it.
	URL string `json:"url,omitempty"`
	// SecretRequired says the admin enters the app's signing secret, in
	// webhook_secrets, and SecretSet that one is saved.
	SecretRequired bool `json:"secret_required"`
	SecretSet      bool `json:"secret_set"`
	// VerificationToken is awaited, ready or revealed, for an app that sends
	// one; empty otherwise.
	VerificationToken string     `json:"verification_token,omitempty"`
	ValidatedAt       *time.Time `json:"validated_at,omitempty"`
}

// DeploymentHookView is the deployment's own endpoint for a toolkit, which
// Oryxa's own app delivers to for every organization.
type DeploymentHookView struct {
	Toolkit string `json:"toolkit"`
	// URL is what an operator puts in Oryxa's own app's settings.
	URL               string     `json:"url"`
	VerificationToken string     `json:"verification_token,omitempty"`
	ValidatedAt       *time.Time `json:"validated_at,omitempty"`
}

// OAuthClient answers an organization's OAuth client for a registration, to
// its admins.
func (s *Service) OAuthClient(ctx context.Context, credentials authclient.Credentials, organizationID, client string) (OAuthClientView, error) {
	if _, err := s.orgAdmin(ctx, credentials, organizationID); err != nil {
		return OAuthClientView{}, err
	}
	client, toolkits, err := s.registration(client)
	if err != nil {
		return OAuthClientView{}, err
	}
	saved, err := s.oryxa.Signin.GetClient(ctx, organizationID, client)
	found := true
	if errors.Is(err, signin.ErrClientNotFound) {
		found, err = false, nil
	}
	if err != nil {
		return OAuthClientView{}, err
	}
	return s.clientView(ctx, organizationID, client, toolkits, saved, found)
}

// PutOAuthClient saves an organization's own OAuth client for a registration,
// or its refusal of Oryxa's, and the signing secrets of the webhooks its app
// delivers. Anything that changed is announced; the secret never is.
func (s *Service) PutOAuthClient(ctx context.Context, credentials authclient.Credentials, organizationID, client string, input OAuthClientInput) (OAuthClientView, error) {
	caller, err := s.orgAdmin(ctx, credentials, organizationID)
	if err != nil {
		return OAuthClientView{}, err
	}
	client, toolkits, err := s.registration(client)
	if err != nil {
		return OAuthClientView{}, err
	}
	// Every webhook secret is checked before anything is saved, so a wrong
	// one leaves nothing half done.
	webhooks := s.organizationWebhookToolkits(toolkits)
	for _, toolkit := range slices.Sorted(maps.Keys(input.WebhookSecrets)) {
		webhook, ok := webhooks[toolkit]
		if !ok || webhook.Handshake == integration.HandshakeNotionVerification {
			return OAuthClientView{}, &domain.InvalidCredentials{Field: "webhook_secrets",
				Rule: "names an app whose deliveries are not signed with a secret an admin enters"}
		}
		if rule := webhookSecretRule(input.WebhookSecrets[toolkit]); rule != "" {
			return OAuthClientView{}, &domain.InvalidCredentials{Field: "webhook_secrets." + toolkit, Rule: rule}
		}
	}
	if len(input.WebhookSecrets) > 0 && strings.TrimSpace(input.ClientID) == "" {
		return OAuthClientView{}, &domain.InvalidCredentials{Field: "client_id", Rule: "is required with a webhook secret"}
	}

	saved, changed, err := s.oryxa.Signin.PutClient(ctx, signin.ClientInput{
		OrganizationID: organizationID, Client: client, ClientID: input.ClientID, ClientSecret: input.ClientSecret,
		Fields: input.Fields, RefuseDefault: input.RefuseDefault, ActorID: caller.UserID,
	})
	if err != nil {
		var failed *signin.Failure
		if errors.As(err, &failed) {
			return OAuthClientView{}, failureOf(client, err).err
		}
		return OAuthClientView{}, err
	}
	webhooksChanged := false
	if saved.ClientID != "" {
		if webhooksChanged, err = s.saveOrganizationWebhooks(ctx, organizationID, webhooks, input.WebhookSecrets); err != nil {
			return OAuthClientView{}, err
		}
	}
	if changed || webhooksChanged {
		if err := s.announceClient(ctx, appsv1.OAuthClientSavedDetailType, organizationID, client, saved.ClientID, caller.UserID); err != nil {
			return OAuthClientView{}, err
		}
		s.logger.InfoContext(ctx, "organization OAuth client saved", "organization_id", organizationID,
			"client", client, "client_changed", changed, "webhooks_changed", webhooksChanged)
	}
	return s.clientView(ctx, organizationID, client, toolkits, saved, true)
}

// RemoveOAuthClient retires an organization's own OAuth client for a
// registration, or its refusal of Oryxa's. Connections made through it keep
// refreshing and revoking through it, and its webhook endpoints stay, since
// those connections' apps still deliver to them. Removing what is not there
// is not an error.
func (s *Service) RemoveOAuthClient(ctx context.Context, credentials authclient.Credentials, organizationID, client string) error {
	caller, err := s.orgAdmin(ctx, credentials, organizationID)
	if err != nil {
		return err
	}
	client, _, err = s.registration(client)
	if err != nil {
		return err
	}
	live, err := s.oryxa.Store.LiveOAuthClient(ctx, organizationID, client)
	if errors.Is(err, signin.ErrNoClient) {
		return nil
	}
	if err != nil {
		return err
	}
	removed, err := s.oryxa.Signin.RemoveClient(ctx, organizationID, client)
	if err != nil || !removed {
		return err
	}
	s.logger.InfoContext(ctx, "organization OAuth client removed", "organization_id", organizationID, "client", client)
	return s.announceClient(ctx, appsv1.OAuthClientRemovedDetailType, organizationID, client, live.ClientID, caller.UserID)
}

// RevealVerificationToken shows an organization's admin, once, the token an
// app sent its own endpoint (Notion's verification token), to paste back
// into the app. Every later call is refused.
func (s *Service) RevealVerificationToken(ctx context.Context, credentials authclient.Credentials, organizationID, toolkit string) (secrets.Secret, error) {
	if _, err := s.orgAdmin(ctx, credentials, organizationID); err != nil {
		return "", err
	}
	toolkit, err := domain.CheckToolkit(toolkit)
	if err != nil {
		return "", err
	}
	manifest, err := s.enabled(toolkit)
	if err != nil {
		return "", err
	}
	if !capturesToken(manifest.Webhook) || !organizationEndpoints(manifest.Webhook) {
		return "", fmt.Errorf("%s's app sends no verification token: %w", toolkit, domain.ErrInvalid)
	}
	endpoint, err := s.oryxa.Hooks.OrganizationEndpoint(ctx, organizationID, toolkit)
	if err != nil {
		return "", err
	}
	token, err := s.oryxa.Hooks.RevealVerificationToken(ctx, endpoint.ID)
	if err != nil {
		return "", err
	}
	s.logger.InfoContext(ctx, "an organization's webhook verification token was revealed to its admin",
		"organization_id", organizationID, "toolkit", toolkit)
	return token, nil
}

// DeploymentHook answers the deployment's own endpoint for a toolkit, making
// it the first time, for an operator to put in Oryxa's own app's settings.
// It is served to services alone, never to members.
func (s *Service) DeploymentHook(ctx context.Context, toolkit string) (DeploymentHookView, error) {
	endpoint, url, err := s.deploymentEndpoint(ctx, toolkit)
	if err != nil {
		return DeploymentHookView{}, err
	}
	return DeploymentHookView{
		Toolkit: endpoint.Toolkit, URL: url, VerificationToken: verificationState(endpoint),
		ValidatedAt: endpoint.ValidatedAt,
	}, nil
}

// RevealDeploymentVerificationToken shows, once, the token an app sent the
// deployment's own endpoint, for an operator to paste back into Oryxa's own
// app. It is served to services alone, never to members.
func (s *Service) RevealDeploymentVerificationToken(ctx context.Context, toolkit string) (secrets.Secret, error) {
	endpoint, _, err := s.deploymentEndpoint(ctx, toolkit)
	if err != nil {
		return "", err
	}
	if !capturesToken(&endpoint.Scheme) {
		return "", fmt.Errorf("%s's app sends no verification token: %w", endpoint.Toolkit, domain.ErrInvalid)
	}
	token, err := s.oryxa.Hooks.RevealVerificationToken(ctx, endpoint.ID)
	if err != nil {
		return "", err
	}
	s.logger.InfoContext(ctx, "the deployment's webhook verification token was revealed to a service", "toolkit", endpoint.Toolkit)
	return token, nil
}

func (s *Service) deploymentEndpoint(ctx context.Context, toolkit string) (hooks.Endpoint, string, error) {
	if s.oryxa == nil {
		return hooks.Endpoint{}, "", errOryxaOff
	}
	toolkit, err := domain.CheckToolkit(toolkit)
	if err != nil {
		return hooks.Endpoint{}, "", err
	}
	if _, err := s.webhookOf(toolkit, integration.HookScopeDeployment); err != nil {
		return hooks.Endpoint{}, "", err
	}
	return s.oryxa.Hooks.DeploymentEndpoint(ctx, toolkit)
}

// orgAdmin answers the caller when they are an admin of the organization
// themselves, and not an agent acting for one.
func (s *Service) orgAdmin(ctx context.Context, credentials authclient.Credentials, organizationID string) (domain.OrgMembership, error) {
	caller, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return domain.OrgMembership{}, err
	}
	if caller.Agent {
		return domain.OrgMembership{}, fmt.Errorf("an agent cannot manage an organization's OAuth apps: %w", domain.ErrForbidden)
	}
	if !caller.Role.IsOrgAdmin() {
		return domain.OrgMembership{}, fmt.Errorf("only an organization's admins manage its OAuth apps: %w", domain.ErrForbidden)
	}
	return caller, nil
}

// registration answers a client registration and the toolkits switched on
// here that sign in through it. One that none does is not found.
func (s *Service) registration(client string) (string, []string, error) {
	if !s.on() {
		return "", nil, errOryxaOff
	}
	client = strings.ToLower(strings.TrimSpace(client))
	if _, err := domain.CheckToolkit(client); err != nil {
		return "", nil, fmt.Errorf("a client registration is a lowercase slug such as github: %w", domain.ErrInvalid)
	}
	var toolkits []string
	for _, toolkit := range s.enabledToolkits() {
		manifest, err := s.enabled(toolkit)
		if err != nil {
			continue
		}
		if slices.ContainsFunc(manifest.Auth.Methods, func(method integration.Method) bool {
			return method.OAuth2 != nil && method.OAuth2.Client == client
		}) {
			toolkits = append(toolkits, toolkit)
		}
	}
	if len(toolkits) == 0 {
		return "", nil, fmt.Errorf("no app switched on for Oryxa's own sign-in signs in through %s: %w", client, domain.ErrNotFound)
	}
	return client, toolkits, nil
}

// organizationWebhookToolkits answers, of toolkits, those whose deliveries an
// organization's own app sends to an endpoint of the organization's.
func (s *Service) organizationWebhookToolkits(toolkits []string) map[string]*integration.Webhook {
	webhooks := map[string]*integration.Webhook{}
	for _, toolkit := range toolkits {
		manifest, err := s.enabled(toolkit)
		if err != nil || !organizationEndpoints(manifest.Webhook) {
			continue
		}
		webhooks[toolkit] = manifest.Webhook
	}
	return webhooks
}

// saveOrganizationWebhooks makes, or gives a new secret to, the endpoints an
// organization's own app delivers to: one whose app sends its own token is
// made at once, and one signed with a secret the admin enters once they
// enter it. It says whether anything changed.
func (s *Service) saveOrganizationWebhooks(ctx context.Context, organizationID string, webhooks map[string]*integration.Webhook, entered map[string]secrets.Secret) (bool, error) {
	changed := false
	for _, toolkit := range slices.Sorted(maps.Keys(webhooks)) {
		captures := capturesToken(webhooks[toolkit])
		secret, given := entered[toolkit]
		endpoint, err := s.oryxa.Hooks.OrganizationEndpoint(ctx, organizationID, toolkit)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			if !captures && !given {
				continue
			}
			input := hooks.CreateInput{Toolkit: toolkit, Scope: integration.HookScopeOrganization, OrganizationID: organizationID}
			if given {
				input.Secret = secret
			}
			if _, err := s.oryxa.Hooks.Create(ctx, input); err != nil {
				return changed, err
			}
			changed = true
		case err != nil:
			return changed, err
		case given:
			if _, err := s.oryxa.Hooks.Rotate(ctx, endpoint.ID, secret); err != nil {
				return changed, err
			}
			changed = true
		}
	}
	return changed, nil
}

// clientView builds what an admin sees of a registration.
func (s *Service) clientView(ctx context.Context, organizationID, client string, toolkits []string, saved signin.ClientView, found bool) (OAuthClientView, error) {
	view := OAuthClientView{
		Client: client, Saved: found, Fields: map[string]string{}, SecretFields: map[string]string{},
		Toolkits: slices.Clone(toolkits), Webhooks: []OrganizationWebhookView{},
	}
	if found {
		view.ClientID, view.SecretSet, view.SecretHint = saved.ClientID, saved.SecretSet, saved.SecretHint
		view.RefuseDefault, view.CreatedBy = saved.RefuseDefault, saved.CreatedBy
		maps.Copy(view.Fields, saved.Fields)
		maps.Copy(view.SecretFields, saved.SecretFields)
		createdAt, updatedAt := saved.CreatedAt, saved.UpdatedAt
		view.CreatedAt, view.UpdatedAt = &createdAt, &updatedAt
	}
	oryxaDefault, err := s.oryxa.Store.LiveOAuthClient(ctx, "", client)
	switch {
	case err == nil:
		view.OryxaDefault = oryxaDefault.ClientID != ""
	case !errors.Is(err, signin.ErrNoClient):
		return OAuthClientView{}, err
	}
	for _, toolkit := range toolkits {
		if manifest, err := s.enabled(toolkit); err == nil && slices.ContainsFunc(manifest.Auth.Methods, func(method integration.Method) bool {
			return method.Kind == integration.KindOAuth2Code && method.OAuth2 != nil && method.OAuth2.Client == client
		}) {
			view.RedirectURI = s.oryxa.RedirectURI
		}
	}
	webhooks := s.organizationWebhookToolkits(toolkits)
	for _, toolkit := range slices.Sorted(maps.Keys(webhooks)) {
		captures := capturesToken(webhooks[toolkit])
		webhook := OrganizationWebhookView{Toolkit: toolkit, SecretRequired: !captures}
		endpoint, err := s.oryxa.Hooks.OrganizationEndpoint(ctx, organizationID, toolkit)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			if captures {
				webhook.VerificationToken = VerificationAwaited
			}
		case err != nil:
			return OAuthClientView{}, err
		default:
			webhook.URL = s.oryxa.Hooks.URL(endpoint.ID)
			webhook.SecretSet = endpoint.Secret.Current != nil
			webhook.VerificationToken = verificationState(endpoint)
			webhook.ValidatedAt = endpoint.ValidatedAt
		}
		view.Webhooks = append(view.Webhooks, webhook)
	}
	return view, nil
}

// announceClient writes an OAuth client event.
func (s *Service) announceClient(ctx context.Context, detailType, organizationID, client, clientID, actorID string) error {
	event, err := s.event(detailType, appsv1.OAuthClientChanged{
		OrganizationID: organizationID, Client: client, ClientID: clientID, ActorID: actorID, ChangedAt: s.now().UTC(),
	})
	if err != nil {
		return err
	}
	return s.oryxa.Store.RecordEvents(ctx, event)
}

// capturesToken says an app sends its endpoint its own verification token,
// rather than signing with a secret Oryxa or an admin chose.
func capturesToken(webhook *integration.Webhook) bool {
	return webhook != nil && webhook.Handshake == integration.HandshakeNotionVerification
}

// verificationState says where an endpoint's captured token is, or "" for an
// endpoint whose app sends none.
func verificationState(endpoint hooks.Endpoint) string {
	switch {
	case !capturesToken(&endpoint.Scheme):
		return ""
	case endpoint.Secret.Current == nil:
		return VerificationAwaited
	case endpoint.Secret.RevealedAt != nil:
		return VerificationRevealed
	}
	return VerificationReady
}

// Bounds on a webhook secret an admin enters, as hooks holds them: shorter is
// guessable, longer a mistake.
const (
	minWebhookSecret = 16
	maxWebhookSecret = 1024
)

// webhookSecretRule answers the rule an entered webhook secret breaks, or "".
// It never quotes the secret.
func webhookSecretRule(secret secrets.Secret) string {
	value := strings.TrimSpace(secret.Reveal())
	if len(value) < minWebhookSecret || len(value) > maxWebhookSecret {
		return fmt.Sprintf("must be %d to %d characters", minWebhookSecret, maxWebhookSecret)
	}
	for i := 0; i < len(value); i++ {
		if value[i] <= ' ' || value[i] > '~' {
			return "must be printable characters without spaces"
		}
	}
	return ""
}
