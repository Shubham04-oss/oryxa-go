package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/actionflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/oauthflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/triggerflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhookroutes"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhooks"
)

type httpOAuth struct {
	registered oauthflow.RegisterBYOCRequest
	begun      oauthflow.BeginRequest
}

func (o *httpOAuth) RotateBYOC(_ context.Context, request oauthflow.RotateBYOCRequest) (domain.OAuthAppProfile, error) {
	return domain.OAuthAppProfile{ID: request.ProfileID, OrganizationID: request.OrganizationID, ClientID: request.ClientID, EncryptedSecret: domain.SealedData{Ciphertext: []byte("rotated-secret")}}, nil
}
func (o *httpOAuth) DisableOAuthApp(context.Context, oauthflow.DisableOAuthAppRequest) error {
	return nil
}

func (o *httpOAuth) RegisterBYOC(_ context.Context, request oauthflow.RegisterBYOCRequest) (domain.OAuthAppProfile, error) {
	o.registered = request
	return domain.OAuthAppProfile{ID: "app-1", OrganizationID: request.OrganizationID, Provider: request.Provider, ClientID: request.ClientID, EncryptedSecret: domain.SealedData{Ciphertext: []byte("never-return")}, Status: domain.OAuthAppStatusActive}, nil
}
func (o *httpOAuth) Begin(_ context.Context, request oauthflow.BeginRequest) (oauthflow.BeginResult, error) {
	o.begun = request
	return oauthflow.BeginResult{AuthorizationURL: "https://slack.example/authorize", ExpiresAt: time.Now().Add(time.Minute)}, nil
}
func (o *httpOAuth) Complete(context.Context, oauthflow.CompleteRequest) (oauthflow.CompleteResult, error) {
	return oauthflow.CompleteResult{Connection: domain.Connection{ID: "con-1"}, ReturnURI: "https://app.example/integrations?from=settings"}, nil
}
func (o *httpOAuth) Disconnect(context.Context, oauthflow.DisconnectRequest) error { return nil }

type httpRoutes struct{ request webhookroutes.RegisterRequest }

func (r *httpRoutes) Register(_ context.Context, request webhookroutes.RegisterRequest) (webhookroutes.Registration, error) {
	r.request = request
	return webhookroutes.Registration{OpaqueRoute: "opaque", WebhookURL: "https://connect.example/v1/webhooks/opaque", Provider: "slack", TriggerName: request.TriggerName}, nil
}

type httpTriggers struct {
	created  triggerflow.CreateRequest
	disabled []string
}

func (t *httpTriggers) Create(_ context.Context, request triggerflow.CreateRequest) (domain.Trigger, error) {
	t.created = request
	return domain.Trigger{ID: "trg-1", OrganizationID: request.OrganizationID, ConnectionID: request.ConnectionID, Provider: "slack", TriggerName: request.TriggerName, Props: request.Props, Status: domain.TriggerStatusActive}, nil
}
func (t *httpTriggers) Disable(_ context.Context, org, member, trigger string) error {
	t.disabled = []string{org, member, trigger}
	return nil
}

type httpWebhook struct{ request webhooks.Request }

func (w *httpWebhook) Admit(_ context.Context, request webhooks.Request) (webhooks.Decision, error) {
	w.request = request
	return webhooks.Decision{Status: 200, Accepted: true, Headers: map[string]string{"content-type": "text/plain", "connection": "close"}, Body: "challenge"}, nil
}

type httpActions struct{ request actionflow.RunRequest }

func (a *httpActions) Run(_ context.Context, request actionflow.RunRequest) (actionflow.RunResult, error) {
	a.request = request
	return actionflow.RunResult{ExecutionID: "act-1", Status: actionflow.ExecutionSucceeded,
		Result: json.RawMessage(`{"channel_id":"C123","message_timestamp":"171.2"}`)}, nil
}

