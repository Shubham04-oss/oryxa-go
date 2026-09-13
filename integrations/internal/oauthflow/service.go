package oauthflow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	integrationevents "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
)

var (
	ErrForbidden          = errors.New("integration permission denied")
	ErrInvalidInput       = errors.New("invalid oauth input")
	ErrProfileInactive    = errors.New("oauth application is inactive")
	ErrProviderMismatch   = errors.New("oauth callback provider mismatch")
	ErrStateExpired       = errors.New("oauth state expired")
	ErrRequiredScope      = errors.New("required oauth scope was not granted")
	ErrProviderNotAllowed = errors.New("oauth provider is not allowed")
	ErrAlreadyConnected   = errors.New("integration is already connected")
	ErrProviderDenied     = errors.New("provider authorization was denied")
)

type Definition struct {
	Provider       string
	PieceName      string
	PieceVersion   string
	RedirectURI    string
	AllowedScopes  []string
	RequiredScopes []string
	SupportsPKCE   bool
}

type Catalog interface {
	OAuthDefinition(provider string) (Definition, bool)
}

type Authorizer interface {
	CanManageOAuthApps(context.Context, string, string) error
	CanCreateConnection(context.Context, string, string) error
}

type Profiles interface {
	CreateOAuthAppWithEvent(context.Context, domain.OAuthAppProfile, domain.Event) error
	OAuthApp(context.Context, string, string) (domain.OAuthAppProfile, error)
	UpdateOAuthAppCredentialsWithEvent(context.Context, domain.OAuthAppProfile, domain.Event) error
	DisableOAuthAppWithEvent(context.Context, string, string, time.Time, domain.Event) error
}

type States interface {
	CreateOAuthState(context.Context, domain.OAuthAuthorizationState) error
	ConsumeOAuthState(context.Context, [32]byte, time.Time) (domain.OAuthAuthorizationState, error)
}

type Connections interface {
	CreateConnectionWithEvent(context.Context, domain.Connection, domain.Event) error
	ActiveConnection(context.Context, string, string, string) (domain.Connection, bool, error)
	ListMemberIntegrations(context.Context, string, string) ([]domain.MemberIntegration, error)
	ConnectionForExecution(context.Context, string, string) (domain.Connection, error)
	DisconnectConnectionWithEvent(context.Context, string, string, string, time.Time, domain.Event) error
}

type SecretBox interface {
	Seal(context.Context, secrets.Binding, []byte) (domain.SealedData, error)
	Open(context.Context, secrets.Binding, domain.SealedData) ([]byte, error)
}

type ReturnURLPolicy interface {
	Allows(string) bool
}

type ProviderRegistry interface {
	Provider(string) (Provider, bool)
}

type StaticProviders map[string]Provider

func (p StaticProviders) Provider(name string) (Provider, bool) {
	provider, ok := p[name]
	return provider, ok
}

type Provider interface {
	AuthorizationURL(AuthorizationRequest) (string, error)
	Exchange(context.Context, ExchangeRequest) (Grant, error)
	ResolveIdentity(context.Context, Grant) (domain.ProviderIdentity, error)
}

type AuthorizationRequest struct {
	ClientID      string
	RedirectURI   string
	Scopes        []string
	State         string
	PKCEChallenge string
}

type ExchangeRequest struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
	Code         string
	PKCEVerifier string
	Scopes       []string
}

type RefreshRequest struct {
	ClientID     string
	ClientSecret string
	RefreshToken string
	Scopes       []string
}

// Refresher is optional because providers such as Slack may issue durable bot
// tokens while Google and Microsoft issue expiring access tokens.
type Refresher interface {
	Refresh(context.Context, RefreshRequest) (Grant, error)
}

type RefreshError struct {
	Code      string
	Permanent bool
}

func (e RefreshError) Error() string {
	if e.Code == "" {
		return "provider credential refresh failed"
	}
	return "provider credential refresh failed: " + e.Code
}

type Grant struct {
	AccessToken  string         `json:"access_token"`
	RefreshToken string         `json:"refresh_token,omitempty"`
	TokenType    string         `json:"token_type,omitempty"`
	Expiry       time.Time      `json:"expiry,omitempty"`
	Scopes       []string       `json:"scopes"`
	Data         map[string]any `json:"data,omitempty"`
}

