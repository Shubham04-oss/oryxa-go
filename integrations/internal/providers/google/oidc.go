package google

import (
	"context"
	"fmt"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

type IDTokenVerifier struct {
	verifier *oidc.IDTokenVerifier
	email    string
}

func NewIDTokenVerifier(ctx context.Context, audience, expectedEmail string) (*IDTokenVerifier, error) {
	if audience == "" {
		return nil, fmt.Errorf("Gmail push audience is required")
	}
	provider, err := oidc.NewProvider(ctx, "https://accounts.google.com")
	if err != nil {
		return nil, fmt.Errorf("discover Google OIDC issuer: %w", err)
	}
	return &IDTokenVerifier{
		verifier: provider.Verifier(&oidc.Config{ClientID: audience}),
		email:    strings.TrimSpace(expectedEmail),
	}, nil
}

func (v *IDTokenVerifier) Verify(ctx context.Context, rawToken, audience string) error {
	if v == nil || v.verifier == nil {
		return fmt.Errorf("Gmail push verifier is not configured")
	}
	token, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return err
	}
	if v.email == "" {
		return nil
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	if err := token.Claims(&claims); err != nil || !claims.EmailVerified || claims.Email != v.email {
		return fmt.Errorf("Gmail push service account mismatch")
	}
	return nil
}
