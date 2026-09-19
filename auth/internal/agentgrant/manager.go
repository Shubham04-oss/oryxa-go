// Package agentgrant implements the member-consent flow that lets an agent
// act for a member while the member is absent. A member authorizes a
// separate, confidential Cognito "agent" app client; auth stores the refresh
// token it is issued, envelope-encrypted, and exchanges it for short-lived
// access tokens on request.
//
// The authorize/callback pair reuses oauthflow's own patterns: PKCE, a
// state hash stored server-side through the same login-flow table and
// double-submit cookie, and a form-encoded call to Cognito's token endpoint.
// It differs from oauthflow in the ways the agent app client requires: the
// state also binds the flow to the member who started it, the member signs
// in however they sign in rather than always with Google, the client is
// confidential (Cognito expects HTTP Basic client authentication rather than
// a bare client_id), and a refresh-token exchange must tell Cognito refusing
// the token apart from Cognito failing to answer.
package agentgrant

import (
	"bytes"
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
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/returnurl"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/secrets"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

// Scope is the fixed OAuth scope an agent grant's refresh token is issued
// with: OpenID Connect plus Cognito's custom scope for agent actions.
const Scope = "openid oryxa/agent.actions"

// identityVerifier checks an ID token issued to the agent app client and
// returns whose it is. authn.Verifier implements it.
type identityVerifier interface {
	VerifyAgentIdentityToken(ctx context.Context, rawToken, expectedNonce string) (subject string, err error)
}

// Store is the slice of auth's Postgres store an agent grant flow needs. The
// login-flow methods are the same ones oauthflow's member sign-in uses,
// reused rather than duplicated into a second flow table.
type Store interface {
	SaveLoginFlow(context.Context, domain.LoginFlow) error
	ConsumeLoginFlow(context.Context, string) (domain.LoginFlow, error)
	GetAgentGrant(context.Context, string) (domain.AgentGrant, error)
	UpsertAgentGrant(context.Context, domain.AgentGrant) error
	DeleteAgentGrant(context.Context, string) error
	ReplaceAgentGrantToken(context.Context, string, domain.SealedData, time.Time) error
}

// Sealer envelope-encrypts and opens a member's stored agent refresh token.
// secrets.Envelope implements it.
type Sealer interface {
	Seal(ctx context.Context, binding secrets.Binding, plaintext []byte) (domain.SealedData, error)
	Open(ctx context.Context, binding secrets.Binding, sealed domain.SealedData) ([]byte, error)
}

type Config struct {
	CognitoDomain   string
	ClientID        string
	ClientSecret    string
	CallbackURL     string
	AllowedReturnTo []string
}

type Manager struct {
	config     Config
	allowed    []*url.URL
	store      Store
	verifier   identityVerifier
	sealer     Sealer
	httpClient *http.Client
	now        func() time.Time
}

func New(config Config, store Store, verifier identityVerifier, sealer Sealer) (*Manager, error) {
	if config.CognitoDomain == "" || config.ClientID == "" || config.ClientSecret == "" || config.CallbackURL == "" || len(config.AllowedReturnTo) == 0 {
		return nil, errors.New("Cognito domain, agent client ID and secret, callback URL, and return URLs are required")
	}
	allowed, err := returnurl.ParseOrigins(config.AllowedReturnTo)
	if err != nil {
		return nil, err
	}
	return &Manager{
		config:     config,
		allowed:    allowed,
		store:      store,
		verifier:   verifier,
		sealer:     sealer,
		httpClient: &http.Client{Timeout: 10 * time.Second, Transport: observability.Transport(nil)},
		now:        time.Now,
	}, nil
}

// Status returns the member's agent grant, or domain.ErrNotFound if they
// have not granted one.
func (m *Manager) Status(ctx context.Context, userID string) (domain.AgentGrant, error) {
	return m.store.GetAgentGrant(ctx, userID)
}

// Authorize validates returnTo against the allowed list, stores a PKCE flow
// bound to memberID, and returns Cognito's authorization URL for the agent
// client along with the opaque state the caller must round-trip back as a
// cookie for Complete to check.
func (m *Manager) Authorize(ctx context.Context, memberID, returnTo string) (string, string, error) {
	if !returnurl.Allowed(returnTo, m.allowed) {
		return "", "", fmt.Errorf("return URL is not allowed: %w", domain.ErrInvalid)
	}
	random, err := secureToken(32)
	if err != nil {
		return "", "", err
	}
	// The state Cognito echoes back binds this flow to the member who
	// started it: memberID never contains '.', since it is always a Cognito
	// subject (a UUID), so Complete can split it back out unambiguously.
	state := memberID + "." + random
	verifier, err := secureToken(32)
	if err != nil {
		return "", "", err
	}
	nonce, err := secureToken(24)
	if err != nil {
		return "", "", err
	}
	if err := m.store.SaveLoginFlow(ctx, domain.LoginFlow{
		StateHash:    tokenHash(state),
		CodeVerifier: verifier,
		Nonce:        nonce,
		ReturnTo:     returnTo,
		ExpiresAt:    m.now().UTC().Add(10 * time.Minute),
	}); err != nil {
		return "", "", err
	}

	challengeDigest := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"client_id":             {m.config.ClientID},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challengeDigest[:])},
		"code_challenge_method": {"S256"},
		// No identity_provider: a member who signs in with a password must
		// be able to grant too. Cognito's sign-in page offers every provider,
		// and passes straight through for a member already signed in there.
		"nonce":         {nonce},
		"redirect_uri":  {m.config.CallbackURL},
		"response_type": {"code"},
		"scope":         {Scope},
		"state":         {state},
	}
	return strings.TrimRight(m.config.CognitoDomain, "/") + "/oauth2/authorize?" + query.Encode(), state, nil
}