type RegisterBYOCRequest struct {
	OrganizationID string
	ActorID        string
	Provider       string
	ClientID       string
	ClientSecret   string
	Scopes         []string
}

type RotateBYOCRequest struct {
	OrganizationID string
	ActorID        string
	ProfileID      string
	ClientID       string
	ClientSecret   string
}

type DisableOAuthAppRequest struct {
	OrganizationID string
	ActorID        string
	ProfileID      string
}

type BeginRequest struct {
	OrganizationID string
	MemberID       string
	ProfileID      string
	ReturnURI      string
}

type BeginResult struct {
	AuthorizationURL string
	ExpiresAt        time.Time
}

type CompleteRequest struct {
	Provider      string
	State         string
	Code          string
	ProviderError string
}

type CompleteResult struct {
	Connection domain.Connection
	ReturnURI  string
}

type DisconnectRequest struct {
	OrganizationID string
	MemberID       string
	ConnectionID   string
}

type Service struct {
	Catalog     Catalog
	Authorizer  Authorizer
	Profiles    Profiles
	States      States
	Connections Connections
	Secrets     SecretBox
	Providers   ProviderRegistry
	ReturnURLs  ReturnURLPolicy
	Now         func() time.Time
	Random      io.Reader
	IDs         func(string) (string, error)
	StateTTL    time.Duration
}

func (s Service) RegisterBYOC(ctx context.Context, request RegisterBYOCRequest) (domain.OAuthAppProfile, error) {
	if request.OrganizationID == "" || request.ActorID == "" || request.Provider == "" || request.ClientID == "" || request.ClientSecret == "" {
		return domain.OAuthAppProfile{}, ErrInvalidInput
	}
	if err := s.Authorizer.CanManageOAuthApps(ctx, request.OrganizationID, request.ActorID); err != nil {
		return domain.OAuthAppProfile{}, fmt.Errorf("%w: %v", ErrForbidden, err)
	}
	definition, ok := s.Catalog.OAuthDefinition(request.Provider)
	if !ok {
		return domain.OAuthAppProfile{}, ErrProviderNotAllowed
	}
	scopes, err := selectScopes(definition, request.Scopes)
	if err != nil {
		return domain.OAuthAppProfile{}, err
	}
	profileID, err := s.newID("iap")
	if err != nil {
		return domain.OAuthAppProfile{}, err
	}
	sealed, err := s.Secrets.Seal(ctx, profileBinding(request.OrganizationID, profileID), []byte(request.ClientSecret))
	if err != nil {
		return domain.OAuthAppProfile{}, fmt.Errorf("encrypt oauth application secret: %w", err)
	}
	now := s.now()
	profile := domain.OAuthAppProfile{
		ID:                 profileID,
		OrganizationID:     request.OrganizationID,
		Provider:           definition.Provider,
		PieceName:          definition.PieceName,
		PieceVersion:       definition.PieceVersion,
		Mode:               domain.OAuthAppModeBYOC,
		ClientID:           request.ClientID,
		EncryptedSecret:    sealed,
		Scopes:             scopes,
		Status:             domain.OAuthAppStatusActive,
		CreatedBy:          request.ActorID,
		CreatedAt:          now,
		UpdatedAt:          now,
		CredentialRevision: 1,
	}
	eventID, err := s.newID("evt")
	if err != nil {
		return domain.OAuthAppProfile{}, err
	}
	event := domain.Event{EventID: eventID, Source: integrationevents.Source,
		DetailType: integrationevents.OAuthAppRegisteredDetailType, Version: 1,
		OrganizationID: profile.OrganizationID, OccurredAt: now,
		Data: integrationevents.OAuthAppRegistered{ProfileID: profile.ID, Provider: profile.Provider,
			Mode: string(profile.Mode), Scopes: append([]string(nil), profile.Scopes...),
			RegisteredBy: profile.CreatedBy, RegisteredAt: now}}
	if err := s.Profiles.CreateOAuthAppWithEvent(ctx, profile, event); err != nil {
		return domain.OAuthAppProfile{}, fmt.Errorf("create oauth application: %w", err)
	}
	return profile, nil
}

