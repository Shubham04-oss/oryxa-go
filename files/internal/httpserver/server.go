package httpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/files/internal/application"
	"github.com/Shubham04-oss/oryxa-go/files/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/files/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/cleantext"
)

const maxJSONBytes = 1 << 20

// maxUploadBytes bounds one upload request as a whole, in either encoding: the
// largest file and the small fields that travel with it. It is read only after
// the caller is known, and never past this.
const maxUploadBytes = maxJSONBytes + int64(domain.MaxBytes)

type filesService interface {
	Create(context.Context, authclient.Credentials, string, application.InputReader) (domain.Object, error)
	List(context.Context, authclient.Credentials, string, application.ListFilter) ([]domain.Object, error)
	Get(context.Context, authclient.Credentials, string, string) (domain.Object, error)
	Content(context.Context, authclient.Credentials, string, string) (domain.Object, []byte, error)
	DownloadURL(context.Context, authclient.Credentials, string, string) (application.DownloadURL, error)
	Delete(context.Context, authclient.Credentials, string, string) error
}

type Config struct {
	AllowedOrigins []string
}

type Server struct {
	service        filesService
	allowedOrigins map[string]struct{}
	logger         *slog.Logger
	mux            *http.ServeMux
	patterns       []string
}

func New(service filesService, config Config, logger *slog.Logger) *Server {
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
	server.handle("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	server.handle("GET /v1/organizations/{organizationID}/files", server.list)
	server.handle("POST /v1/organizations/{organizationID}/files", server.create)
	server.handle("GET /v1/organizations/{organizationID}/files/{fileID}/content", server.content)
	server.handle("POST /v1/organizations/{organizationID}/files/{fileID}/download-url", server.downloadURL)
	server.handle("GET /v1/organizations/{organizationID}/files/{fileID}", server.get)
	server.handle("DELETE /v1/organizations/{organizationID}/files/{fileID}", server.delete)
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

func (server *Server) create(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	// The service asks for the body only after it has admitted the caller, so a
	// token nobody issued costs a refusal and not a megabyte-sized decode.
	object, err := server.service.Create(request.Context(), credentials, request.PathValue("organizationID"),
		func() (application.CreateInput, error) { return readCreateInput(writer, request) })
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, object)
}

func (server *Server) list(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	filter, err := readListFilter(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, strings.TrimSuffix(err.Error(), ": "+domain.ErrInvalid.Error()))
		return
	}
	objects, err := server.service.List(request.Context(), credentials, request.PathValue("organizationID"), filter)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	if objects == nil {
		objects = []domain.Object{}
	}
	writeJSON(writer, http.StatusOK, map[string]any{"files": objects})
}

