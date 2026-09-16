package authn

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

const testIssuer = "https://cognito-idp.ap-south-1.amazonaws.com/pool-1"

// signer signs RS256 tokens the way Cognito does, for a verifier that trusts
// only its key.
type signer struct {
	t   *testing.T
	key *rsa.PrivateKey
}

func newSigner(t *testing.T) signer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return signer{t: t, key: key}
}

func (s signer) verifier(clientID, agentClientID string) *Verifier {
	keys := &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&s.key.PublicKey}}
	return newVerifier(func(config *oidc.Config) *oidc.IDTokenVerifier {
		return oidc.NewVerifier(testIssuer, keys, config)
	}, clientID, agentClientID)
}

func (s signer) token(claims map[string]any) string {
	s.t.Helper()
	claims["iss"] = testIssuer
	claims["exp"] = time.Now().Add(time.Hour).Unix()
	claims["iat"] = time.Now().Unix()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "key-1"})
	payload, err := json.Marshal(claims)
	if err != nil {
		s.t.Fatal(err)
	}
	signed := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signed))
	signature, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, digest[:])
	if err != nil {
		s.t.Fatal(err)
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func accessToken(s signer, clientID string) string {
	return s.token(map[string]any{"sub": "member-1", "client_id": clientID, "token_use": "access", "scope": "openid"})
}

func TestVerifyAcceptsTheWebClientAndTheAgentClient(t *testing.T) {
	s := newSigner(t)
	verifier := s.verifier("web-client", "agent-client")

	for _, clientID := range []string{"web-client", "agent-client"} {
		claims, err := verifier.Verify(context.Background(), accessToken(s, clientID))
		if err != nil || claims.Subject != "member-1" || claims.ClientID != clientID {
			t.Fatalf("Verify(%s token) = %+v, %v", clientID, claims, err)
		}
	}
	if _, err := verifier.Verify(context.Background(), accessToken(s, "another-client")); err == nil {
		t.Fatal("Verify() accepted a token issued to another app client")
	}
}

func TestVerifyRefusesAgentTokensWithoutAnAgentClient(t *testing.T) {
	s := newSigner(t)
	verifier := s.verifier("web-client", "")

	if _, err := verifier.Verify(context.Background(), accessToken(s, "agent-client")); err == nil {
		t.Fatal("Verify() accepted an agent token with no agent client configured")
	}
	if _, err := verifier.Verify(context.Background(), accessToken(s, "")); err == nil {
		t.Fatal("Verify() accepted a token with no client_id")
	}
}

func TestVerifyAgentIdentityToken(t *testing.T) {
	s := newSigner(t)
	verifier := s.verifier("web-client", "agent-client")
	identity := func(audience, nonce string) string {
		// No email: an agent grant's ID token needs nothing of the profile.
		return s.token(map[string]any{"sub": "member-1", "aud": audience, "nonce": nonce, "token_use": "id"})
	}

	subject, err := verifier.VerifyAgentIdentityToken(context.Background(), identity("agent-client", "nonce-1"), "nonce-1")
	if err != nil || subject != "member-1" {
		t.Fatalf("VerifyAgentIdentityToken() = %q, %v", subject, err)
	}
	for name, test := range map[string]struct{ token, nonce string }{
		"the web client's ID token": {identity("web-client", "nonce-1"), "nonce-1"},
		"another nonce":             {identity("agent-client", "nonce-2"), "nonce-1"},
		"no expected nonce":         {identity("agent-client", ""), ""},
		"no token":                  {"", "nonce-1"},
	} {
		if _, err := verifier.VerifyAgentIdentityToken(context.Background(), test.token, test.nonce); err == nil {
			t.Fatalf("VerifyAgentIdentityToken() accepted %s", name)
		}
	}
	if _, err := s.verifier("web-client", "").VerifyAgentIdentityToken(context.Background(), identity("agent-client", "nonce-1"), "nonce-1"); err == nil {
		t.Fatal("VerifyAgentIdentityToken() accepted a token with no agent client configured")
	}
	// The web client's own identity check still refuses the agent client's
	// ID token.
	if _, err := verifier.VerifyIdentityToken(context.Background(), identity("agent-client", "nonce-1"), "nonce-1"); err == nil {
		t.Fatal("VerifyIdentityToken() accepted the agent client's ID token")
	}
}