func (s Service) RotateBYOC(ctx context.Context, request RotateBYOCRequest) (domain.OAuthAppProfile, error) {
	if request.OrganizationID == "" || request.ActorID == "" || request.ProfileID == "" || request.ClientID == "" || request.ClientSecret == "" {
		return domain.OAuthAppProfile{}, ErrInvalidInput
	}
	if err := s.Authorizer.CanManageOAuthApps(ctx, request.OrganizationID, request.ActorID); err != nil {
		return domain.OAuthAppProfile{}, fmt.Errorf("%w: %v", ErrForbidden, err)
	}
	profile, err := s.Profiles.OAuthApp(ctx, request.OrganizationID, request.ProfileID)
	if err != nil {
		return domain.OAuthAppProfile{}, err
	}
	if profile.Mode != domain.OAuthAppModeBYOC || profile.Status != domain.OAuthAppStatusActive {
		return domain.OAuthAppProfile{}, ErrProfileInactive
	}
	sealed, err := s.Secrets.Seal(ctx, profileBinding(profile.OrganizationID, profile.ID), []byte(request.ClientSecret))
	if err != nil {
		return domain.OAuthAppProfile{}, fmt.Errorf("encrypt rotated oauth application secret: %w", err)
	}
	now := s.now()
	profile.ClientID = request.ClientID
	profile.EncryptedSecret = sealed
	profile.CredentialRevision++
	profile.UpdatedAt = now
	eventID, err := s.newID("evt")
	if err != nil {
		return domain.OAuthAppProfile{}, err
	}
	event := domain.Event{EventID: eventID, Source: integrationevents.Source,
		DetailType: integrationevents.OAuthAppCredentialsRotatedDetailType, Version: 1,
		OrganizationID: profile.OrganizationID, OccurredAt: now,
		Data: integrationevents.OAuthAppCredentialsRotated{ProfileID: profile.ID, Provider: profile.Provider,
			CredentialRevision: profile.CredentialRevision, RotatedBy: request.ActorID, RotatedAt: now}}
	if err := s.Profiles.UpdateOAuthAppCredentialsWithEvent(ctx, profile, event); err != nil {
		return domain.OAuthAppProfile{}, fmt.Errorf("rotate oauth application: %w", err)
	}
	return profile, nil
}

func (s Service) DisableOAuthApp(ctx context.Context, request DisableOAuthAppRequest) error {
	if request.OrganizationID == "" || request.ActorID == "" || request.ProfileID == "" {
		return ErrInvalidInput
	}
	if err := s.Authorizer.CanManageOAuthApps(ctx, request.OrganizationID, request.ActorID); err != nil {
		return fmt.Errorf("%w: %v", ErrForbidden, err)
	}
	profile, err := s.Profiles.OAuthApp(ctx, request.OrganizationID, request.ProfileID)
	if err != nil {
		return err
	}
	if profile.Status != domain.OAuthAppStatusActive {
		return ErrProfileInactive
	}
	now := s.now()
	eventID, err := s.newID("evt")
	if err != nil {
		return err
	}
	event := domain.Event{EventID: eventID, Source: integrationevents.Source,
		DetailType: integrationevents.OAuthAppDisabledDetailType, Version: 1,
		OrganizationID: profile.OrganizationID, OccurredAt: now,
		Data: integrationevents.OAuthAppDisabled{ProfileID: profile.ID, Provider: profile.Provider, DisabledBy: request.ActorID, DisabledAt: now}}
	return s.Profiles.DisableOAuthAppWithEvent(ctx, request.OrganizationID, request.ProfileID, now, event)
}