func (server *Server) get(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	object, err := server.service.Get(request.Context(), credentials, request.PathValue("organizationID"), request.PathValue("fileID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, object)
}

func (server *Server) content(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	object, body, err := server.service.Content(request.Context(), credentials, request.PathValue("organizationID"), request.PathValue("fileID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writer.Header().Set("Content-Type", object.ContentType)
	writer.Header().Set("Content-Length", strconv.FormatInt(int64(len(body)), 10))
	writer.Header().Set("X-Oryxa-SHA256", object.SHA256)
	writer.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, object.Filename))
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(body)
}

func (server *Server) downloadURL(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	result, err := server.service.DownloadURL(request.Context(), credentials, request.PathValue("organizationID"), request.PathValue("fileID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (server *Server) delete(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	if err := server.service.Delete(request.Context(), credentials, request.PathValue("organizationID"), request.PathValue("fileID")); err != nil {
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
	case errors.Is(err, domain.ErrTooLarge):
		writeError(writer, http.StatusRequestEntityTooLarge, strings.TrimSuffix(err.Error(), ": "+domain.ErrTooLarge.Error()))
	case errors.Is(err, domain.ErrUnauthorized):
		writeError(writer, http.StatusUnauthorized, "authentication failed")
	case errors.Is(err, domain.ErrForbidden):
		writeError(writer, http.StatusForbidden, "operation is not allowed")
	case errors.Is(err, domain.ErrNotFound):
		writeError(writer, http.StatusNotFound, "resource not found")
	case errors.Is(err, domain.ErrConflict):
		writeError(writer, http.StatusConflict, "resource state conflicts with the request")
	default:
		server.logger.ErrorContext(request.Context(), "files request failed", "method", request.Method, "path", request.URL.Path, "error", err)
		writeError(writer, http.StatusInternalServerError, "internal server error")
	}
}

func readCreateInput(writer http.ResponseWriter, request *http.Request) (application.CreateInput, error) {
	request.Body = http.MaxBytesReader(writer, request.Body, maxUploadBytes)
	read := readJSONCreate
	if mediaType, _, _ := mime.ParseMediaType(request.Header.Get("Content-Type")); strings.HasPrefix(mediaType, "multipart/") {
		read = readMultipart
	}
	input, err := read(request)
	if err != nil {
		return application.CreateInput{}, err
	}
	// A file's bytes may hold anything; its name, team and the rest are text
	// the database will not take a NUL in.
	if err := cleantext.Check(input); err != nil {
		return application.CreateInput{}, fmt.Errorf("%v: %w", err, domain.ErrInvalid)
	}
	return input, nil
}

func readJSONCreate(request *http.Request) (application.CreateInput, error) {
	var body struct {
		Filename      string `json:"filename"`
		ContentType   string `json:"content_type"`
		Purpose       string `json:"purpose"`
		Scope         string `json:"scope"`
		TeamID        string `json:"team_id"`
		RetentionDays int    `json:"retention_days"`
		Content       string `json:"content"`
	}
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return application.CreateInput{}, unreadable(err, "invalid request body")
	}
	content, err := base64.StdEncoding.DecodeString(strings.TrimSpace(body.Content))
	if err != nil {
		content, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(body.Content))
		if err != nil {
			return application.CreateInput{}, fmt.Errorf("content must be base64: %w", domain.ErrInvalid)
		}
	}
	scope, err := parseOptionalScope(body.Scope)
	if err != nil {
		return application.CreateInput{}, err
	}
	return application.CreateInput{
		Filename:      body.Filename,
		ContentType:   body.ContentType,
		Purpose:       domain.Purpose(strings.TrimSpace(body.Purpose)),
		Scope:         scope,
		TeamID:        strings.TrimSpace(body.TeamID),
		RetentionDays: body.RetentionDays,
		Content:       content,
	}, nil
}

func readMultipart(request *http.Request) (application.CreateInput, error) {
	if err := request.ParseMultipartForm(maxUploadBytes); err != nil {
		return application.CreateInput{}, unreadable(err, "invalid multipart body")
	}
	file, header, err := request.FormFile("file")
	if err != nil {
		return application.CreateInput{}, fmt.Errorf("file is required: %w", domain.ErrInvalid)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, int64(domain.MaxBytes)+1))
	if err != nil {
		return application.CreateInput{}, fmt.Errorf("read file: %w", domain.ErrInvalid)
	}
	filename := request.FormValue("filename")
	if filename == "" && header != nil {
		filename = header.Filename
	}
	contentType := request.FormValue("content_type")
	if contentType == "" && header != nil {
		contentType = header.Header.Get("Content-Type")
	}
	days := 0
	if raw := strings.TrimSpace(request.FormValue("retention_days")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return application.CreateInput{}, fmt.Errorf("retention_days must be a whole number: %w", domain.ErrInvalid)
		}
		days = parsed
	}
	scope, err := parseOptionalScope(request.FormValue("scope"))
	if err != nil {
		return application.CreateInput{}, err
	}
	return application.CreateInput{
		Filename:      filename,
		ContentType:   contentType,
		Purpose:       domain.Purpose(strings.TrimSpace(request.FormValue("purpose"))),
		Scope:         scope,
		TeamID:        strings.TrimSpace(request.FormValue("team_id")),
		RetentionDays: days,
		Content:       content,
	}, nil
}

// unreadable says why a body could not be read: it ran past its limit, which is
// a 413, or it was not what it claimed to be, which is a 400.
func unreadable(err error, invalid string) error {
	var over *http.MaxBytesError
	if errors.As(err, &over) {
		return fmt.Errorf("the upload must be at most %d bytes: %w", over.Limit, domain.ErrTooLarge)
	}
	return fmt.Errorf("%s: %w", invalid, domain.ErrInvalid)
}

func readListFilter(request *http.Request) (application.ListFilter, error) {
	query := request.URL.Query()
	scope, err := parseOptionalScope(query.Get("scope"))
	if err != nil {
		return application.ListFilter{}, err
	}
	purpose := domain.Purpose(strings.TrimSpace(query.Get("purpose")))
	if purpose != "" && !purpose.Valid() {
		return application.ListFilter{}, fmt.Errorf("purpose is not allowed: %w", domain.ErrInvalid)
	}
	year, err := parseOptionalInt(query.Get("year"), "year")
	if err != nil {
		return application.ListFilter{}, err
	}
	month, err := parseOptionalInt(query.Get("month"), "month")
	if err != nil {
		return application.ListFilter{}, err
	}
	day, err := parseOptionalInt(query.Get("day"), "day")
	if err != nil {
		return application.ListFilter{}, err
	}
	return application.ListFilter{
		Scope:   scope,
		TeamID:  strings.TrimSpace(query.Get("team_id")),
		Purpose: purpose,
		Date:    domain.DateFilter{Year: year, Month: month, Day: day},
	}, nil
}

func parseOptionalInt(raw, name string) (int, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a whole number: %w", name, domain.ErrInvalid)
	}
	return parsed, nil
}

func parseOptionalScope(raw string) (domain.Scope, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	scope, err := domain.ParseScope(value)
	if err != nil {
		return "", err
	}
	return scope, nil
}

func isBearer(value string) bool {
	parts := strings.Fields(value)
	return len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") && parts[1] != ""
}

func writeError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, map[string]string{"error": message})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
