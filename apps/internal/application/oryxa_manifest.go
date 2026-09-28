package application

import (
	"fmt"
	"slices"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
)

// IntegrationView is a toolkit Oryxa's own sign-in is switched on for, as the
// web app learns which apps to connect through it.
type IntegrationView struct {
	Toolkit string `json:"toolkit"`
	Name    string `json:"name"`
	// Kinds are the ways a member signs in to it, the default first.
	Kinds []string `json:"kinds"`
}

// Integrations lists the toolkits Oryxa's own sign-in is switched on for, in
// order. It is empty while it is off.
func (s *Service) Integrations() []IntegrationView {
	views := []IntegrationView{}
	if !s.on() {
		return views
	}
	for _, toolkit := range s.enabledToolkits() {
		manifest, err := s.enabled(toolkit)
		if err != nil {
			continue
		}
		view := IntegrationView{Toolkit: toolkit, Name: manifest.Name, Kinds: []string{}}
		for _, method := range manifest.Auth.Methods {
			view.Kinds = append(view.Kinds, method.Kind)
		}
		views = append(views, view)
	}
	return views
}

// ManifestView is what the web app renders a toolkit's sign-in from: the
// ways to sign in, the fields each takes, and what an admin copies into
// their own OAuth app. It never carries a secret, nor where a value is
// placed on a request.
type ManifestView struct {
	Toolkit string       `json:"toolkit"`
	Name    string       `json:"name"`
	Version int          `json:"version"`
	Methods []MethodView `json:"methods"`
	// RedirectURI is the one callback to register with an OAuth app, when a
	// method signs in with OAuth.
	RedirectURI string       `json:"redirect_uri,omitempty"`
	Webhook     *WebhookView `json:"webhook,omitempty"`
}

// MethodView is one way to sign in.
type MethodView struct {
	Kind string `json:"kind"`
	// Client is the OAuth client registration the method signs in through,
	// shared across toolkits (github, google): the one an organization's own
	// app is saved under.
	Client string   `json:"client,omitempty"`
	Scopes []string `json:"scopes,omitempty"`
	// Fields are the kind's standard fields (token; username and password;
	// client_id and client_secret) and the manifest's own. A member types
	// the connection-level ones, an admin the client-level ones with their
	// OAuth client, and discovered ones come from the app.
	Fields []FieldView `json:"fields"`
}

// FieldView is one field, as a form asks for it.
type FieldView struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Help  string `json:"help,omitempty"`
	// Secret fields are write-only: masked as they are typed, and never
	// answered back.
	Secret bool `json:"secret"`
	// Level is connection (the member types it), client (an admin, with the
	// organization's OAuth client) or discovered (read from the app).
	Level string `json:"level"`
	// Standard says the field is the credential kind's own rather than one
	// the manifest declares.
	Standard   bool           `json:"standard,omitempty"`
	Validation ValidationView `json:"validation"`
}

// ValidationView is what a field's value must be.
type ValidationView struct {
	Required bool `json:"required"`
	// Format is hostname, https_url, uuid or email.
	Format string `json:"format,omitempty"`
	// Pattern is RE2, matched against the whole value.
	Pattern   string   `json:"pattern,omitempty"`
	Enum      []string `json:"enum,omitempty"`
	MaxLength int      `json:"max_length,omitempty"`
}

// WebhookView says what an app's deliveries need of an admin.
type WebhookView struct {
	// Scope is whose endpoint the app delivers to: connection (one Oryxa
	// makes for each subscription), organization or deployment (Oryxa's own
	// app, for everyone).
	Scope string `json:"scope"`
	// OrganizationEndpoint says an organization that brings its own app has
	// it deliver to an endpoint of the organization's own, whose URL the
	// organization's OAuth client shows for an admin to copy into the app.
	OrganizationEndpoint bool `json:"organization_endpoint"`
	// OrganizationSecret says the admin enters their own app's signing
	// secret with its OAuth client, for that endpoint.
	OrganizationSecret bool `json:"organization_secret"`
	// VerificationToken says the app sends the endpoint a verification token
	// once, which an admin reveals once and pastes back into the app.
	VerificationToken bool `json:"verification_token"`
}

