package tools

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/Shubham04-oss/oryxa-go/integrations/host"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/httpserver"
)

const maxBody = 1 << 20

// Routes registers POST /v1/tools/call (internal: apps calls it for a member
// and agent it has already authenticated) and GET /v1/tools (internal: the
// tools a member's connections offer, for apps' tool search).
func Routes(server *httpserver.Server, service *Service, logger *slog.Logger) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	h := handlers{service: service, logger: logger}
	server.Internal("POST /v1/tools/call", h.call)
	server.Internal("GET /v1/tools", h.list)
}

// list answers 200 {"tools": […]} for ?org=&member=, and optionally &app=.
func (h handlers) list(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	offered, err := h.service.List(r.Context(), query.Get("org"), query.Get("member"), query.Get("app"))
	switch {
	case err == nil:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string][]Offered{"tools": offered})
	case errors.Is(err, ErrInvalid):
		httpserver.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		h.logger.ErrorContext(r.Context(), "list a member's tools", "error", err)
		httpserver.WriteError(w, http.StatusInternalServerError, "internal", "the tools could not be listed")
	}
}

type handlers struct {
	service *Service
	logger  *slog.Logger
}

type callBody struct {
	Org        string          `json:"org"`
	Member     string          `json:"member"`
	Agent      string          `json:"agent"`
	Role       string          `json:"role"`
	Connection string          `json:"connection"`
	Tool       string          `json:"tool"`
	Input      json.RawMessage `json:"input"`
	Confirmed  bool            `json:"confirmed"`
}

// call answers 200 {"output": …} when the tool ran, and 200 {"tool_error": …}
// when it ran and the app said no; anything that stopped it is a 4xx or 5xx
// with the service's error body.
func (h handlers) call(w http.ResponseWriter, r *http.Request) {
	var body callBody
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		httpserver.WriteError(w, http.StatusBadRequest, "invalid_request", "the body is not the JSON this route takes")
		return
	}
	result, err := h.service.Call(r.Context(), Request(body))
	switch {
	case err == nil:
		w.Header().Set("Content-Type", "application/json")
		if result.ToolError != nil {
			_ = json.NewEncoder(w).Encode(map[string]*ToolError{"tool_error": result.ToolError})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]json.RawMessage{"output": result.Output})
	case errors.Is(err, ErrInvalid):
		httpserver.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, ErrNotFound):
		httpserver.WriteError(w, http.StatusNotFound, "not_found", "no such connection or tool")
	case errors.Is(err, ErrConfirmation):
		httpserver.WriteError(w, http.StatusConflict, "confirmation_required", "this tool changes something; ask the member, then call again with confirmed")
	case errors.Is(err, ErrNeedsReauth):
		httpserver.WriteError(w, http.StatusConflict, "needs_reauth", "the connection needs to be signed in again")
	case errors.Is(err, ErrUnavailable):
		httpserver.WriteError(w, http.StatusConflict, "connection_unavailable", "the connection is not usable right now")
	case errors.Is(err, ErrOutputTooLarge):
		httpserver.WriteError(w, http.StatusBadGateway, "output_too_large", "the tool's output is over the limit")
	case errors.Is(err, host.ErrInvalidMessage):
		h.logger.WarnContext(r.Context(), "a connector broke the contract", "error", err)
		httpserver.WriteError(w, http.StatusBadGateway, "invalid_input_or_output", "the tool's input or output did not match its schema")
	default:
		h.logger.ErrorContext(r.Context(), "call a tool", "error", err)
		httpserver.WriteError(w, http.StatusInternalServerError, "internal", "the tool could not be called")
	}
}
