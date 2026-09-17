// Package httpserver is rag's HTTP surface.
//
// It decodes, it delegates, and it maps a domain error onto a status. It makes
// no access decisions of its own: those live in the application service, which
// asks auth and teams, so there is one place to read to know who can see what.
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
	"strconv"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/application"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/retrieval"
)

// maxJSONBytes bounds a request body. A document's own limit is larger and is
// checked in the domain; this is the outer guard on the envelope.
const maxJSONBytes = 24 << 20

type ragService interface {
	CreateCorpus(context.Context, authclient.Credentials, string, application.CreateCorpusInput) (domain.Corpus, error)
	Corpora(context.Context, authclient.Credentials, string, application.ListFilter) ([]domain.Corpus, error)
	Corpus(context.Context, authclient.Credentials, string, string) (domain.Corpus, error)
	DeleteCorpus(context.Context, authclient.Credentials, string, string) error

	AddDocument(context.Context, authclient.Credentials, string, string, application.AddDocumentInput) (domain.Document, error)
	Documents(context.Context, authclient.Credentials, string, string, domain.IngestStatus, int) ([]domain.Document, error)
	Document(context.Context, authclient.Credentials, string, string, string) (domain.Document, error)
	DeleteDocument(context.Context, authclient.Credentials, string, string, string) error

	Search(context.Context, authclient.Credentials, string, string, domain.Query) (retrieval.Results, error)
	Answer(context.Context, authclient.Credentials, string, string, domain.Query) (application.Answered, error)
}

type Config struct {
	AllowedOrigins []string
	// Capabilities is what this deployment can actually serve, for the
	// discovery endpoint. An operator reading it learns which providers and
	// backends are configured without trying one and reading the error.
	Capabilities Capabilities
}

// Capabilities is what a caller needs to know before creating a corpus.
type Capabilities struct {
	Providers    []domain.Provider     `json:"embedding_providers"`
	Backends     []domain.Backend      `json:"backends"`
	Models       []domain.ModelProfile `json:"models"`
	ContentTypes []string              `json:"content_types"`
	Answering    bool                  `json:"answering"`
	Reranking    bool                  `json:"reranking"`
}

type Server struct {
	service        ragService
	allowedOrigins map[string]struct{}
	capabilities   Capabilities
	logger         *slog.Logger
	mux            *http.ServeMux
}

func New(service ragService, config Config, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	server := &Server{
		service:        service,
		allowedOrigins: map[string]struct{}{},
		capabilities:   config.Capabilities,
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
	server.mux.HandleFunc("GET /v1/capabilities", server.readCapabilities)

	server.mux.HandleFunc("GET /v1/organizations/{organizationID}/corpora", server.listCorpora)
	server.mux.HandleFunc("POST /v1/organizations/{organizationID}/corpora", server.createCorpus)
	server.mux.HandleFunc("GET /v1/organizations/{organizationID}/corpora/{corpusID}", server.getCorpus)
	server.mux.HandleFunc("DELETE /v1/organizations/{organizationID}/corpora/{corpusID}", server.deleteCorpus)

	server.mux.HandleFunc("GET /v1/organizations/{organizationID}/corpora/{corpusID}/documents", server.listDocuments)
	server.mux.HandleFunc("POST /v1/organizations/{organizationID}/corpora/{corpusID}/documents", server.addDocument)
	server.mux.HandleFunc("GET /v1/organizations/{organizationID}/corpora/{corpusID}/documents/{documentID}", server.getDocument)
	server.mux.HandleFunc("DELETE /v1/organizations/{organizationID}/corpora/{corpusID}/documents/{documentID}", server.deleteDocument)

	server.mux.HandleFunc("POST /v1/organizations/{organizationID}/corpora/{corpusID}/search", server.search)
	server.mux.HandleFunc("POST /v1/organizations/{organizationID}/corpora/{corpusID}/answer", server.answer)
	return server
}

func (server *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if strings.HasPrefix(request.URL.Path, "/v1/") {
		// Answers and search results are an organization's own text. Nothing
		// in between should be keeping a copy.
		writer.Header().Set("Cache-Control", "no-store")
	}
	server.mux.ServeHTTP(writer, request)
}

func (server *Server) readCapabilities(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, server.capabilities)
}

type corpusRequest struct {
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	Scope      string `json:"scope"`
	TeamID     string `json:"team_id"`
	Provider   string `json:"embedding_provider"`
	Model      string `json:"embedding_model"`
	Dimensions int    `json:"embedding_dimensions"`
	Metric     string `json:"embedding_metric"`
	Backend    string `json:"backend"`
	Chunking   *struct {
		Strategy      string `json:"strategy"`
		MaxTokens     int    `json:"max_tokens"`
		OverlapTokens int    `json:"overlap_tokens"`
	} `json:"chunking"`
	Metadata map[string]string `json:"metadata"`
}