// Complete finishes the callback: it checks state against its cookie and
// against the member it names, exchanges the code, requires the ID token's
// subject to be that same member, and seals and stores the grant. It returns
// the return_to the flow was started with.
func (m *Manager) Complete(ctx context.Context, state, stateCookie, code string) (string, error) {
	if state == "" || stateCookie == "" || code == "" || subtle.ConstantTimeCompare([]byte(state), []byte(stateCookie)) != 1 {
		return "", fmt.Errorf("invalid agent grant callback state: %w", domain.ErrUnauthorized)
	}
	memberID, _, ok := strings.Cut(state, ".")
	if !ok || memberID == "" {
		return "", fmt.Errorf("invalid agent grant callback state: %w", domain.ErrUnauthorized)
	}
	flow, err := m.store.ConsumeLoginFlow(ctx, tokenHash(state))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return "", fmt.Errorf("agent grant flow is missing or expired: %w", domain.ErrUnauthorized)
		}
		return "", err
	}
	// Re-validated even though Authorize already checked it: Complete must
	// never redirect anywhere it did not itself confirm is allowed.
	if !returnurl.Allowed(flow.ReturnTo, m.allowed) {
		return "", fmt.Errorf("return URL is not allowed: %w", domain.ErrInvalid)
	}

	tokens, err := m.exchangeCode(ctx, code, flow.CodeVerifier)
	if err != nil {
		return "", err
	}
	subject, err := m.verifier.VerifyAgentIdentityToken(ctx, tokens.IDToken, flow.Nonce)
	if err != nil {
		return "", fmt.Errorf("identity token rejected: %w", domain.ErrUnauthorized)
	}
	if subject != memberID {
		return "", fmt.Errorf("identity token does not match the member who started: %w", domain.ErrUnauthorized)
	}

	sealed, err := m.seal(ctx, memberID, tokens.RefreshToken)
	if err != nil {
		return "", err
	}
	if err := m.store.UpsertAgentGrant(ctx, domain.AgentGrant{
		UserID:                memberID,
		EncryptedRefreshToken: sealed,
		Scope:                 Scope,
		CreatedAt:             m.now().UTC(),
	}); err != nil {
		return "", err
	}
	return flow.ReturnTo, nil
}

