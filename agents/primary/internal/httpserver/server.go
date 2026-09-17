package httpserver

import (
	"net/http"
	"slices"
)

type Server struct {
	mux      *http.ServeMux
	patterns []string
}

func New() *Server {
	server := &Server{mux: http.NewServeMux()}
	server.handle("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
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
	server.mux.ServeHTTP(writer, request)
}
