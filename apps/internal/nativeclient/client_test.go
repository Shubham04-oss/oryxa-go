package nativeclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fixedSigner struct {
	audience, purpose string
}

func (signer *fixedSigner) Token(_ context.Context, audience, purpose string) (string, error) {
	signer.audience, signer.purpose = audience, purpose
	return "service-token", nil
}

func TestConnectSendsTheSignedRequestAndAnswersTheURL(t *testing.T) {
	t.Parallel()
	var got ConnectRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/connect" || r.Header.Get("Authorization") != "Bearer service-token" {
			t.Errorf("request = %s %s %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"url":"https://github.com/login/oauth/authorize?state=x"}`))
	}))
	defer server.Close()
	signer := &fixedSigner{}
	client, err := New(Config{BaseURL: server.URL + "/", Signer: signer})
	if err != nil {
		t.Fatal(err)
	}
	want := ConnectRequest{Org: "org_1", Member: "usr_1", App: "github", Level: "personal", ReturnTo: "https://app.example/done"}
	url, err := client.Connect(context.Background(), want)
	if err != nil || url != "https://github.com/login/oauth/authorize?state=x" {
		t.Fatalf("Connect() = %q, %v", url, err)
	}
	if got != want || signer.audience != Audience || signer.purpose != ConnectPurpose {
		t.Fatalf("sent %+v as %q/%q", got, signer.audience, signer.purpose)
	}
}

func TestConnectMapsTheServicesRefusals(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		status int
		code   string
		want   error
	}{
		"not set up":  {http.StatusConflict, "not_set_up", ErrNotSetUp},
		"broken app":  {http.StatusConflict, "app_broken", ErrBroken},
		"unknown app": {http.StatusNotFound, "not_found", ErrNotFound},
		"invalid":     {http.StatusBadRequest, "invalid_request", ErrInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte(`{"error":{"code":"` + testCase.code + `","message":"words"}}`))
			}))
			defer server.Close()
			client, err := New(Config{BaseURL: server.URL, Signer: &fixedSigner{}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Connect(context.Background(), ConnectRequest{}); !errors.Is(err, testCase.want) {
				t.Fatalf("Connect() error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestConnectDoesNotFollowARedirectWithItsToken(t *testing.T) {
	t.Parallel()
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the redirect target was called")
	}))
	defer elsewhere.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, Signer: &fixedSigner{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Connect(context.Background(), ConnectRequest{}); err == nil {
		t.Fatal("Connect() followed a redirect")
	}
}

func TestNewRefusesAURLAServiceTokenShouldNotTravelOn(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"", "http://integrations.internal", "ftp://localhost", "https://user:pw@host", "https://host?x=1", "/relative"} {
		if _, err := New(Config{BaseURL: raw, Signer: &fixedSigner{}}); err == nil {
			t.Errorf("New(%q) accepted the URL", raw)
		}
	}
	if _, err := New(Config{BaseURL: "https://integrations.oryxa.in", Signer: &fixedSigner{}}); err != nil {
		t.Errorf("https refused: %v", err)
	}
	if _, err := New(Config{BaseURL: "https://integrations.oryxa.in"}); err == nil || !strings.Contains(err.Error(), "signer") {
		t.Errorf("no signer: %v", err)
	}
}
