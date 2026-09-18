// Package httpserver is knowledge's HTTP API: the routes a member registers
// what to index with, reads how it has been going, and fetches a document an
// event was too small to carry. It turns requests into application calls and
// domain errors into the documented error codes.
//
// The server is a plain http.Handler with its own mux. Nothing here assumes
// it owns the process, so the same handler serves whether knowledge runs on
// its own or is mounted alongside other services.
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

	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/application"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

const maxJSONBytes = 64 << 10

type knowledgeService interface {
	RegisterSource(ctx context.Context, credentials authclient.Credentials, organizationID string, input application.RegisterInput) (domain.Source, error)
	Sources(ctx context.Context, credentials authclient.Credentials, organizationID string, query domain.SourceQuery) ([]domain.Source, error)
	Source(ctx context.Context, credentials authclient.Credentials, organizationID, sourceID string) (domain.Source, error)
	RemoveSource(ctx context.Context, credentials authclient.Credentials, organizationID, sourceID string) error
	SyncSource(ctx context.Context, credentials authclient.Credentials, organizationID, sourceID string) (domain.Source, error)
	Runs(ctx context.Context, credentials authclient.Credentials, organizationID, sourceID string, limit int) ([]domain.SyncRun, error)
	Document(ctx context.Context, credentials authclient.Credentials, organizationID, documentID string) (domain.Document, error)
	DocumentBody(ctx context.Context, organizationID, documentID string) (domain.Document, error)
	Extractors() []string
}

type Config struct {
	// AllowedOrigins may send cookie-authenticated mutations.
	AllowedOrigins []string
	// Services identifies a service calling the internal API, which is how a
	// retrieval pipeline reads back a body too large to have travelled on
	// the bus. Nil means the internal API refuses every call.
	Services ServiceVerifier
}

// ServiceVerifier checks a signed service token naming knowledge as its
// audience.
type ServiceVerifier interface {
	Verify(context.Context, string) (serviceauth.Claims, error)
}

type Server struct {
	service        knowledgeService
	allowedOrigins map[string]struct{}
	services       ServiceVerifier
	logger         *slog.Logger
	mux            *http.ServeMux
	patterns       []string
}

func New(service knowledgeService, config Config, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	server := &Server{
		service: service, allowedOrigins: map[string]struct{}{},
		services: config.Services, logger: logger, mux: http.NewServeMux(),
	}
	for _, origin := range config.AllowedOrigins {
		if parsed, err := url.Parse(origin); err == nil && parsed.Scheme != "" && parsed.Host != "" {
			server.allowedOrigins[parsed.Scheme+"://"+parsed.Host] = struct{}{}
		}
	}

	server.handle("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	server.handle("GET /v1/sources/providers", server.providers)
	server.handle("GET /v1/internal/organizations/{organizationID}/documents/{documentID}/body", server.documentBody)
	server.handle("GET /v1/organizations/{organizationID}/sources", server.listSources)
	server.handle("POST /v1/organizations/{organizationID}/sources", server.registerSource)
	server.handle("GET /v1/organizations/{organizationID}/sources/{sourceID}", server.getSource)
	server.handle("DELETE /v1/organizations/{organizationID}/sources/{sourceID}", server.removeSource)
	server.handle("POST /v1/organizations/{organizationID}/sources/{sourceID}/sync", server.syncSource)
	server.handle("GET /v1/organizations/{organizationID}/sources/{sourceID}/runs", server.listRuns)
	server.handle("GET /v1/organizations/{organizationID}/documents/{documentID}", server.getDocument)
	return server
}

// handle registers a route and remembers its pattern, so a host serving
// several services knows which paths are this one's.
func (server *Server) handle(pattern string, handler http.HandlerFunc) {
	server.mux.HandleFunc(pattern, handler)
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

// providers reads aloud what this build can extract from. It holds nothing of
// any organization, so it takes no credentials: a caller needs it before it
// has anything to register.
func (server *Server) providers(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string][]string{"providers": server.service.Extractors()})
}

