package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/passwordflow"
)

type fakeCLIPassword struct {
	fakePasswordAuth
	email, name, password, verified string
}

func (fake *fakeCLIPassword) SignUp(_ context.Context, email, password, name string) error {
	fake.email, fake.password, fake.name = email, password, name
	return nil
}

func (fake *fakeCLIPassword) ConfirmEmail(_ context.Context, token string) error {
	fake.verified = token
	return nil
}

func TestCLIPasswordFlowReturnsOnlyShortLivedToken(t *testing.T) {
	passwords := &fakeCLIPassword{fakePasswordAuth: fakePasswordAuth{session: passwordflow.Session{
		AccessToken: "access-only", RefreshToken: "refresh-secret", ExpiresIn: 900,
		User: domain.User{ID: "u1", Email: "creator@example.com"},
	}}}
	server := New(fakeVerifier{}, &fakeAuthFlow{}, passwords, &fakeAuthService{}, Config{AllowedOrigins: []string{"https://app.example.com"}})
	request := func(path, body string, origin string) *httptest.ResponseRecorder {
		t.Helper()
		input := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		input.Header.Set("Content-Type", "application/json")
		if origin != "" {
			input.Header.Set("Origin", origin)
		}
		response := httptest.NewRecorder()
		server.ServeHTTP(response, input)
		return response
	}
	signup := request("/v1/auth/cli/password/signup", `{"email":"creator@example.com","password":"secret","display_name":"Creator"}`, "")
	if signup.Code != http.StatusAccepted || passwords.email != "creator@example.com" || passwords.name != "Creator" || passwords.password != "secret" {
		t.Fatalf("signup status=%d or creator fields were incorrect", signup.Code)
	}
	verification := request("/v1/auth/cli/password/verify", `{"token":"email-token"}`, "")
	if verification.Code != http.StatusNoContent || passwords.verified != "email-token" {
		t.Fatalf("verification status=%d token=%q", verification.Code, passwords.verified)
	}
	login := request("/v1/auth/cli/password/login", `{"email":"creator@example.com","password":"secret"}`, "")
	var session map[string]any
	if err := json.Unmarshal(login.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if login.Code != http.StatusOK || session["access_token"] != "access-only" || session["refresh_token"] != nil || login.Header().Get("Set-Cookie") != "" || login.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("native login status=%d body=%v headers=%v", login.Code, session, login.Header())
	}
	if response := request("/v1/auth/password/login", `{"email":"creator@example.com","password":"secret"}`, ""); response.Code != http.StatusForbidden {
		t.Fatalf("browser login without origin status=%d", response.Code)
	}
	if response := request("/v1/auth/cli/password/login", `{"email":"creator@example.com","password":"secret"}`, "https://app.example.com"); response.Code != http.StatusForbidden {
		t.Fatalf("native login with browser origin status=%d", response.Code)
	}
}
