package providers

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

func TestKeyToken(t *testing.T) {
	provider := Provider{
		Name: "acme",
		Keys: map[domain.CredentialType]KeyAuth{
			domain.CredentialAPIKey: {Placement: domain.TokenPlacement{In: "query", Name: "api_key"}},
			domain.CredentialBearer: {}, // no configured placement: falls back to the default
			domain.CredentialBasic:  {Placement: domain.TokenPlacement{In: "header", Name: "X-Auth", Prefix: "Token "}},
		},
	}
	for _, tc := range []struct {
		name           string
		credentialType domain.CredentialType
		credential     domain.CredentialInput
		wantValue      string
		wantPlacement  domain.TokenPlacement
	}{
		{"api_key always uses the provider's placement", domain.CredentialAPIKey,
			domain.CredentialInput{APIKey: "key-1"}, "key-1", domain.TokenPlacement{In: "query", Name: "api_key"}},
		{"bearer defaults to a Bearer Authorization header", domain.CredentialBearer,
			domain.CredentialInput{Token: "token-1"}, "token-1", domain.TokenPlacement{In: "header", Name: "Authorization", Prefix: "Bearer "}},
		{"basic uses the provider's configured placement", domain.CredentialBasic,
			domain.CredentialInput{Username: "user", Password: "pass"},
			base64.StdEncoding.EncodeToString([]byte("user:pass")), domain.TokenPlacement{In: "header", Name: "X-Auth", Prefix: "Token "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, placement, err := KeyToken(provider, tc.credentialType, tc.credential)
			if err != nil || value != tc.wantValue || placement != tc.wantPlacement {
				t.Fatalf("KeyToken() = %q %+v %v, want %q %+v", value, placement, err, tc.wantValue, tc.wantPlacement)
			}
		})
	}
	if _, _, err := KeyToken(provider, domain.CredentialOAuth2, domain.CredentialInput{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("oauth2 KeyToken() error = %v, want %v", err, domain.ErrInvalid)
	}
	unsupported := Provider{Name: "acme"}
	if _, _, err := KeyToken(unsupported, domain.CredentialAPIKey, domain.CredentialInput{APIKey: "x"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unsupported provider KeyToken() error = %v, want %v", err, domain.ErrInvalid)
	}
}

func TestApplyPlacementQuery(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "https://example.test/x?existing=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	applyPlacement(domain.TokenPlacement{In: "query", Name: "api_key"}, "secret")(request)
	if request.URL.Query().Get("api_key") != "secret" || request.URL.Query().Get("existing") != "1" {
		t.Fatalf("query = %v", request.URL.Query())
	}
}

func TestKeyIdentityGitHubBearer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/user" || request.Header.Get("Authorization") != "Bearer pat-token" ||
			request.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			t.Errorf("identity request = %s %v", request.URL.Path, request.Header)
		}
		_, _ = io.WriteString(writer, `{"id":42,"login":"octocat"}`)
	}))
	defer server.Close()

	provider := GitHub()
	keys := provider.Keys[domain.CredentialBearer]
	keys.APIURL = server.URL
	provider.Keys = map[domain.CredentialType]KeyAuth{domain.CredentialBearer: keys}

	account, err := testClient(server).KeyIdentity(context.Background(), provider, domain.CredentialBearer, domain.CredentialInput{Token: "pat-token"})
	if err != nil || account != (domain.ProviderAccount{ID: "42", Name: "octocat"}) {
		t.Fatalf("KeyIdentity() = %+v %v", account, err)
	}
}

func TestKeyIdentityLinearAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		// Linear personal API keys carry no "Bearer " prefix.
		if request.URL.Path != "/graphql" || request.Header.Get("Authorization") != "lin_api_key123" {
			t.Errorf("identity request = %s %v", request.URL.Path, request.Header)
		}
		_, _ = io.WriteString(writer, `{"data":{"viewer":{"id":"v1","name":"ada","displayName":"Ada"},"organization":{"name":"Oryxa"}}}`)
	}))
	defer server.Close()

	provider := Linear()
	keys := provider.Keys[domain.CredentialAPIKey]
	keys.APIURL = server.URL
	provider.Keys = map[domain.CredentialType]KeyAuth{domain.CredentialAPIKey: keys}

	account, err := testClient(server).KeyIdentity(context.Background(), provider, domain.CredentialAPIKey, domain.CredentialInput{APIKey: "lin_api_key123"})
	if err != nil || account != (domain.ProviderAccount{ID: "v1", Name: "Ada (Oryxa)"}) {
		t.Fatalf("KeyIdentity() = %+v %v", account, err)
	}
}

func TestKeyIdentityErrorsAreMappedAndNeverLeak(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Header.Get("Authorization") {
		case "Bearer rejected":
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(writer, "secret-value")
		default:
			writer.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(writer, "secret-value")
		}
	}))
	defer server.Close()

	provider := GitHub()
	keys := provider.Keys[domain.CredentialBearer]
	keys.APIURL = server.URL
	provider.Keys = map[domain.CredentialType]KeyAuth{domain.CredentialBearer: keys}
	client := testClient(server)

	_, err := client.KeyIdentity(context.Background(), provider, domain.CredentialBearer, domain.CredentialInput{Token: "rejected"})
	if !errors.Is(err, domain.ErrCredentialRejected) || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("rejected token error = %v", err)
	}
	_, err = client.KeyIdentity(context.Background(), provider, domain.CredentialBearer, domain.CredentialInput{Token: "other"})
	if !errors.Is(err, domain.ErrProviderUnavailable) || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("unavailable error = %v", err)
	}
}

func TestKeyIdentityGuards(t *testing.T) {
	if _, err := (Client{}).KeyIdentity(context.Background(), Provider{Name: "acme"}, domain.CredentialAPIKey, domain.CredentialInput{APIKey: "x"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unsupported credential type error = %v, want %v", err, domain.ErrInvalid)
	}
	noIdentity := Provider{Name: "acme", Keys: map[domain.CredentialType]KeyAuth{
		domain.CredentialAPIKey: {Placement: domain.TokenPlacement{In: "header", Name: "X-Key"}},
	}}
	if _, err := (Client{}).KeyIdentity(context.Background(), noIdentity, domain.CredentialAPIKey, domain.CredentialInput{APIKey: "x"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("no identity check error = %v, want %v", err, domain.ErrInvalid)
	}
}

func TestKeyIdentityRejectsIncompleteAccount(t *testing.T) {
	provider := Provider{Name: "acme", Keys: map[domain.CredentialType]KeyAuth{
		domain.CredentialAPIKey: {
			Placement: domain.TokenPlacement{In: "header", Name: "X-Key"},
			Identity: func(context.Context, *http.Client, string, func(*http.Request)) (domain.ProviderAccount, error) {
				return domain.ProviderAccount{}, nil
			},
		},
	}}
	if _, err := (Client{}).KeyIdentity(context.Background(), provider, domain.CredentialAPIKey, domain.CredentialInput{APIKey: "x"}); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("incomplete account error = %v, want %v", err, domain.ErrProviderUnavailable)
	}
}
