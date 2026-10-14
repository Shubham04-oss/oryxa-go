// Package httpserver serves the integrations service's HTTP API.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// ServiceVerifier checks the token another Oryxa service signed.
type ServiceVerifier interface {
	Verify(context.Context, string) (serviceauth.Claims, error)
}

type claimsKey struct{}

// Claims returns who the verified caller of an internal route is.
func Claims(ctx context.Context) (serviceauth.Claims, bool) {
	claims, ok := ctx.Value(claimsKey{}).(serviceauth.Claims)
	return claims, ok
}

// Server is the service's routes.
type Server struct {
	verifier ServiceVerifier
	logger   *slog.Logger
	mux      *http.ServeMux
	patterns []string
}

// New returns the server. A nil verifier refuses every internal call.
func New(verifier ServiceVerifier, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	server := &Server{verifier: verifier, logger: logger, mux: http.NewServeMux()}
	server.Public("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	return server
}

// Public registers a route that needs no service token: liveness, and the
// browser- and app-facing routes that carry their own proof.
func (server *Server) Public(pattern string, handler http.HandlerFunc) {
	server.mux.HandleFunc(pattern, handler)
	server.patterns = append(server.patterns, pattern)
}

// Internal registers a route only another Oryxa service may call. Whatever
// fails to verify is refused; nothing falls back to allowing the call.
func (server *Server) Internal(pattern string, handler http.HandlerFunc) {
	server.Public(pattern, func(writer http.ResponseWriter, request *http.Request) {
		token := request.Header.Get(serviceauth.Header)
		if token == "" || server.verifier == nil {
			writeError(writer, http.StatusUnauthorized, "unauthorized", "a service token is required")
			return
		}
		claims, err := server.verifier.Verify(request.Context(), token)
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
		handler(writer, request.WithContext(context.WithValue(request.Context(), claimsKey{}, claims)))
	})
}

// Patterns are the routes registered, for the host to route to the server.
func (server *Server) Patterns() []string { return append([]string(nil), server.patterns...) }

func (server *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	server.mux.ServeHTTP(writer, request)
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]map[string]string{"error": {"code": code, "message": message}})
}
