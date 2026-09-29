package authn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

type Claims struct {
	ExpiresAt int64  `json:"exp"`
	Subject   string `json:"sub"`
	ClientID  string `json:"client_id"`
	TokenUse  string `json:"token_use"`
	Scope     string `json:"scope"`
}

type IdentityClaims struct {
	Subject       string    `json:"sub"`
	TokenUse      string    `json:"token_use"`
	Email         string    `json:"email"`
	EmailVerified claimBool `json:"email_verified"`
	Name          string    `json:"name"`
	Picture       string    `json:"picture"`
	Nonce         string    `json:"nonce"`
}

// claimBool is a boolean claim that may arrive spelled as a string. Cognito
// keeps user attributes as strings, and an attribute mapped from a federated
// identity provider, such as Google's email_verified, can be issued as "true".
type claimBool bool

func (value *claimBool) UnmarshalJSON(data []byte) error {
	var boolean bool
	if err := json.Unmarshal(data, &boolean); err == nil {
		*value = claimBool(boolean)
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return fmt.Errorf("claim is neither a boolean nor a string: %s", data)
	}
	parsed, err := strconv.ParseBool(text)
	if err != nil {
		return fmt.Errorf("claim %q is not a boolean", text)
	}
	*value = claimBool(parsed)
	return nil
}

type Verifier struct {
	clientID              string
	agentClientID         string
	cliClientID           string
	accessVerifier        *oidc.IDTokenVerifier
	identityVerifier      *oidc.IDTokenVerifier
	agentIdentityVerifier *oidc.IDTokenVerifier
	cliIdentityVerifier   *oidc.IDTokenVerifier
}

// New verifies tokens Cognito issues to the web app client clientID and, when
// agentClientID is set, to the agent app client too: an agent's access token
// is the member's own, issued to the agent client, and verifies here like
// the member's; what it may do is for the caller to decide.
func New(provider *oidc.Provider, clientID, agentClientID, cliClientID string) *Verifier {
	return newVerifier(provider.Verifier, clientID, agentClientID, cliClientID)
}

func newVerifier(verifierFor func(*oidc.Config) *oidc.IDTokenVerifier, clientID, agentClientID, cliClientID string) *Verifier {
	verifier := &Verifier{
		clientID:      clientID,
		agentClientID: agentClientID,
		accessVerifier: verifierFor(&oidc.Config{
			SkipClientIDCheck:    true,
			SupportedSigningAlgs: []string{oidc.RS256},
		}),
		identityVerifier: verifierFor(&oidc.Config{
			ClientID:             clientID,
			SupportedSigningAlgs: []string{oidc.RS256},
		}),
	}
	if agentClientID != "" {
		verifier.agentIdentityVerifier = verifierFor(&oidc.Config{
			ClientID:             agentClientID,
			SupportedSigningAlgs: []string{oidc.RS256},
		})
	}
	if cliClientID != "" {
		verifier.cliClientID = cliClientID
		verifier.cliIdentityVerifier = verifierFor(&oidc.Config{
			ClientID:             cliClientID,
			SupportedSigningAlgs: []string{oidc.RS256},
		})
	}
	return verifier
}

func (v *Verifier) Verify(ctx context.Context, rawToken string) (Claims, error) {
	if strings.TrimSpace(rawToken) == "" {
		return Claims{}, errors.New("access token is required")
	}

	token, err := v.accessVerifier.Verify(ctx, rawToken)
	if err != nil {
		return Claims{}, fmt.Errorf("verify token: %w", err)
	}

	var claims Claims
	if err := token.Claims(&claims); err != nil {
		return Claims{}, fmt.Errorf("decode token claims: %w", err)
	}
	if claims.TokenUse != "access" {
		return Claims{}, errors.New("token_use must be access")
	}
	if claims.ClientID != v.clientID && (v.agentClientID == "" || claims.ClientID != v.agentClientID) && (v.cliClientID == "" || claims.ClientID != v.cliClientID) {
		return Claims{}, errors.New("token client_id does not match")
	}
	if claims.Subject == "" {
		return Claims{}, errors.New("token subject is required")
	}

	return claims, nil
}

// VerifyCLIIdentityToken accepts only the public CLI app client's ID token
// and checks the nonce the native flow sent before auth admits the user.
func (v *Verifier) VerifyCLIIdentityToken(ctx context.Context, rawToken, expectedNonce string) (IdentityClaims, error) {
	if v.cliIdentityVerifier == nil || strings.TrimSpace(rawToken) == "" || expectedNonce == "" {
		return IdentityClaims{}, errors.New("CLI identity token and nonce are required")
	}
	token, err := v.cliIdentityVerifier.Verify(ctx, rawToken)
	if err != nil {
		return IdentityClaims{}, fmt.Errorf("verify CLI identity token: %w", err)
	}
	var claims IdentityClaims
	if err := token.Claims(&claims); err != nil {
		return IdentityClaims{}, fmt.Errorf("decode CLI identity claims: %w", err)
	}
	if claims.Subject == "" || claims.TokenUse != "id" || claims.Email == "" || !claims.EmailVerified || claims.Nonce != expectedNonce {
		return IdentityClaims{}, errors.New("CLI identity token claims are invalid")
	}
	return claims, nil
}

func (v *Verifier) VerifyIdentityToken(ctx context.Context, rawToken, expectedNonce string) (IdentityClaims, error) {
	if strings.TrimSpace(rawToken) == "" {
		return IdentityClaims{}, errors.New("identity token is required")
	}
	token, err := v.identityVerifier.Verify(ctx, rawToken)
	if err != nil {
		return IdentityClaims{}, fmt.Errorf("verify identity token: %w", err)
	}
	var claims IdentityClaims
	if err := token.Claims(&claims); err != nil {
		return IdentityClaims{}, fmt.Errorf("decode identity claims: %w", err)
	}
	if claims.Subject == "" || claims.Email == "" || !claims.EmailVerified {
		return IdentityClaims{}, errors.New("verified subject and email are required")
	}
	if expectedNonce != "" && claims.Nonce != expectedNonce {
		return IdentityClaims{}, errors.New("identity token nonce does not match")
	}
	return claims, nil
}

// VerifyAgentIdentityToken verifies an ID token issued to the agent app
// client and returns whose it is. Unlike VerifyIdentityToken it asks nothing
// of the member's profile: an agent grant only needs to know which member
// signed in, and that it answers the nonce its flow sent.
func (v *Verifier) VerifyAgentIdentityToken(ctx context.Context, rawToken, expectedNonce string) (string, error) {
	if v.agentIdentityVerifier == nil {
		return "", errors.New("no agent app client is configured")
	}
	if strings.TrimSpace(rawToken) == "" || expectedNonce == "" {
		return "", errors.New("identity token and nonce are required")
	}
	token, err := v.agentIdentityVerifier.Verify(ctx, rawToken)
	if err != nil {
		return "", fmt.Errorf("verify agent identity token: %w", err)
	}
	var claims IdentityClaims
	if err := token.Claims(&claims); err != nil {
		return "", fmt.Errorf("decode agent identity claims: %w", err)
	}
	if claims.Subject == "" {
		return "", errors.New("agent identity token subject is required")
	}
	if claims.Nonce != expectedNonce {
		return "", errors.New("agent identity token nonce does not match")
	}
	return claims.Subject, nil
}
