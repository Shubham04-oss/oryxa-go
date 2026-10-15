package orgapps

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// maxBody bounds a request: the largest secret, and room for the rest.
const maxBody = 64 << 10

// Routes registers PUT, GET and DELETE /v1/orgs/{org}/apps/{app} on server.
// They are internal: apps calls them for an admin it has already checked.
func Routes(server *httpserver.Server, service *Service, logger *slog.Logger) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	h := handlers{service: service, logger: logger}
	server.Internal("PUT /v1/orgs/{org}/apps/{app}", h.put)
	server.Internal("GET /v1/orgs/{org}/apps/{app}", h.get)
	server.Internal("DELETE /v1/orgs/{org}/apps/{app}", h.retire)
}

type handlers struct {
	service *Service
	logger  *slog.Logger
}

type putBody struct {
	ClientID      string            `json:"client_id"`
	Secret        string            `json:"secret"`
	WebhookSecret string            `json:"webhook_secret"`
	Extra         map[string]string `json:"extra"`
	ExpiresAt     *time.Time        `json:"expires_at"`
	ActorID       string            `json:"actor_id"`
}

func (h handlers) put(w http.ResponseWriter, r *http.Request) {
	var body putBody
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		httpserver.WriteError(w, http.StatusBadRequest, "invalid_request", "the body is not the JSON this route takes")
		return
	}
	view, changed, err := h.service.Put(r.Context(), r.PathValue("org"), r.PathValue("app"), Input{
		ClientID: body.ClientID, Secret: secrets.Secret(body.Secret), WebhookSecret: secrets.Secret(body.WebhookSecret),
		Extra: body.Extra, ExpiresAt: body.ExpiresAt, ActorID: body.ActorID,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, struct {
		View
		Changed bool `json:"changed"`
	}{view, changed})
}

func (h handlers) get(w http.ResponseWriter, r *http.Request) {
	view, err := h.service.Get(r.Context(), r.PathValue("org"), r.PathValue("app"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, view)
}

func (h handlers) retire(w http.ResponseWriter, r *http.Request) {
	retired, err := h.service.Retire(r.Context(), r.PathValue("org"), r.PathValue("app"))
	switch {
	case err != nil:
		h.fail(w, r, err)
	case !retired:
		h.fail(w, r, ErrNotFound)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// fail answers err. An invalid input says why; anything unexpected says
// nothing of itself, so a database or KMS message never reaches a caller.
func (h handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrInvalid):
		httpserver.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, ErrNotFound):
		httpserver.WriteError(w, http.StatusNotFound, "not_found", "the org has no app of its own for that app")
	case errors.Is(err, ErrConflict):
		httpserver.WriteError(w, http.StatusConflict, "conflict", "the app changed while it was being saved; retry")
	default:
		h.logger.ErrorContext(r.Context(), "org apps", "error", err)
		httpserver.WriteError(w, http.StatusInternalServerError, "internal", "the app could not be saved or read")
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
