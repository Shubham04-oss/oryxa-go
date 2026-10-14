package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

type fakeVerifier struct {
	claims serviceauth.Claims
	err    error
}

func (verifier fakeVerifier) Verify(context.Context, string) (serviceauth.Claims, error) {
	return verifier.claims, verifier.err
}

func call(server *Server, token string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/v1/internal/ping", nil)
	if token != "" {
		request.Header.Set(serviceauth.Header, token)
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	return recorder
}

func serverWith(verifier ServiceVerifier) (*Server, *bool) {
	reached := false
	server := New(verifier, nil)
	server.Internal("GET /v1/internal/ping", func(writer http.ResponseWriter, request *http.Request) {
		reached = true
		if claims, ok := Claims(request.Context()); !ok || claims.Service != "apps" {
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	return server, &reached
}

func TestHealthzNeedsNoToken(t *testing.T) {
	server, _ := serverWith(nil)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("healthz answered %d", recorder.Code)
	}
}

func TestAnInternalCallIsRefusedUnlessItsTokenVerifies(t *testing.T) {
	tests := []struct {
		name     string
		verifier ServiceVerifier
		token    string
		want     int
	}{
		{"unsigned", fakeVerifier{claims: serviceauth.Claims{Service: "apps"}}, "", http.StatusUnauthorized},
		{"no callers configured", nil, "token", http.StatusUnauthorized},
		{"invalid token", fakeVerifier{err: serviceauth.ErrInvalidToken}, "token", http.StatusUnauthorized},
		{"purpose not allowed", fakeVerifier{err: serviceauth.ErrPurposeNotAllowed}, "token", http.StatusForbidden},
		{"key service down", fakeVerifier{err: errors.New("kms down")}, "token", http.StatusServiceUnavailable},
		{"verified", fakeVerifier{claims: serviceauth.Claims{Service: "apps"}}, "token", http.StatusNoContent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, reached := serverWith(test.verifier)
			if got := call(server, test.token).Code; got != test.want {
				t.Fatalf("answered %d, want %d", got, test.want)
			}
			if *reached != (test.want == http.StatusNoContent) {
				t.Fatalf("handler reached = %v", *reached)
			}
		})
	}
}

func TestARealVerifierWithNoCallersRefusesAnyToken(t *testing.T) {
	verifier, err := serviceauth.NewVerifier(serviceauth.VerifierConfig{Audience: "integrations"})
	if err != nil {
		t.Fatal(err)
	}
	server, reached := serverWith(verifier)
	if got := call(server, "a.b.c").Code; got != http.StatusUnauthorized || *reached {
		t.Fatalf("answered %d, reached %v", got, *reached)
	}
}