func (server *Server) createCorpus(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var body corpusRequest
	if !readJSON(writer, request, &body) {
		return
	}
	scope, err := domain.ParseScope(body.Scope)
	if err != nil {
		writeInvalid(writer, err)
		return
	}
	input := application.CreateCorpusInput{
		Slug: body.Slug, Name: body.Name, Scope: scope, TeamID: body.TeamID,
		Provider: domain.Provider(strings.TrimSpace(body.Provider)),
		Model:    body.Model, Dimensions: body.Dimensions,
		Metric:   domain.Metric(strings.TrimSpace(body.Metric)),
		Backend:  domain.Backend(strings.TrimSpace(body.Backend)),
		Metadata: domain.Metadata(body.Metadata),
	}
	if body.Chunking != nil {
		input.Chunking = domain.ChunkPolicy{
			Strategy:      domain.Strategy(strings.TrimSpace(body.Chunking.Strategy)),
			MaxTokens:     body.Chunking.MaxTokens,
			OverlapTokens: body.Chunking.OverlapTokens,
		}
	}
	corpus, err := server.service.CreateCorpus(request.Context(), credentials, request.PathValue("organizationID"), input)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, corpus)
}

func (server *Server) listCorpora(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	query := request.URL.Query()
	scope, err := domain.ParseScope(query.Get("scope"))
	if err != nil {
		writeInvalid(writer, err)
		return
	}
	// An absent scope means the caller's own plus the organization's, which
	// ParseScope cannot express, so it is read from the raw parameter.
	if strings.TrimSpace(query.Get("scope")) == "" {
		scope = ""
	}
	corpora, err := server.service.Corpora(request.Context(), credentials,
		request.PathValue("organizationID"),
		application.ListFilter{Scope: scope, TeamID: strings.TrimSpace(query.Get("team_id"))})
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	if corpora == nil {
		corpora = []domain.Corpus{}
	}
	writeJSON(writer, http.StatusOK, map[string]any{"corpora": corpora})
}

