// Package httpserver is apps' HTTP API: the catalog of apps, a member's
// connections to them, the triggers subscribed on those connections, tools
// run in them, and the two routes Composio itself calls — the browser's
// return from connecting, and webhook deliveries. Oryxa's own sign-in adds
// its OAuth callback, key replacement, the manifests its forms are rendered
// from, organizations' own OAuth clients, and the webhook ingress its apps
// deliver to. It turns requests into application calls and domain errors
// into the documented error codes.
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
	"strconv"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/application"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/cleantext"
)

const (
	maxJSONBytes = 256 << 10
	// maxWebhookBytes bounds a Composio delivery. EventBridge carries at most
	// 256 KiB, and a payload apps would drop anyway need not be read whole.
	maxWebhookBytes = 1 << 20
)

type appsService interface {
	Catalog(ctx context.Context, query application.CatalogQuery) ([]application.ToolkitView, string, error)
	CatalogToolkit(ctx context.Context, toolkit string) (application.ToolkitView, error)
	CatalogTools(ctx context.Context, toolkit, search, cursor string, limit int) ([]application.ToolView, string, error)
	CatalogTriggers(ctx context.Context, toolkit, cursor string, limit int) ([]application.TriggerTypeView, string, error)
	Connect(ctx context.Context, credentials authclient.Credentials, organizationID string, input application.ConnectInput) (domain.Connection, string, error)
	CompleteConnection(ctx context.Context, state, status, accountID string) (application.Completion, error)
	Connections(ctx context.Context, credentials authclient.Credentials, organizationID, toolkit string, organization bool) ([]domain.Connection, error)
	Connection(ctx context.Context, credentials authclient.Credentials, organizationID, id string) (domain.Connection, error)
	RemoveConnection(ctx context.Context, credentials authclient.Credentials, organizationID, id string) error
	RunTool(ctx context.Context, credentials authclient.Credentials, organizationID string, input application.RunToolInput) (application.ToolRun, error)
	Proxy(ctx context.Context, credentials authclient.Credentials, organizationID string, input application.ProxyInput) (application.ProxyResult, error)
	ConnectionTargets(ctx context.Context, credentials authclient.Credentials, organizationID, connectionID string) ([]application.Target, error)
	CreateTrigger(ctx context.Context, credentials authclient.Credentials, organizationID string, input application.TriggerInput) (domain.Trigger, error)
	Triggers(ctx context.Context, credentials authclient.Credentials, organizationID, connectionID string) ([]domain.Trigger, error)
	RemoveTrigger(ctx context.Context, credentials authclient.Credentials, organizationID, id string) error
	ReceiveWebhook(ctx context.Context, header http.Header, body []byte) error
}

type Config struct {
	// AllowedOrigins may send cookie-authenticated mutations.
	AllowedOrigins []string
	// MCP serves the apps MCP endpoint, when set.
	MCP http.Handler
	// Oryxa serves Oryxa's own sign-in. Without it, its routes answer that
	// it is not switched on.
	Oryxa OryxaService
	// Hooks is the webhook ingress Oryxa's own integrations' apps deliver
	// to, at hooks.Pattern. Without it, every endpoint is unknown.
	Hooks http.Handler
	// Services verifies the service tokens apps' internal routes take.
	// Without it there are no internal routes.
	Services ServiceVerifier
}

type Server struct {
	service        appsService
	oryxa          OryxaService
	services       ServiceVerifier
	allowedOrigins map[string]struct{}
	logger         *slog.Logger
	mux            *http.ServeMux
	patterns       []string
	// callbacks bounds how many OAuth callbacks one address brings back.
	callbacks *application.RateLimiter
}