func TestLifecycleHTTPRoutesKeepSecretsAtIngressAndPreserveWebhookBytes(t *testing.T) {
	t.Parallel()
	oauth := &httpOAuth{}
	routes := &httpRoutes{}
	triggers := &httpTriggers{}
	webhook := &httpWebhook{}
	actions := &httpActions{}
	server := NewAPI(Dependencies{Authenticator: testAuthenticator{memberID: "member-1"}, Catalog: &testCatalog{}, OAuth: oauth, WebhookRoutes: routes, Triggers: triggers, Webhooks: webhook, Actions: actions})

	response := perform(server, http.MethodPost, "/v1/organizations/org-1/integration-apps", `{"provider":"slack","client_id":"client","client_secret":"top-secret","scopes":["channels:history"]}`, nil)
	if response.Code != http.StatusCreated || oauth.registered.ActorID != "member-1" || oauth.registered.OrganizationID != "org-1" {
		t.Fatalf("registration status/request = %d/%+v", response.Code, oauth.registered)
	}
	for _, forbidden := range []string{"top-secret", "never-return", "encrypted_secret", "ciphertext"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("registration response leaked %q: %s", forbidden, response.Body.String())
		}
	}

	response = perform(server, http.MethodPost, "/v1/organizations/org-1/connections/authorize", `{"profile_id":"app-1","return_uri":"https://app.example/integrations"}`, nil)
	if response.Code != http.StatusCreated || oauth.begun.MemberID != "member-1" || oauth.begun.ProfileID != "app-1" {
		t.Fatalf("authorization status/request = %d/%+v", response.Code, oauth.begun)
	}

	response = perform(server, http.MethodGet, "/v1/oauth/callback/slack?state=state&code=code", "", nil)
	if response.Code != http.StatusSeeOther || response.Header().Get("location") != "https://app.example/integrations?connection_id=con-1&from=settings&integration=connected" {
		t.Fatalf("callback = %d/%s", response.Code, response.Header().Get("location"))
	}

	response = perform(server, http.MethodPost, "/v1/organizations/org-1/integration-apps/app-1/webhook-routes", `{"trigger_name":"new-message","signing_secret":"signing-secret"}`, nil)
	if response.Code != http.StatusCreated || routes.request.SigningSecret != "signing-secret" || strings.Contains(response.Body.String(), "signing-secret") {
		t.Fatalf("route status/request/body = %d/%+v/%s", response.Code, routes.request, response.Body.String())
	}

	response = perform(server, http.MethodPost, "/v1/organizations/org-1/triggers", `{"connection_id":"con-1","trigger_name":"new-message","props":{"ignoreBots":true}}`, nil)
	if response.Code != http.StatusCreated || triggers.created.MemberID != "member-1" || string(triggers.created.Props) != `{"ignoreBots":true}` {
		t.Fatalf("trigger status/request = %d/%+v", response.Code, triggers.created)
	}

	response = perform(server, http.MethodDelete, "/v1/organizations/org-1/triggers/trg-1", "", nil)
	if response.Code != http.StatusNoContent || strings.Join(triggers.disabled, "/") != "org-1/member-1/trg-1" {
		t.Fatalf("disable = %d/%+v", response.Code, triggers.disabled)
	}

	raw := `{"challenge":"exact bytes"}`
	response = perform(server, http.MethodPost, "/v1/webhooks/opaque?source=slack", raw, map[string]string{"content-type": "application/json", "x-slack-signature": "v0=test"})
	if response.Code != http.StatusOK || response.Body.String() != "challenge" || string(webhook.request.RawBody) != raw || webhook.request.OpaqueRoute != "opaque" || webhook.request.Query["source"] != "slack" {
		t.Fatalf("webhook response/request = %d/%q/%+v", response.Code, response.Body.String(), webhook.request)
	}
	if response.Header().Get("connection") != "" {
		t.Fatal("hop-by-hop response header was forwarded")
	}

	response = perform(server, http.MethodPost, "/v1/organizations/org-1/connections/con-1/actions/post-message", `{"props":{"channel":"C123","text":"hello"}}`, map[string]string{"Idempotency-Key": "request-123"})
	if response.Code != http.StatusOK || actions.request.MemberID != "member-1" || actions.request.ConnectionID != "con-1" || actions.request.IdempotencyKey != "request-123" || strings.Contains(response.Body.String(), "hello") {
		t.Fatalf("action response/request = %d/%s/%+v", response.Code, response.Body.String(), actions.request)
	}
}

func TestLifecycleHTTPRejectsUnknownJSONFields(t *testing.T) {
	t.Parallel()
	oauth := &httpOAuth{}
	server := NewAPI(Dependencies{Authenticator: testAuthenticator{memberID: "member-1"}, OAuth: oauth})
	response := perform(server, http.MethodPost, "/v1/organizations/org-1/integration-apps", `{"provider":"slack","client_id":"id","client_secret":"secret","organization_id":"another-org"}`, nil)
	if response.Code != http.StatusBadRequest || oauth.registered.OrganizationID != "" {
		t.Fatalf("status/request = %d/%+v", response.Code, oauth.registered)
	}
}

func perform(handler http.Handler, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		request.Header.Set("content-type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
