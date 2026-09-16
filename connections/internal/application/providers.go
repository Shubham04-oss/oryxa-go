package application

// This file describes the built-in providers for callers that offer them to
// people, such as the web app's tool settings. The description holds nothing
// of an organization's: it is the registry read aloud, so that a caller need
// not keep its own copy of what each provider takes.

import (
	"cmp"
	"slices"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/providers"
)

// ProviderDescription is a built-in provider and the ways it can be enabled.
type ProviderDescription struct {
	Provider string `json:"provider"`
	// CredentialTypes are the ways connections to the provider authenticate.
	// The first is what an app that names no credential type gets.
	CredentialTypes []CredentialTypeDescription `json:"credential_types"`
}

// CredentialTypeDescription is one way to enable a provider: what the app
// registers, what a connection supplies, and the scopes an admin may allow.
type CredentialTypeDescription struct {
	CredentialType domain.CredentialType `json:"credential_type"`
	// Client is whether the app carries a client the organization registers
	// with the provider, as OAuth 2.0 and installation apps do.
	Client bool `json:"client"`
	// Authorizes is whether members connect through a provider redirect
	// rather than by supplying a secret of their own.
	Authorizes bool `json:"authorizes"`
	// Fields are the credential fields a connection supplies when it is not
	// made through a redirect, such as username and password for basic.
	Fields []string `json:"fields,omitempty"`
	// RequiredScopes are part of every app's allowed scopes, directly or
	// through a broader scope. AllowedScopes is everything an app may allow,
	// and is empty where the credential type carries no scopes. Exclusive
	// scopes may be allowed only on their own.
	RequiredScopes  []string `json:"required_scopes,omitempty"`
	AllowedScopes   []string `json:"allowed_scopes,omitempty"`
	ExclusiveScopes []string `json:"exclusive_scopes,omitempty"`
	// Domain is the address of the organization's own that the provider is
	// reached at, where this credential type needs one.
	Domain *DomainDescription `json:"domain,omitempty"`
}

// DomainDescription is what an app's domain must look like.
type DomainDescription struct {
	// Required is whether the app must name a domain. A provider with a
	// default serves most customers at it.
	Required bool `json:"required"`
	// Example is a valid domain, to show alongside the field.
	Example string `json:"example"`
	// Default is what an app that names no domain gets.
	Default string `json:"default,omitempty"`
}

// Providers describes the built-in providers, ordered by name. It reads only
// the registry, so it takes neither a caller nor the store.
func (s *Service) Providers() []ProviderDescription {
	descriptions := make([]ProviderDescription, 0, len(s.providers))
	for _, provider := range s.providers {
		descriptions = append(descriptions, describeProvider(provider))
	}
	slices.SortFunc(descriptions, func(first, second ProviderDescription) int {
		return cmp.Compare(first.Provider, second.Provider)
	})
	return descriptions
}

// describeProvider keeps the provider's own order of credential types, so the
// first stays the one an app that names none gets.
func describeProvider(provider providers.Provider) ProviderDescription {
	description := ProviderDescription{Provider: provider.Name}
	for _, credentialType := range provider.CredentialTypes {
		if !provider.Supports(credentialType) {
			continue
		}
		description.CredentialTypes = append(description.CredentialTypes, describeCredentialType(provider, credentialType))
	}
	return description
}

func describeCredentialType(provider providers.Provider, credentialType domain.CredentialType) CredentialTypeDescription {
	rules := provider.ScopesFor(credentialType)
	description := CredentialTypeDescription{
		CredentialType:  credentialType,
		Client:          usesClient(credentialType),
		Authorizes:      credentialType.Authorizes(),
		Fields:          credentialFields(credentialType),
		RequiredScopes:  rules.Required,
		AllowedScopes:   rules.Ceiling,
		ExclusiveScopes: rules.Exclusive,
	}
	if provider.NeedsDomain(credentialType) && provider.Domain != nil {
		description.Domain = &DomainDescription{
			Required: provider.Domain.Default == "",
			Example:  provider.Domain.Example,
			Default:  provider.Domain.Default,
		}
	}
	return description
}

// credentialFields names the domain.CredentialInput fields a connection of a
// credential type supplies. Types made through a redirect supply none.
func credentialFields(credentialType domain.CredentialType) []string {
	switch credentialType {
	case domain.CredentialAPIKey:
		return []string{"api_key"}
	case domain.CredentialBearer:
		return []string{"token"}
	case domain.CredentialBasic:
		return []string{"username", "password"}
	case domain.CredentialOAuth2ClientCredentials:
		return []string{"tenant"}
	default:
		return nil
	}
}
