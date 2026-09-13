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

type Verifier struct {
	clientID string
	verifier *oidc.IDTokenVerifier
}

func New(provider *oidc.Provider, clientID string) *Verifier {
	return &Verifier{
		clientID: clientID,
		verifier: provider.Verifier(&oidc.Config{
			SkipClientIDCheck:    true,
			SupportedSigningAlgs: []string{oidc.RS256},
		}),
	}
}

func (v *Verifier) Verify(ctx context.Context, rawToken string) (Claims, error) {
	if strings.TrimSpace(rawToken) == "" {
		return Claims{}, errors.New("access token is required")
	}

	token, err := v.verifier.Verify(ctx, rawToken)
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
