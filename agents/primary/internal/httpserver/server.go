// Package httpserver is the agents service's HTTP API: a member's threads
// with Krude, and the runs that answer them, streamed as server-sent events.
// It decides who the caller is by asking auth, and turns the threads
// service's errors into the documented error codes.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

const maxJSONBytes = 256 << 10

// keepAliveInterval is how often a quiet stream sends a comment, so a proxy
// that closes idle connections does not cut a run off while Krude works.
const keepAliveInterval = 15 * time.Second

// streamGrace is how much longer than a run its stream may stay open, for
// the run's last items to go out after the run itself stops.
const streamGrace = 30 * time.Second

// Directory is auth, deciding who a caller is in an organization.
// authclient.Client implements it.
type Directory interface {
	Membership(ctx context.Context, organizationID string, credentials authclient.Credentials) (authclient.Membership, error)
}

// Threads is the run API beneath HTTP. threads.Service implements it.
type Threads interface {
	Create(ctx context.Context, member threads.Member) (threads.Thread, error)
	List(ctx context.Context, member threads.Member) ([]threads.Thread, error)
	Get(ctx context.Context, member threads.Member, threadID string) (threads.Detail, error)
	Delete(ctx context.Context, member threads.Member, threadID string) error
	StartRun(ctx context.Context, member threads.Member, threadID string, input threads.RunInput) (Run, error)
	Artifact(ctx context.Context, member threads.Member, threadID, name string, version int64) (threads.File, error)
}

// Run is a started run. *threads.Run implements it.
type Run interface {
	ID() string
	Execute(ctx context.Context, emit func(threads.Item) error)
	Discard(ctx context.Context, reason string)
}

// Config is how the server runs.
type Config struct {
	// AllowedOrigins may send cookie-authenticated requests that change
	// something, such as starting a run.
	AllowedOrigins []string
	// RunTimeout bounds one run, so a stream's own deadline can outlast it.
	RunTimeout time.Duration
	// Unavailable, when set, is why the service cannot run agents here: every
	// thread route answers 503 with it, and the health check still passes.
	Unavailable string
}

// Server serves the API.
type Server struct {
	directory      Directory
	threads        Threads
	allowedOrigins map[string]struct{}
	runTimeout     time.Duration
	unavailable    string
	logger         *slog.Logger
	mux            *http.ServeMux
	patterns       []string
}

// New returns the server. directory and service may be nil only when
// config.Unavailable says why.
func New(directory Directory, service Threads, config Config, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	server := &Server{
		directory:      directory,
		threads:        service,
		allowedOrigins: map[string]struct{}{},
		runTimeout:     config.RunTimeout,
		unavailable:    config.Unavailable,
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
	server.handle("GET /v1/organizations/{organizationID}/threads", server.listThreads)
	server.handle("POST /v1/organizations/{organizationID}/threads", server.createThread)
	server.handle("GET /v1/organizations/{organizationID}/threads/{threadID}", server.getThread)
	server.handle("DELETE /v1/organizations/{organizationID}/threads/{threadID}", server.deleteThread)
	server.handle("POST /v1/organizations/{organizationID}/threads/{threadID}/runs", server.run)
	server.handle("GET /v1/organizations/{organizationID}/threads/{threadID}/artifacts/{name}", server.artifact)
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

func (server *Server) listThreads(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	listed, err := server.threads.List(request.Context(), member)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"threads": listed})
}

func (server *Server) createThread(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	// The body is optional, and nothing in it is read yet: a thread starts
	// empty. An object with fields this version does not know is refused
	// rather than silently dropped.
	var body struct{}
	if request.ContentLength != 0 && !server.decode(writer, request, &body) {
		return
	}
	created, err := server.threads.Create(request.Context(), member)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, created)
}