func (s Service) Begin(ctx context.Context, request BeginRequest) (BeginResult, error) {
	if request.OrganizationID == "" || request.MemberID == "" || request.ProfileID == "" || request.ReturnURI == "" {
		return BeginResult{}, ErrInvalidInput
	}
	if err := s.Authorizer.CanCreateConnection(ctx, request.OrganizationID, request.MemberID); err != nil {
		return BeginResult{}, fmt.Errorf("%w: %v", ErrForbidden, err)
	}
	if s.ReturnURLs == nil || !s.ReturnURLs.Allows(request.ReturnURI) {
		return BeginResult{}, fmt.Errorf("%w: return URI is not allowed", ErrInvalidInput)
	}
	profile, err := s.Profiles.OAuthApp(ctx, request.OrganizationID, request.ProfileID)
	if err != nil {
		return BeginResult{}, fmt.Errorf("load oauth application: %w", err)
	}
	if profile.OrganizationID != request.OrganizationID || profile.Status != domain.OAuthAppStatusActive {
		return BeginResult{}, ErrProfileInactive
	}
	if _, connected, err := s.Connections.ActiveConnection(ctx, request.OrganizationID, request.MemberID, profile.ID); err != nil {
		return BeginResult{}, fmt.Errorf("check active connection: %w", err)
	} else if connected {
		return BeginResult{}, ErrAlreadyConnected
	}
	definition, ok := s.Catalog.OAuthDefinition(profile.Provider)
	if !ok {
		return BeginResult{}, ErrProviderNotAllowed
	}
	provider, ok := s.Providers.Provider(profile.Provider)
	if !ok {
		return BeginResult{}, ErrProviderNotAllowed
	}
	rawState, err := s.randomToken(32)
	if err != nil {
		return BeginResult{}, fmt.Errorf("generate oauth state: %w", err)
	}
	stateHash := sha256.Sum256([]byte(rawState))
	var verifier string
	var sealedVerifier domain.SealedData
	if definition.SupportsPKCE {
		verifier, err = s.randomToken(32)
		if err != nil {
			return BeginResult{}, fmt.Errorf("generate PKCE verifier: %w", err)
		}
		sealedVerifier, err = s.Secrets.Seal(ctx, stateBinding(request.OrganizationID, stateHash), []byte(verifier))
		if err != nil {
			return BeginResult{}, fmt.Errorf("encrypt PKCE verifier: %w", err)
		}
	}
	now := s.now()
	ttl := s.StateTTL
	if ttl == 0 {
		ttl = 10 * time.Minute
	}
	state := domain.OAuthAuthorizationState{
		StateHash:         stateHash,
		OrganizationID:    request.OrganizationID,
		MemberID:          request.MemberID,
		ProfileID:         profile.ID,
		Provider:          profile.Provider,
		RedirectURI:       definition.RedirectURI,
		ReturnURI:         request.ReturnURI,
		Scopes:            append([]string(nil), profile.Scopes...),
		EncryptedVerifier: sealedVerifier,
		ExpiresAt:         now.Add(ttl),
		CreatedAt:         now,
	}
	if err := s.States.CreateOAuthState(ctx, state); err != nil {
		return BeginResult{}, fmt.Errorf("store oauth state: %w", err)
	}
	challenge := ""
	if definition.SupportsPKCE {
		sum := sha256.Sum256([]byte(verifier))
		challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	}
	authorizationURL, err := provider.AuthorizationURL(AuthorizationRequest{
		ClientID:      profile.ClientID,
		RedirectURI:   definition.RedirectURI,
		Scopes:        append([]string(nil), profile.Scopes...),
		State:         rawState,
		PKCEChallenge: challenge,
	})
	if err != nil {
		return BeginResult{}, fmt.Errorf("build provider authorization URL: %w", err)
	}
	return BeginResult{AuthorizationURL: authorizationURL, ExpiresAt: state.ExpiresAt}, nil
}

