package authn

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
)

type fakeVerifier struct {
	claims Claims
	err    error
	raw    string
}

func (v *fakeVerifier) Verify(_ context.Context, raw string) (Claims, error) {
	v.raw = raw
	return v.claims, v.err
}

func TestBearerAuthenticatorReturnsVerifiedCognitoSubject(t *testing.T) {
	t.Parallel()
	verifier := &fakeVerifier{claims: Claims{Subject: "user-1", ClientID: "client", TokenUse: "access"}}
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("authorization", "Bearer signed-token")
	member, err := (BearerAuthenticator{Verifier: verifier}).Authenticate(request)
	if err != nil || member != "user-1" || verifier.raw != "signed-token" {
		t.Fatalf("member/raw/error = %q/%q/%v", member, verifier.raw, err)
	}
}

func TestBearerAuthenticatorRejectsMissingAndInvalidTokens(t *testing.T) {
	t.Parallel()
	verifier := &fakeVerifier{err: errors.New("invalid signature")}
	if _, err := (BearerAuthenticator{Verifier: verifier}).Authenticate(httptest.NewRequest("GET", "/", nil)); err == nil {
		t.Fatal("missing bearer token accepted")
	}
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("authorization", "Bearer bad")
	if _, err := (BearerAuthenticator{Verifier: verifier}).Authenticate(request); err == nil {
		t.Fatal("invalid bearer token accepted")
	}
}
