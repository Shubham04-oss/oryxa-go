package application

import (
	"slices"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/providers"
)

// find returns a described provider, failing when the registry lost it.
func find(t *testing.T, descriptions []ProviderDescription, name string) ProviderDescription {
	t.Helper()
	index := slices.IndexFunc(descriptions, func(description ProviderDescription) bool {
		return description.Provider == name
	})
	if index < 0 {
		t.Fatalf("%s is missing from %d providers", name, len(descriptions))
	}
	return descriptions[index]
}

func credentialType(t *testing.T, description ProviderDescription, wanted domain.CredentialType) CredentialTypeDescription {
	t.Helper()
	index := slices.IndexFunc(description.CredentialTypes, func(candidate CredentialTypeDescription) bool {
		return candidate.CredentialType == wanted
	})
	if index < 0 {
		t.Fatalf("%s does not describe %s", description.Provider, wanted)
	}
	return description.CredentialTypes[index]
}

// TestProvidersDescribesTheRegistry checks the description against the
// registry itself, so a provider added later is described without a change
// here.
func TestProvidersDescribesTheRegistry(t *testing.T) {
	registry := providers.Builtin()
	service := &Service{providers: registry}
	descriptions := service.Providers()

	if len(descriptions) != len(registry) {
		t.Fatalf("described %d of %d providers", len(descriptions), len(registry))
	}
	for index := 1; index < len(descriptions); index++ {
		if descriptions[index-1].Provider >= descriptions[index].Provider {
			t.Fatalf("providers are not ordered by name: %s before %s", descriptions[index-1].Provider, descriptions[index].Provider)
		}
	}

	for _, description := range descriptions {
		provider, ok := registry.Lookup(description.Provider)
		if !ok {
			t.Fatalf("%s is not in the registry", description.Provider)
		}
		if len(description.CredentialTypes) == 0 {
			t.Fatalf("%s describes no credential type", description.Provider)
		}
		if description.CredentialTypes[0].CredentialType != provider.CredentialTypes[0] {
			t.Fatalf("%s leads with %s, not the default %s", description.Provider,
				description.CredentialTypes[0].CredentialType, provider.CredentialTypes[0])
		}
		for _, credential := range description.CredentialTypes {
			if !provider.Supports(credential.CredentialType) {
				t.Fatalf("%s describes unsupported %s", description.Provider, credential.CredentialType)
			}
			rules := provider.ScopesFor(credential.CredentialType)
			if !slices.Equal(credential.RequiredScopes, rules.Required) || !slices.Equal(credential.AllowedScopes, rules.Ceiling) {
				t.Fatalf("%s %s scopes = %v of %v, want %v of %v", description.Provider, credential.CredentialType,
					credential.RequiredScopes, credential.AllowedScopes, rules.Required, rules.Ceiling)
			}
			if credential.Authorizes != credential.CredentialType.Authorizes() {
				t.Fatalf("%s %s authorizes = %t", description.Provider, credential.CredentialType, credential.Authorizes)
			}
			if wantsDomain := provider.NeedsDomain(credential.CredentialType); wantsDomain != (credential.Domain != nil) {
				t.Fatalf("%s %s needs a domain = %t, described %+v", description.Provider,
					credential.CredentialType, wantsDomain, credential.Domain)
			}
			// A credential the member supplies names its fields; one made
			// through a redirect has none to name.
			if credential.CredentialType.KeyBased() && len(credential.Fields) == 0 {
				t.Fatalf("%s %s names no credential fields", description.Provider, credential.CredentialType)
			}
			if credential.Authorizes && len(credential.Fields) > 0 {
				t.Fatalf("%s %s is a redirect but names %v", description.Provider, credential.CredentialType, credential.Fields)
			}
		}
	}
}

// TestProvidersDescribeWhatEnablingTakes covers the cases a caller's form is
// built from: a domain an app must name, a domain it may leave to a default,
// an exclusive scope, and the fields of a key.
func TestProvidersDescribeWhatEnablingTakes(t *testing.T) {
	service := &Service{providers: providers.Builtin()}
	descriptions := service.Providers()

	atlassian := credentialType(t, find(t, descriptions, "atlassian"), domain.CredentialBasic)
	if atlassian.Domain == nil || !atlassian.Domain.Required || atlassian.Domain.Example == "" || atlassian.Domain.Default != "" {
		t.Fatalf("atlassian basic domain = %+v", atlassian.Domain)
	}
	if !slices.Equal(atlassian.Fields, []string{"username", "password"}) || atlassian.Client {
		t.Fatalf("atlassian basic = %+v", atlassian)
	}

	salesforce := credentialType(t, find(t, descriptions, "salesforce"), domain.CredentialOAuth2)
	if salesforce.Domain == nil || salesforce.Domain.Required || salesforce.Domain.Default != "login.salesforce.com" {
		t.Fatalf("salesforce oauth2 domain = %+v", salesforce.Domain)
	}
	if !salesforce.Client || !salesforce.Authorizes {
		t.Fatalf("salesforce oauth2 = %+v", salesforce)
	}

	asana := credentialType(t, find(t, descriptions, "asana"), domain.CredentialOAuth2)
	if !slices.Contains(asana.ExclusiveScopes, "default") {
		t.Fatalf("asana exclusive scopes = %v", asana.ExclusiveScopes)
	}

	github := find(t, descriptions, "github")
	if github.CredentialTypes[0].CredentialType != domain.CredentialOAuth2 {
		t.Fatalf("github leads with %s", github.CredentialTypes[0].CredentialType)
	}
	if bearer := credentialType(t, github, domain.CredentialBearer); !slices.Equal(bearer.Fields, []string{"token"}) ||
		bearer.Domain != nil || len(bearer.AllowedScopes) > 0 {
		t.Fatalf("github bearer = %+v", bearer)
	}
	if installation := credentialType(t, github, domain.CredentialInstallation); !installation.Client || !installation.Authorizes {
		t.Fatalf("github installation = %+v", installation)
	}
}