func (s Service) Complete(ctx context.Context, request CompleteRequest) (CompleteResult, error) {
	if request.Provider == "" || request.State == "" || (request.Code == "" && request.ProviderError == "") {
		return CompleteResult{}, ErrInvalidInput
	}
	stateHash := sha256.Sum256([]byte(request.State))
	state, err := s.States.ConsumeOAuthState(ctx, stateHash, s.now())
	if err != nil {
		return CompleteResult{}, fmt.Errorf("consume oauth state: %w", err)
	}
	if !s.now().Before(state.ExpiresAt) {
		return CompleteResult{}, ErrStateExpired
	}
	if request.Provider != state.Provider {
		return CompleteResult{}, ErrProviderMismatch
	}
	if request.ProviderError != "" {
		return CompleteResult{}, ErrProviderDenied
	}
	if err := s.Authorizer.CanCreateConnection(ctx, state.OrganizationID, state.MemberID); err != nil {
		return CompleteResult{}, fmt.Errorf("%w: membership changed: %v", ErrForbidden, err)
	}
	profile, err := s.Profiles.OAuthApp(ctx, state.OrganizationID, state.ProfileID)
	if err != nil {
		return CompleteResult{}, fmt.Errorf("load oauth application: %w", err)
	}
	if profile.Status != domain.OAuthAppStatusActive || profile.Provider != state.Provider {
		return CompleteResult{}, ErrProfileInactive
	}
	definition, ok := s.Catalog.OAuthDefinition(state.Provider)
	if !ok || definition.RedirectURI != state.RedirectURI {
		return CompleteResult{}, ErrProviderNotAllowed
	}
	provider, ok := s.Providers.Provider(state.Provider)
	if !ok {
		return CompleteResult{}, ErrProviderNotAllowed
	}
	clientSecret, err := s.Secrets.Open(ctx, profileBinding(state.OrganizationID, profile.ID), profile.EncryptedSecret)
	if err != nil {
		return CompleteResult{}, fmt.Errorf("decrypt oauth application secret: %w", err)
	}
	defer wipe(clientSecret)
	var verifier []byte
	if definition.SupportsPKCE {
		verifier, err = s.Secrets.Open(ctx, stateBinding(state.OrganizationID, stateHash), state.EncryptedVerifier)
		if err != nil {
			return CompleteResult{}, fmt.Errorf("decrypt PKCE verifier: %w", err)
		}
		defer wipe(verifier)
	}
	grant, err := provider.Exchange(ctx, ExchangeRequest{
		ClientID:     profile.ClientID,
		ClientSecret: string(clientSecret),
		RedirectURI:  state.RedirectURI,
		Code:         request.Code,
		PKCEVerifier: string(verifier),
		Scopes:       append([]string(nil), state.Scopes...),
	})
	if err != nil {
		return CompleteResult{}, fmt.Errorf("exchange oauth code: %w", err)
	}
	if missing := missingScopes(state.Scopes, grant.Scopes); len(missing) > 0 {
		return CompleteResult{}, fmt.Errorf("%w: %s", ErrRequiredScope, strings.Join(missing, ","))
	}
	identity, err := provider.ResolveIdentity(ctx, grant)
	if err != nil {
		return CompleteResult{}, fmt.Errorf("resolve provider identity: %w", err)
	}
	if identity.SubjectID == "" || (identity.AccountID == "" && identity.TenantID == "") {
		return CompleteResult{}, fmt.Errorf("%w: provider identity is incomplete", ErrInvalidInput)
	}
	connectionID, err := s.newID("con")
	if err != nil {
		return CompleteResult{}, err
	}
	credentialJSON, err := json.Marshal(grant)
	if err != nil {
		return CompleteResult{}, fmt.Errorf("encode provider grant: %w", err)
	}
	sealedCredentials, err := s.Secrets.Seal(ctx, connectionBinding(state.OrganizationID, connectionID), credentialJSON)
	wipe(credentialJSON)
	if err != nil {
		return CompleteResult{}, fmt.Errorf("encrypt provider grant: %w", err)
	}
	now := s.now()
	connection := domain.Connection{
		ID:                   connectionID,
		OrganizationID:       state.OrganizationID,
		MemberID:             state.MemberID,
		ProfileID:            profile.ID,
		Provider:             state.Provider,
		PieceName:            definition.PieceName,
		PieceVersion:         definition.PieceVersion,
		Identity:             identity,
		GrantedScopes:        append([]string(nil), grant.Scopes...),
		EncryptedCredentials: sealedCredentials,
		Status:               domain.ConnectionStatusActive,
		CreatedAt:            now,
		UpdatedAt:            now,
		Version:              1,
	}
	eventID, err := s.newID("evt")
	if err != nil {
		return CompleteResult{}, err
	}
	event := domain.Event{
		EventID:        eventID,
		Source:         integrationevents.Source,
		DetailType:     integrationevents.ConnectionConnectedDetailType,
		Version:        1,
		OrganizationID: state.OrganizationID,
		OccurredAt:     now,
		Data: integrationevents.ConnectionConnected{
			ConnectionID: connection.ID,
			MemberID:     connection.MemberID,
			ProfileID:    connection.ProfileID,
			Provider:     connection.Provider,
			Identity: integrationevents.ProviderIdentity{
				SubjectID:   connection.Identity.SubjectID,
				AccountID:   connection.Identity.AccountID,
				AccountName: connection.Identity.AccountName,
				TenantID:    connection.Identity.TenantID,
				TenantName:  connection.Identity.TenantName,
			},
			Scopes:      append([]string(nil), connection.GrantedScopes...),
			ConnectedAt: now,
		},
	}
	if err := s.Connections.CreateConnectionWithEvent(ctx, connection, event); err != nil {
		return CompleteResult{}, fmt.Errorf("persist connection and event: %w", err)
	}
	return CompleteResult{Connection: connection, ReturnURI: state.ReturnURI}, nil
}

