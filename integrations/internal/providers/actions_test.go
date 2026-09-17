package providers

import (
	"testing"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

// TestProviderBaseURL checks where an action calls a provider reached through
// the organization's own address or account. Getting this wrong sends a
// credential to the wrong host, so every way a connection can carry the
// address is covered.
func TestProviderBaseURL(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		credential domain.Credential
		preferred  string
		want       string
	}{
		{
			name:       "a bare app domain, as Zendesk names its subdomain",
			credential: domain.Credential{Domain: "acme"},
			want:       "acme",
		},
		{
			name:       "a whole origin as the app domain",
			credential: domain.Credential{Domain: "https://acme.atlassian.net"},
			want:       "https://acme.atlassian.net",
		},
		{
			name:       "the address the provider reported with the token",
			credential: domain.Credential{Domain: "login.salesforce.com", Metadata: map[string]string{"instance_url": "https://acme.my.salesforce.com"}},
			want:       "https://acme.my.salesforce.com",
		},
		{
			name:       "the reported address wins over a domain that is only the login host",
			credential: domain.Credential{Domain: "login.salesforce.com", Metadata: map[string]string{"instance_url": "https://acme--uat.sandbox.my.salesforce.com"}},
			want:       "https://acme--uat.sandbox.my.salesforce.com",
		},
		{
			name:       "a caller names the field it wants read first",
			credential: domain.Credential{Metadata: map[string]string{"instance_url": "https://instance.example", "base_url": "https://base.example"}},
			preferred:  "base_url",
			want:       "https://base.example",
		},
		{
			name:       "a preferred field that is absent falls back",
			credential: domain.Credential{Metadata: map[string]string{"instance_url": "https://instance.example"}},
			preferred:  "base_url",
			want:       "https://instance.example",
		},
		{
			name:       "surrounding space and a trailing slash are trimmed",
			credential: domain.Credential{Metadata: map[string]string{"instance_url": "  https://acme.example/  "}},
			want:       "https://acme.example",
		},
		{
			name:       "a connection that carries neither answers nothing",
			credential: domain.Credential{},
			want:       "",
		},
		{
			name:       "empty metadata values do not win over a domain",
			credential: domain.Credential{Domain: "acme", Metadata: map[string]string{"instance_url": "", "base_url": "  "}},
			want:       "acme",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ProviderBaseURL(testCase.credential, testCase.preferred); got != testCase.want {
				t.Fatalf("ProviderBaseURL() = %q, want %q", got, testCase.want)
			}
		})
	}
}
