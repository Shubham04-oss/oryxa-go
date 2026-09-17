package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/billing/internal/application"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/catalog"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/dodo"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/domain"
)

const maxBodyBytes = 1 << 20

type billingService interface {
	Plans() ([]catalog.Plan, catalog.Allowance)
	Summary(context.Context, authclient.Credentials, string) (application.Summary, error)
	StartCheckout(context.Context, authclient.Credentials, string, string, int) (string, error)
	OpenPortal(context.Context, authclient.Credentials, string) (string, error)
	HandleWebhook(context.Context, dodo.Webhook) error
}

type webhookParser interface {
	ParseWebhook([]byte, http.Header) (dodo.Webhook, error)
}

type Config struct {
	// AllowedOrigins may send cookie-authenticated requests that change
	// something, which keeps other sites from starting checkouts in a
	// signed-in browser.
	AllowedOrigins []string
}

type Server struct {
	service        billingService
	webhooks       webhookParser
	allowedOrigins map[string]struct{}
	logger         *slog.Logger
	mux            *http.ServeMux
	patterns       []string
}

func New(service billingService, webhooks webhookParser, config Config, logger *slog.Logger) *Server {
	server := &Server{
		service:        service,
		webhooks:       webhooks,
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
	server.handle("GET /v1/billing/plans", server.plans)
	server.handle("GET /v1/billing/organizations/{organizationID}", server.summary)
	server.handle("POST /v1/billing/organizations/{organizationID}/checkout", server.checkout)
	server.handle("POST /v1/billing/organizations/{organizationID}/portal", server.portal)
	server.handle("POST /v1/billing/webhooks/dodo", server.dodoWebhook)
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

type allowanceResponse struct {
	MemberLimit *int `json:"member_limit"`
}

type planResponse struct {
	Key                string `json:"key"`
	MemberLimit        *int   `json:"member_limit"`
	MemberLimitPerUnit *int   `json:"member_limit_per_unit,omitempty"`
	MinQuantity        int    `json:"min_quantity,omitempty"`
	MaxQuantity        int    `json:"max_quantity,omitempty"`
}

func (server *Server) plans(writer http.ResponseWriter, _ *http.Request) {
	plans, unpaid := server.service.Plans()
	response := struct {
		Unpaid allowanceResponse `json:"unpaid"`
		Plans  []planResponse    `json:"plans"`
	}{Unpaid: allowanceResponse{MemberLimit: unpaid.MemberLimit}, Plans: []planResponse{}}
	for _, plan := range plans {
		response.Plans = append(response.Plans, planResponse{
			Key:                plan.Key,
			MemberLimit:        plan.MemberLimit,
			MemberLimitPerUnit: plan.MemberLimitPerUnit,
			MinQuantity:        plan.MinQuantity,
			MaxQuantity:        plan.MaxQuantity,
		})
	}
	writeJSON(writer, http.StatusOK, response)
}

type entitlementsResponse struct {
	Plan        string     `json:"plan"`
	Status      string     `json:"status"`
	MemberLimit *int       `json:"member_limit"`
	GraceEndsAt *time.Time `json:"grace_ends_at"`
}

type subscriptionResponse struct {
	Plan        string     `json:"plan"`
	Status      string     `json:"status"`
	Quantity    int        `json:"quantity"`
	RenewsAt    *time.Time `json:"renews_at"`
	EndsAt      *time.Time `json:"ends_at"`
	GraceEndsAt *time.Time `json:"grace_ends_at"`
}

func (server *Server) summary(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	summary, err := server.service.Summary(request.Context(), credentials, request.PathValue("organizationID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	response := struct {
		OrganizationID string                `json:"organization_id"`
		CanManage      bool                  `json:"can_manage"`
		Entitlements   entitlementsResponse  `json:"entitlements"`
		Subscription   *subscriptionResponse `json:"subscription"`
	}{
		OrganizationID: summary.OrganizationID,
		CanManage:      summary.CanManage,
		Entitlements: entitlementsResponse{
			Plan:        summary.Entitlements.Plan,
			Status:      summary.Entitlements.Status,
			MemberLimit: summary.Entitlements.MemberLimit,
			GraceEndsAt: summary.Entitlements.GraceEndsAt,
		},
	}
	if subscription := summary.Subscription; subscription != nil {
		response.Subscription = &subscriptionResponse{
			Plan:        subscription.Plan,
			Status:      string(subscription.Status),
			Quantity:    subscription.Quantity,
			RenewsAt:    subscription.RenewsAt,
			EndsAt:      subscription.EndsAt,
			GraceEndsAt: subscription.GraceEndsAt,
		}
	}
	writeJSON(writer, http.StatusOK, response)
}

func (server *Server) checkout(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var input struct {
		Plan     string `json:"plan"`
		Quantity int    `json:"quantity"`
	}
	if err := readJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	checkoutURL, err := server.service.StartCheckout(request.Context(), credentials, request.PathValue("organizationID"), input.Plan, input.Quantity)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]string{"checkout_url": checkoutURL})
}

func (server *Server) portal(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	portalURL, err := server.service.OpenPortal(request.Context(), credentials, request.PathValue("organizationID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]string{"portal_url": portalURL})
}

// dodoWebhook answers 2xx only once the delivery is applied, so Dodo retries
// anything that failed.
func (server *Server) dodoWebhook(writer http.ResponseWriter, request *http.Request) {
	payload, err := io.ReadAll(io.LimitReader(request.Body, maxBodyBytes+1))
	if err != nil || len(payload) > maxBodyBytes {
		writeError(writer, http.StatusBadRequest, "invalid webhook body")
		return
	}
	webhook, err := server.webhooks.ParseWebhook(payload, request.Header)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	if err := server.service.HandleWebhook(request.Context(), webhook); err != nil {
		server.handleError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// credentials reads the caller's auth credentials, refusing a request that has
// none, or that changes something on a session cookie from an origin not
// allowed to.
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
	case errors.Is(err, dodo.ErrInvalidWebhook):
		writeError(writer, http.StatusUnauthorized, "webhook signature is invalid")
	case errors.Is(err, domain.ErrInvalid):
		writeError(writer, http.StatusBadRequest, strings.TrimSuffix(err.Error(), ": "+domain.ErrInvalid.Error()))
	case errors.Is(err, domain.ErrUnauthorized):
		writeError(writer, http.StatusUnauthorized, "authentication failed")
	case errors.Is(err, domain.ErrForbidden):
		writeError(writer, http.StatusForbidden, "operation is not allowed")
	case errors.Is(err, domain.ErrNotFound):
		writeError(writer, http.StatusNotFound, "resource not found")
	case errors.Is(err, domain.ErrConflict):
		writeError(writer, http.StatusConflict, strings.TrimSuffix(err.Error(), ": "+domain.ErrConflict.Error()))
	case errors.Is(err, domain.ErrNotConfigured):
		writeError(writer, http.StatusServiceUnavailable, "billing is not configured")
	default:
		server.logger.ErrorContext(request.Context(), "billing request failed", "method", request.Method, "path", request.URL.Path, "error", err)
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

func writeError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, map[string]string{"error": message})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
