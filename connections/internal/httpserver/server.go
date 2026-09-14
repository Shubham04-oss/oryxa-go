// Package httpserver is connections' HTTP API. It turns requests into
// application calls and domain errors into the documented error codes.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/application"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

const maxJSONBytes = 64 << 10

type connectionsService interface {
	ListApps(context.Context, authclient.Credentials, string) (application.AppList, error)
	CreateApp(context.Context, authclient.Credentials, string, application.CreateAppInput) (domain.App, error)
	UpdateApp(context.Context, authclient.Credentials, string, string, application.UpdateAppInput) (domain.App, error)
	SetAppStatus(context.Context, authclient.Credentials, string, string, domain.AppStatus) (domain.App, error)
	DeleteApp(context.Context, authclient.Credentials, string, string) error

	ListConnections(context.Context, authclient.Credentials, string, application.ConnectionFilter) ([]domain.Connection, error)
	Authorize(context.Context, authclient.Credentials, string, application.AuthorizeInput) (application.Authorization, error)
	Callback(ctx context.Context, provider string, params application.CallbackParams) (string, error)
	Complete(context.Context, authclient.Credentials, string, string) (application.Completion, error)
	GetConnection(context.Context, authclient.Credentials, string, string) (domain.Connection, error)
	Reconnect(context.Context, authclient.Credentials, string, string, string) (application.Authorization, error)
	Disconnect(context.Context, authclient.Credentials, string, string) error
	CreateConnection(context.Context, authclient.Credentials, string, application.CreateConnectionInput) (domain.Connection, error)
	ReplaceCredential(context.Context, authclient.Credentials, string, string, domain.CredentialInput) (domain.Connection, error)
	UpdateConnection(context.Context, authclient.Credentials, string, string, application.UpdateConnectionInput) (domain.Connection, error)

	IssueToken(context.Context, application.Actor, string, application.TokenRequest) (domain.IssuedToken, error)
	InternalConnection(context.Context, application.Actor, string, string) (domain.Connection, error)
	AddHold(context.Context, application.Actor, string, string, string) error
	ReleaseHold(context.Context, application.Actor, string, string, string) error
	Report(context.Context, application.Actor, string, string) error
}

type Config struct {
	// AllowedOrigins may send cookie-authenticated mutations.
	AllowedOrigins []string
	// Services verifies a service token naming connections as its audience.
	// Nil means service tokens are not accepted, and internal routes only
	// take forwarded member credentials.
	Services ServiceVerifier
}

type Server struct {
	service        connectionsService
	allowedOrigins map[string]struct{}
	services       ServiceVerifier
	logger         *slog.Logger
	mux            *http.ServeMux
}

func New(service connectionsService, config Config, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	server := &Server{
		service:        service,
		allowedOrigins: map[string]struct{}{},
		services:       config.Services,
		logger:         logger,
		mux:            http.NewServeMux(),
	}
	for _, origin := range config.AllowedOrigins {
		if parsed, err := url.Parse(origin); err == nil && parsed.Scheme != "" && parsed.Host != "" {
			server.allowedOrigins[parsed.Scheme+"://"+parsed.Host] = struct{}{}
		}
	}
	server.mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})

	server.mux.HandleFunc("GET /v1/organizations/{organizationID}/apps", server.listApps)
	server.mux.HandleFunc("POST /v1/organizations/{organizationID}/apps", server.createApp)
	server.mux.HandleFunc("PATCH /v1/organizations/{organizationID}/apps/{appID}", server.updateApp)
	server.mux.HandleFunc("POST /v1/organizations/{organizationID}/apps/{appID}/disable", server.setAppStatus(domain.AppStatusDisabled))
	server.mux.HandleFunc("POST /v1/organizations/{organizationID}/apps/{appID}/enable", server.setAppStatus(domain.AppStatusActive))
	server.mux.HandleFunc("DELETE /v1/organizations/{organizationID}/apps/{appID}", server.deleteApp)

	server.mux.HandleFunc("GET /v1/organizations/{organizationID}/connections", server.listConnections)
	server.mux.HandleFunc("POST /v1/organizations/{organizationID}/connections/authorize", server.authorize)
	server.mux.HandleFunc("POST /v1/organizations/{organizationID}/connections/complete", server.complete)
	server.mux.HandleFunc("POST /v1/organizations/{organizationID}/connections", server.createConnection)
	server.mux.HandleFunc("GET /v1/organizations/{organizationID}/connections/{connectionID}", server.getConnection)
	server.mux.HandleFunc("PATCH /v1/organizations/{organizationID}/connections/{connectionID}", server.updateConnection)
	server.mux.HandleFunc("POST /v1/organizations/{organizationID}/connections/{connectionID}/reconnect", server.reconnect)
	server.mux.HandleFunc("PUT /v1/organizations/{organizationID}/connections/{connectionID}/credential", server.replaceCredential)
	server.mux.HandleFunc("DELETE /v1/organizations/{organizationID}/connections/{connectionID}", server.disconnect)

	server.mux.HandleFunc("GET "+application.CallbackPath+"{provider}", server.callback)

	server.mux.HandleFunc("POST /v1/internal/connections/{connectionID}/token", server.issueToken)
	server.mux.HandleFunc("GET /v1/internal/connections/{connectionID}", server.internalConnection)
	server.mux.HandleFunc("POST /v1/internal/connections/{connectionID}/holds", server.addHold)
	server.mux.HandleFunc("DELETE /v1/internal/connections/{connectionID}/holds/{holder}", server.releaseHold)
	server.mux.HandleFunc("POST /v1/internal/connections/{connectionID}/report", server.report)
	return server
}

