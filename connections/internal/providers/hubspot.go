package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// HubSpot grants reach one HubSpot account, its portal. A connection is named
// by the portal, whose refresh token does not expire and may be shared by
// everyone who connected the app to that portal, so disconnecting one
// connection leaves it alone; uninstalling the app in HubSpot ends it.
func HubSpot() Provider {
	return Provider{
		Name:            "hubspot",
		CredentialTypes: []domain.CredentialType{domain.CredentialOAuth2, domain.CredentialBearer},
		Scopes: ScopeRules{
			// The scopes HubSpot lists for apps, without the deprecated ones.
			Ceiling: []string{
				"account-info.security.read", "analytics.behavioral_events.send", "automation",
				"automation.sequences.enrollments.write", "automation.sequences.read", "behavioral_events.event_definitions.read_write",
				"business-intelligence", "business_units_view.read", "cms.domains.read", "cms.domains.write", "cms.functions.read",
				"cms.functions.write", "cms.knowledge_base.articles.publish", "cms.knowledge_base.articles.read",
				"cms.knowledge_base.articles.write", "cms.knowledge_base.settings.read", "cms.knowledge_base.settings.write",
				"cms.membership.access_groups.read", "cms.membership.access_groups.write", "collector.graphql_query.execute",
				"collector.graphql_schema.read", "communication_preferences.read", "communication_preferences.read_write",
				"communication_preferences.statuses.batch.read", "communication_preferences.statuses.batch.write",
				"communication_preferences.write", "content", "conversations.custom_channels.read",
				"conversations.custom_channels.write", "conversations.read", "conversations.visitor_identification.tokens.create",
				"conversations.write", "crm.dealsplits.read_write", "crm.export", "crm.import", "crm.lists.read", "crm.lists.write",
				"crm.objects.appointments.read", "crm.objects.appointments.sensitive.read", "crm.objects.appointments.sensitive.write",
				"crm.objects.appointments.write", "crm.objects.carts.read", "crm.objects.carts.write",
				"crm.objects.commercepayments.read", "crm.objects.companies.highly_sensitive.read",
				"crm.objects.companies.highly_sensitive.write", "crm.objects.companies.read", "crm.objects.companies.sensitive.read",
				"crm.objects.companies.sensitive.write", "crm.objects.companies.write", "crm.objects.contacts.highly_sensitive.read",
				"crm.objects.contacts.highly_sensitive.write", "crm.objects.contacts.read", "crm.objects.contacts.sensitive.read",
				"crm.objects.contacts.sensitive.write", "crm.objects.contacts.write", "crm.objects.courses.read",
				"crm.objects.courses.write", "crm.objects.custom.highly_sensitive.read", "crm.objects.custom.highly_sensitive.write",
				"crm.objects.custom.read", "crm.objects.custom.sensitive.read", "crm.objects.custom.sensitive.write",
				"crm.objects.custom.write", "crm.objects.deals.highly_sensitive.read", "crm.objects.deals.highly_sensitive.write",
				"crm.objects.deals.read", "crm.objects.deals.sensitive.read", "crm.objects.deals.sensitive.write",
				"crm.objects.deals.write", "crm.objects.feedback_submission.read", "crm.objects.goals.read",
				"crm.objects.invoices.read", "crm.objects.leads.read", "crm.objects.leads.write", "crm.objects.line_items.read",
				"crm.objects.line_items.write", "crm.objects.listings.read", "crm.objects.listings.write",
				"crm.objects.marketing_events.read", "crm.objects.marketing_events.write", "crm.objects.orders.read",
				"crm.objects.orders.write", "crm.objects.owners.read", "crm.objects.partner-clients.read",
				"crm.objects.partner-clients.write", "crm.objects.partner-services.read", "crm.objects.partner-services.write",
				"crm.objects.quotes.read", "crm.objects.quotes.write", "crm.objects.services.read", "crm.objects.services.write",
				"crm.objects.subscriptions.read", "crm.objects.users.read", "crm.objects.users.write", "crm.pipelines.orders.read",
				"crm.pipelines.orders.write", "crm.schemas.appointments.read", "crm.schemas.appointments.write",
				"crm.schemas.carts.read", "crm.schemas.carts.write", "crm.schemas.commercepayments.read", "crm.schemas.companies.read",
				"crm.schemas.companies.write", "crm.schemas.contacts.read", "crm.schemas.contacts.write", "crm.schemas.courses.read",
				"crm.schemas.courses.write", "crm.schemas.custom.read", "crm.schemas.deals.read", "crm.schemas.deals.write",
				"crm.schemas.invoices.read", "crm.schemas.invoices.write", "crm.schemas.line_items.read", "crm.schemas.listings.read",
				"crm.schemas.listings.write", "crm.schemas.orders.read", "crm.schemas.orders.write", "crm.schemas.quotes.read",
				"crm.schemas.quotes.write", "crm.schemas.services.read", "crm.schemas.services.write", "crm.schemas.subscriptions.read",
				"crm.schemas.subscriptions.write", "ctas.read", "external_integrations.forms.access", "files", "files.ui_hidden.read",
				"forms", "forms-uploaded-files", "hubdb", "marketing-email", "marketing.campaigns.read",
				"marketing.campaigns.revenue.read", "marketing.campaigns.write", "media_bridge.read", "media_bridge.write", "oauth",
				"sales-email-read", "scheduler.meetings.meeting-link.read", "settings.billing.write", "settings.currencies.read",
				"settings.currencies.write", "settings.users.read", "settings.users.teams.read", "settings.users.teams.write",
				"settings.users.write", "tax_rates.read", "tickets", "tickets.highly_sensitive", "tickets.sensitive", "timeline",
				"transactional-email",
			},
			// Every HubSpot app has oauth, which the identity check needs.
			Required: []string{"oauth"},
		},
		OAuth2: &OAuth2{
			AuthorizationURL: "https://app.hubspot.com/oauth/authorize",
			// The dated OAuth API; v1 stops working on 2027-02-16.
			TokenURL:            "https://api.hubspot.com/oauth/2026-09/token",
			APIURL:              "https://api.hubapi.com",
			RequireRefreshToken: true,
			PermanentErrors:     []string{"invalid_client", "invalid_grant"},
			DecodeToken:         decodeHubSpotToken,
			Metadata:            []string{"hub_id", "user_id"},
			Identity:            hubspotIdentity,
		},
		Keys: map[domain.CredentialType]KeyAuth{
			// A private app's access token.
			domain.CredentialBearer: {APIURL: "https://api.hubapi.com", Identity: hubspotPortal},
		},
	}
}

