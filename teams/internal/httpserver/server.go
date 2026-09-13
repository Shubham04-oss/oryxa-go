package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/teams/internal/application"
	"github.com/Shubham04-oss/oryxa-go/teams/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/teams/internal/domain"
)

const maxBodyBytes = 1 << 20

type teamsService interface {
	CreateTeam(context.Context, authclient.Credentials, string, application.CreateTeamInput) (domain.Team, error)
	Teams(context.Context, authclient.Credentials, string) ([]application.TeamSummary, error)
	Team(context.Context, authclient.Credentials, string, string) (application.TeamDetail, error)
	UpdateTeam(context.Context, authclient.Credentials, string, string, application.UpdateTeamInput) (domain.Team, error)
	DeleteTeam(context.Context, authclient.Credentials, string, string) error
	Members(context.Context, authclient.Credentials, string, string) ([]domain.Membership, error)
	AddMember(context.Context, authclient.Credentials, string, string, application.AddMemberInput) (domain.Membership, error)
	ChangeMemberRole(context.Context, authclient.Credentials, string, string, string, domain.Role) (domain.Membership, error)
	RemoveMember(context.Context, authclient.Credentials, string, string, string) error
	Tools(context.Context, authclient.Credentials, string) ([]domain.Tool, error)
	CallTool(context.Context, authclient.Credentials, string, string, json.RawMessage) (any, error)
	SetNotification(context.Context, authclient.Credentials, string, string, bool) (domain.Membership, error)
}

type Config struct {
	AllowedOrigins []string
}

type Server struct {
	service        teamsService
	allowedOrigins map[string]struct{}
	logger         *slog.Logger
	mux            *http.ServeMux
}