func (server *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if strings.HasPrefix(request.URL.Path, "/v1/") {
		writer.Header().Set("Cache-Control", "no-store")
	}
	server.mux.ServeHTTP(writer, request)
}

// appView is an app as the API shows it. Only admins see its client ID, and
// no one sees its secret.
type appView struct {
	domain.App
	ClientID string `json:"client_id,omitempty"`
}

func viewApp(app domain.App, admin bool) appView {
	view := appView{App: app}
	if admin {
		view.ClientID = app.ClientID
	}
	if view.Scopes == nil {
		view.Scopes = []string{}
	}
	return view
}

func (server *Server) listApps(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	list, err := server.service.ListApps(request.Context(), credentials, request.PathValue("organizationID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	views := make([]appView, 0, len(list.Apps))
	for _, app := range list.Apps {
		views = append(views, viewApp(app, list.Admin))
	}
	writeJSON(writer, http.StatusOK, map[string]any{"apps": views})
}

func (server *Server) createApp(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var body struct {
		Provider       string   `json:"provider"`
		CredentialType string   `json:"credential_type"`
		ClientID       string   `json:"client_id"`
		ClientSecret   string   `json:"client_secret"`
		Scopes         []string `json:"scopes"`
		GitHubAppID    string   `json:"github_app_id"`
		GitHubAppSlug  string   `json:"github_app_slug"`
		PrivateKey     string   `json:"private_key"`
	}
	if !server.decode(writer, request, &body) {
		return
	}
	app, err := server.service.CreateApp(request.Context(), credentials, request.PathValue("organizationID"), application.CreateAppInput{
		Provider: body.Provider, CredentialType: domain.CredentialType(strings.TrimSpace(body.CredentialType)),
		ClientID: body.ClientID, ClientSecret: body.ClientSecret, Scopes: body.Scopes,
		GitHubAppID: body.GitHubAppID, GitHubAppSlug: body.GitHubAppSlug, PrivateKey: body.PrivateKey,
	})
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, viewApp(app, true))
}

func (server *Server) updateApp(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var body struct {
		Scopes       []string `json:"scopes"`
		ClientID     string   `json:"client_id"`
		ClientSecret string   `json:"client_secret"`
		PrivateKey   string   `json:"private_key"`
	}
	if !server.decode(writer, request, &body) {
		return
	}
	app, err := server.service.UpdateApp(request.Context(), credentials, request.PathValue("organizationID"), request.PathValue("appID"),
		application.UpdateAppInput{Scopes: body.Scopes, ClientID: body.ClientID, ClientSecret: body.ClientSecret, PrivateKey: body.PrivateKey})
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, viewApp(app, true))
}

func (server *Server) setAppStatus(status domain.AppStatus) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		credentials, ok := server.credentials(writer, request)
		if !ok {
			return
		}
		app, err := server.service.SetAppStatus(request.Context(), credentials, request.PathValue("organizationID"), request.PathValue("appID"), status)
		if err != nil {
			server.handleError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusOK, viewApp(app, true))
	}
}

