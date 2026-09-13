package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/actionflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/credentials"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/oauthflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/triggerflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhookroutes"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhooks"
)

var ErrUnauthenticated = errors.New("authentication required")

const maxRequestBody = 1024 * 1024

type Authenticator interface {
	Authenticate(*http.Request) (memberID string, err error)
}

type MemberCatalog interface {
	MemberIntegrations(context.Context, string, string) ([]domain.MemberIntegration, error)
}

type OAuthLifecycle interface {
	RegisterBYOC(context.Context, oauthflow.RegisterBYOCRequest) (domain.OAuthAppProfile, error)
	RotateBYOC(context.Context, oauthflow.RotateBYOCRequest) (domain.OAuthAppProfile, error)
	DisableOAuthApp(context.Context, oauthflow.DisableOAuthAppRequest) error
	Begin(context.Context, oauthflow.BeginRequest) (oauthflow.BeginResult, error)
	Complete(context.Context, oauthflow.CompleteRequest) (oauthflow.CompleteResult, error)
	Disconnect(context.Context, oauthflow.DisconnectRequest) error
}

type WebhookRouteLifecycle interface {
	Register(context.Context, webhookroutes.RegisterRequest) (webhookroutes.Registration, error)
}

type TriggerLifecycle interface {
	Create(context.Context, triggerflow.CreateRequest) (domain.Trigger, error)
	Disable(context.Context, string, string, string) error
}

type WebhookAdmission interface {
	Admit(context.Context, webhooks.Request) (webhooks.Decision, error)
}

type ActionLifecycle interface {
	Run(context.Context, actionflow.RunRequest) (actionflow.RunResult, error)
}

type Dependencies struct {
	Authenticator Authenticator
	Catalog       MemberCatalog
	OAuth         OAuthLifecycle
	WebhookRoutes WebhookRouteLifecycle
	Triggers      TriggerLifecycle
	Webhooks      WebhookAdmission
	Actions       ActionLifecycle
}

type Server struct {
	dependencies Dependencies
	handler      http.Handler
}

// New retains the small read-only constructor used by existing callers.
func New(authenticator Authenticator, catalog MemberCatalog) *Server {
	return NewAPI(Dependencies{Authenticator: authenticator, Catalog: catalog})
}

func NewAPI(dependencies Dependencies) *Server {
	server := &Server{dependencies: dependencies}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/organizations/{organizationID}/integrations", server.listMemberIntegrations)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/integration-apps", server.registerOAuthApp)
	mux.HandleFunc("PATCH /v1/organizations/{organizationID}/integration-apps/{profileID}", server.rotateOAuthApp)
	mux.HandleFunc("DELETE /v1/organizations/{organizationID}/integration-apps/{profileID}", server.disableOAuthApp)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/integration-apps/{profileID}/webhook-routes", server.registerWebhookRoute)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/connections/authorize", server.beginAuthorization)
	mux.HandleFunc("GET /v1/oauth/callback/{provider}", server.completeAuthorization)
	mux.HandleFunc("DELETE /v1/organizations/{organizationID}/connections/{connectionID}", server.disconnect)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/triggers", server.createTrigger)
	mux.HandleFunc("DELETE /v1/organizations/{organizationID}/triggers/{triggerID}", server.disableTrigger)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/connections/{connectionID}/actions/{actionName}", server.runAction)
	mux.HandleFunc("POST /v1/webhooks/{opaqueRoute}", server.admitWebhook)
	server.handler = mux
	return server
}

func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	s.handler.ServeHTTP(writer, request)
}

func (s *Server) authenticate(writer http.ResponseWriter, request *http.Request) (string, bool) {
	if s.dependencies.Authenticator == nil {
		writeError(writer, http.StatusNotImplemented, "not_configured", "endpoint is not configured")
		return "", false
	}
	memberID, err := s.dependencies.Authenticator.Authenticate(request)
	if err != nil || memberID == "" {
		writeError(writer, http.StatusUnauthorized, "unauthenticated", "authentication is required")
		return "", false
	}
	return memberID, true
}

