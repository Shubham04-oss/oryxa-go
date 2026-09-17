// Package httpserver is integrations' HTTP API: the webhook URLs providers
// deliver to, and the routes members manage subscriptions and app webhooks
// with. It turns requests into application calls and domain errors into the
// documented error codes.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/application"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers"
)

const (
	maxJSONBytes = 64 << 10
	// maxDeliveryBytes bounds a delivery. GitHub sends up to 25 MB, but
	// integrations publishes at most integrationsv1.MaxPayloadBytes of one.
	maxDeliveryBytes = 8 << 20
)

type integrationsService interface {
	Receive(ctx context.Context, provider, route string, delivery providers.Delivered) (providers.Reply, error)

	ListSubscriptions(ctx context.Context, credentials authclient.Credentials, organizationID, connectionID string) ([]domain.Subscription, error)
	CreateSubscription(ctx context.Context, credentials authclient.Credentials, organizationID string, input application.CreateSubscriptionInput) (domain.Subscription, error)
	GetSubscription(ctx context.Context, credentials authclient.Credentials, organizationID, subscriptionID string) (domain.Subscription, error)
	StopSubscription(ctx context.Context, credentials authclient.Credentials, organizationID, subscriptionID string) error

	RunAction(ctx context.Context, credentials authclient.Credentials, organizationID string, input application.RunActionInput) (domain.Action, error)
	Catalog() []providers.Description

	GetAppWebhook(ctx context.Context, credentials authclient.Credentials, organizationID, appID string) (application.AppWebhookView, error)
	PutAppWebhook(ctx context.Context, credentials authclient.Credentials, organizationID, appID, secret string) (application.AppWebhookView, error)
	DeleteAppWebhook(ctx context.Context, credentials authclient.Credentials, organizationID, appID string) error
}

type Config struct {
	// AllowedOrigins may send cookie-authenticated mutations.
	AllowedOrigins []string
	// MCP, when set, serves the provider's actions as MCP tools under
	// /v1/organizations/{organizationID}/mcp/{provider}.
	MCP http.Handler
}

type Server struct {
	service        integrationsService
	allowedOrigins map[string]struct{}
	logger         *slog.Logger
	mux            *http.ServeMux
	patterns       []string
}