// Revoke revokes the member's refresh token at Cognito on a best-effort
// basis, logging rather than failing on a Cognito-side problem, and always
// deletes the local grant: exactly how logout always clears cookies even
// when Cognito's own revoke call fails. A missing grant is not an error.
func (m *Manager) Revoke(ctx context.Context, userID string) error {
	grant, err := m.store.GetAgentGrant(ctx, userID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if plaintext, openErr := m.open(ctx, userID, grant.EncryptedRefreshToken); openErr != nil {
		slog.ErrorContext(ctx, "open agent refresh token for revocation", "error", openErr)
	} else {
		if revokeErr := m.revokeAtCognito(ctx, string(plaintext)); revokeErr != nil {
			slog.ErrorContext(ctx, "revoke agent refresh token at Cognito", "error", revokeErr)
		}
		secrets.Wipe(plaintext)
	}
	return m.store.DeleteAgentGrant(ctx, userID)
}

// MintToken exchanges a member's stored refresh token for a short-lived
// access token. domain.ErrNotFound means the member has no grant.
// domain.ErrConflict means Cognito refused the refresh token, in which case
// the now-useless grant is deleted before the error is returned.
// domain.ErrUnavailable means Cognito did not answer. A refresh token
// Cognito rotates is sealed and saved before the access token is returned.
//
// Another mint for the same member, say on another instance, may rotate the
// stored token while this one exchanges it, and Cognito then refuses the
// token this one read. So a refusal deletes the grant only when the stored
// token is still the one refused; otherwise the exchange is tried once more
// with the token now stored.
func (m *Manager) MintToken(ctx context.Context, userID string) (string, int, error) {
	grant, err := m.store.GetAgentGrant(ctx, userID)
	if err != nil {
		return "", 0, err
	}
	tokens, err := m.exchangeGrant(ctx, grant)
	if errors.Is(err, domain.ErrConflict) {
		current, getErr := m.store.GetAgentGrant(ctx, userID)
		if getErr != nil {
			return "", 0, getErr
		}
		if sameSealedToken(current.EncryptedRefreshToken, grant.EncryptedRefreshToken) {
			if delErr := m.store.DeleteAgentGrant(ctx, userID); delErr != nil {
				return "", 0, delErr
			}
			return "", 0, err
		}
		tokens, err = m.exchangeGrant(ctx, current)
	}
	if err != nil {
		return "", 0, err
	}
	if tokens.RefreshToken != "" {
		sealed, sealErr := m.seal(ctx, userID, tokens.RefreshToken)
		if sealErr != nil {
			return "", 0, sealErr
		}
		if err := m.store.ReplaceAgentGrantToken(ctx, userID, sealed, m.now().UTC()); err != nil {
			return "", 0, err
		}
	}
	return tokens.AccessToken, tokens.ExpiresIn, nil
}

// exchangeGrant opens a grant's refresh token just long enough to exchange it.
func (m *Manager) exchangeGrant(ctx context.Context, grant domain.AgentGrant) (tokenResponse, error) {
	plaintext, err := m.open(ctx, grant.UserID, grant.EncryptedRefreshToken)
	if err != nil {
		return tokenResponse{}, err
	}
	defer secrets.Wipe(plaintext)
	return m.exchangeRefresh(ctx, string(plaintext))
}

// sameSealedToken reports whether two sealed values are the same sealing:
// every seal draws a fresh nonce and data key, so a rotated or re-granted
// token never compares equal to the one it replaced.
func sameSealedToken(a, b domain.SealedData) bool {
	return bytes.Equal(a.Nonce, b.Nonce) && bytes.Equal(a.Ciphertext, b.Ciphertext) && bytes.Equal(a.EncryptedDataKey, b.EncryptedDataKey)
}

func (m *Manager) seal(ctx context.Context, userID, refreshToken string) (domain.SealedData, error) {
	sealed, err := m.sealer.Seal(ctx, secrets.Binding{ResourceType: secrets.ResourceAgentGrant, ResourceID: userID}, []byte(refreshToken))
	if err != nil {
		return domain.SealedData{}, fmt.Errorf("seal agent refresh token: %w", err)
	}
	return sealed, nil
}

func (m *Manager) open(ctx context.Context, userID string, sealed domain.SealedData) ([]byte, error) {
	plaintext, err := m.sealer.Open(ctx, secrets.Binding{ResourceType: secrets.ResourceAgentGrant, ResourceID: userID}, sealed)
	if err != nil {
		return nil, fmt.Errorf("open agent refresh token: %w", err)
	}
	return plaintext, nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

// errUnreachable, errRefused and errRejected let the two callers of
// tokenRequest each apply their own error-wrapping policy: the callback's code
// exchange collapses all of them into domain.ErrUnauthorized like oauthflow
// does, while the internal route's refresh exchange must tell them apart.
// Only errRefused, Cognito saying the grant itself is no longer valid, may
// cost a member their grant: Cognito failing (503) or refusing the agent
// client itself, such as for a wrong secret, is never the grant's fault.
var (
	errUnreachable = errors.New("Cognito did not respond")
	errRefused     = errors.New("Cognito refused the grant")
	errRejected    = errors.New("Cognito rejected the request")
)

// tokenRequest authenticates with HTTP Basic client_id/client_secret, as
// Cognito expects from a confidential client, unlike oauthflow's public web
// client which authenticates with a bare client_id in the form body alone.
func (m *Manager) tokenRequest(ctx context.Context, form url.Values) (tokenResponse, error) {
	form.Set("client_id", m.config.ClientID)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(m.config.CognitoDomain, "/")+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("create token request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetBasicAuth(m.config.ClientID, m.config.ClientSecret)
	response, err := m.httpClient.Do(request)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("%w: %v", errUnreachable, err)
	}
	defer response.Body.Close()

	if response.StatusCode >= http.StatusInternalServerError || response.StatusCode == http.StatusTooManyRequests {
		return tokenResponse{}, fmt.Errorf("%w: status %d", errUnreachable, response.StatusCode)
	}
	var tokens tokenResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&tokens); err != nil {
		return tokenResponse{}, fmt.Errorf("%w: decode response: %v", errUnreachable, err)
	}
	switch {
	case tokens.Error == "invalid_grant":
		return tokenResponse{}, fmt.Errorf("%w: %s", errRefused, tokens.Error)
	case response.StatusCode < 200 || response.StatusCode >= 300 || tokens.Error != "":
		return tokenResponse{}, fmt.Errorf("%w: status %d: %s", errRejected, response.StatusCode, tokens.Error)
	case tokens.AccessToken == "" || tokens.ExpiresIn <= 0:
		return tokenResponse{}, fmt.Errorf("%w: incomplete response", errUnreachable)
	}
	return tokens, nil
}

func (m *Manager) exchangeCode(ctx context.Context, code, codeVerifier string) (tokenResponse, error) {
	tokens, err := m.tokenRequest(ctx, url.Values{
		"code":          {code},
		"code_verifier": {codeVerifier},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {m.config.CallbackURL},
	})
	if err != nil {
		return tokenResponse{}, fmt.Errorf("Cognito agent code exchange rejected: %w", domain.ErrUnauthorized)
	}
	return tokens, nil
}

func (m *Manager) exchangeRefresh(ctx context.Context, refreshToken string) (tokenResponse, error) {
	tokens, err := m.tokenRequest(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	})
	switch {
	case errors.Is(err, errUnreachable):
		return tokenResponse{}, fmt.Errorf("agent refresh exchange: %w", domain.ErrUnavailable)
	case errors.Is(err, errRefused):
		return tokenResponse{}, fmt.Errorf("Cognito refused the agent refresh token: %w", domain.ErrConflict)
	case err != nil:
		// The agent client itself was refused, such as for a wrong secret:
		// an operator's problem, which must not cost any member their grant.
		return tokenResponse{}, fmt.Errorf("agent refresh exchange: %w", err)
	}
	return tokens, nil
}

func (m *Manager) revokeAtCognito(ctx context.Context, refreshToken string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(m.config.CognitoDomain, "/")+"/oauth2/revoke", strings.NewReader(url.Values{
		"client_id": {m.config.ClientID},
		"token":     {refreshToken},
	}.Encode()))
	if err != nil {
		return fmt.Errorf("create agent token revocation request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetBasicAuth(m.config.ClientID, m.config.ClientSecret)
	response, err := m.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("revoke agent refresh token: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return fmt.Errorf("revoke agent refresh token: status %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
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