func (s *Server) listMemberIntegrations(writer http.ResponseWriter, request *http.Request) {
	memberID, ok := s.authenticate(writer, request)
	if !ok {
		return
	}
	if s.dependencies.Catalog == nil {
		writeError(writer, http.StatusNotImplemented, "not_configured", "endpoint is not configured")
		return
	}
	items, err := s.dependencies.Catalog.MemberIntegrations(request.Context(), request.PathValue("organizationID"), memberID)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	if items == nil {
		items = []domain.MemberIntegration{}
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) registerOAuthApp(writer http.ResponseWriter, request *http.Request) {
	actorID, ok := s.authenticate(writer, request)
	if !ok {
		return
	}
	if s.dependencies.OAuth == nil {
		writeError(writer, 501, "not_configured", "endpoint is not configured")
		return
	}
	var body struct {
		Provider     string   `json:"provider"`
		ClientID     string   `json:"client_id"`
		ClientSecret string   `json:"client_secret"`
		Scopes       []string `json:"scopes"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeError(writer, 400, "invalid_request", "request body is invalid")
		return
	}
	profile, err := s.dependencies.OAuth.RegisterBYOC(request.Context(), oauthflow.RegisterBYOCRequest{
		OrganizationID: request.PathValue("organizationID"), ActorID: actorID,
		Provider: body.Provider, ClientID: body.ClientID, ClientSecret: body.ClientSecret, Scopes: body.Scopes,
	})
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, safeProfile(profile))
}

func (s *Server) registerWebhookRoute(writer http.ResponseWriter, request *http.Request) {
	actorID, ok := s.authenticate(writer, request)
	if !ok {
		return
	}
	if s.dependencies.WebhookRoutes == nil {
		writeError(writer, 501, "not_configured", "endpoint is not configured")
		return
	}
	var body struct {
		TriggerName   string `json:"trigger_name"`
		SigningSecret string `json:"signing_secret"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeError(writer, 400, "invalid_request", "request body is invalid")
		return
	}
	result, err := s.dependencies.WebhookRoutes.Register(request.Context(), webhookroutes.RegisterRequest{
		OrganizationID: request.PathValue("organizationID"), ActorID: actorID,
		ProfileID: request.PathValue("profileID"), TriggerName: body.TriggerName, SigningSecret: body.SigningSecret,
	})
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, result)
}

func (s *Server) rotateOAuthApp(writer http.ResponseWriter, request *http.Request) {
	actorID, ok := s.authenticate(writer, request)
	if !ok {
		return
	}
	if s.dependencies.OAuth == nil {
		writeError(writer, 501, "not_configured", "endpoint is not configured")
		return
	}
	var body struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeError(writer, 400, "invalid_request", "request body is invalid")
		return
	}
	profile, err := s.dependencies.OAuth.RotateBYOC(request.Context(), oauthflow.RotateBYOCRequest{OrganizationID: request.PathValue("organizationID"), ActorID: actorID, ProfileID: request.PathValue("profileID"), ClientID: body.ClientID, ClientSecret: body.ClientSecret})
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, safeProfile(profile))
}

func (s *Server) disableOAuthApp(writer http.ResponseWriter, request *http.Request) {
	actorID, ok := s.authenticate(writer, request)
	if !ok {
		return
	}
	if s.dependencies.OAuth == nil {
		writeError(writer, 501, "not_configured", "endpoint is not configured")
		return
	}
	if err := s.dependencies.OAuth.DisableOAuthApp(request.Context(), oauthflow.DisableOAuthAppRequest{OrganizationID: request.PathValue("organizationID"), ActorID: actorID, ProfileID: request.PathValue("profileID")}); err != nil {
		writeServiceError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) beginAuthorization(writer http.ResponseWriter, request *http.Request) {
	memberID, ok := s.authenticate(writer, request)
	if !ok {
		return
	}
	if s.dependencies.OAuth == nil {
		writeError(writer, 501, "not_configured", "endpoint is not configured")
		return
	}
	var body struct {
		ProfileID string `json:"profile_id"`
		ReturnURI string `json:"return_uri"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeError(writer, 400, "invalid_request", "request body is invalid")
		return
	}
	result, err := s.dependencies.OAuth.Begin(request.Context(), oauthflow.BeginRequest{
		OrganizationID: request.PathValue("organizationID"), MemberID: memberID,
		ProfileID: body.ProfileID, ReturnURI: body.ReturnURI,
	})
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"authorization_url": result.AuthorizationURL, "expires_at": result.ExpiresAt})
}

func (s *Server) completeAuthorization(writer http.ResponseWriter, request *http.Request) {
	if s.dependencies.OAuth == nil {
		writeError(writer, 501, "not_configured", "endpoint is not configured")
		return
	}
	result, err := s.dependencies.OAuth.Complete(request.Context(), oauthflow.CompleteRequest{
		Provider: request.PathValue("provider"), State: request.URL.Query().Get("state"), Code: request.URL.Query().Get("code"), ProviderError: request.URL.Query().Get("error"),
	})
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	destination, err := url.Parse(result.ReturnURI)
	if err != nil || !destination.IsAbs() {
		writeError(writer, 500, "internal_error", "request could not be completed")
		return
	}
	query := destination.Query()
	query.Set("integration", "connected")
	query.Set("connection_id", result.Connection.ID)
	destination.RawQuery = query.Encode()
	http.Redirect(writer, request, destination.String(), http.StatusSeeOther)
}

func (s *Server) createTrigger(writer http.ResponseWriter, request *http.Request) {
	memberID, ok := s.authenticate(writer, request)
	if !ok {
		return
	}
	if s.dependencies.Triggers == nil {
		writeError(writer, 501, "not_configured", "endpoint is not configured")
		return
	}
	var body struct {
		ConnectionID string          `json:"connection_id"`
		TriggerName  string          `json:"trigger_name"`
		Props        json.RawMessage `json:"props"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeError(writer, 400, "invalid_request", "request body is invalid")
		return
	}
	trigger, err := s.dependencies.Triggers.Create(request.Context(), triggerflow.CreateRequest{
		OrganizationID: request.PathValue("organizationID"), MemberID: memberID,
		ConnectionID: body.ConnectionID, TriggerName: body.TriggerName, Props: body.Props,
	})
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, safeTrigger(trigger))
}

