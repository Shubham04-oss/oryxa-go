package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/billing/internal/application"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/catalog"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/dodo"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/domain"
)

type fakeService struct {
	credentials authclient.Credentials
	checkout    []string
	err         error
	webhooks    []dodo.Webhook
}

func (fake *fakeService) Plans() ([]catalog.Plan, catalog.Allowance) {
	limit, perUnit := 20, 1
	return []catalog.Plan{
		{Key: "team", ProductID: "pdt_team", MemberLimit: &limit},
		{Key: "seats", ProductID: "pdt_seats", MemberLimitPerUnit: &perUnit, MinQuantity: 5, MaxQuantity: 100},
	}, catalog.Allowance{}
}

func (fake *fakeService) Summary(_ context.Context, credentials authclient.Credentials, organizationID string) (application.Summary, error) {
	fake.credentials = credentials
	if fake.err != nil {
		return application.Summary{}, fake.err
	}
	limit := 20
	renewsAt := time.Date(2026, time.October, 13, 12, 0, 0, 0, time.UTC)
	return application.Summary{
		OrganizationID: organizationID,
		CanManage:      true,
		Entitlements:   domain.Entitlements{Plan: "team", Status: "active", MemberLimit: &limit},
		Subscription:   &application.SubscriptionSummary{Plan: "team", Status: domain.SubscriptionActive, Quantity: 1, RenewsAt: &renewsAt},
	}, nil
}

func (fake *fakeService) StartCheckout(_ context.Context, credentials authclient.Credentials, organizationID, plan string, quantity int) (string, error) {
	fake.credentials = credentials
	fake.checkout = []string{organizationID, plan, fmt.Sprint(quantity)}
	if fake.err != nil {
		return "", fake.err
	}
	return "https://checkout.test/session", nil
}

func (fake *fakeService) OpenPortal(_ context.Context, credentials authclient.Credentials, _ string) (string, error) {
	fake.credentials = credentials
	return "https://portal.test/session", fake.err
}

func (fake *fakeService) HandleWebhook(_ context.Context, webhook dodo.Webhook) error {
	fake.webhooks = append(fake.webhooks, webhook)
	return fake.err
}

type fakeParser struct {
	err error
}

func (parser fakeParser) ParseWebhook(payload []byte, headers http.Header) (dodo.Webhook, error) {
	if parser.err != nil {
		return dodo.Webhook{}, parser.err
	}
	return dodo.Webhook{ID: headers.Get("webhook-id"), Type: string(payload)}, nil
}

func newServer(service *fakeService, parser fakeParser) *Server {
	return New(service, parser, Config{AllowedOrigins: []string{"https://app.example.com/settings"}}, slog.New(slog.DiscardHandler))
}

