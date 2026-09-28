package hooks

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// Claims is what a verified jwt_push token says about who sent it.
type Claims struct {
	Email         string
	EmailVerified bool
}

// TokenVerifier checks a jwt_push delivery's bearer token: signed with one of
// issuer's published keys, issued by issuer, for audience, and not expired.
// A failure to reach the issuer's keys wraps errUnavailable, so the app is
// asked to retry; any other failure means the token is refused.
type TokenVerifier interface {
	VerifyToken(ctx context.Context, issuer, audience, raw string) (Claims, error)
}

// OIDCVerifier finds an issuer's keys through OpenID Connect discovery, with
// go-oidc, once per issuer, and keeps them as go-oidc does: cached, and
// fetched again when a token names a key it has not seen.
type OIDCVerifier struct {
	client *http.Client
	now    func() time.Time

	mu        sync.Mutex
	providers map[string]*oidc.Provider
}

// NewOIDCVerifier verifies tokens with keys fetched through client, which
// should reach the public internet only (internal/safehttp).
func NewOIDCVerifier(client *http.Client, now func() time.Time) *OIDCVerifier {
	if now == nil {
		now = time.Now
	}
	return &OIDCVerifier{client: client, now: now, providers: map[string]*oidc.Provider{}}
}

// VerifyToken checks raw as an ID token from issuer for audience, and answers
// its email claims.
func (v *OIDCVerifier) VerifyToken(ctx context.Context, issuer, audience, raw string) (Claims, error) {
	provider, err := v.provider(ctx, issuer)
	if err != nil {
		return Claims{}, err
	}
	// Verifier, unlike VerifierContext, shares the provider's one key set,
	// so keys are fetched once rather than for every delivery.
	token, err := provider.Verifier(&oidc.Config{ClientID: audience, Now: v.now}).Verify(ctx, raw)
	if err != nil {
		return Claims{}, refuse("the token was refused")
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified any    `json:"email_verified"`
	}
	if err := token.Claims(&claims); err != nil {
		return Claims{}, refuse("the token's claims could not be read")
	}
	// Google sends email_verified as a boolean; some issuers send a string.
	verified := claims.EmailVerified == true || claims.EmailVerified == "true"
	return Claims{Email: claims.Email, EmailVerified: verified}, nil
}

// provider discovers an issuer once. A failed discovery is not kept, so the
// next delivery tries again.
func (v *OIDCVerifier) provider(ctx context.Context, issuer string) (*oidc.Provider, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if provider, ok := v.providers[issuer]; ok {
		return provider, nil
	}
	// The client travels in the context to discovery, and go-oidc keeps it
	// for the key set it builds from it.
	discoverCtx := ctx
	if v.client != nil {
		discoverCtx = oidc.ClientContext(ctx, v.client)
	}
	provider, err := oidc.NewProvider(discoverCtx, issuer)
	if err != nil {
		return nil, fmt.Errorf("%w: discover the issuer's keys: %v", errUnavailable, err)
	}
	v.providers[issuer] = provider
	return provider, nil
}

// bearer reads the one Bearer token in an Authorization header.
func bearer(header http.Header) (string, bool) {
	values := header.Values("Authorization")
	if len(values) != 1 {
		return "", false
	}
	scheme, token, ok := strings.Cut(values[0], " ")
	token = strings.TrimSpace(token)
	return token, ok && strings.EqualFold(scheme, "Bearer") && token != ""
}

// verifyJWT checks a jwt_push delivery: a token the scheme's issuer signed,
// for the endpoint's audience, naming the configured account as its verified
// email.
func (s *Service) verifyJWT(ctx context.Context, endpoint Endpoint, d *delivery) error {
	verify := endpoint.Scheme.Verify
	raw, ok := bearer(d.header)
	if !ok {
		return refuse("no bearer token")
	}
	email, ok := s.expand(verify.Email)
	if !ok {
		// The deployment is missing a value it was told to have; the
		// delivery is not at fault, so the app is asked to retry.
		return fmt.Errorf("%w: the email a %s token must name is not configured", errUnavailable, endpoint.Toolkit)
	}
	audience := strings.ReplaceAll(verify.Audience, "{endpoint_url}", s.URL(endpoint.ID))
	claims, err := s.tokens.VerifyToken(ctx, verify.Issuer, audience, raw)
	switch {
	case errors.Is(err, errUnavailable):
		return err
	case err != nil:
		return refuse("the token was refused")
	case email != "" && !(claims.EmailVerified && strings.EqualFold(claims.Email, email)):
		return refuse("the token names another account")
	}
	return nil
}

// expand fills a {config:name} value from the deployment's configuration. A
// value without one is answered as it is. It answers false for a name that is
// not configured.
func (s *Service) expand(value string) (string, bool) {
	name, isConfig := strings.CutPrefix(value, "{config:")
	if !isConfig {
		return value, true
	}
	name, closed := strings.CutSuffix(name, "}")
	configured := s.values[name]
	return configured, closed && configured != ""
}