func New(service integrationsService, config Config, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	server := &Server{service: service, allowedOrigins: map[string]struct{}{}, logger: logger, mux: http.NewServeMux()}
	for _, origin := range config.AllowedOrigins {
		if parsed, err := url.Parse(origin); err == nil && parsed.Scheme != "" && parsed.Host != "" {
			server.allowedOrigins[parsed.Scheme+"://"+parsed.Host] = struct{}{}
		}
	}
	server.handle("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	server.handle("POST /v1/webhooks/{provider}/{route}", server.receive)

	server.handle("GET /v1/organizations/{organizationID}/subscriptions", server.listSubscriptions)
	server.handle("POST /v1/organizations/{organizationID}/subscriptions", server.createSubscription)
	server.handle("GET /v1/organizations/{organizationID}/subscriptions/{subscriptionID}", server.getSubscription)
	server.handle("DELETE /v1/organizations/{organizationID}/subscriptions/{subscriptionID}", server.stopSubscription)

	server.handle("POST /v1/organizations/{organizationID}/actions", server.runAction)
	server.handle("GET /v1/catalog", server.catalog)
	if config.MCP != nil {
		// An MCP endpoint keeps no session and answers only POST; the mux
		// refuses the other methods for it.
		server.handleHandler("POST /v1/organizations/{organizationID}/mcp/{provider}", config.MCP)
		// The same endpoint at one fixed URL per provider, naming the
		// organization in a header, as an agent's toolset is configured.
		server.handleHandler("POST /v1/mcp/{provider}", config.MCP)
	}

	server.handle("GET /v1/organizations/{organizationID}/apps/{appID}/webhook", server.getAppWebhook)
	server.handle("PUT /v1/organizations/{organizationID}/apps/{appID}/webhook", server.putAppWebhook)
	server.handle("DELETE /v1/organizations/{organizationID}/apps/{appID}/webhook", server.deleteAppWebhook)
	return server
}

// handle registers a route and remembers its pattern, so a host serving
// several services knows which paths are this one's.
func (server *Server) handle(pattern string, handler http.HandlerFunc) {
	server.mux.HandleFunc(pattern, handler)
	server.patterns = append(server.patterns, pattern)
}

// handleHandler is handle for a route served by an http.Handler rather than a
// HandlerFunc, such as the MCP endpoint.
func (server *Server) handleHandler(pattern string, handler http.Handler) {
	server.mux.Handle(pattern, handler)
	server.patterns = append(server.patterns, pattern)
}

// Patterns lists every route the server registered.
func (server *Server) Patterns() []string { return slices.Clone(server.patterns) }

func (server *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if strings.HasPrefix(request.URL.Path, "/v1/") {
		writer.Header().Set("Cache-Control", "no-store")
	}
	server.mux.ServeHTTP(writer, request)
}

// receive answers a provider's delivery. It reads the body as it arrived,
// since the signature covers those bytes, and never says why a delivery was
// refused.
func (server *Server) receive(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(io.LimitReader(request.Body, maxDeliveryBytes+1))
	if err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if len(body) > maxDeliveryBytes {
		writer.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	reply, err := server.service.Receive(request.Context(), request.PathValue("provider"), request.PathValue("route"), providers.Delivered{
		Header: request.Header, Query: request.URL.Query(), Body: body,
		Method: request.Method, URI: request.URL.RequestURI(),
	})
	switch {
	case err == nil:
	case errors.Is(err, domain.ErrNotFound):
		writer.WriteHeader(http.StatusNotFound)
		return
	case errors.Is(err, domain.ErrUnauthorized):
		server.logger.WarnContext(request.Context(), "delivery refused", "provider", request.PathValue("provider"), "error", err)
		writer.WriteHeader(http.StatusUnauthorized)
		return
	case errors.Is(err, domain.ErrInvalid):
		server.logger.WarnContext(request.Context(), "delivery not understood", "provider", request.PathValue("provider"), "error", err)
		writer.WriteHeader(http.StatusBadRequest)
		return
	default:
		// Most providers retry a delivery that fails.
		server.logger.ErrorContext(request.Context(), "delivery failed", "provider", request.PathValue("provider"), "error", err)
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}
	if reply.ContentType != "" {
		writer.Header().Set("Content-Type", reply.ContentType)
	}
	status := reply.Status
	if status == 0 {
		status = http.StatusOK
	}
	writer.WriteHeader(status)
	_, _ = writer.Write(reply.Body)
}

func (server *Server) listSubscriptions(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	subscriptions, err := server.service.ListSubscriptions(request.Context(), credentials, request.PathValue("organizationID"), request.URL.Query().Get("connection_id"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"subscriptions": subscriptions})
}

func (server *Server) createSubscription(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var body struct {
		ConnectionID string            `json:"connection_id"`
		Target       map[string]string `json:"target"`
		Events       []string          `json:"events"`
	}
	if !server.decode(writer, request, &body) {
		return
	}
	subscription, err := server.service.CreateSubscription(request.Context(), credentials, request.PathValue("organizationID"), application.CreateSubscriptionInput{
		ConnectionID: body.ConnectionID, Target: body.Target, Events: body.Events,
	})
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, subscription)
}

// runAction carries out one action with a connection, as the member asking.
func (server *Server) runAction(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var body struct {
		ConnectionID   string          `json:"connection_id"`
		Action         string          `json:"action"`
		Input          json.RawMessage `json:"input"`
		IdempotencyKey string          `json:"idempotency_key"`
	}
	if !server.decode(writer, request, &body) {
		return
	}
	action, err := server.service.RunAction(request.Context(), credentials, request.PathValue("organizationID"), application.RunActionInput{
		ConnectionID: body.ConnectionID, Action: body.Action, Input: body.Input, IdempotencyKey: body.IdempotencyKey,
	})
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, action)
}

