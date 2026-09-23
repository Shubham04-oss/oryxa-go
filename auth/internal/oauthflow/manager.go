package oauthflow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/application"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/authn"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/returnurl"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

type identityVerifier interface {
	VerifyIdentityToken(context.Context, string, string) (authn.IdentityClaims, error)
}

type loginService interface {
	SaveLoginFlow(context.Context, domain.LoginFlow) error
	ConsumeLoginFlow(context.Context, string) (domain.LoginFlow, error)
	CompleteLogin(context.Context, application.FederatedIdentity) (domain.User, bool, error)
}

type Config struct {
	CognitoDomain   string
	ClientID        string
	CallbackURL     string
	AllowedReturnTo []string
}

type Manager struct {
	config     Config
	allowed    []*url.URL
	service    loginService
	verifier   identityVerifier
	httpClient *http.Client
	now        func() time.Time
}

type Session struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	User         domain.User
	ReturnTo     string
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

func New(config Config, service loginService, verifier identityVerifier) (*Manager, error) {
	if config.CognitoDomain == "" || config.ClientID == "" || config.CallbackURL == "" || len(config.AllowedReturnTo) == 0 {
		return nil, errors.New("Cognito domain, client ID, callback URL, and return URLs are required")
	}
	allowed, err := returnurl.ParseOrigins(config.AllowedReturnTo)
	if err != nil {
		return nil, err
	}
	return &Manager{
		config:     config,
		allowed:    allowed,
		service:    service,
		verifier:   verifier,
		httpClient: &http.Client{Timeout: 10 * time.Second, Transport: observability.Transport(nil)},
		now:        time.Now,
	}, nil
}

func (manager *Manager) Begin(ctx context.Context, returnTo string) (string, string, error) {
	if returnTo == "" {
		returnTo = manager.config.AllowedReturnTo[0]
	}
	if !returnurl.Allowed(returnTo, manager.allowed) {
		return "", "", fmt.Errorf("return URL is not allowed: %w", domain.ErrInvalid)
	}
	state, err := secureToken(32)
	if err != nil {
		return "", "", err
	}
	verifier, err := secureToken(32)
	if err != nil {
		return "", "", err
	}
	nonce, err := secureToken(24)
	if err != nil {
		return "", "", err
	}
	if err := manager.service.SaveLoginFlow(ctx, domain.LoginFlow{
		StateHash:    tokenHash(state),
		CodeVerifier: verifier,
		Nonce:        nonce,
		ReturnTo:     returnTo,
		ExpiresAt:    manager.now().UTC().Add(10 * time.Minute),
	}); err != nil {
		return "", "", err
	}

	challengeDigest := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"client_id":             {manager.config.ClientID},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challengeDigest[:])},
		"code_challenge_method": {"S256"},
		"identity_provider":     {"Google"},
		"nonce":                 {nonce},
		"redirect_uri":          {manager.config.CallbackURL},
		"response_type":         {"code"},
		"scope":                 {"openid email profile"},
		"state":                 {state},
	}
	return state, strings.TrimRight(manager.config.CognitoDomain, "/") + "/oauth2/authorize?" + query.Encode(), nil
}

func (manager *Manager) Complete(ctx context.Context, state, stateCookie, code string) (Session, error) {
	if state == "" || stateCookie == "" || code == "" || subtle.ConstantTimeCompare([]byte(state), []byte(stateCookie)) != 1 {
		return Session{}, fmt.Errorf("invalid OAuth callback state: %w", domain.ErrUnauthorized)
	}
	flow, err := manager.service.ConsumeLoginFlow(ctx, tokenHash(state))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return Session{}, fmt.Errorf("login flow is missing or expired: %w", domain.ErrUnauthorized)
		}
		return Session{}, err
	}
	tokens, err := manager.exchange(ctx, url.Values{
		"client_id":     {manager.config.ClientID},
		"code":          {code},
		"code_verifier": {flow.CodeVerifier},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {manager.config.CallbackURL},
	})
	if err != nil {
		return Session{}, err
	}
	claims, err := manager.verifier.VerifyIdentityToken(ctx, tokens.IDToken, flow.Nonce)
	if err != nil {
		return Session{}, fmt.Errorf("identity token rejected (%v): %w", err, domain.ErrUnauthorized)
	}
	user, _, err := manager.service.CompleteLogin(ctx, application.FederatedIdentity{
		Subject:     claims.Subject,
		Email:       claims.Email,
		DisplayName: claims.Name,
		AvatarURL:   claims.Picture,
		Provider:    "Google",
	})
	if err != nil {
		return Session{}, err
	}
	return Session{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresIn:    tokens.ExpiresIn,
		User:         user,
		ReturnTo:     flow.ReturnTo,
	}, nil
}

func (manager *Manager) Revoke(ctx context.Context, refreshToken string) error {
	if refreshToken == "" {
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(manager.config.CognitoDomain, "/")+"/oauth2/revoke", strings.NewReader(url.Values{
		"client_id": {manager.config.ClientID},
		"token":     {refreshToken},
	}.Encode()))
	if err != nil {
		return fmt.Errorf("create token revocation request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := manager.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("revoke refresh token: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return fmt.Errorf("revoke refresh token: status %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (manager *Manager) exchange(ctx context.Context, form url.Values) (tokenResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(manager.config.CognitoDomain, "/")+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("create token request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := manager.httpClient.Do(request)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("exchange Cognito token: %w", err)
	}
	defer response.Body.Close()

	var tokens tokenResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&tokens); err != nil {
		return tokenResponse{}, fmt.Errorf("decode Cognito token response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || tokens.Error != "" {
		return tokenResponse{}, fmt.Errorf("Cognito token exchange rejected (%d %s): %w", response.StatusCode, tokens.Error, domain.ErrUnauthorized)
	}
	if tokens.AccessToken == "" || tokens.ExpiresIn <= 0 {
		return tokenResponse{}, fmt.Errorf("Cognito token response is incomplete: %w", domain.ErrUnauthorized)
	}
	return tokens, nil
}

func secureToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate secure token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func tokenHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