func serve(server http.Handler, method, target, body string, change func(*http.Request)) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if change != nil {
		change(request)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

func bearer(request *http.Request) {
	request.Header.Set("Authorization", "Bearer token-1")
}

func TestPatternsNameEveryRoute(t *testing.T) {
	server := New(&fakeService{}, fakeParser{}, Config{}, nil)
	patterns := server.Patterns()
	if len(patterns) < 6 {
		t.Fatalf("patterns = %v", patterns)
	}
	wildcards := regexp.MustCompile(`\{[^}]+\}`)
	for _, pattern := range patterns {
		method, path, _ := strings.Cut(pattern, " ")
		request := httptest.NewRequest(method, wildcards.ReplaceAllString(path, "x"), nil)
		if _, matched := server.mux.Handler(request); matched != pattern {
			t.Errorf("%s routes to %q", pattern, matched)
		}
	}
}

func TestPlans(t *testing.T) {
	response := serve(newServer(&fakeService{}, fakeParser{}), http.MethodGet, "/v1/billing/plans", "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var body struct {
		Unpaid struct {
			MemberLimit *int `json:"member_limit"`
		} `json:"unpaid"`
		Plans []map[string]any `json:"plans"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Unpaid.MemberLimit != nil || len(body.Plans) != 2 || body.Plans[0]["key"] != "team" || body.Plans[0]["member_limit"] != float64(20) ||
		body.Plans[1]["member_limit_per_unit"] != float64(1) || body.Plans[1]["min_quantity"] != float64(5) {
		t.Fatalf("body = %s", response.Body)
	}
	if _, leaks := body.Plans[0]["product_id"]; leaks {
		t.Fatalf("plans expose Dodo product ids: %s", response.Body)
	}
}

func TestSummaryResponse(t *testing.T) {
	service := &fakeService{}
	response := serve(newServer(service, fakeParser{}), http.MethodGet, "/v1/billing/organizations/org_1", "", bearer)

	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status = %d, cache = %q", response.Code, response.Header().Get("Cache-Control"))
	}
	want := `{"organization_id":"org_1","can_manage":true,"entitlements":{"plan":"team","status":"active","member_limit":20,"grace_ends_at":null},"subscription":{"plan":"team","status":"active","quantity":1,"renews_at":"2026-10-13T12:00:00Z","ends_at":null,"grace_ends_at":null}}`
	if strings.TrimSpace(response.Body.String()) != want {
		t.Fatalf("body = %s\nwant   %s", response.Body, want)
	}
	if service.credentials.Authorization != "Bearer token-1" {
		t.Fatalf("credentials = %+v", service.credentials)
	}
}

func TestCredentials(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		target     string
		change     func(*http.Request)
		wantStatus int
	}{
		{name: "none", method: http.MethodGet, target: "/v1/billing/organizations/org_1", wantStatus: http.StatusUnauthorized},
		{name: "not a bearer token", method: http.MethodGet, target: "/v1/billing/organizations/org_1", wantStatus: http.StatusUnauthorized, change: func(request *http.Request) {
			request.Header.Set("Authorization", "Basic abc")
		}},
		{name: "cookie read", method: http.MethodGet, target: "/v1/billing/organizations/org_1", wantStatus: http.StatusOK, change: func(request *http.Request) {
			request.AddCookie(&http.Cookie{Name: authclient.AccessCookieName, Value: "cookie-1"})
		}},
		{name: "cookie checkout without origin", method: http.MethodPost, target: "/v1/billing/organizations/org_1/checkout", wantStatus: http.StatusForbidden, change: func(request *http.Request) {
			request.AddCookie(&http.Cookie{Name: authclient.AccessCookieName, Value: "cookie-1"})
		}},
		{name: "cookie checkout from another site", method: http.MethodPost, target: "/v1/billing/organizations/org_1/checkout", wantStatus: http.StatusForbidden, change: func(request *http.Request) {
			request.AddCookie(&http.Cookie{Name: authclient.AccessCookieName, Value: "cookie-1"})
			request.Header.Set("Origin", "https://evil.example")
		}},
		{name: "cookie checkout from the app", method: http.MethodPost, target: "/v1/billing/organizations/org_1/checkout", wantStatus: http.StatusCreated, change: func(request *http.Request) {
			request.AddCookie(&http.Cookie{Name: authclient.AccessCookieName, Value: "cookie-1"})
			request.Header.Set("Origin", "https://app.example.com")
		}},
		{name: "bearer checkout needs no origin", method: http.MethodPost, target: "/v1/billing/organizations/org_1/checkout", wantStatus: http.StatusCreated, change: bearer},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := ""
			if test.method == http.MethodPost {
				body = `{"plan":"team"}`
			}
			response := serve(newServer(&fakeService{}, fakeParser{}), test.method, test.target, body, test.change)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", response.Code, test.wantStatus, response.Body)
			}
		})
	}
}

func TestCheckout(t *testing.T) {
	service := &fakeService{}
	response := serve(newServer(service, fakeParser{}), http.MethodPost, "/v1/billing/organizations/org_1/checkout", `{"plan":"seats","quantity":12}`, bearer)

	if response.Code != http.StatusCreated || strings.TrimSpace(response.Body.String()) != `{"checkout_url":"https://checkout.test/session"}` {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body)
	}
	if fmt.Sprint(service.checkout) != "[org_1 seats 12]" {
		t.Fatalf("checkout call = %v", service.checkout)
	}

	for _, body := range []string{`{"plan":"team","coupon":"x"}`, `{"plan":`, `{"plan":"team"}{}`} {
		if response := serve(newServer(&fakeService{}, fakeParser{}), http.MethodPost, "/v1/billing/organizations/org_1/checkout", body, bearer); response.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d, want 400", body, response.Code)
		}
	}
}

func TestPortal(t *testing.T) {
	response := serve(newServer(&fakeService{}, fakeParser{}), http.MethodPost, "/v1/billing/organizations/org_1/portal", "", bearer)
	if response.Code != http.StatusCreated || strings.TrimSpace(response.Body.String()) != `{"portal_url":"https://portal.test/session"}` {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body)
	}
}

func TestErrorStatuses(t *testing.T) {
	tests := map[error]int{
		fmt.Errorf("unknown plan %q: %w", "x", domain.ErrInvalid): http.StatusBadRequest,
		domain.ErrUnauthorized: http.StatusUnauthorized,
		domain.ErrForbidden:    http.StatusForbidden,
		domain.ErrNotFound:     http.StatusNotFound,
		fmt.Errorf("organization already has a active subscription: %w", domain.ErrConflict): http.StatusConflict,
		domain.ErrNotConfigured:     http.StatusServiceUnavailable,
		errors.New("database down"): http.StatusInternalServerError,
	}
	for err, want := range tests {
		response := serve(newServer(&fakeService{err: err}, fakeParser{}), http.MethodPost, "/v1/billing/organizations/org_1/checkout", `{"plan":"team"}`, bearer)
		if response.Code != want {
			t.Fatalf("%v: status = %d, want %d", err, response.Code, want)
		}
		if want == http.StatusInternalServerError && strings.Contains(response.Body.String(), "database") {
			t.Fatalf("internal error leaked: %s", response.Body)
		}
	}
	if response := serve(newServer(&fakeService{err: fmt.Errorf("unknown plan %q: %w", "gold", domain.ErrInvalid)}, fakeParser{}), http.MethodPost, "/v1/billing/organizations/org_1/checkout", `{"plan":"gold"}`, bearer); !strings.Contains(response.Body.String(), `unknown plan \"gold\"`) {
		t.Fatalf("invalid input message = %s", response.Body)
	}
}

func TestDodoWebhook(t *testing.T) {
	service := &fakeService{}
	response := serve(newServer(service, fakeParser{}), http.MethodPost, "/v1/billing/webhooks/dodo", "subscription.active", func(request *http.Request) {
		request.Header.Set("webhook-id", "msg_1")
	})
	if response.Code != http.StatusNoContent || len(service.webhooks) != 1 || service.webhooks[0].ID != "msg_1" {
		t.Fatalf("status = %d, webhooks = %+v", response.Code, service.webhooks)
	}

	tests := []struct {
		name    string
		service *fakeService
		parser  fakeParser
		body    string
		want    int
	}{
		{name: "bad signature", service: &fakeService{}, parser: fakeParser{err: fmt.Errorf("%w: no match", dodo.ErrInvalidWebhook)}, body: "x", want: http.StatusUnauthorized},
		{name: "not configured", service: &fakeService{}, parser: fakeParser{err: domain.ErrNotConfigured}, body: "x", want: http.StatusServiceUnavailable},
		{name: "processing failed", service: &fakeService{err: errors.New("database down")}, body: "subscription.active", want: http.StatusInternalServerError},
		{name: "too large", service: &fakeService{}, body: strings.Repeat("x", maxBodyBytes+1), want: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if response := serve(newServer(test.service, test.parser), http.MethodPost, "/v1/billing/webhooks/dodo", test.body, nil); response.Code != test.want {
				t.Fatalf("status = %d, want %d", response.Code, test.want)
			}
		})
	}
}
