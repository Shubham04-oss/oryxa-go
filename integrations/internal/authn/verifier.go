package authn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

type Claims struct {
	Subject  string `json:"sub"`
	ClientID string `json:"client_id"`
	TokenUse string `json:"token_use"`
}

type TokenVerifier interface {
	Verify(context.Context, string) (Claims, error)
}

type Verifier struct {
	clientID string
	verifier *oidc.IDTokenVerifier
}

func New(provider *oidc.Provider, clientID string) *Verifier {
	return &Verifier{clientID: clientID, verifier: provider.Verifier(&oidc.Config{
		SkipClientIDCheck: true, SupportedSigningAlgs: []string{oidc.RS256},
	})}
}

func (v *Verifier) Verify(ctx context.Context, raw string) (Claims, error) {
	if strings.TrimSpace(raw) == "" {
		return Claims{}, errors.New("access token is required")
	}
	token, err := v.verifier.Verify(ctx, raw)
	if err != nil {
		return Claims{}, fmt.Errorf("verify access token: %w", err)
	}
	var claims Claims
	if err := token.Claims(&claims); err != nil {
		return Claims{}, fmt.Errorf("decode access token: %w", err)
	}
	if claims.TokenUse != "access" || claims.ClientID != v.clientID || claims.Subject == "" {
		return Claims{}, errors.New("access token claims are invalid")
	}
	return claims, nil
}

type BearerAuthenticator struct{ Verifier TokenVerifier }

func (a BearerAuthenticator) Authenticate(request *http.Request) (string, error) {
	if a.Verifier == nil {
		return "", errors.New("token verifier is not configured")
	}
	parts := strings.Fields(request.Header.Get("authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", errors.New("bearer access token is required")
	}
	claims, err := a.Verifier.Verify(request.Context(), parts[1])
	if err != nil {
		return "", err
	}
	return claims.Subject, nil
}
