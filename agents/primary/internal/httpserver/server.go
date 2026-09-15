package httpserver

import "net/http"

type Server struct {
	mux *http.ServeMux
}

func New() *Server {
	server := &Server{mux: http.NewServeMux()}
	server.mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	return server
}

func (server *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	server.mux.ServeHTTP(writer, request)
}