func (server *Server) deleteApp(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	if err := server.service.DeleteApp(request.Context(), credentials, request.PathValue("organizationID"), request.PathValue("appID")); err != nil {
		server.handleError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) listConnections(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	query := request.URL.Query()
	scope := domain.Scope(strings.TrimSpace(query.Get("scope")))
	if scope != "" && !scope.Valid() {
		writeError(writer, http.StatusBadRequest, "invalid_request", "scope is not allowed")
		return
	}
	connections, err := server.service.ListConnections(request.Context(), credentials, request.PathValue("organizationID"), application.ConnectionFilter{
		AppID: query.Get("app_id"), Scope: scope, TeamID: query.Get("team_id"),
	})
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	if connections == nil {
		connections = []domain.Connection{}
	}
	writeJSON(writer, http.StatusOK, map[string]any{"connections": connections})
}

func (server *Server) authorize(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var body struct {
		AppID     string `json:"app_id"`
		Scope     string `json:"scope"`
		TeamID    string `json:"team_id"`
		Usage     string `json:"usage"`
		ReturnURI string `json:"return_uri"`
	}
	if !server.decode(writer, request, &body) {
		return
	}
	authorization, err := server.service.Authorize(request.Context(), credentials, request.PathValue("organizationID"), application.AuthorizeInput{
		AppID: body.AppID, Scope: domain.Scope(body.Scope), TeamID: body.TeamID, Usage: domain.Usage(body.Usage), ReturnURI: body.ReturnURI,
	})
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, authorization)
}

func (server *Server) complete(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var body struct {
		SessionID string `json:"session_id"`
	}
	if !server.decode(writer, request, &body) {
		return
	}
	completion, err := server.service.Complete(request.Context(), credentials, request.PathValue("organizationID"), body.SessionID)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	status := http.StatusCreated
	if completion.Reconnected {
		status = http.StatusOK
	}
	writeJSON(writer, status, completion.Connection)
}