func New(service appsService, config Config, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	server := &Server{
		service: service, oryxa: config.Oryxa, services: config.Services,
		allowedOrigins: map[string]struct{}{}, logger: logger, mux: http.NewServeMux(),
		callbacks: application.NewRateLimiter(application.CallbackBudget, application.CallbackWindow, nil),
	}
	for _, origin := range config.AllowedOrigins {
		if parsed, err := url.Parse(origin); err == nil && parsed.Scheme != "" && parsed.Host != "" {
			server.allowedOrigins[parsed.Scheme+"://"+parsed.Host] = struct{}{}
		}
	}

	server.handle("GET /v1/apps/catalog", server.catalog)
	server.handle("GET /v1/apps/catalog/{toolkit}", server.catalogToolkit)
	server.handle("GET /v1/apps/catalog/{toolkit}/tools", server.catalogTools)
	server.handle("GET /v1/apps/catalog/{toolkit}/triggers", server.catalogTriggers)
	server.handle("GET /v1/apps/connect/callback", server.completeConnection)
	server.handle("POST /v1/apps/webhooks/composio", server.receiveWebhook)
	server.handle("GET /v1/organizations/{organizationID}/apps/connections", server.listConnections)
	server.handle("POST /v1/organizations/{organizationID}/apps/connections", server.connect)
	server.handle("GET /v1/organizations/{organizationID}/apps/connections/{connectionID}", server.getConnection)
	server.handle("DELETE /v1/organizations/{organizationID}/apps/connections/{connectionID}", server.removeConnection)
	server.handle("POST /v1/organizations/{organizationID}/apps/tools/{tool}/run", server.runTool)
	server.handle("POST /v1/organizations/{organizationID}/apps/read", server.proxy)
	server.handle("GET /v1/organizations/{organizationID}/apps/connections/{connectionID}/targets", server.connectionTargets)
	server.handle("GET /v1/organizations/{organizationID}/apps/triggers", server.listTriggers)
	server.handle("POST /v1/organizations/{organizationID}/apps/triggers", server.createTrigger)
	server.handle("DELETE /v1/organizations/{organizationID}/apps/triggers/{triggerID}", server.removeTrigger)
	server.routeOryxa(config.Hooks)
	if config.MCP != nil {
		server.handleHandler("POST /v1/mcp/apps", config.MCP)
		server.handleHandler("POST /v1/organizations/{organizationID}/apps/mcp", config.MCP)
	}
	return server
}

// handle registers a route and remembers its pattern, so a host serving
// several services knows which paths are this one's.
func (server *Server) handle(pattern string, handler http.HandlerFunc) {
	server.handleHandler(pattern, handler)
}

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

func (server *Server) catalog(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	toolkits, next, err := server.service.Catalog(request.Context(), application.CatalogQuery{
		Search: query.Get("search"), Category: query.Get("category"), Cursor: query.Get("cursor"), Limit: intValue(query.Get("limit")),
	})
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"toolkits": toolkits, "next_cursor": optional(next)})
}

func (server *Server) catalogToolkit(writer http.ResponseWriter, request *http.Request) {
	toolkit, err := server.service.CatalogToolkit(request.Context(), request.PathValue("toolkit"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, toolkit)
}

func (server *Server) catalogTools(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	tools, next, err := server.service.CatalogTools(request.Context(), request.PathValue("toolkit"),
		query.Get("search"), query.Get("cursor"), intValue(query.Get("limit")))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"tools": tools, "next_cursor": optional(next)})
}

func (server *Server) catalogTriggers(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	triggers, next, err := server.service.CatalogTriggers(request.Context(), request.PathValue("toolkit"),
		query.Get("cursor"), intValue(query.Get("limit")))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"triggers": triggers, "next_cursor": optional(next)})
}