func (server *Server) getThread(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	detail, err := server.threads.Get(request.Context(), member, request.PathValue("threadID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, detail)
}

func (server *Server) deleteThread(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	if err := server.threads.Delete(request.Context(), member, request.PathValue("threadID")); err != nil {
		server.handleError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// shownInline are the kinds of file a browser may show in place. Everything
// else a run kept — HTML, scripts, anything a page could run — is only ever
// downloaded, since it is served from the app's own origin.
var shownInline = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
	"application/pdf": true, "text/plain": true,
}

// artifact serves a file a thread's runs kept.
func (server *Server) artifact(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	var version int64
	if raw := request.URL.Query().Get("version"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 1 {
			writeError(writer, http.StatusBadRequest, "invalid_request", "version must be a positive whole number")
			return
		}
		version = parsed
	}
	file, err := server.threads.Artifact(request.Context(), member, request.PathValue("threadID"), request.PathValue("name"), version)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	mediaType, _, err := mime.ParseMediaType(file.MIMEType)
	if err != nil || mediaType == "" {
		mediaType = "application/octet-stream"
	}
	header := writer.Header()
	header.Set("Content-Type", mediaType)
	if mediaType == "text/plain" {
		header.Set("Content-Type", "text/plain; charset=utf-8")
	}
	// Nothing a run kept may run as a page of this origin, even if a
	// browser is talked into rendering it.
	header.Set("Content-Security-Policy", "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; sandbox")
	disposition := "attachment"
	if shownInline[mediaType] {
		disposition = "inline"
	}
	// A "user:" file is the member's own; the prefix is how ADK scopes it,
	// not part of its name, and no file system would take the colon.
	header.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{
		"filename": path.Base(strings.TrimPrefix(file.Name, "user:")),
	}))
	header.Set("Content-Length", strconv.Itoa(len(file.Data)))
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(file.Data)
}

// run starts a run on a thread and streams it. Everything that can refuse the
// run happens before the stream opens, so a refusal is an ordinary JSON
// error; once the stream is open, the run's own end is its last event.
func (server *Server) run(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	var body struct {
		Message       string `json:"message"`
		Confirmations []struct {
			ID        string `json:"id"`
			Confirmed *bool  `json:"confirmed"`
		} `json:"confirmations"`
	}
	if !server.decode(writer, request, &body) {
		return
	}
	input := threads.RunInput{Message: body.Message}
	for _, answer := range body.Confirmations {
		if answer.ID == "" || answer.Confirmed == nil {
			writeError(writer, http.StatusBadRequest, "invalid_request", "every confirmation needs an id and whether it is confirmed")
			return
		}
		input.Confirmations = append(input.Confirmations, threads.Answer{ID: answer.ID, Confirmed: *answer.Confirmed})
	}

	started, err := server.threads.StartRun(request.Context(), member, request.PathValue("threadID"), input)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}

	controller := http.NewResponseController(writer)
	// The host sets no write deadline for this service; a stream sets its
	// own, just past the longest a run may take.
	if err := controller.SetWriteDeadline(time.Now().Add(server.runTimeout + streamGrace)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		started.Discard(request.Context(), "the stream could not be opened")
		writeError(writer, http.StatusInternalServerError, "internal", "internal server error")
		return
	}
	header := writer.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("X-Accel-Buffering", "no")
	header.Set("X-Oryxa-Run", started.ID())
	writer.WriteHeader(http.StatusOK)
	if err := controller.Flush(); err != nil {
		started.Discard(request.Context(), "the stream could not be opened")
		return
	}

	stream := newEventStream(writer, controller)
	stopKeepAlive := stream.keepAlive(keepAliveInterval)
	defer stopKeepAlive()
	started.Execute(request.Context(), stream.item)
}

// member decides who the caller is in the path's organization. An agent
// acting for a member is refused: a thread is the member's own conversation,
// and an agent reaches Krude from inside a run, never through this API.
func (server *Server) member(writer http.ResponseWriter, request *http.Request) (threads.Member, bool) {
	if server.unavailable != "" {
		writeError(writer, http.StatusServiceUnavailable, "agents_unavailable", server.unavailable)
		return threads.Member{}, false
	}
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return threads.Member{}, false
	}
	organizationID := request.PathValue("organizationID")
	membership, err := server.directory.Membership(request.Context(), organizationID, credentials)
	if err != nil {
		server.handleError(writer, request, err)
		return threads.Member{}, false
	}
	if membership.Agent {
		writeError(writer, http.StatusForbidden, "forbidden", "an agent cannot hold a member's conversation")
		return threads.Member{}, false
	}
	return threads.Member{
		UserID:         membership.UserID,
		OrganizationID: organizationID,
		AccessToken:    credentials.AccessToken(),
	}, true
}

// credentials reads how the caller signs in. A cookie alone cannot change
// anything unless the request comes from an allowed origin, so another site
// cannot start a run in a member's name.
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

// ErrorCode names a failure the way the API does.
func ErrorCode(err error) (int, string) {
	switch {
	case errors.Is(err, threads.ErrInvalid):
		return http.StatusBadRequest, "invalid_request"
	case errors.Is(err, authclient.ErrUnauthorized):
		return http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, authclient.ErrForbidden):
		return http.StatusForbidden, "forbidden"
	case errors.Is(err, threads.ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, threads.ErrRunInProgress):
		return http.StatusConflict, "run_in_progress"
	case errors.Is(err, threads.ErrConfirmationPending):
		return http.StatusConflict, "confirmation_pending"
	case errors.Is(err, authclient.ErrRateLimited):
		return http.StatusTooManyRequests, "rate_limited"
	case errors.Is(err, authclient.ErrUnavailable):
		return http.StatusServiceUnavailable, "unavailable"
	default:
		return http.StatusInternalServerError, "internal"
	}
}

// message is what a caller is told about a failure: the service's own words
// for what they can act on, and nothing for the rest.
func message(err error) string {
	status, code := ErrorCode(err)
	switch {
	case status == http.StatusInternalServerError:
		return "internal server error"
	case code == "invalid_request":
		// The wording after the sentinel says what to fix.
		text := strings.TrimPrefix(err.Error(), threads.ErrInvalid.Error()+": ")
		return strings.TrimSpace(text)
	default:
		return err.Error()
	}
}

// handleError answers a failed call with its error code. Errors never carry
// tokens or conversation content, so they can be logged.
func (server *Server) handleError(writer http.ResponseWriter, request *http.Request, err error) {
	status, code := ErrorCode(err)
	switch {
	case status >= 500:
		server.logger.ErrorContext(request.Context(), "agents request failed",
			"method", request.Method, "path", request.URL.Path, "error", err)
	case code == "rate_limited":
		server.logger.WarnContext(request.Context(), "agents request rate limited", "path", request.URL.Path)
	}
	writeError(writer, status, code, message(err))
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]map[string]string{"error": {"code": code, "message": message}})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
