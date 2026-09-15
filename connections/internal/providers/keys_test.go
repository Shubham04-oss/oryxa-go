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

func TestKeyIdentityAsanaBearer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/users/me" || request.Header.Get("Authorization") != "Bearer 2/1204/1205:abc" {
			t.Errorf("identity request = %s %v", request.URL.Path, request.Header)
		}
		_, _ = io.WriteString(writer, `{"data":{"gid":"4673218951","email":"ada@example.test","name":"Ada Lovelace"}}`)
	}))
	defer server.Close()

	provider := Asana()
	keys := provider.Keys[domain.CredentialBearer]
	keys.APIURL = server.URL
	provider.Keys = map[domain.CredentialType]KeyAuth{domain.CredentialBearer: keys}

	account, err := testClient(server).KeyIdentity(context.Background(), provider, domain.CredentialBearer, domain.CredentialInput{Token: "2/1204/1205:abc"})
	if err != nil || account != (domain.ProviderAccount{ID: "4673218951", Name: "ada@example.test"}) {
		t.Fatalf("KeyIdentity() = %+v %v", account, err)
	}
}

func TestKeyIdentityClickUpAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		// ClickUp personal API tokens carry no "Bearer " prefix.
		if request.URL.Path != "/user" || request.Header.Get("Authorization") != "pk_183_ABC" {
			t.Errorf("identity request = %s %v", request.URL.Path, request.Header)
		}
		_, _ = io.WriteString(writer, `{"user":{"id":183,"username":"ada","email":"ada@example.test"}}`)
	}))
	defer server.Close()

	provider := ClickUp()
	keys := provider.Keys[domain.CredentialAPIKey]
	keys.APIURL = server.URL
	provider.Keys = map[domain.CredentialType]KeyAuth{domain.CredentialAPIKey: keys}

	account, err := testClient(server).KeyIdentity(context.Background(), provider, domain.CredentialAPIKey, domain.CredentialInput{APIKey: "pk_183_ABC"})
	if err != nil || account != (domain.ProviderAccount{ID: "183", Name: "ada@example.test"}) {
		t.Fatalf("KeyIdentity() = %+v %v", account, err)
	}
}

func TestKeyIdentityAtlassianAPIToken(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.URL.Path)
		user, password, basic := request.BasicAuth()
		if !basic || user != "ada@example.test" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case password == "jira-token" && request.URL.Path == "/rest/api/3/myself":
			_, _ = io.WriteString(writer, `{"accountId":"557058:f58131cb","emailAddress":"ada@example.test","displayName":"Ada Lovelace"}`)
		case password == "confluence-token" && request.URL.Path == "/wiki/rest/api/user/current":
			_, _ = io.WriteString(writer, `{"type":"known","accountId":"557058:f58131cb","email":"ada@example.test","publicName":"ada"}`)
		case password == "anonymous" && request.URL.Path == "/wiki/rest/api/user/current":
			_, _ = io.WriteString(writer, `{"type":"anonymous","displayName":"Anonymous"}`)
		case password == "revoked":
			writer.WriteHeader(http.StatusUnauthorized)
		default:
			// A site without Jira.
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	provider := Atlassian()
	keys := provider.Keys[domain.CredentialBasic]
	keys.APIURL = server.URL
	provider.Keys = map[domain.CredentialType]KeyAuth{domain.CredentialBasic: keys}
	identity := func(token string) (domain.ProviderAccount, error) {
		paths = nil
		return testClient(server).KeyIdentity(context.Background(), provider, domain.CredentialBasic,
			domain.CredentialInput{Username: "ada@example.test", Password: token})
	}
	want := domain.ProviderAccount{ID: "557058:f58131cb", Name: "ada@example.test"}

	if account, err := identity("jira-token"); err != nil || account != want || len(paths) != 1 {
		t.Fatalf("KeyIdentity() on a Jira site = %+v %v, paths %v", account, err, paths)
	}
	if account, err := identity("confluence-token"); err != nil || account != want || len(paths) != 2 {
		t.Fatalf("KeyIdentity() on a Confluence-only site = %+v %v, paths %v", account, err, paths)
	}
	// A refused token stops at Jira; one Confluence reads as anonymous is refused too.
	if _, err := identity("revoked"); !errors.Is(err, domain.ErrCredentialRejected) || len(paths) != 1 {
		t.Fatalf("KeyIdentity() with a revoked token error = %v, paths %v", err, paths)
	}
	if _, err := identity("anonymous"); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("KeyIdentity() read as anonymous error = %v", err)
	}
}
