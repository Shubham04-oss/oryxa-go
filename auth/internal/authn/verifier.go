package authn

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

type Claims struct {
	Subject  string `json:"sub"`
	ClientID string `json:"client_id"`
	TokenUse string `json:"token_use"`
	Scope    string `json:"scope"`
}

type IdentityClaims struct {
	Subject       string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
	Nonce         string `json:"nonce"`
}

type Verifier struct {
	clientID         string
	accessVerifier   *oidc.IDTokenVerifier
	identityVerifier *oidc.IDTokenVerifier
}

func New(provider *oidc.Provider, clientID string) *Verifier {
	return &Verifier{
		clientID: clientID,
		accessVerifier: provider.Verifier(&oidc.Config{
			SkipClientIDCheck:    true,
			SupportedSigningAlgs: []string{oidc.RS256},
		}),
		identityVerifier: provider.Verifier(&oidc.Config{
			ClientID:             clientID,
			SupportedSigningAlgs: []string{oidc.RS256},
		}),
	}
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
	if claims.ClientID != v.clientID {
		return Claims{}, errors.New("token client_id does not match")
	}
	if claims.Subject == "" {
		return Claims{}, errors.New("token subject is required")
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
