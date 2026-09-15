package providers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// pointClientCredentialsAt aims a provider's client-credentials flow at a
// test server, the way pointAt does for an OAuth2 flow. TokenURL keeps a
// {tenant} placeholder so Mint's path-escaping is exercised end to end.
func pointClientCredentialsAt(provider Provider, server *httptest.Server) ClientCredentials {
	cc := *provider.ClientCredentials
	cc.TokenURL = server.URL + "/{tenant}/token"
	cc.AdminConsentURL = server.URL + "/consent"
	return cc
}

func TestMint(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		if request.ParseForm() != nil {
			t.Fatal("could not parse token request form")
		}
		switch request.Form.Get("client_secret") {
		case "bad-secret":
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(writer, `{"error":"invalid_client","error_description":"secret-value"}`)
		case "transient":
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(writer, `{"error":"temporarily_unavailable"}`)
		default:
			if request.Form.Get("grant_type") != "client_credentials" || request.Form.Get("client_id") != "client-1" ||
				request.Form.Get("scope") != "https://graph.microsoft.com/.default" {
				t.Errorf("token request form = %v", request.Form)
			}
			_, _ = io.WriteString(writer, `{"access_token":"app-token","token_type":"Bearer","expires_in":3599,"scope":"https://graph.microsoft.com/.default Extra.Scope"}`)
		}
	}))
	defer server.Close()

	provider := Microsoft()
	cc := pointClientCredentialsAt(provider, server)
	provider.ClientCredentials = &cc
	client := testClient(server)

	grant, err := client.Mint(context.Background(), provider, MintRequest{ClientID: "client-1", ClientSecret: "secret-1", Tenant: "contoso.onmicrosoft.com"})
	if err != nil || grant.AccessToken != "app-token" || grant.TokenType != "Bearer" ||
		!slices.Equal(grant.Scopes, []string{"https://graph.microsoft.com/.default"}) || !grant.Expiry.Equal(testNow.Add(3599*time.Second)) {
		t.Fatalf("Mint() = %+v %v", grant, err)
	}
	if gotPath != "/contoso.onmicrosoft.com/token" {
		t.Fatalf("token URL path = %q", gotPath)
	}

	_, err = client.Mint(context.Background(), provider, MintRequest{ClientID: "client-1", ClientSecret: "bad-secret", Tenant: "contoso.onmicrosoft.com"})
	if !errors.Is(err, domain.ErrCredentialRejected) || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("permanent error = %v", err)
	}

	_, err = client.Mint(context.Background(), provider, MintRequest{ClientID: "client-1", ClientSecret: "transient", Tenant: "contoso.onmicrosoft.com"})
	if !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("transient error = %v", err)
	}

	_, err = client.Mint(context.Background(), provider, MintRequest{ClientID: "client-1", ClientSecret: "secret-1", Tenant: "bad tenant!"})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid tenant error = %v, want %v", err, domain.ErrInvalid)
	}

	_, err = client.Mint(context.Background(), provider, MintRequest{ClientID: "", ClientSecret: "secret-1", Tenant: "contoso.onmicrosoft.com"})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("missing client ID error = %v, want %v", err, domain.ErrInvalid)
	}

	if _, err := client.Mint(context.Background(), Google(), MintRequest{ClientID: "x", ClientSecret: "y", Tenant: "contoso.onmicrosoft.com"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unsupported provider error = %v, want %v", err, domain.ErrInvalid)
	}
}

func TestAdminConsentURL(t *testing.T) {
	provider := Microsoft()
	cc := *provider.ClientCredentials
	cc.AdminConsentURL = "https://consent.example.test/adminconsent"
	provider.ClientCredentials = &cc

	value, err := (Client{}).AdminConsentURL(provider, AdminConsentRequest{ClientID: "client-1", RedirectURI: "https://connect.example.test/cb", State: "state-1"})
	if err != nil {
		t.Fatalf("AdminConsentURL() error = %v", err)
	}
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if parsed.Scheme != "https" || parsed.Host != "consent.example.test" || query.Get("client_id") != "client-1" ||
		query.Get("redirect_uri") != "https://connect.example.test/cb" || query.Get("state") != "state-1" ||
		query.Get("scope") != "https://graph.microsoft.com/.default" {
		t.Fatalf("AdminConsentURL() = %q", value)
	}

	for _, request := range []AdminConsentRequest{
		{RedirectURI: "https://connect.example.test/cb", State: "state-1"},
		{ClientID: "client-1", State: "state-1"},
		{ClientID: "client-1", RedirectURI: "https://connect.example.test/cb"},
	} {
		if _, err := (Client{}).AdminConsentURL(provider, request); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("AdminConsentURL(%+v) error = %v, want %v", request, err, domain.ErrInvalid)
		}
	}

	if _, err := (Client{}).AdminConsentURL(Google(), AdminConsentRequest{ClientID: "x", RedirectURI: "https://connect.example.test/cb", State: "s"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unsupported provider error = %v, want %v", err, domain.ErrInvalid)
	}
}