// decodeHubSpotToken reads HubSpot's token response, which lists the granted
// scopes as an array under scopes rather than a scope string.
func decodeHubSpotToken(body []byte) (TokenResponse, error) {
	token, err := decodeStandardToken(body)
	if err != nil {
		return TokenResponse{}, err
	}
	var granted struct {
		Scopes []string `json:"scopes"`
	}
	if err := json.Unmarshal(body, &granted); err != nil {
		return TokenResponse{}, err
	}
	if token.Scope == "" {
		token.Scope = strings.Join(granted.Scopes, " ")
	}
	return token, nil
}

func hubspotIdentity(ctx context.Context, client *http.Client, apiURL string, grant Grant) (domain.ProviderAccount, error) {
	return hubspotPortal(ctx, client, apiURL, bearerCredential(grant.AccessToken))
}

// hubspotPortal reads the portal a credential reaches.
func hubspotPortal(ctx context.Context, client *http.Client, apiURL string, apply func(*http.Request)) (domain.ProviderAccount, error) {
	var portal struct {
		PortalID   int64  `json:"portalId"`
		PortalName string `json:"portalName"`
	}
	if err := callJSON(ctx, client, http.MethodGet, apiURL+"/account-info/2026-09/details", apply, nil, nil, &portal); err != nil {
		return domain.ProviderAccount{}, err
	}
	if portal.PortalID <= 0 {
		return domain.ProviderAccount{}, fmt.Errorf("%w: HubSpot account is incomplete", domain.ErrProviderUnavailable)
	}
	return domain.ProviderAccount{ID: strconv.FormatInt(portal.PortalID, 10), Name: strings.TrimSpace(portal.PortalName)}, nil
}