func (s Service) MemberIntegrations(ctx context.Context, organizationID, memberID string) ([]domain.MemberIntegration, error) {
	if organizationID == "" || memberID == "" {
		return nil, ErrInvalidInput
	}
	if err := s.Authorizer.CanCreateConnection(ctx, organizationID, memberID); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrForbidden, err)
	}
	items, err := s.Connections.ListMemberIntegrations(ctx, organizationID, memberID)
	if err != nil {
		return nil, fmt.Errorf("list member integrations: %w", err)
	}
	return items, nil
}

func (s Service) Disconnect(ctx context.Context, request DisconnectRequest) error {
	if request.OrganizationID == "" || request.MemberID == "" || request.ConnectionID == "" {
		return ErrInvalidInput
	}
	if err := s.Authorizer.CanCreateConnection(ctx, request.OrganizationID, request.MemberID); err != nil {
		return fmt.Errorf("%w: %v", ErrForbidden, err)
	}
	connection, err := s.Connections.ConnectionForExecution(ctx, request.OrganizationID, request.ConnectionID)
	if err != nil {
		return err
	}
	if connection.MemberID != request.MemberID {
		return ErrForbidden
	}
	now := s.now()
	eventID, err := s.newID("evt")
	if err != nil {
		return err
	}
	event := domain.Event{EventID: eventID, Source: integrationevents.Source,
		DetailType: integrationevents.ConnectionDisconnectedDetailType, Version: 1,
		OrganizationID: request.OrganizationID, OccurredAt: now,
		Data: integrationevents.ConnectionDisconnected{ConnectionID: request.ConnectionID,
			Provider: connection.Provider, DisconnectedBy: request.MemberID, DisconnectedAt: now}}
	return s.Connections.DisconnectConnectionWithEvent(ctx, request.OrganizationID, request.MemberID, request.ConnectionID, now, event)
}

func selectScopes(definition Definition, requested []string) ([]string, error) {
	if len(requested) == 0 {
		requested = definition.RequiredScopes
	}
	normalized := uniqueSorted(requested)
	if missing := missingScopes(definition.RequiredScopes, normalized); len(missing) > 0 {
		return nil, fmt.Errorf("%w: required scope %s was omitted", ErrInvalidInput, strings.Join(missing, ","))
	}
	if outside := missingScopes(normalized, definition.AllowedScopes); len(outside) > 0 {
		return nil, fmt.Errorf("%w: scope %s is outside catalog policy", ErrInvalidInput, strings.Join(outside, ","))
	}
	return normalized, nil
}

func missingScopes(required, actual []string) []string {
	set := make(map[string]struct{}, len(actual))
	for _, scope := range actual {
		set[scope] = struct{}{}
	}
	var missing []string
	for _, scope := range uniqueSorted(required) {
		if _, ok := set[scope]; !ok {
			missing = append(missing, scope)
		}
	}
	return missing
}

func uniqueSorted(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			set[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	slices.Sort(result)
	return result
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s Service) randomToken(size int) (string, error) {
	random := s.Random
	if random == nil {
		random = rand.Reader
	}
	value := make([]byte, size)
	if _, err := io.ReadFull(random, value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (s Service) newID(prefix string) (string, error) {
	if s.IDs != nil {
		return s.IDs(prefix)
	}
	value, err := s.randomToken(16)
	if err != nil {
		return "", fmt.Errorf("generate %s ID: %w", prefix, err)
	}
	return prefix + "_" + value, nil
}

func profileBinding(organizationID, profileID string) secrets.Binding {
	return secrets.Binding{OrganizationID: organizationID, ResourceType: "oauth-app", ResourceID: profileID}
}

func stateBinding(organizationID string, stateHash [32]byte) secrets.Binding {
	return secrets.Binding{OrganizationID: organizationID, ResourceType: "oauth-state", ResourceID: hex.EncodeToString(stateHash[:])}
}

func connectionBinding(organizationID, connectionID string) secrets.Binding {
	return secrets.Binding{OrganizationID: organizationID, ResourceType: "connection", ResourceID: connectionID}
}

func wipe(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
