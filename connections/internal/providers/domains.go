package providers

// This file is per-organization domains: providers reached at an address of
// the organization's own, such as an Atlassian site. A provider's URLs name
// {domain}; each app supplies its domain once, and every flow for the app runs
// against the provider with that domain filled in.

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

const domainPlaceholder = "{domain}"

// DomainRule is what an app's domain must be.
type DomainRule struct {
	// Pattern is what a domain must match in full, lowercase. It must admit
	// only hostnames or hostname labels.
	Pattern *regexp.Regexp
	// Example is a valid domain, shown when one is missing or does not match.
	Example string
	// Default is the domain of an app that gives none, where one domain serves
	// most customers, such as Salesforce's login.salesforce.com. Empty means
	// every app gives its own.
	Default string
}

// NeedsDomain reports whether apps of a credential type supply a domain: any
// URL the type uses names {domain}.
func (provider Provider) NeedsDomain(credentialType domain.CredentialType) bool {
	for _, endpoint := range provider.endpoints(credentialType) {
		if strings.Contains(endpoint, domainPlaceholder) {
			return true
		}
	}
	return false
}

// AppDomain checks the domain an app of a credential type gives, trimmed and
// lowercased. When the type needs one, an app that gives none gets the rule's
// default, or is refused without one; other types take no domain.
func (provider Provider) AppDomain(credentialType domain.CredentialType, value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if !provider.NeedsDomain(credentialType) {
		if value != "" {
			return "", fmt.Errorf("%s %s apps take no domain: %w", provider.Name, credentialType, domain.ErrInvalid)
		}
		return "", nil
	}
	if provider.Domain == nil || provider.Domain.Pattern == nil {
		return "", fmt.Errorf("%s has no rule for its domain: %w", provider.Name, domain.ErrInvalid)
	}
	if value == "" {
		value = provider.Domain.Default
	}
	if !provider.Domain.Pattern.MatchString(value) {
		return "", fmt.Errorf("%s %s apps need a domain such as %s: %w", provider.Name, credentialType, provider.Domain.Example, domain.ErrInvalid)
	}
	return value, nil
}

// WithDomain returns the provider with every {domain} in its URLs replaced by
// value, which AppDomain has accepted. The result shares nothing it changes
// with provider, so a registry's providers stay as they are.
func (provider Provider) WithDomain(value string) Provider {
	if value == "" {
		return provider
	}
	fill := func(endpoint string) string { return strings.ReplaceAll(endpoint, domainPlaceholder, value) }
	if provider.OAuth2 != nil {
		flow := *provider.OAuth2
		flow.AuthorizationURL, flow.TokenURL, flow.APIURL = fill(flow.AuthorizationURL), fill(flow.TokenURL), fill(flow.APIURL)
		flow.RefreshURL, flow.RevokeURL = fill(flow.RefreshURL), fill(flow.RevokeURL)
		provider.OAuth2 = &flow
	}
	if provider.ClientCredentials != nil {
		credentials := *provider.ClientCredentials
		credentials.TokenURL, credentials.AdminConsentURL = fill(credentials.TokenURL), fill(credentials.AdminConsentURL)
		provider.ClientCredentials = &credentials
	}
	if provider.Installation != nil {
		installation := *provider.Installation
		installation.InstallURL, installation.APIURL = fill(installation.InstallURL), fill(installation.APIURL)
		provider.Installation = &installation
	}
	if provider.Keys != nil {
		keys := make(map[domain.CredentialType]KeyAuth, len(provider.Keys))
		for credentialType, auth := range provider.Keys {
			auth.APIURL = fill(auth.APIURL)
			keys[credentialType] = auth
		}
		provider.Keys = keys
	}
	return provider
}

// endpoints returns the URLs apps of a credential type use.
func (provider Provider) endpoints(credentialType domain.CredentialType) []string {
	var endpoints []string
	if provider.OAuth2 != nil && (credentialType == domain.CredentialOAuth2 || credentialType == domain.CredentialInstallation) {
		endpoints = append(endpoints, provider.OAuth2.AuthorizationURL, provider.OAuth2.TokenURL, provider.OAuth2.RefreshURL,
			provider.OAuth2.APIURL, provider.OAuth2.RevokeURL)
	}
	switch {
	case credentialType == domain.CredentialInstallation && provider.Installation != nil:
		endpoints = append(endpoints, provider.Installation.InstallURL, provider.Installation.APIURL)
	case credentialType == domain.CredentialOAuth2ClientCredentials && provider.ClientCredentials != nil:
		endpoints = append(endpoints, provider.ClientCredentials.TokenURL, provider.ClientCredentials.AdminConsentURL)
	case credentialType.KeyBased():
		endpoints = append(endpoints, provider.Keys[credentialType].APIURL)
	}
	return endpoints
}