// connectionView is what a connection looks like over the wire. The state
// that names its browser round trip is nobody's business once issued.
type connectionView struct {
	ID      string `json:"id"`
	Toolkit string `json:"toolkit"`
	// Broker is composio or oryxa: who holds the connection's credential.
	Broker string `json:"broker"`
	// CredentialKind is an Oryxa connection's credential kind (oauth2_code,
	// api_token, basic, oauth2_client_credentials) once it holds one. Only a
	// key, a password or client credentials can be replaced.
	CredentialKind    string `json:"credential_kind,omitempty"`
	Status            string `json:"status"`
	StatusReason      string `json:"status_reason,omitempty"`
	Label             string `json:"label,omitempty"`
	MemberID          string `json:"member_id"`
	ComposioAccountID string `json:"composio_account_id,omitempty"`
	// AutoSubscribe says this connection is subscribed to everything its app
	// delivers; AutoSubscribedAt when that finished. Unset does not mean
	// still in progress forever - auto-subscribe gives up after a few failed
	// attempts, logging a warning when it does, and the member can always
	// subscribe by hand. AutoSubscribeAttempts is how a caller tells the two
	// apart: at the limit with AutoSubscribedAt unset, it has given up.
	AutoSubscribe         bool       `json:"auto_subscribe"`
	AutoSubscribedAt      *time.Time `json:"auto_subscribed_at,omitempty"`
	AutoSubscribeAttempts int        `json:"auto_subscribe_attempts,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
	ConnectedAt           *time.Time `json:"connected_at,omitempty"`
	RemovedAt             *time.Time `json:"removed_at,omitempty"`
}

func viewConnection(connection domain.Connection) connectionView {
	broker := connection.Broker
	if broker == "" {
		broker = domain.BrokerComposio
	}
	return connectionView{
		ID: connection.ID, Toolkit: connection.Toolkit, Broker: broker, CredentialKind: connection.CredentialKind,
		Status:       string(connection.Status),
		StatusReason: connection.StatusReason, Label: connection.Label, MemberID: connection.MemberID,
		ComposioAccountID: connection.ComposioAccountID, AutoSubscribe: connection.AutoSubscribe,
		AutoSubscribedAt: connection.AutoSubscribedAt, AutoSubscribeAttempts: connection.AutoSubscribeAttempts,
		CreatedAt: connection.CreatedAt,
		UpdatedAt: connection.UpdatedAt, ConnectedAt: connection.ConnectedAt, RemovedAt: connection.RemovedAt,
	}
}

func (server *Server) connect(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var body struct {
		Toolkit  string `json:"toolkit"`
		ReturnTo string `json:"return_to"`
		Alias    string `json:"alias"`
		// AutoSubscribe left out means the deployment's default, which is to
		// subscribe to everything the app can deliver.
		AutoSubscribe *bool `json:"auto_subscribe"`
		// Broker is composio, the default, or oryxa; Kind and Fields are
		// Oryxa's own sign-in's.
		Broker string            `json:"broker"`
		Kind   string            `json:"kind"`
		Fields map[string]string `json:"fields"`
	}
	if !server.decode(writer, request, &body) {
		return
	}
	broker, err := domain.CheckBroker(body.Broker)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	if broker == domain.BrokerOryxa {
		server.connectOryxa(writer, request, credentials, application.ConnectInput{
			Toolkit: body.Toolkit, ReturnTo: body.ReturnTo, Alias: body.Alias, AutoSubscribe: body.AutoSubscribe,
			Kind: body.Kind, Fields: body.Fields, Host: request.Host,
		})
		return
	}
	if body.Kind != "" || body.Fields != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", "kind and fields are for connecting through broker oryxa")
		return
	}
	connection, redirectURL, err := server.service.Connect(request.Context(), credentials, request.PathValue("organizationID"),
		application.ConnectInput{
			Toolkit: body.Toolkit, ReturnTo: body.ReturnTo, Alias: body.Alias,
			AutoSubscribe: body.AutoSubscribe,
		})
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"connection": viewConnection(connection), "redirect_url": redirectURL})
}

// completeConnection is where Composio sends the member's browser back. It
// answers the browser, not an API client: with a return page it redirects
// there, saying how it went; without one it answers JSON.
func (server *Server) completeConnection(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	completion, err := server.service.CompleteConnection(request.Context(), query.Get("state"), query.Get("status"), query.Get("connected_account_id"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	server.sendBack(writer, request, completion)
}

func (server *Server) listConnections(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	query := request.URL.Query()
	connections, err := server.service.Connections(request.Context(), credentials, request.PathValue("organizationID"),
		query.Get("toolkit"), query.Get("scope") == "organization")
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	views := make([]connectionView, 0, len(connections))
	for _, connection := range connections {
		views = append(views, viewConnection(connection))
	}
	writeJSON(writer, http.StatusOK, map[string]any{"connections": views})
}

func (server *Server) getConnection(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	connection, err := server.service.Connection(request.Context(), credentials, request.PathValue("organizationID"), request.PathValue("connectionID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, viewConnection(connection))
}

func (server *Server) removeConnection(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	if err := server.service.RemoveConnection(request.Context(), credentials, request.PathValue("organizationID"), request.PathValue("connectionID")); err != nil {
		server.handleError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) runTool(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var body struct {
		ConnectionID   string          `json:"connection_id"`
		Arguments      json.RawMessage `json:"arguments"`
		IdempotencyKey string          `json:"idempotency_key"`
	}
	if !server.decode(writer, request, &body) {
		return
	}
	run, err := server.service.RunTool(request.Context(), credentials, request.PathValue("organizationID"), application.RunToolInput{
		Tool: request.PathValue("tool"), ConnectionID: body.ConnectionID, Arguments: body.Arguments, IdempotencyKey: body.IdempotencyKey,
	})
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, RunView(run))
}

// RunView is a tool run as the API and the MCP endpoint both answer it.
func RunView(run application.ToolRun) map[string]any {
	view := map[string]any{
		"action_id": run.Action.ID, "tool": run.Action.Tool, "connection_id": run.Action.ConnectionID,
		"successful": run.Action.Status == domain.ActionSucceeded, "replayed": run.Replayed,
	}
	if len(run.Data) > 0 {
		view["data"] = run.Data
	}
	if run.Action.Error != "" {
		view["error"] = run.Action.Error
	}
	return view
}

// proxy reads an app's own API through the caller's connection, for a service
// inside Oryxa that has to page through what an app holds rather than run a
// tool. Only reads pass; the application decides which those are.
func (server *Server) proxy(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var body struct {
		ConnectionID string            `json:"connection_id"`
		Toolkit      string            `json:"toolkit"`
		Endpoint     string            `json:"endpoint"`
		Method       string            `json:"method"`
		Body         json.RawMessage   `json:"body"`
		Headers      map[string]string `json:"headers"`
	}
	if !server.decode(writer, request, &body) {
		return
	}
	result, err := server.service.Proxy(request.Context(), credentials, request.PathValue("organizationID"), application.ProxyInput{
		ConnectionID: body.ConnectionID, Toolkit: body.Toolkit, Endpoint: body.Endpoint,
		Method: body.Method, Body: body.Body, Headers: body.Headers,
	})
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

type triggerView struct {
	ID           string          `json:"id"`
	ConnectionID string          `json:"connection_id"`
	Toolkit      string          `json:"toolkit"`
	Trigger      string          `json:"trigger"`
	Config       json.RawMessage `json:"config"`
	Status       string          `json:"status"`
	StatusReason string          `json:"status_reason,omitempty"`
	MemberID     string          `json:"member_id"`
	// Kind is webhook when the app pushes its events and poll when Composio
	// polls for them, which is the difference between an event arriving at
	// once and arriving on an interval.
	Kind string `json:"kind,omitempty"`
	// ManualSetup says this subscription delivers nothing until the app's own
	// webhook is registered by hand.
	ManualSetup bool `json:"manual_setup,omitempty"`
	// Setup is what is left to do, or to expect, in a member's terms. Empty
	// for a trigger that needs nothing.
	Setup string `json:"setup,omitempty"`
	// DeliveryCount and LastDeliveryAt tell a live trigger from one that has
	// never delivered anything.
	DeliveryCount  int64      `json:"delivery_count"`
	LastDeliveryAt *time.Time `json:"last_delivery_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func viewTrigger(trigger domain.Trigger) triggerView {
	return triggerView{
		ID: trigger.ID, ConnectionID: trigger.ConnectionID, Toolkit: trigger.Toolkit, Trigger: trigger.Slug,
		Config: trigger.Config, Status: string(trigger.Status), StatusReason: trigger.StatusReason,
		MemberID: trigger.MemberID, Kind: trigger.Kind, ManualSetup: trigger.ManualSetup,
		Setup: application.TriggerSetup(trigger), DeliveryCount: trigger.DeliveryCount,
		LastDeliveryAt: trigger.LastDeliveryAt, CreatedAt: trigger.CreatedAt, UpdatedAt: trigger.UpdatedAt,
	}
}

