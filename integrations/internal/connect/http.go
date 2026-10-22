package connect

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/orgapps"
)

// CallbackPath is the browser-facing callback every app redirects to.
const CallbackPath = "/v1/integrations/oauth/callback"

const maxBody = 16 << 10

// Routes registers POST /v1/connect (internal: apps calls it for a member it
// has already authenticated), GET /v1/connections and GET and DELETE
// /v1/connections/{id} (internal), and GET /v1/integrations/oauth/callback
// (public: the browser, which proves itself by the state).
func Routes(server *httpserver.Server, service *Service, logger *slog.Logger) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	h := handlers{service: service, logger: logger}
	server.Internal("POST /v1/connect", h.connect)
	server.Internal("GET /v1/connections", h.list)
	server.Internal("GET /v1/connections/{id}", h.get)
	server.Internal("DELETE /v1/connections/{id}", h.remove)
	server.Public("GET "+CallbackPath, h.callback)
}

type handlers struct {
	service *Service
	logger  *slog.Logger
}

type connectBody struct {
	Org      string `json:"org"`
	Member   string `json:"member"`
	App      string `json:"app"`
	Level    string `json:"level"`
	ReturnTo string `json:"return_to"`
}

func (h handlers) connect(w http.ResponseWriter, r *http.Request) {
	var body connectBody
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		httpserver.WriteError(w, http.StatusBadRequest, "invalid_request", "the body is not the JSON this route takes")
		return
	}
	authorizeURL, err := h.service.Start(r.Context(), StartRequest(body))
	switch {
	case err == nil:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"url": authorizeURL})
	case errors.Is(err, ErrInvalid):
		httpserver.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, ErrNoConnector):
		httpserver.WriteError(w, http.StatusNotFound, "not_found", "that app cannot be connected here")
	case errors.Is(err, orgapps.ErrNotSetUp):
		httpserver.WriteError(w, http.StatusConflict, "not_set_up", "Your admin needs to set up "+body.App)
	case errors.Is(err, orgapps.ErrBroken):
		httpserver.WriteError(w, http.StatusConflict, "app_broken", "The organization's "+body.App+" app is not working; your admin needs to fix it")
	default:
		h.logger.ErrorContext(r.Context(), "start a sign-in", "error", err)
		httpserver.WriteError(w, http.StatusInternalServerError, "internal", "the sign-in could not be started")
	}
}

func (h handlers) callback(w http.ResponseWriter, r *http.Request) {
	// The code is in this URL and the answer carries a connection id.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	query := r.URL.Query()
	completion, err := h.service.Complete(r.Context(), Callback{
		State: query.Get("state"), Code: query.Get("code"), Error: query.Get("error"),
	})
	var failure *Failure
	switch {
	case errors.Is(err, ErrBadState):
		httpserver.WriteError(w, http.StatusBadRequest, "bad_state", "this sign-in is unknown, already used or expired; start again")
		return
	case err != nil && !errors.As(err, &failure):
		h.logger.ErrorContext(r.Context(), "finish a sign-in", "error", err)
		httpserver.WriteError(w, http.StatusInternalServerError, "internal", "the sign-in could not be finished")
		return
	}
	result := url.Values{"status": {"connected"}, "app": {completion.App}}
	status := http.StatusOK
	if failure != nil {
		result = url.Values{"status": {"failed"}, "app": {completion.App}, "reason": {failure.Reason}}
		status = http.StatusBadGateway
		if failure.Reason == ReasonDenied || failure.Reason == ReasonInvalid {
			status = http.StatusForbidden
		}
	} else {
		result.Set("connection", completion.ConnectionID)
	}
	if completion.ReturnTo == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": result.Get("status"), "reason": result.Get("reason")})
		return
	}
	// ReturnTo was checked against the allowed origins a moment ago.
	target, _ := url.Parse(completion.ReturnTo)
	merged := target.Query()
	for name, values := range result {
		merged[name] = values
	}
	target.RawQuery = merged.Encode()
	http.Redirect(w, r, target.String(), http.StatusSeeOther)
}

func (h handlers) list(w http.ResponseWriter, r *http.Request) {
	org, member := r.URL.Query().Get("org"), r.URL.Query().Get("member")
	if org == "" {
		httpserver.WriteError(w, http.StatusBadRequest, "invalid_request", "org is required")
		return
	}
	list, err := h.service.List(r.Context(), org, member)
	if err != nil {
		h.fail(w, r, "list connections", err)
		return
	}
	writeJSON(w, map[string]any{"connections": list})
}

func (h handlers) get(w http.ResponseWriter, r *http.Request) {
	c, err := h.service.Get(r.Context(), r.URL.Query().Get("org"), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, "read a connection", err)
		return
	}
	writeJSON(w, c)
}

// remove revokes the connection, but only after finding it in the org asked
// for, so an id from another org is as missing as one that never existed.
func (h handlers) remove(w http.ResponseWriter, r *http.Request) {
	c, err := h.service.Get(r.Context(), r.URL.Query().Get("org"), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, "find a connection to remove", err)
		return
	}
	providerRevoked, err := h.service.Revoke(r.Context(), c.ID)
	if err != nil {
		h.fail(w, r, "remove a connection", err)
		return
	}
	writeJSON(w, map[string]bool{"provider_revoked": providerRevoked})
}

func (h handlers) fail(w http.ResponseWriter, r *http.Request, doing string, err error) {
	if errors.Is(err, ErrNotFound) {
		httpserver.WriteError(w, http.StatusNotFound, "not_found", "no such connection")
		return
	}
	h.logger.ErrorContext(r.Context(), doing, "error", err)
	httpserver.WriteError(w, http.StatusInternalServerError, "internal", "the request could not be completed")
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}