func (s *Server) disconnect(writer http.ResponseWriter, request *http.Request) {
	memberID, ok := s.authenticate(writer, request)
	if !ok {
		return
	}
	if s.dependencies.OAuth == nil {
		writeError(writer, 501, "not_configured", "endpoint is not configured")
		return
	}
	if err := s.dependencies.OAuth.Disconnect(request.Context(), oauthflow.DisconnectRequest{OrganizationID: request.PathValue("organizationID"), MemberID: memberID, ConnectionID: request.PathValue("connectionID")}); err != nil {
		writeServiceError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) disableTrigger(writer http.ResponseWriter, request *http.Request) {
	memberID, ok := s.authenticate(writer, request)
	if !ok {
		return
	}
	if s.dependencies.Triggers == nil {
		writeError(writer, 501, "not_configured", "endpoint is not configured")
		return
	}
	err := s.dependencies.Triggers.Disable(request.Context(), request.PathValue("organizationID"), memberID, request.PathValue("triggerID"))
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) runAction(writer http.ResponseWriter, request *http.Request) {
	memberID, ok := s.authenticate(writer, request)
	if !ok {
		return
	}
	if s.dependencies.Actions == nil {
		writeError(writer, 501, "not_configured", "endpoint is not configured")
		return
	}
	var body struct {
		Props json.RawMessage `json:"props"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeError(writer, 400, "invalid_request", "request body is invalid")
		return
	}
	result, err := s.dependencies.Actions.Run(request.Context(), actionflow.RunRequest{
		OrganizationID: request.PathValue("organizationID"), MemberID: memberID,
		ConnectionID: request.PathValue("connectionID"), ActionName: request.PathValue("actionName"),
		IdempotencyKey: request.Header.Get("Idempotency-Key"), Props: body.Props,
	})
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) admitWebhook(writer http.ResponseWriter, request *http.Request) {
	if s.dependencies.Webhooks == nil {
		writeError(writer, 501, "not_configured", "endpoint is not configured")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, maxRequestBody+1))
	if err != nil {
		writeError(writer, 400, "invalid_request", "request body could not be read")
		return
	}
	if len(raw) > maxRequestBody {
		writeError(writer, 413, "payload_too_large", "webhook payload is too large")
		return
	}
	body, err := parseBody(request.Header.Get("content-type"), raw)
	if err != nil {
		writeError(writer, 400, "invalid_request", "webhook payload is invalid")
		return
	}
	decision, err := s.dependencies.Webhooks.Admit(request.Context(), webhooks.Request{
		OpaqueRoute: request.PathValue("opaqueRoute"), Method: request.Method,
		Headers: flattenHeaders(request.Header), Query: firstQueryValues(request.URL.Query()), Body: body, RawBody: raw,
	})
	if err != nil {
		code := "webhook_rejected"
		if errors.Is(err, webhooks.ErrPayloadTooLarge) {
			code = "payload_too_large"
		}
		if decision.Status == 0 {
			decision.Status = 500
		}
		writeError(writer, decision.Status, code, "webhook could not be accepted")
		return
	}
	for key, value := range decision.Headers {
		if safeResponseHeader(key) {
			writer.Header().Set(key, value)
		}
	}
	if decision.Body == nil {
		writer.WriteHeader(decision.Status)
		return
	}
	if value, ok := decision.Body.(string); ok && !strings.Contains(strings.ToLower(writer.Header().Get("content-type")), "json") {
		writer.WriteHeader(decision.Status)
		_, _ = io.WriteString(writer, value)
		return
	}
	writeJSON(writer, decision.Status, decision.Body)
}

func safeProfile(profile domain.OAuthAppProfile) map[string]any {
	return map[string]any{"id": profile.ID, "organization_id": profile.OrganizationID, "provider": profile.Provider,
		"piece_name": profile.PieceName, "piece_version": profile.PieceVersion, "mode": profile.Mode,
		"client_id": profile.ClientID, "scopes": profile.Scopes, "status": profile.Status,
		"created_at": profile.CreatedAt, "updated_at": profile.UpdatedAt}
}

func safeTrigger(trigger domain.Trigger) map[string]any {
	return map[string]any{"id": trigger.ID, "organization_id": trigger.OrganizationID, "connection_id": trigger.ConnectionID,
		"provider": trigger.Provider, "trigger_name": trigger.TriggerName, "props": trigger.Props,
		"status": trigger.Status, "created_at": trigger.CreatedAt}
}

func decodeJSON(request *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxRequestBody+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func parseBody(contentType string, raw []byte) (any, error) {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType == "application/x-www-form-urlencoded" {
		values, err := url.ParseQuery(string(raw))
		if err != nil {
			return nil, err
		}
		result := make(map[string]any, len(values))
		for key, entries := range values {
			if len(entries) > 0 {
				result[key] = entries[0]
			}
		}
		return result, nil
	}
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var result any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func flattenHeaders(headers http.Header) map[string]string {
	result := make(map[string]string, len(headers))
	for key, value := range headers {
		result[key] = strings.Join(value, ",")
	}
	return result
}

func firstQueryValues(values url.Values) map[string]string {
	result := make(map[string]string, len(values))
	for key, entries := range values {
		if len(entries) > 0 {
			result[key] = entries[0]
		}
	}
	return result
}

func safeResponseHeader(key string) bool {
	switch strings.ToLower(key) {
	case "content-length", "transfer-encoding", "connection":
		return false
	}
	return true
}

func writeServiceError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, oauthflow.ErrForbidden):
		writeError(writer, 403, "forbidden", "organization permission is required")
	case errors.Is(err, actionflow.ErrForbidden):
		writeError(writer, 403, "forbidden", "connection permission is required")
	case errors.Is(err, domain.ErrNotFound):
		writeError(writer, 404, "not_found", "resource was not found")
	case errors.Is(err, actionflow.ErrIdempotencyConflict):
		writeError(writer, 409, "idempotency_conflict", "idempotency key was already used with different input")
	case errors.Is(err, actionflow.ErrActionInProgress):
		writeError(writer, 409, "action_in_progress", "action execution is still in progress")
	case errors.Is(err, actionflow.ErrActionPreviouslyFailed):
		writeError(writer, 409, "action_failed", "use a new idempotency key to explicitly retry the action")
	case errors.Is(err, domain.ErrConflict), errors.Is(err, oauthflow.ErrAlreadyConnected):
		writeError(writer, 409, "conflict", "resource already exists")
	case errors.Is(err, oauthflow.ErrInvalidInput), errors.Is(err, webhookroutes.ErrInvalidInput), errors.Is(err, triggerflow.ErrInvalidInput), errors.Is(err, actionflow.ErrInvalidInput):
		writeError(writer, 400, "invalid_request", "request is invalid")
	case errors.Is(err, oauthflow.ErrStateExpired):
		writeError(writer, 400, "state_expired", "authorization state expired")
	case errors.Is(err, oauthflow.ErrProviderMismatch):
		writeError(writer, 400, "provider_mismatch", "authorization provider does not match")
	case errors.Is(err, oauthflow.ErrProviderDenied):
		writeError(writer, 400, "provider_denied", "provider authorization was not completed")
	case errors.Is(err, triggerflow.ErrOperationDisabled):
		writeError(writer, 422, "operation_disabled", "integration operation is not enabled")
	case errors.Is(err, actionflow.ErrOperationDisabled):
		writeError(writer, 422, "operation_disabled", "integration action is not enabled")
	case errors.Is(err, actionflow.ErrRequiredScope):
		writeError(writer, 422, "scope_required", "connection requires additional authorization")
	case errors.Is(err, actionflow.ErrExecutionFailed), errors.Is(err, actionflow.ErrPieceOutputInvalid):
		writeError(writer, 502, "provider_execution_failed", "provider action could not be completed")
	case errors.Is(err, credentials.ErrReauthorizationRequired):
		writeError(writer, 409, "reauthorization_required", "connection requires fresh authorization")
	case errors.Is(err, credentials.ErrRefreshInProgress):
		writeError(writer, 409, "credential_refresh_in_progress", "connection credentials are being refreshed")
	case errors.Is(err, credentials.ErrCredentialRefreshFailed):
		writeError(writer, 502, "credential_refresh_failed", "connection credentials could not be refreshed")
	case errors.Is(err, triggerflow.ErrRouteUnavailable):
		writeError(writer, 409, "webhook_route_required", "webhook route must be configured first")
	default:
		writeError(writer, 500, "internal_error", "request could not be completed")
	}
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("content-type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