// maxStandardLength is how long a standard field's value may be, as signin
// checks it.
const maxStandardLength = 4096

// standardFields are each kind's own fields, which a manifest never
// declares.
var standardFields = map[string][]FieldView{
	integration.KindAPIToken: {
		{Name: "token", Label: "Token", Secret: true},
	},
	integration.KindBasic: {
		{Name: "username", Label: "Username"},
		{Name: "password", Label: "Password", Secret: true},
	},
	integration.KindOAuth2ClientCredentials: {
		{Name: "client_id", Label: "Client ID"},
		{Name: "client_secret", Label: "Client secret", Secret: true},
	},
}

// Manifest answers a toolkit's sign-in as the web app renders it, for a
// toolkit Oryxa's own sign-in is switched on for.
func (s *Service) Manifest(toolkit string) (ManifestView, error) {
	toolkit, err := domain.CheckToolkit(toolkit)
	if err != nil {
		return ManifestView{}, err
	}
	manifest, err := s.enabled(toolkit)
	if err != nil {
		return ManifestView{}, err
	}
	view := ManifestView{Toolkit: manifest.Toolkit, Name: manifest.Name, Version: manifest.Version, Methods: []MethodView{}}
	for _, method := range manifest.Auth.Methods {
		methodView := MethodView{Kind: method.Kind, Fields: []FieldView{}}
		if method.OAuth2 != nil {
			methodView.Client = method.OAuth2.Client
			methodView.Scopes = slices.Clone(method.OAuth2.Scopes)
			if method.Kind == integration.KindOAuth2Code {
				view.RedirectURI = s.oryxa.RedirectURI
			}
		}
		for _, standard := range standardFields[method.Kind] {
			standard.Level, standard.Standard = integration.LevelConnection, true
			standard.Validation = ValidationView{Required: true, MaxLength: maxStandardLength}
			methodView.Fields = append(methodView.Fields, standard)
		}
		for _, field := range method.Fields {
			methodView.Fields = append(methodView.Fields, FieldView{
				Name: field.Name, Label: field.Label, Help: field.Help, Secret: field.Secret, Level: field.Level,
				Validation: ValidationView{
					Required: field.Validate.Required, Format: field.Validate.Format, Pattern: field.Validate.Pattern,
					Enum: slices.Clone(field.Validate.Enum), MaxLength: field.Validate.MaxLen,
				},
			})
		}
		view.Methods = append(view.Methods, methodView)
	}
	if manifest.Webhook != nil {
		organization := organizationEndpoints(manifest.Webhook)
		captures := manifest.Webhook.Handshake == integration.HandshakeNotionVerification
		view.Webhook = &WebhookView{
			Scope: manifest.Webhook.Scope, OrganizationEndpoint: organization,
			OrganizationSecret: organization && !captures, VerificationToken: captures,
		}
	}
	return view, nil
}

// organizationEndpoints says whether an organization's own app can deliver to
// an endpoint of the organization's: any webhook that is not one per
// subscription, unless its deliveries are signed for the deployment.
func organizationEndpoints(webhook *integration.Webhook) bool {
	return webhook != nil && webhook.Scope != integration.HookScopeConnection &&
		webhook.Verify.Type != integration.VerifyJWTPush
}

// webhookOf answers a registered toolkit's webhook section, or an error when
// it has none of the scope wanted.
func (s *Service) webhookOf(toolkit, scope string) (*integration.Webhook, error) {
	registered, ok := s.oryxa.Integrations.Lookup(toolkit)
	if !ok {
		return nil, fmt.Errorf("%s is not one of Oryxa's own integrations: %w", toolkit, domain.ErrNotFound)
	}
	webhook := registered.Manifest().Webhook
	if webhook == nil || webhook.Scope != scope {
		return nil, fmt.Errorf("%s's app does not deliver to the %s: %w", toolkit, scope, domain.ErrInvalid)
	}
	return webhook, nil
}
