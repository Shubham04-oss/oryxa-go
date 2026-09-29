package main

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/url"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

func verifyLoginIDToken(ctx context.Context, config loginConfig, raw, nonce string) error {
	issuer, err := url.Parse(config.Issuer)
	if err != nil || issuer.Scheme != "https" || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" {
		return fmt.Errorf("the server's CLI token issuer is invalid")
	}
	ctx = oidc.ClientContext(ctx, loginHTTPClient(ctx))
	keys := oidc.NewRemoteKeySet(ctx, strings.TrimRight(config.Issuer, "/")+"/.well-known/jwks.json")
	verifier := oidc.NewVerifier(config.Issuer, keys, &oidc.Config{ClientID: config.ClientID, SupportedSigningAlgs: []string{"RS256"}})
	verified, err := verifier.Verify(ctx, raw)
	if err != nil {
		return fmt.Errorf("identity provider ID token could not be verified")
	}
	var claims struct {
		Nonce string `json:"nonce"`
		Use   string `json:"token_use"`
	}
	if verified.Claims(&claims) != nil || claims.Use != "id" || nonce == "" || subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(nonce)) != 1 {
		return fmt.Errorf("identity provider ID token did not match this sign-in session")
	}
	return nil
}