// connectionTargets answers what a member could index through a connection,
// read from the app itself, so nobody has to know a channel's id.
func (server *Server) connectionTargets(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	targets, err := server.service.ConnectionTargets(request.Context(), credentials,
		request.PathValue("organizationID"), request.PathValue("connectionID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"targets": targets})
}

func (server *Server) listTriggers(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	triggers, err := server.service.Triggers(request.Context(), credentials, request.PathValue("organizationID"), request.URL.Query().Get("connection_id"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	views := make([]triggerView, 0, len(triggers))
	for _, trigger := range triggers {
		views = append(views, viewTrigger(trigger))
	}
	writeJSON(writer, http.StatusOK, map[string]any{"triggers": views})
}

func (server *Server) createTrigger(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var body struct {
		ConnectionID string          `json:"connection_id"`
		Trigger      string          `json:"trigger"`
		Config       json.RawMessage `json:"config"`
	}
	if !server.decode(writer, request, &body) {
		return
	}
	trigger, err := server.service.CreateTrigger(request.Context(), credentials, request.PathValue("organizationID"),
		application.TriggerInput{ConnectionID: body.ConnectionID, Trigger: body.Trigger, Config: body.Config})
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, viewTrigger(trigger))
}

func (server *Server) removeTrigger(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	if err := server.service.RemoveTrigger(request.Context(), credentials, request.PathValue("organizationID"), request.PathValue("triggerID")); err != nil {
		server.handleError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// receiveWebhook takes a Composio delivery. It answers 2xx once the delivery
// is handled or safely ignorable, 401 for one that is not Composio's, and 5xx
// when it could not be recorded, so Composio retries it.
func (server *Server) receiveWebhook(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(io.LimitReader(request.Body, maxWebhookBytes+1))
	if err != nil || len(body) > maxWebhookBytes {
		writeError(writer, http.StatusRequestEntityTooLarge, "invalid_request", "the delivery is too large")
		return
	}
	if err := server.service.ReceiveWebhook(request.Context(), request.Header, body); err != nil {
		if errors.Is(err, application.ErrUnverified) {
			server.logger.WarnContext(request.Context(), "unverified webhook refused", "error", err)
			writeError(writer, http.StatusUnauthorized, "unauthorized", "the delivery is not signed by Composio")
			return
		}
		server.handleError(writer, request, err)
		return
	}
	// Composio counts only 200 and 201 as delivered. A 204 is a failure to
	// it: it logs the delivery as failed and sends it again, so every event
	// arrived four times.
	writer.WriteHeader(http.StatusOK)
}

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
	if err := cleantext.Check(target); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return false
	}
	return true
}

// ErrorCode names a failure the way the API and the MCP endpoint both do.
func ErrorCode(err error) (int, string) {
	switch {
	case errors.Is(err, domain.ErrInvalidCredentials):
		return http.StatusBadRequest, "invalid_credentials"
	case errors.Is(err, domain.ErrInvalid):
		return http.StatusBadRequest, "invalid_request"
	case errors.Is(err, domain.ErrUnauthorized):
		return http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, domain.ErrNotReadOnly):
		return http.StatusForbidden, "not_read_only"
	case errors.Is(err, domain.ErrForbidden):
		return http.StatusForbidden, "forbidden"
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, domain.ErrNotConnected):
		return http.StatusConflict, "not_connected"
	case errors.Is(err, domain.ErrChooseConnection):
		return http.StatusConflict, "choose_connection"
	case errors.Is(err, domain.ErrNeedsReconnect):
		return http.StatusConflict, "needs_reconnect"
	case errors.Is(err, domain.ErrNotConnectable):
		return http.StatusUnprocessableEntity, "not_connectable"
	case errors.Is(err, domain.ErrConflict):
		return http.StatusConflict, "conflict"
	case errors.Is(err, domain.ErrRateLimited):
		return http.StatusTooManyRequests, "rate_limited"
	case errors.Is(err, domain.ErrUnavailable):
		return http.StatusServiceUnavailable, "unavailable"
	default:
		return http.StatusInternalServerError, "internal"
	}
}

// Message is what a caller is told about a failure: the application's own
// wording for what the caller can act on, and nothing for the rest.
func Message(err error) string {
	status, code := ErrorCode(err)
	switch {
	case status == http.StatusInternalServerError:
		return "internal server error"
	case code == "unauthorized":
		return "authentication failed"
	case code == "not_found":
		return "resource not found"
	case code == "rate_limited":
		return "too many requests; retry"
	case code == "unavailable":
		return "Composio or a service apps depends on did not answer; retry"
	}
	sentinels := []error{
		domain.ErrInvalid, domain.ErrForbidden, domain.ErrConflict, domain.ErrNotConnected, domain.ErrChooseConnection,
		domain.ErrNeedsReconnect, domain.ErrNotReadOnly, domain.ErrNotConnectable, domain.ErrInvalidCredentials,
	}
	// A sentinel says the same thing the code already does, so it is dropped
	// from the wording however it was attached: wrapped onto the end with %w,
	// or joined on its own line by errors.Join, which a validator uses to
	// carry both the kind and the advice.
	message := err.Error()
	lines := make([]string, 0, 2)
	for _, line := range strings.Split(message, "\n") {
		if line = strip(strings.TrimSpace(line), sentinels); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		// Nothing but the sentinel: its own words are better than silence.
		return strings.TrimSpace(message)
	}
	return strings.Join(lines, ": ")
}

// strip takes a sentinel off one line of an error's wording, wherever it sits:
// on its own, or after the words that wrap it.
func strip(line string, sentinels []error) string {
	for _, sentinel := range sentinels {
		if line == sentinel.Error() {
			return ""
		}
		line = strings.TrimSpace(strings.TrimSuffix(line, ": "+sentinel.Error()))
	}
	return line
}

// handleError answers a failed call with the README's error codes. Errors
// never carry tokens, secrets or Composio's responses beyond a refused
// request's own reason, so they can be logged. An invalid_credentials error
// also names its field, never the field's value.
func (server *Server) handleError(writer http.ResponseWriter, request *http.Request, err error) {
	status, code := ErrorCode(err)
	switch {
	case status >= 500:
		server.logger.ErrorContext(request.Context(), "apps request failed",
			"method", request.Method, "path", request.URL.Path, "error", err)
	case code == "rate_limited":
		server.logger.WarnContext(request.Context(), "apps request rate limited", "path", request.URL.Path)
	}
	failure := map[string]string{"code": code, "message": Message(err)}
	var invalid *domain.InvalidCredentials
	if code == "invalid_credentials" && errors.As(err, &invalid) && invalid.Field != "" {
		failure["field"] = invalid.Field
	}
	writeJSON(writer, status, map[string]map[string]string{"error": failure})
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]map[string]string{"error": {"code": code, "message": message}})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func intValue(value string) int {
	parsed, _ := strconv.Atoi(strings.TrimSpace(value))
	return parsed
}

func optional(value string) any {
	if value == "" {
		return nil
	}
	return value
}