// catalog answers what every provider watches, delivers and can do. It is the
// same for everyone and holds nothing of an organization's, so a caller needs
// it before it has a connection and sends no credentials for it.
func (server *Server) catalog(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{"providers": server.service.Catalog()})
}

func (server *Server) getSubscription(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	subscription, err := server.service.GetSubscription(request.Context(), credentials, request.PathValue("organizationID"), request.PathValue("subscriptionID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, subscription)
}

func (server *Server) stopSubscription(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	if err := server.service.StopSubscription(request.Context(), credentials, request.PathValue("organizationID"), request.PathValue("subscriptionID")); err != nil {
		server.handleError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) getAppWebhook(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	webhook, err := server.service.GetAppWebhook(request.Context(), credentials, request.PathValue("organizationID"), request.PathValue("appID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, webhook)
}

func (server *Server) putAppWebhook(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var body struct {
		Secret string `json:"secret"`
	}
	if !server.decode(writer, request, &body) {
		return
	}
	webhook, err := server.service.PutAppWebhook(request.Context(), credentials, request.PathValue("organizationID"), request.PathValue("appID"), body.Secret)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, webhook)
}

func (server *Server) deleteAppWebhook(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	if err := server.service.DeleteAppWebhook(request.Context(), credentials, request.PathValue("organizationID"), request.PathValue("appID")); err != nil {
		server.handleError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// credentials reads the caller's access token from the Authorization header or
// the access cookie. A mutation made with the cookie must come from an
// allowed origin.
func (server *Server) credentials(writer http.ResponseWriter, request *http.Request) (authclient.Credentials, bool) {
	credentials := authclient.Credentials{Authorization: request.Header.Get("Authorization")}
	if credentials.Authorization == "" {
		if cookie, err := request.Cookie(authclient.AccessCookieName); err == nil {
			credentials.AccessCookie = cookie.Value
		}
	}
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

// handleError answers a failed call with the README's error codes. Errors never
// carry tokens, secrets or provider responses, so they can be logged.
func (server *Server) handleError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalid):
		writeError(writer, http.StatusBadRequest, "invalid_request", strings.TrimSuffix(err.Error(), ": "+domain.ErrInvalid.Error()))
	case errors.Is(err, domain.ErrUnauthorized):
		writeError(writer, http.StatusUnauthorized, "unauthorized", "authentication failed")
	case errors.Is(err, domain.ErrForbidden):
		writeError(writer, http.StatusForbidden, "forbidden", "operation is not allowed")
	case errors.Is(err, domain.ErrNotFound):
		writeError(writer, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, domain.ErrNeedsReconnect):
		writeError(writer, http.StatusConflict, "needs_reconnect", "the connection must be reconnected first")
	case errors.Is(err, domain.ErrWebhookSecretRequired):
		writeError(writer, http.StatusConflict, "webhook_secret_required", "an org admin must set the app's webhook signing secret first")
	case errors.Is(err, domain.ErrConflict):
		writeError(writer, http.StatusConflict, "conflict", "the resource changed; retry")
	case errors.Is(err, domain.ErrCredentialRejected):
		server.logger.WarnContext(request.Context(), "provider rejected a credential", "path", request.URL.Path, "error", err)
		writeError(writer, http.StatusUnprocessableEntity, "credential_rejected", "the provider rejected the connection's credential")
	case errors.Is(err, domain.ErrProviderUnavailable):
		server.logger.WarnContext(request.Context(), "provider unavailable", "path", request.URL.Path, "error", err)
		writeError(writer, http.StatusServiceUnavailable, "provider_unavailable", "the provider or connections did not answer; retry")
	default:
		server.logger.ErrorContext(request.Context(), "integrations request failed", "method", request.Method, "path", request.URL.Path, "error", err)
		writeError(writer, http.StatusInternalServerError, "internal", "internal server error")
	}
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]map[string]string{"error": {"code": code, "message": message}})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
