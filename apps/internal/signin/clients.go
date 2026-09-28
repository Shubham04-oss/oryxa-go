package signin

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// ErrClientNotFound means an organization has no OAuth client saved for a
// registration.
var ErrClientNotFound = fmt.Errorf("signin: no OAuth client is saved for that registration: %w", domain.ErrNotFound)

// maxClientIDLength bounds an OAuth client's id.
const maxClientIDLength = 512

// ClientInput is an admin saving an organization's own OAuth client for a
// registration, or, with OrganizationID "", Oryxa's default.
type ClientInput struct {
	OrganizationID string
	// Client is the registration, shared across toolkits: github, google.
	Client   string
	ClientID string
	// ClientSecret is write-only. A new client needs one; saving the client
	// already live with none keeps its secret.
	ClientSecret secrets.Secret
	// Fields are the registration's client-level fields. One left out keeps
	// its value; an empty one clears it, unless it is required.
	Fields map[string]string
	// RefuseDefault stops members signing in through Oryxa's default client
	// for this registration. With no ClientID, the organization brings no
	// client of its own either, and nobody signs in until it does.
	RefuseDefault bool
	// ActorID is who saved it.
	ActorID string
}

// ClientView is an OAuth client as an admin may see it: never a secret, and
// only a hint of one.
type ClientView struct {
	ID             string
	OrganizationID string
	Client         string
	ClientID       string
	// SecretSet says a secret is saved, and SecretHint is its last four
	// characters when it is 16 characters or longer.
	SecretSet  bool
	SecretHint string
	// Fields are the client-level fields that are not secret.
	Fields map[string]string
	// SecretFields are the secret client-level fields that are set, each
	// with its hint.
	SecretFields  map[string]string
	RefuseDefault bool
	CreatedBy     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// PutClient saves an OAuth client for a registration. The same client ID as
// the live one is updated in place, so the connections made through it
// refresh with what was saved. Another client ID makes a new live row, and
// the replaced one stays for its connections to refresh and revoke through.
// It answers whether anything changed.
func (s *Service) PutClient(ctx context.Context, input ClientInput) (ClientView, bool, error) {
	definitions, known := s.registration(input.Client)
	if !known {
		return ClientView{}, false, fmt.Errorf("signin: no integration signs in through that client: %w", domain.ErrNotFound)
	}
	if input.OrganizationID == "" && input.RefuseDefault {
		return ClientView{}, false, fmt.Errorf("signin: Oryxa's default client cannot refuse itself: %w", domain.ErrInvalid)
	}
	clientID := strings.TrimSpace(input.ClientID)
	secret := secrets.Secret(strings.TrimSpace(input.ClientSecret.Reveal()))
	switch {
	case clientID == "" && !input.RefuseDefault:
		return ClientView{}, false, invalidField("client_id", "is required")
	case clientID == "" && (secret != "" || len(input.Fields) > 0):
		return ClientView{}, false, invalidField("client_id", "is required with a secret or fields")
	case clientID != "" && (checkValue(integration.Validation{MaxLen: maxClientIDLength}, clientID) != "" ||
		strings.ContainsFunc(clientID, unicode.IsSpace)):
		return ClientView{}, false, invalidField("client_id", "must be one word of at most 512 characters")
	}
	if secret != "" {
		if rule := checkValue(integration.Validation{}, secret.Reveal()); rule != "" {
			return ClientView{}, false, invalidField("client_secret", rule)
		}
	}
	settings, secretFields, cleared, err := clientFields(definitions, input.Fields)
	if err != nil {
		return ClientView{}, false, err
	}

	current, err := s.store.LiveOAuthClient(ctx, input.OrganizationID, input.Client)
	switch {
	case err == nil && clientID != "" && current.ClientID == clientID:
		return s.updateClient(ctx, current, input, secret, settings, secretFields, cleared, definitions)
	case err == nil && clientID == "" && current.ClientID == "":
		// Refusing Oryxa's default again, with no client of its own again.
		view, err := s.clientView(ctx, current)
		return view, false, err
	case err != nil && !errors.Is(err, ErrNoClient):
		return ClientView{}, false, err
	}

	if clientID != "" && secret == "" {
		return ClientView{}, false, invalidField("client_secret", "is required for a new client")
	}
	if clientID != "" {
		for _, field := range definitions {
			_, isSecret := secretFields[field.Name]
			if field.Validate.Required && settings[field.Name] == "" && !isSecret {
				return ClientView{}, false, invalidField(field.Name, "is required")
			}
		}
	}
	id, err := newID("aoc")
	if err != nil {
		return ClientView{}, false, err
	}
	now := s.now().UTC()
	row := OAuthClient{
		ID: id, OrganizationID: input.OrganizationID, Client: input.Client, ClientID: clientID,
		Settings: settings, Status: ClientActive, CreatedBy: input.ActorID, CreatedAt: now, UpdatedAt: now,
	}
	if input.RefuseDefault {
		row.Settings[SettingRefuseDefault] = "true"
	}
	if secret != "" {
		sealed, err := s.sealEach(ctx, func(name string) secrets.Binding { return clientBinding(id, name) },
			map[string]secrets.Secret{secretClientField: secret})
		if err != nil {
			return ClientView{}, false, err
		}
		envelope := sealed[secretClientField]
		row.SealedSecret = &envelope
	}
	if row.SealedFields, err = s.sealEach(ctx, func(name string) secrets.Binding { return clientBinding(id, name) }, secretFields); err != nil {
		return ClientView{}, false, err
	}
	if err := s.store.InsertOAuthClient(ctx, row); err != nil {
		return ClientView{}, false, err
	}
	view, err := s.clientView(ctx, row)
	return view, true, err
}

// updateClient saves over the live client with the same client ID.
func (s *Service) updateClient(ctx context.Context, current OAuthClient, input ClientInput, secret secrets.Secret,
	settings map[string]string, secretFields map[string]secrets.Secret, cleared []string, definitions []integration.Field) (ClientView, bool, error) {
	next := current
	next.Settings = maps.Clone(current.Settings)
	if next.Settings == nil {
		next.Settings = map[string]string{}
	}
	next.SealedFields = maps.Clone(current.SealedFields)
	if next.SealedFields == nil {
		next.SealedFields = map[string]secrets.SealedData{}
	}
	changed := false
	bind := func(name string) secrets.Binding { return clientBinding(current.ID, name) }

	if secret != "" {
		same := false
		if current.SealedSecret != nil {
			existing, err := s.clientSecret(ctx, current)
			if err != nil {
				return ClientView{}, false, err
			}
			same = subtle.ConstantTimeCompare([]byte(existing.Reveal()), []byte(secret.Reveal())) == 1
		}
		if !same {
			sealed, err := s.sealEach(ctx, bind, map[string]secrets.Secret{secretClientField: secret})
			if err != nil {
				return ClientView{}, false, err
			}
			envelope := sealed[secretClientField]
			next.SealedSecret = &envelope
			changed = true
		}
	}
	for name, value := range settings {
		if next.Settings[name] != value {
			next.Settings[name] = value
			changed = true
		}
	}
	for name, value := range secretFields {
		if sealed, ok := current.SealedFields[name]; ok {
			existing, err := s.open(ctx, bind(name), sealed)
			if err != nil {
				return ClientView{}, false, err
			}
			if subtle.ConstantTimeCompare([]byte(existing.Reveal()), []byte(value.Reveal())) == 1 {
				continue
			}
		}
		sealed, err := s.sealEach(ctx, bind, map[string]secrets.Secret{name: value})
		if err != nil {
			return ClientView{}, false, err
		}
		next.SealedFields[name] = sealed[name]
		changed = true
	}
	for _, name := range cleared {
		_, hadSetting := next.Settings[name]
		_, hadSealed := next.SealedFields[name]
		if hadSetting || hadSealed {
			delete(next.Settings, name)
			delete(next.SealedFields, name)
			changed = true
		}
	}
	for _, field := range definitions {
		_, sealedSet := next.SealedFields[field.Name]
		if field.Validate.Required && next.Settings[field.Name] == "" && !sealedSet {
			return ClientView{}, false, invalidField(field.Name, "is required")
		}
	}
	if refuses := next.Settings[SettingRefuseDefault] == "true"; refuses != input.RefuseDefault {
		if input.RefuseDefault {
			next.Settings[SettingRefuseDefault] = "true"
		} else {
			delete(next.Settings, SettingRefuseDefault)
		}
		changed = true
	}
	if !changed {
		view, err := s.clientView(ctx, current)
		return view, false, err
	}
	next.UpdatedAt = s.now().UTC()
	updated, err := s.store.UpdateOAuthClient(ctx, next)
	if err != nil {
		return ClientView{}, false, err
	}
	if !updated {
		return ClientView{}, false, fmt.Errorf("signin: the client was replaced or removed meanwhile: %w", domain.ErrConflict)
	}
	view, err := s.clientView(ctx, next)
	return view, true, err
}

// GetClient answers an organization's live OAuth client for a registration,
// or Oryxa's default with organizationID "".
func (s *Service) GetClient(ctx context.Context, organizationID, client string) (ClientView, error) {
	row, err := s.store.LiveOAuthClient(ctx, organizationID, client)
	if errors.Is(err, ErrNoClient) {
		return ClientView{}, ErrClientNotFound
	}
	if err != nil {
		return ClientView{}, err
	}
	return s.clientView(ctx, row)
}

// RemoveClient retires an organization's live OAuth client for a
// registration. Connections made through it keep refreshing and revoking
// through it. It answers false when none was live.
func (s *Service) RemoveClient(ctx context.Context, organizationID, client string) (bool, error) {
	return s.store.RemoveOAuthClient(ctx, organizationID, client, s.now().UTC())
}

func (s *Service) clientView(ctx context.Context, row OAuthClient) (ClientView, error) {
	view := ClientView{
		ID: row.ID, OrganizationID: row.OrganizationID, Client: row.Client, ClientID: row.ClientID,
		Fields: map[string]string{}, SecretFields: map[string]string{},
		RefuseDefault: row.Settings[SettingRefuseDefault] == "true",
		CreatedBy:     row.CreatedBy, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	for name, value := range row.Settings {
		if fieldNamePattern.MatchString(name) {
			view.Fields[name] = value
		}
	}
	if row.SealedSecret != nil {
		secret, err := s.clientSecret(ctx, row)
		if err != nil {
			return ClientView{}, err
		}
		view.SecretSet, view.SecretHint = true, hint(secret)
	}
	for name, sealed := range row.SealedFields {
		value, err := s.open(ctx, clientBinding(row.ID, name), sealed)
		if err != nil {
			return ClientView{}, err
		}
		view.SecretFields[name] = hint(value)
	}
	return view, nil
}

// registration answers the client-level fields of every method that signs
// in through a registration, and false when no registered manifest names
// it.
func (s *Service) registration(client string) ([]integration.Field, bool) {
	var fields []integration.Field
	known := false
	for _, toolkit := range s.manifests.Toolkits() {
		registered, ok := s.manifests.Lookup(toolkit)
		if !ok {
			continue
		}
		for _, method := range registered.Manifest().Auth.Methods {
			if method.OAuth2 == nil || method.OAuth2.Client == "" || method.OAuth2.Client != client {
				continue
			}
			known = true
			for _, field := range method.Fields {
				if field.Level == integration.LevelClient &&
					!slices.ContainsFunc(fields, func(seen integration.Field) bool { return seen.Name == field.Name }) {
					fields = append(fields, field)
				}
			}
		}
	}
	return fields, known
}

// clientFields checks what an admin typed for a registration's client-level
// fields. It answers the values that are not secret, the secret ones, and
// the fields to clear, apart.
func clientFields(definitions []integration.Field, typed map[string]string) (map[string]string, map[string]secrets.Secret, []string, error) {
	settings := map[string]string{}
	secretValues := map[string]secrets.Secret{}
	var cleared []string
	for _, name := range slices.Sorted(maps.Keys(typed)) {
		index := slices.IndexFunc(definitions, func(field integration.Field) bool { return field.Name == name })
		if index < 0 {
			if !fieldNamePattern.MatchString(name) {
				return nil, nil, nil, invalidField("", "names a field this client does not take")
			}
			return nil, nil, nil, invalidField(name, "is not a field this client takes")
		}
		field := definitions[index]
		value := strings.TrimSpace(typed[name])
		if value == "" {
			if field.Validate.Required {
				return nil, nil, nil, invalidField(name, "is required")
			}
			cleared = append(cleared, name)
			continue
		}
		if rule := checkValue(field.Validate, value); rule != "" {
			return nil, nil, nil, invalidField(name, rule)
		}
		if field.Secret {
			secretValues[name] = secrets.Secret(value)
		} else {
			settings[name] = value
		}
	}
	return settings, secretValues, cleared, nil
}

// resolveClient answers the OAuth client a member of an organization signs
// in through: the organization's own, or Oryxa's default unless the
// organization refused it.
func (s *Service) resolveClient(ctx context.Context, organizationID, registration string) (OAuthClient, error) {
	own, err := s.store.LiveOAuthClient(ctx, organizationID, registration)
	switch {
	case err == nil && own.ClientID != "":
		return own, nil
	case err == nil && own.Settings[SettingRefuseDefault] == "true":
		return OAuthClient{}, fmt.Errorf("%w: the organization refused Oryxa's and has none of its own", ErrNoClient)
	case err != nil && !errors.Is(err, ErrNoClient):
		return OAuthClient{}, err
	case organizationID == "":
		return OAuthClient{}, ErrNoClient
	}
	fallback, err := s.store.LiveOAuthClient(ctx, "", registration)
	if err != nil {
		return OAuthClient{}, err
	}
	if fallback.ClientID == "" {
		return OAuthClient{}, ErrNoClient
	}
	return fallback, nil
}

// requireClientFields refuses a client that lacks a required client-level
// field of method.
func requireClientFields(method integration.Method, client *OAuthClient) error {
	for _, field := range method.Fields {
		if field.Level != integration.LevelClient || !field.Validate.Required {
			continue
		}
		set := false
		if client != nil {
			_, sealed := client.SealedFields[field.Name]
			set = client.Settings[field.Name] != "" || sealed
		}
		if !set {
			return fmt.Errorf("%w: its OAuth client has no %s, which the app needs", ErrNoClient, field.Name)
		}
	}
	return nil
}

// hasClientFields says whether a method declares any client-level field.
func hasClientFields(method integration.Method) bool {
	return slices.ContainsFunc(method.Fields, func(field integration.Field) bool {
		return field.Level == integration.LevelClient
	})
}

// clientValues answers a client's values for a method's client-level fields
// that are not secret.
func clientValues(method integration.Method, client *OAuthClient) map[string]string {
	values := map[string]string{}
	if client == nil {
		return values
	}
	for _, field := range method.Fields {
		if field.Level == integration.LevelClient && !field.Secret && client.Settings[field.Name] != "" {
			values[field.Name] = client.Settings[field.Name]
		}
	}
	return values
}

// clientSecret opens a client's secret, or answers "" for a client with
// none.
func (s *Service) clientSecret(ctx context.Context, client OAuthClient) (secrets.Secret, error) {
	if client.SealedSecret == nil {
		return "", nil
	}
	return s.open(ctx, clientBinding(client.ID, secretClientField), *client.SealedSecret)
}

// clientFieldSecrets opens a client's secret client-level fields that a
// method declares.
func (s *Service) clientFieldSecrets(ctx context.Context, method integration.Method, client *OAuthClient) (map[string]secrets.Secret, error) {
	opened := map[string]secrets.Secret{}
	if client == nil {
		return opened, nil
	}
	for _, field := range method.Fields {
		if field.Level != integration.LevelClient || !field.Secret {
			continue
		}
		sealed, ok := client.SealedFields[field.Name]
		if !ok {
			continue
		}
		value, err := s.open(ctx, clientBinding(client.ID, field.Name), sealed)
		if err != nil {
			return nil, err
		}
		opened[field.Name] = value
	}
	return opened, nil
}