type sourceView struct {
	ID           string            `json:"id"`
	Provider     string            `json:"provider"`
	ConnectionID string            `json:"connection_id,omitempty"`
	Target       map[string]string `json:"target"`
	Scope        string            `json:"scope"`
	TeamID       string            `json:"team_id,omitempty"`
	MemberID     string            `json:"member_id,omitempty"`
	CreatedBy    string            `json:"created_by"`
	Status       string            `json:"status"`
	Reason       string            `json:"reason,omitempty"`
	IntervalSecs int               `json:"interval_seconds"`
	SyncAt       *time.Time        `json:"sync_at,omitempty"`
	LastSyncedAt *time.Time        `json:"last_synced_at,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

// view is what a source looks like over the wire. The lease and version a
// source carries are how replicas share the work, and are nobody else's
// business.
func view(source domain.Source) sourceView {
	return sourceView{
		ID: source.ID, Provider: source.Provider, ConnectionID: source.ConnectionID,
		Target: source.Target, Scope: string(source.Scope), TeamID: source.TeamID,
		MemberID: source.MemberID, CreatedBy: source.CreatedBy, Status: string(source.Status),
		Reason: source.Reason, IntervalSecs: int(source.Interval / time.Second),
		SyncAt: source.SyncAt, LastSyncedAt: source.LastSyncedAt,
		CreatedAt: source.CreatedAt, UpdatedAt: source.UpdatedAt,
	}
}

func (server *Server) registerSource(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var body struct {
		ConnectionID    string            `json:"connection_id"`
		Provider        string            `json:"provider"`
		Target          map[string]string `json:"target"`
		IntervalSeconds int               `json:"interval_seconds"`
	}
	if !server.decode(writer, request, &body) {
		return
	}
	source, err := server.service.RegisterSource(request.Context(), credentials,
		request.PathValue("organizationID"), application.RegisterInput{
			ConnectionID: body.ConnectionID,
			Provider:     body.Provider,
			Target:       body.Target,
			Interval:     time.Duration(body.IntervalSeconds) * time.Second,
		})
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, view(source))
}

func (server *Server) listSources(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	query := domain.SourceQuery{
		ConnectionID: request.URL.Query().Get("connection_id"),
		Provider:     request.URL.Query().Get("provider"),
		Admin:        request.URL.Query().Get("scope") == "organization",
	}
	listed, err := server.service.Sources(request.Context(), credentials, request.PathValue("organizationID"), query)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	views := make([]sourceView, 0, len(listed))
	for _, source := range listed {
		views = append(views, view(source))
	}
	writeJSON(writer, http.StatusOK, map[string][]sourceView{"sources": views})
}

func (server *Server) getSource(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	source, err := server.service.Source(request.Context(), credentials,
		request.PathValue("organizationID"), request.PathValue("sourceID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, view(source))
}

func (server *Server) removeSource(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	if err := server.service.RemoveSource(request.Context(), credentials,
		request.PathValue("organizationID"), request.PathValue("sourceID")); err != nil {
		server.handleError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) listRuns(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
	runs, err := server.service.Runs(request.Context(), credentials,
		request.PathValue("organizationID"), request.PathValue("sourceID"), limit)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string][]domain.SyncRun{"runs": runs})
}

// getDocument answers the body an event was too small to carry.
func (server *Server) getDocument(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	document, err := server.service.Document(request.Context(), credentials,
		request.PathValue("organizationID"), request.PathValue("documentID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"id": document.ID, "source_id": document.SourceID, "external_id": document.ExternalID,
		"kind": document.Kind, "title": document.Title, "url": document.URL,
		"mime": document.MIME, "content_hash": document.ContentHash, "body": document.Body,
		"byte_size": document.ByteSize, "metadata": document.Metadata,
		"source_updated_at": document.SourceUpdatedAt, "extracted_at": document.ExtractedAt,
		"removed_at": document.RemovedAt,
	})
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
	return true
}

// handleError answers a failed call with the README's error codes. Errors
// never carry tokens, secrets or provider responses, so they can be logged.
func (server *Server) handleError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalid):
		writeError(writer, http.StatusBadRequest, "invalid_request", strings.TrimSuffix(err.Error(), ": "+domain.ErrInvalid.Error()))
	case errors.Is(err, domain.ErrUnauthorized):
		writeError(writer, http.StatusUnauthorized, "unauthorized", "authentication failed")
	case errors.Is(err, domain.ErrForbidden):
		writeError(writer, http.StatusForbidden, "forbidden", strings.TrimSuffix(err.Error(), ": "+domain.ErrForbidden.Error()))
	case errors.Is(err, domain.ErrNotFound):
		writeError(writer, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, domain.ErrNeedsReconnect):
		writeError(writer, http.StatusConflict, "needs_reconnect", "the connection must be reconnected first")
	case errors.Is(err, domain.ErrScopeRequired):
		writeError(writer, http.StatusForbidden, "scope_required", "the connection is missing a scope")
	case errors.Is(err, domain.ErrConflict):
		writeError(writer, http.StatusConflict, "conflict", strings.TrimSuffix(err.Error(), ": "+domain.ErrConflict.Error()))
	case errors.Is(err, domain.ErrCredentialRejected):
		server.logger.WarnContext(request.Context(), "provider rejected a credential", "path", request.URL.Path, "error", err)
		writeError(writer, http.StatusUnprocessableEntity, "credential_rejected", "the provider rejected the credential")
	case errors.Is(err, domain.ErrRateLimited):
		writeError(writer, http.StatusTooManyRequests, "rate_limited", "too many requests; retry")
	case errors.Is(err, domain.ErrProviderUnavailable):
		server.logger.WarnContext(request.Context(), "provider unavailable", "path", request.URL.Path, "error", err)
		writeError(writer, http.StatusServiceUnavailable, "provider_unavailable", "the provider or a service it depends on did not answer; retry")
	default:
		server.logger.ErrorContext(request.Context(), "knowledge request failed",
			"method", request.Method, "path", request.URL.Path, "error", err)
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

// internalServicePurpose is the only purpose the internal API accepts,
// whatever else a calling service may be configured for.
const internalServicePurpose = "documents"

// documentBody answers one document's body to a service, which is how a
// retrieval pipeline reads back what an event was too small to carry.
//
// It identifies the caller and nothing else: a service has no member to be,
// so there is no access rule here to apply beyond which services a
// deployment admits. That is why it answers one document by id and never a
// listing.
func (server *Server) documentBody(writer http.ResponseWriter, request *http.Request) {
	if server.services == nil {
		// A deployment that named no callers has no internal API at all,
		// and says so the same way it would for a route that is not there.
		writeError(writer, http.StatusNotFound, "not_found", "no such route")
		return
	}
	token := request.Header.Get(serviceauth.Header)
	if token == "" {
		writeError(writer, http.StatusUnauthorized, "unauthorized", "a service token is required")
		return
	}
	claims, err := server.services.Verify(request.Context(), token)
	switch {
	case errors.Is(err, serviceauth.ErrPurposeNotAllowed):
		writeError(writer, http.StatusForbidden, "forbidden", "the service may not use this purpose")
		return
	case errors.Is(err, serviceauth.ErrInvalidToken):
		writeError(writer, http.StatusUnauthorized, "unauthorized", "the service token is invalid")
		return
	case err != nil:
		server.logger.ErrorContext(request.Context(), "check an internal caller", "error", err)
		writeError(writer, http.StatusServiceUnavailable, "provider_unavailable", "service identity could not be checked; retry")
		return
	}
	if claims.Purpose != internalServicePurpose {
		writeError(writer, http.StatusForbidden, "forbidden", "the service may not use this purpose")
		return
	}

	document, err := server.service.DocumentBody(request.Context(),
		request.PathValue("organizationID"), request.PathValue("documentID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	// Written without HTML escaping: the default turns every <, > and & into
	// six bytes, and a body near knowledge's 4 MiB ceiling that is mostly
	// markup-like text could otherwise outgrow what the caller will read.
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(map[string]any{
		"id": document.ID, "source_id": document.SourceID, "external_id": document.ExternalID,
		"mime": document.MIME, "content_hash": document.ContentHash, "body": document.Body,
		"byte_size": document.ByteSize,
	})
}

// syncSource reads a source now rather than at its next interval, because a
// member asked. The pass runs on the worker that was going to run it anyway,
// so this answers as soon as the schedule is moved.
func (server *Server) syncSource(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	source, err := server.service.SyncSource(request.Context(), credentials,
		request.PathValue("organizationID"), request.PathValue("sourceID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, view(source))
}