func New(service teamsService, config Config, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	server := &Server{
		service:        service,
		allowedOrigins: map[string]struct{}{},
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
	server.mux.HandleFunc("GET /v1/organizations/{organizationID}/tools", server.tools)
	server.mux.HandleFunc("POST /v1/organizations/{organizationID}/tools/{name}", server.callTool)
	server.mux.HandleFunc("GET /v1/organizations/{organizationID}/teams", server.teams)
	server.mux.HandleFunc("POST /v1/organizations/{organizationID}/teams", server.createTeam)
	server.mux.HandleFunc("GET /v1/organizations/{organizationID}/teams/{teamID}", server.team)
	server.mux.HandleFunc("PATCH /v1/organizations/{organizationID}/teams/{teamID}", server.updateTeam)
	server.mux.HandleFunc("DELETE /v1/organizations/{organizationID}/teams/{teamID}", server.deleteTeam)
	server.mux.HandleFunc("PATCH /v1/organizations/{organizationID}/teams/{teamID}/notification", server.setNotification)
	server.mux.HandleFunc("GET /v1/organizations/{organizationID}/teams/{teamID}/members", server.members)
	server.mux.HandleFunc("POST /v1/organizations/{organizationID}/teams/{teamID}/members", server.addMember)
	server.mux.HandleFunc("PATCH /v1/organizations/{organizationID}/teams/{teamID}/members/{userID}", server.changeRole)
	server.mux.HandleFunc("DELETE /v1/organizations/{organizationID}/teams/{teamID}/members/{userID}", server.removeMember)
	return server
}

func (server *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if strings.HasPrefix(request.URL.Path, "/v1/") {
		writer.Header().Set("Cache-Control", "no-store")
	}
	server.mux.ServeHTTP(writer, request)
}

func (server *Server) tools(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	tools, err := server.service.Tools(request.Context(), credentials, request.PathValue("organizationID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	if tools == nil {
		tools = []domain.Tool{}
	}
	writeJSON(writer, http.StatusOK, map[string]any{"tools": tools})
}

func (server *Server) callTool(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	arguments, err := readOptionalJSON(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	result, err := server.service.CallTool(request.Context(), credentials, request.PathValue("organizationID"), request.PathValue("name"), arguments)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (server *Server) teams(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	teams, err := server.service.Teams(request.Context(), credentials, request.PathValue("organizationID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	if teams == nil {
		teams = []application.TeamSummary{}
	}
	writeJSON(writer, http.StatusOK, map[string]any{"teams": teams})
}

func (server *Server) createTeam(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var input application.CreateTeamInput
	if err := readJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	team, err := server.service.CreateTeam(request.Context(), credentials, request.PathValue("organizationID"), input)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, team)
}

func (server *Server) team(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	detail, err := server.service.Team(request.Context(), credentials, request.PathValue("organizationID"), request.PathValue("teamID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, detail)
}

func (server *Server) updateTeam(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var input application.UpdateTeamInput
	if err := readJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	team, err := server.service.UpdateTeam(request.Context(), credentials, request.PathValue("organizationID"), request.PathValue("teamID"), input)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, team)
}

func (server *Server) deleteTeam(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	if err := server.service.DeleteTeam(request.Context(), credentials, request.PathValue("organizationID"), request.PathValue("teamID")); err != nil {
		server.handleError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) setNotification(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var input struct {
		NotifyEmail *bool `json:"notify_email"`
	}
	if err := readJSON(request, &input); err != nil || input.NotifyEmail == nil {
		writeError(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	membership, err := server.service.SetNotification(request.Context(), credentials, request.PathValue("organizationID"), request.PathValue("teamID"), *input.NotifyEmail)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, membership)
}

func (server *Server) members(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	members, err := server.service.Members(request.Context(), credentials, request.PathValue("organizationID"), request.PathValue("teamID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	if members == nil {
		members = []domain.Membership{}
	}
	writeJSON(writer, http.StatusOK, map[string]any{"members": members})
}

func (server *Server) addMember(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var input application.AddMemberInput
	if err := readJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	membership, err := server.service.AddMember(request.Context(), credentials, request.PathValue("organizationID"), request.PathValue("teamID"), input)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, membership)
}

func (server *Server) changeRole(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var input struct {
		Role domain.Role `json:"role"`
	}
	if err := readJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	membership, err := server.service.ChangeMemberRole(
		request.Context(),
		credentials,
		request.PathValue("organizationID"),
		request.PathValue("teamID"),
		request.PathValue("userID"),
		input.Role,
	)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, membership)
}

func (server *Server) removeMember(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	if err := server.service.RemoveMember(
		request.Context(),
		credentials,
		request.PathValue("organizationID"),
		request.PathValue("teamID"),
		request.PathValue("userID"),
	); err != nil {
		server.handleError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) credentials(writer http.ResponseWriter, request *http.Request) (authclient.Credentials, bool) {
	var credentials authclient.Credentials
	if value := request.Header.Get("Authorization"); isBearer(value) {
		credentials.Authorization = value
	} else if cookie, err := request.Cookie(authclient.AccessCookieName); err == nil && cookie.Value != "" {
		credentials.AccessCookie = cookie.Value
	}
	if !credentials.Present() {
		writeError(writer, http.StatusUnauthorized, "access token is required")
		return credentials, false
	}
	if credentials.Authorization == "" && request.Method != http.MethodGet && request.Method != http.MethodHead {
		if _, ok := server.allowedOrigins[request.Header.Get("Origin")]; !ok {
			writeError(writer, http.StatusForbidden, "origin is required for cookie authentication")
			return credentials, false
		}
	}
	return credentials, true
}

func (server *Server) handleError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalid):
		writeError(writer, http.StatusBadRequest, strings.TrimSuffix(err.Error(), ": "+domain.ErrInvalid.Error()))
	case errors.Is(err, domain.ErrUnauthorized):
		writeError(writer, http.StatusUnauthorized, "authentication failed")
	case errors.Is(err, domain.ErrForbidden):
		writeError(writer, http.StatusForbidden, "operation is not allowed")
	case errors.Is(err, domain.ErrNotFound):
		writeError(writer, http.StatusNotFound, "resource not found")
	case errors.Is(err, domain.ErrConflict):
		writeError(writer, http.StatusConflict, "resource state conflicts with the request")
	default:
		server.logger.ErrorContext(request.Context(), "teams request failed", "method", request.Method, "path", request.URL.Path, "error", err)
		writeError(writer, http.StatusInternalServerError, "internal server error")
	}
}

func isBearer(value string) bool {
	parts := strings.Fields(value)
	return len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") && parts[1] != ""
}

func readJSON(request *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func readOptionalJSON(request *http.Request) (json.RawMessage, error) {
	body, err := io.ReadAll(io.LimitReader(request.Body, maxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBodyBytes {
		return nil, errors.New("request body is too large")
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil, nil
	}
	if !json.Valid(body) {
		return nil, errors.New("invalid request body")
	}
	return json.RawMessage(body), nil
}

func writeError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, map[string]string{"error": message})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