func (server *Server) getConnection(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	connection, err := server.service.GetConnection(request.Context(), credentials, request.PathValue("organizationID"), request.PathValue("connectionID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, connection)
}

func (server *Server) reconnect(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var body struct {
		ReturnURI string `json:"return_uri"`
	}
	if !server.decode(writer, request, &body) {
		return
	}
	authorization, err := server.service.Reconnect(request.Context(), credentials, request.PathValue("organizationID"),
		request.PathValue("connectionID"), body.ReturnURI)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, authorization)
}

func (server *Server) disconnect(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	if err := server.service.Disconnect(request.Context(), credentials, request.PathValue("organizationID"), request.PathValue("connectionID")); err != nil {
		server.handleError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// callback is where the provider sends the browser. It carries no Oryxa
// credentials: the state names the session, and the browser goes back to the
// app, which completes the session as the member who started it.
func (server *Server) callback(writer http.ResponseWriter, request *http.Request) {
	// The URL carries the provider's code; never let it leak as a referrer.
	writer.Header().Set("Referrer-Policy", "no-referrer")
	query := request.URL.Query()
	target, err := server.service.Callback(request.Context(), request.PathValue("provider"), application.CallbackParams{
		State: query.Get("state"), Code: query.Get("code"), Error: query.Get("error"),
		InstallationID: query.Get("installation_id"), SetupAction: query.Get("setup_action"),
		Tenant: query.Get("tenant"), AdminConsent: query.Get("admin_consent"),
	})
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writePage(writer, http.StatusBadRequest, "This connection link is not valid. Go back to Oryxa and connect again.")
	case err != nil:
		server.logger.ErrorContext(request.Context(), "oauth callback failed", "provider", request.PathValue("provider"), "error", err)
		writePage(writer, http.StatusInternalServerError, "Something went wrong while connecting. Go back to Oryxa and try again.")
	default:
		http.Redirect(writer, request, target, http.StatusSeeOther)
	}
}

// extractCredentials reads whatever member credentials a request carries,
// without answering an error when there are none.
func extractCredentials(request *http.Request) authclient.Credentials {
	var credentials authclient.Credentials
	if value := request.Header.Get("Authorization"); isBearer(value) {
		credentials.Authorization = value
	} else if cookie, err := request.Cookie(authclient.AccessCookieName); err == nil && cookie.Value != "" {
		credentials.AccessCookie = cookie.Value
	}
	return credentials
}

func (server *Server) credentials(writer http.ResponseWriter, request *http.Request) (authclient.Credentials, bool) {
	credentials := extractCredentials(request)
	if !credentials.Present() {
		writeError(writer, http.StatusUnauthorized, "unauthorized", "access token is required")
		return credentials, false
	}
	if credentials.Authorization == "" && request.Method != http.MethodGet && request.Method != http.MethodHead {
		if _, ok := server.allowedOrigins[request.Header.Get("Origin")]; !ok {
			writeError(writer, http.StatusForbidden, "forbidden", "origin is required for cookie authentication")
			return credentials, false
		}
	}
	return credentials, true
}

// decode reads a JSON body with no unknown fields, answering 400 itself when
// it cannot.
func (server *Server) decode(writer http.ResponseWriter, request *http.Request, target any) bool {
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxJSONBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || decoder.More() {
		writeError(writer, http.StatusBadRequest, "invalid_request", "request body must be one JSON object with known fields")
		return false
	}
	return true
}

// handleError answers a failed call with the README's error codes. Provider
// errors never carry tokens or response bodies, so they can be logged.
func (server *Server) handleError(writer http.ResponseWriter, request *http.Request, err error) {
	var scopes domain.ScopeRequiredError
	switch {
	case errors.Is(err, domain.ErrInvalid):
		writeError(writer, http.StatusBadRequest, "invalid_request", strings.TrimSuffix(err.Error(), ": "+domain.ErrInvalid.Error()))
	case errors.Is(err, domain.ErrUnauthorized):
		writeError(writer, http.StatusUnauthorized, "unauthorized", "authentication failed")
	case errors.Is(err, domain.ErrForbidden):
		writeError(writer, http.StatusForbidden, "forbidden", "operation is not allowed")
	case errors.Is(err, domain.ErrNotFound):
		writeError(writer, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, domain.ErrAlreadyConnected):
		writeError(writer, http.StatusConflict, "already_connected", "that provider account is already connected here; reconnect it instead")
	case errors.Is(err, domain.ErrAccountChanged):
		writeError(writer, http.StatusConflict, "account_changed", "the provider returned a different account")
	case errors.Is(err, domain.ErrNeedsReconnect):
		writeError(writer, http.StatusConflict, "needs_reconnect", "the connection must be reconnected first")
	case errors.Is(err, domain.ErrAppDisabled):
		writeError(writer, http.StatusConflict, "app_disabled", "the app is disabled")
	case errors.Is(err, domain.ErrConflict):
		writeError(writer, http.StatusConflict, "conflict", "the resource changed; retry")
	case errors.As(err, &scopes):
		writeError(writer, http.StatusUnprocessableEntity, "scope_required", scopes.Error())
	case errors.Is(err, domain.ErrScopeRequired):
		writeError(writer, http.StatusUnprocessableEntity, "scope_required", domain.ErrScopeRequired.Error())
	case errors.Is(err, domain.ErrCredentialRejected):
		server.logger.WarnContext(request.Context(), "provider rejected a credential", "path", request.URL.Path, "error", err)
		writeError(writer, http.StatusUnprocessableEntity, "credential_rejected", "the provider rejected the credential")
	case errors.Is(err, domain.ErrSessionExpired):
		writeError(writer, http.StatusBadRequest, "session_expired", "the authorization session is used up or expired; start again")
	case errors.Is(err, domain.ErrProviderUnavailable):
		server.logger.WarnContext(request.Context(), "provider unavailable", "path", request.URL.Path, "error", err)
		writeError(writer, http.StatusServiceUnavailable, "provider_unavailable", "the provider did not answer; retry")
	case errors.Is(err, domain.ErrRateLimited):
		writeError(writer, http.StatusTooManyRequests, "rate_limited", "too many requests; retry later")
	default:
		server.logger.ErrorContext(request.Context(), "connections request failed", "method", request.Method, "path", request.URL.Path, "error", err)
		writeError(writer, http.StatusInternalServerError, "internal", "internal server error")
	}
}

func isBearer(value string) bool {
	parts := strings.Fields(value)
	return len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") && parts[1] != ""
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]map[string]string{"error": {"code": code, "message": message}})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

// writePage answers the browser, which is not an API client, in plain text.
func writePage(writer http.ResponseWriter, status int, message string) {
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.Header().Set("Content-Security-Policy", "default-src 'none'")
	writer.WriteHeader(status)
	_, _ = fmt.Fprintln(writer, message)
}
