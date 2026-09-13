package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/authn"
)

type fakeVerifier struct {
	claims authn.Claims
	err    error
}

func (f fakeVerifier) Verify(_ context.Context, _ string) (authn.Claims, error) {
	return f.claims, f.err
}

func TestMeRejectsMissingBearerToken(t *testing.T) {
	handler := New(fakeVerifier{}, Config{})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/auth/me", nil))

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestMeReturnsVerifiedSubject(t *testing.T) {
	handler := New(fakeVerifier{claims: authn.Claims{Subject: "user-1", Scope: "openid email"}}, Config{})
	request := httptest.NewRequest(http.MethodGet, "/v1/auth/me", nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestMeRejectsInvalidToken(t *testing.T) {
	handler := New(fakeVerifier{err: errors.New("bad token")}, Config{})
	request := httptest.NewRequest(http.MethodGet, "/v1/auth/me", nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}
