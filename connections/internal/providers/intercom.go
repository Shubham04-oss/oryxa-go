package providers

import (
	"context"
	"fmt"
	"net/http"
	"regexp"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// intercomVersion is the Intercom API version connections calls: the version
// the Intercom client in internal/providerapi is generated from.
const intercomVersion = "2.16"

// intercomAuthorizationHostPattern is one of Intercom's regional authorization
// hosts: app.intercom.com for the US, app.eu.intercom.com and
// app.au.intercom.com.
var intercomAuthorizationHostPattern = regexp.MustCompile(`^app(?:\.(?:eu|au))?\.intercom\.com$`)

// Intercom has no OAuth scopes in the flow: an app's permissions are set on it
// in Intercom's Developer Hub. Its access tokens do not expire, so there is
// nothing to refresh. Each region has its own authorization host, which an app
// names as its domain; tokens and API calls go to api.intercom.io, which routes
// them to the workspace's region. A connection is an admin in a workspace, and
// Intercom's only way to end a grant uninstalls the app from the whole
// workspace, so disconnecting leaves it installed.
func Intercom() Provider {
	return Provider{
		Name:            "intercom",
		CredentialTypes: []domain.CredentialType{domain.CredentialOAuth2, domain.CredentialBearer},
		OAuth2: &OAuth2{
			AuthorizationURL: "https://{domain}/oauth",
			TokenURL:         "https://api.intercom.io/auth/eagle/token",
			APIURL:           "https://api.intercom.io",
			Identity:         intercomIdentity,
		},
		Keys: map[domain.CredentialType]KeyAuth{
			// An access token for the app's own workspace.
			domain.CredentialBearer: {APIURL: "https://api.intercom.io", Identity: intercomAdmin},
		},
		// Sending a member to the US host lets them pick their region, but not
		// when they sign in to Intercom with Google.
		Domain: &DomainRule{Pattern: intercomAuthorizationHostPattern, Example: "app.eu.intercom.com", Default: "app.intercom.com"},
	}
}

func intercomIdentity(ctx context.Context, client *http.Client, apiURL string, grant Grant) (domain.ProviderAccount, error) {
	return intercomAdmin(ctx, client, apiURL, bearerCredential(grant.AccessToken))
}

// intercomAdmin reads the admin a credential acts as, and their workspace.
func intercomAdmin(ctx context.Context, client *http.Client, apiURL string, apply func(*http.Request)) (domain.ProviderAccount, error) {
	var admin struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
		App   struct {
			IDCode string `json:"id_code"`
			Name   string `json:"name"`
		} `json:"app"`
	}
	header := http.Header{"Intercom-Version": {intercomVersion}}
	if err := callJSON(ctx, client, http.MethodGet, apiURL+"/me", apply, header, nil, &admin); err != nil {
		return domain.ProviderAccount{}, err
	}
	if admin.ID == "" || admin.App.IDCode == "" {
		return domain.ProviderAccount{}, fmt.Errorf("%w: Intercom account is incomplete", domain.ErrProviderUnavailable)
	}
	name := firstNonEmpty(admin.Email, admin.Name, admin.ID)
	if admin.App.Name != "" {
		name += " (" + admin.App.Name + ")"
	}
	return domain.ProviderAccount{ID: admin.App.IDCode + ":" + admin.ID, Name: name}, nil
}
