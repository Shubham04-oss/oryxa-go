package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"
)

func loginTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
func signedLoginToken(t *testing.T, key *rsa.PrivateKey, issuer, nonce string) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", "test"))
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"iss": issuer, "aud": "cli", "sub": "user", "exp": time.Now().Add(15 * time.Minute).Unix(), "iat": time.Now().Unix(), "nonce": nonce, "token_use": "id"})
	signed, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	token, err := signed.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return token
}
func writeTestJWKS(writer http.ResponseWriter, key *rsa.PrivateKey) {
	json.NewEncoder(writer).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}})
}
func TestLoginIDTokenVerifiesSignatureIssuerAudienceAndRetainedNonce(t *testing.T) {
	key := loginTestKey(t)
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeTestJWKS(w, key) }))
	defer provider.Close()
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, provider.Client())
	config := loginConfig{Issuer: provider.URL, ClientID: "cli"}
	valid := signedLoginToken(t, key, provider.URL, "retained")
	if err := verifyLoginIDToken(ctx, config, valid, "retained"); err != nil {
		t.Fatal(err)
	}
	for name, token := range map[string]string{"nonce": signedLoginToken(t, key, provider.URL, "other"), "issuer": signedLoginToken(t, key, "https://other.example", "retained"), "signature": signedLoginToken(t, loginTestKey(t), provider.URL, "retained")} {
		t.Run(name, func(t *testing.T) {
			if err := verifyLoginIDToken(ctx, config, token, "retained"); err == nil {
				t.Fatal("mismatched token accepted")
			}
		})
	}
	config.ClientID = "different-client"
	if err := verifyLoginIDToken(ctx, config, valid, "retained"); err == nil {
		t.Fatal("wrong audience accepted")
	}
}
