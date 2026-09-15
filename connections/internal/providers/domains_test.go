package providers

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

func TestDomains(t *testing.T) {
	provider := Provider{
		Name:            "zendesk",
		CredentialTypes: []domain.CredentialType{domain.CredentialOAuth2, domain.CredentialAPIKey, domain.CredentialBearer},
		OAuth2: &OAuth2{
			AuthorizationURL: "https://{domain}.zendesk.test/oauth/authorizations/new",
			TokenURL:         "https://{domain}.zendesk.test/oauth/tokens",
			APIURL:           "https://{domain}.zendesk.test/api/v2",
			RevokeURL:        "https://{domain}.zendesk.test/oauth/revoke",
		},
		Keys: map[domain.CredentialType]KeyAuth{
			domain.CredentialAPIKey: {APIURL: "https://{domain}.zendesk.test/api/v2"},
			domain.CredentialBearer: {APIURL: "https://api.zendesk.test"},
		},
		Domain: &DomainRule{Pattern: regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`), Example: "acme"},
	}

	// A domain is needed only where the credential type's URLs name one.
	if !provider.NeedsDomain(domain.CredentialOAuth2) || !provider.NeedsDomain(domain.CredentialAPIKey) || provider.NeedsDomain(domain.CredentialBearer) {
		t.Fatal("NeedsDomain does not follow the URLs each credential type uses")
	}
	if value, err := provider.AppDomain(domain.CredentialOAuth2, " ACME "); err != nil || value != "acme" {
		t.Fatalf("AppDomain() = %q %v, want acme", value, err)
	}
	for name, test := range map[string]struct {
		credentialType domain.CredentialType
		value          string
	}{
		"missing":                   {domain.CredentialOAuth2, ""},
		"a full URL":                {domain.CredentialOAuth2, "https://acme.zendesk.test"},
		"a host where a label goes": {domain.CredentialAPIKey, "acme.zendesk.test"},
		"a path":                    {domain.CredentialAPIKey, "acme/../admin"},
		"given where none is taken": {domain.CredentialBearer, "acme"},
	} {
		if _, err := provider.AppDomain(test.credentialType, test.value); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%s: AppDomain(%s, %q) error = %v, want invalid", name, test.credentialType, test.value, err)
		}
	}
	if _, err := (Provider{Name: "broken", OAuth2: &OAuth2{APIURL: "https://{domain}"}}).AppDomain(domain.CredentialOAuth2, "acme"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("AppDomain() with no rule error = %v", err)
	}

	filled := provider.WithDomain("acme")
	for _, endpoint := range []string{filled.OAuth2.AuthorizationURL, filled.OAuth2.TokenURL, filled.OAuth2.APIURL, filled.OAuth2.RevokeURL, filled.Keys[domain.CredentialAPIKey].APIURL} {
		if !strings.HasPrefix(endpoint, "https://acme.zendesk.test/") {
			t.Errorf("filled endpoint = %q", endpoint)
		}
	}
	if filled.Keys[domain.CredentialBearer].APIURL != "https://api.zendesk.test" {
		t.Errorf("an endpoint without a domain changed to %q", filled.Keys[domain.CredentialBearer].APIURL)
	}
	// Filling a domain leaves the registry's provider as it was.
	if provider.OAuth2.APIURL != "https://{domain}.zendesk.test/api/v2" || provider.Keys[domain.CredentialAPIKey].APIURL != "https://{domain}.zendesk.test/api/v2" {
		t.Fatal("WithDomain() changed the provider it was called on")
	}
	if unchanged := provider.WithDomain(""); unchanged.OAuth2 != provider.OAuth2 {
		t.Fatal("WithDomain(\"\") copied a provider with nothing to fill")
	}

	// A rule with a default gives it to an app that names no domain, and only
	// where the credential type needs one.
	provider.Domain.Default = "support"
	for credentialType, want := range map[domain.CredentialType]string{domain.CredentialOAuth2: "support", domain.CredentialBearer: ""} {
		if value, err := provider.AppDomain(credentialType, " "); err != nil || value != want {
			t.Errorf("AppDomain(%s) without a domain = %q %v, want %q", credentialType, value, err, want)
		}
	}
	if value, err := provider.AppDomain(domain.CredentialOAuth2, "acme"); err != nil || value != "acme" {
		t.Errorf("AppDomain() with a domain = %q %v, want acme", value, err)
	}
	if _, err := provider.AppDomain(domain.CredentialOAuth2, "acme.zendesk.test"); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("AppDomain() with an invalid domain error = %v, want invalid despite the default", err)
	}
}