func (server *Server) getCorpus(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	corpus, err := server.service.Corpus(request.Context(), credentials,
		request.PathValue("organizationID"), request.PathValue("corpusID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, corpus)
}

func (server *Server) deleteCorpus(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	if err := server.service.DeleteCorpus(request.Context(), credentials,
		request.PathValue("organizationID"), request.PathValue("corpusID")); err != nil {
		server.handleError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

type documentRequest struct {
	Source struct {
		Kind string `json:"kind"`
		Ref  string `json:"ref"`
	} `json:"source"`
	Title       string            `json:"title"`
	ContentType string            `json:"content_type"`
	Text        string            `json:"text"`
	Metadata    map[string]string `json:"metadata"`
}

func (server *Server) addDocument(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var body documentRequest
	if !readJSON(writer, request, &body) {
		return
	}
	kind := domain.SourceKind(strings.TrimSpace(body.Source.Kind))
	if kind == "" {
		kind = domain.SourceInline
	}
	document, err := server.service.AddDocument(request.Context(), credentials,
		request.PathValue("organizationID"), request.PathValue("corpusID"),
		application.AddDocumentInput{
			Source:      domain.Source{Kind: kind, Ref: strings.TrimSpace(body.Source.Ref)},
			Title:       body.Title,
			ContentType: body.ContentType,
			Text:        body.Text,
			Metadata:    domain.Metadata(body.Metadata),
		})
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	// Accepted, not created: it is recorded and queued, and it is not
	// searchable until the indexed event says so.
	writeJSON(writer, http.StatusAccepted, document)
}

func (server *Server) listDocuments(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	query := request.URL.Query()
	limit := 0
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			writeError(writer, http.StatusBadRequest, "limit must be a positive whole number")
			return
		}
		limit = parsed
	}
	documents, err := server.service.Documents(request.Context(), credentials,
		request.PathValue("organizationID"), request.PathValue("corpusID"),
		domain.IngestStatus(strings.TrimSpace(query.Get("status"))), limit)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	if documents == nil {
		documents = []domain.Document{}
	}
	writeJSON(writer, http.StatusOK, map[string]any{"documents": documents})
}

func (server *Server) getDocument(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	document, err := server.service.Document(request.Context(), credentials,
		request.PathValue("organizationID"), request.PathValue("corpusID"), request.PathValue("documentID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, document)
}

func (server *Server) deleteDocument(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	if err := server.service.DeleteDocument(request.Context(), credentials,
		request.PathValue("organizationID"), request.PathValue("corpusID"), request.PathValue("documentID")); err != nil {
		server.handleError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

type queryRequest struct {
	Query     string  `json:"query"`
	TopK      int     `json:"top_k"`
	Mode      string  `json:"mode"`
	MinScore  float64 `json:"min_score"`
	Diversity float64 `json:"diversity"`
	Rerank    bool    `json:"rerank"`
	OverFetch int     `json:"over_fetch"`
	Filter    *struct {
		DocumentIDs []string `json:"document_ids"`
		Conditions  []struct {
			Key      string   `json:"key"`
			Operator string   `json:"operator"`
			Values   []string `json:"values"`
		} `json:"conditions"`
	} `json:"filter"`
}

func (body queryRequest) query() domain.Query {
	query := domain.Query{
		Text: body.Query, TopK: body.TopK, Mode: domain.SearchMode(strings.TrimSpace(body.Mode)),
		MinScore: body.MinScore, Diversity: body.Diversity, Rerank: body.Rerank, OverFetch: body.OverFetch,
	}
	if body.Filter != nil {
		query.Filter.DocumentIDs = body.Filter.DocumentIDs
		for _, condition := range body.Filter.Conditions {
			query.Filter.Conditions = append(query.Filter.Conditions, domain.Condition{
				Key:      strings.TrimSpace(condition.Key),
				Operator: domain.Operator(strings.TrimSpace(condition.Operator)),
				Values:   condition.Values,
			})
		}
	}
	return query
}

func (server *Server) search(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var body queryRequest
	if !readJSON(writer, request, &body) {
		return
	}
	results, err := server.service.Search(request.Context(), credentials,
		request.PathValue("organizationID"), request.PathValue("corpusID"), body.query())
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	if results.Matches == nil {
		results.Matches = []domain.Match{}
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"matches":  results.Matches,
		"usage":    results.Usage,
		"reranked": results.Reranked,
	})
}

func (server *Server) answer(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var body queryRequest
	if !readJSON(writer, request, &body) {
		return
	}
	answered, err := server.service.Answer(request.Context(), credentials,
		request.PathValue("organizationID"), request.PathValue("corpusID"), body.query())
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	if answered.Matches == nil {
		answered.Matches = []domain.Match{}
	}
	writeJSON(writer, http.StatusOK, answered)
}

// credentials reads the caller's token or cookie, and enforces the origin
// allowlist on the cookie path. A cookie travels on a cross-site request by
// itself; a bearer token does not, so only the cookie needs the check.
func (server *Server) credentials(writer http.ResponseWriter, request *http.Request) (authclient.Credentials, bool) {
	credentials := authclient.Credentials{Authorization: request.Header.Get("Authorization")}
	if cookie, err := request.Cookie(authclient.AccessCookieName); err == nil {
		credentials.AccessCookie = cookie.Value
	}
	if !credentials.Present() {
		writeError(writer, http.StatusUnauthorized, "sign in to use rag")
		return authclient.Credentials{}, false
	}
	if credentials.Authorization == "" && request.Method != http.MethodGet {
		origin := strings.TrimSpace(request.Header.Get("Origin"))
		if origin != "" {
			if _, allowed := server.allowedOrigins[origin]; !allowed {
				writeError(writer, http.StatusForbidden, "this origin may not use rag")
				return authclient.Credentials{}, false
			}
		}
	}
	return credentials, true
}

// handleError maps a domain error onto a status.
//
// Forbidden becomes not found for anything scoped to a corpus, because the
// alternative tells a caller that something exists which they may not see.
func (server *Server) handleError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrUnauthorized):
		writeError(writer, http.StatusUnauthorized, "sign in to use rag")
	case errors.Is(err, domain.ErrForbidden):
		writeError(writer, http.StatusForbidden, "you may not do that here")
	case errors.Is(err, domain.ErrNotFound):
		writeError(writer, http.StatusNotFound, "not found")
	case errors.Is(err, domain.ErrConflict):
		writeError(writer, http.StatusConflict, message(err))
	case errors.Is(err, domain.ErrInvalid):
		writeError(writer, http.StatusBadRequest, message(err))
	default:
		// An unexpected error can quote a provider, a statement or an
		// organization's own text. It goes to the log, not to the caller.
		server.logger.ErrorContext(request.Context(), "request failed",
			"method", request.Method, "path", request.URL.Path, "error", err)
		writeError(writer, http.StatusInternalServerError, "something went wrong")
	}
}

// message strips the sentinel suffix so the reason reads as a sentence.
func message(err error) string {
	text := err.Error()
	for _, sentinel := range []error{domain.ErrInvalid, domain.ErrConflict} {
		text = strings.TrimSuffix(text, ": "+sentinel.Error())
	}
	return text
}

func readJSON(writer http.ResponseWriter, request *http.Request, target any) bool {
	if contentType := request.Header.Get("Content-Type"); contentType != "" &&
		!strings.HasPrefix(strings.ToLower(contentType), "application/json") {
		writeError(writer, http.StatusUnsupportedMediaType, "send application/json")
		return false
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxJSONBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		// Naming the unexpected field is the difference between a caller
		// fixing a typo in a minute and guessing at it for an afternoon.
		writeError(writer, http.StatusBadRequest, fmt.Sprintf("the request body could not be read: %v", err))
		return false
	}
	return true
}

func writeJSON(writer http.ResponseWriter, status int, payload any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(payload); err != nil {
		// The status line is already out; there is nothing left to say.
		return
	}
}

func writeError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, map[string]string{"error": message})
}

func writeInvalid(writer http.ResponseWriter, err error) {
	writeError(writer, http.StatusBadRequest, message(err))
}
