package dodo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeDodo answers the Dodo Payments API calls billing makes, recording each
// request it receives.
type fakeDodo struct {
	t        *testing.T
	mu       sync.Mutex
	requests []recordedRequest
	respond  map[string]func(recordedRequest) (int, string)
}

type recordedRequest struct {
	Method string
	Path   string
	Query  string
	Auth   string
	Body   map[string]any
}

func newFakeDodo(t *testing.T) (*fakeDodo, *Client) {
	t.Helper()
	fake := &fakeDodo{t: t, respond: map[string]func(recordedRequest) (int, string){}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	client, err := New(Config{APIKey: "test-key", WebhookKey: testWebhookKey, BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return fake, client
}

func (fake *fakeDodo) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	raw, _ := io.ReadAll(request.Body)
	recorded := recordedRequest{
		Method: request.Method,
		Path:   request.URL.Path,
		Query:  request.URL.RawQuery,
		Auth:   request.Header.Get("Authorization"),
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &recorded.Body); err != nil {
			fake.t.Errorf("request body is not JSON: %s", raw)
		}
	}
	fake.mu.Lock()
	fake.requests = append(fake.requests, recorded)
	respond, ok := fake.respond[request.Method+" "+request.URL.Path]
	fake.mu.Unlock()
	if !ok {
		fake.t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
		http.NotFound(writer, request)
		return
	}
	status, body := respond(recorded)
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = io.WriteString(writer, body)
}

func (fake *fakeDodo) on(route string, status int, body string) {
	fake.respond[route] = func(recordedRequest) (int, string) { return status, body }
}

func (fake *fakeDodo) only(t *testing.T, route string) recordedRequest {
	t.Helper()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	var matched []recordedRequest
	for _, request := range fake.requests {
		if request.Method+" "+request.Path == route {
			matched = append(matched, request)
		}
	}
	if len(matched) != 1 {
		t.Fatalf("%s requests = %d, want 1 (all requests: %+v)", route, len(matched), fake.requests)
	}
	return matched[0]
}

func TestCreateCustomer(t *testing.T) {
	fake, client := newFakeDodo(t)
	fake.on("POST /customers", http.StatusOK, `{"business_id":"bus_1","created_at":"2026-09-13T12:00:00Z","customer_id":"cus_1","email":"asha@example.com","name":"Asha"}`)

	customerID, err := client.CreateCustomer(context.Background(), CustomerRequest{OrganizationID: "org_1", Email: "asha@example.com", Name: "Asha"})
	if err != nil {
		t.Fatalf("CreateCustomer() error = %v", err)
	}
	if customerID != "cus_1" {
		t.Fatalf("customer id = %q, want cus_1", customerID)
	}

	request := fake.only(t, "POST /customers")
	if request.Auth != "Bearer test-key" {
		t.Fatalf("Authorization = %q", request.Auth)
	}
	metadata, _ := request.Body["metadata"].(map[string]any)
	if request.Body["email"] != "asha@example.com" || request.Body["name"] != "Asha" || metadata["organization_id"] != "org_1" {
		t.Fatalf("customer request body = %+v", request.Body)
	}
}

func TestCreateCheckout(t *testing.T) {
	fake, client := newFakeDodo(t)
	fake.on("POST /checkouts", http.StatusOK, `{"session_id":"cks_1","checkout_url":"https://test.checkout.dodopayments.com/cks_1"}`)

	url, err := client.CreateCheckout(context.Background(), CheckoutRequest{
		OrganizationID: "org_1",
		CustomerID:     "cus_1",
		ProductID:      "pdt_team",
		Quantity:       3,
		ReturnURL:      "https://app.example.com/settings/billing?organization_id=org_1",
	})
	if err != nil {
		t.Fatalf("CreateCheckout() error = %v", err)
	}
	if url != "https://test.checkout.dodopayments.com/cks_1" {
		t.Fatalf("checkout url = %q", url)
	}

	body := fake.only(t, "POST /checkouts").Body
	cart, _ := body["product_cart"].([]any)
	if len(cart) != 1 {
		t.Fatalf("product_cart = %+v", body["product_cart"])
	}
	item, _ := cart[0].(map[string]any)
	customer, _ := body["customer"].(map[string]any)
	metadata, _ := body["metadata"].(map[string]any)
	if item["product_id"] != "pdt_team" || item["quantity"] != float64(3) {
		t.Fatalf("cart item = %+v", item)
	}
	if customer["customer_id"] != "cus_1" || metadata["organization_id"] != "org_1" {
		t.Fatalf("checkout customer %+v, metadata %+v", customer, metadata)
	}
	if body["return_url"] != "https://app.example.com/settings/billing?organization_id=org_1" {
		t.Fatalf("return_url = %v", body["return_url"])
	}
}

func TestCreateCheckoutWithoutURL(t *testing.T) {
	fake, client := newFakeDodo(t)
	fake.on("POST /checkouts", http.StatusOK, `{"session_id":"cks_1","checkout_url":null}`)

	if _, err := client.CreateCheckout(context.Background(), CheckoutRequest{OrganizationID: "org_1", CustomerID: "cus_1", ProductID: "pdt_team", Quantity: 1}); err == nil {
		t.Fatal("CreateCheckout() error = nil for a session without a URL")
	}
}

func TestCreatePortalSession(t *testing.T) {
	fake, client := newFakeDodo(t)
	fake.on("POST /customers/cus_1/customer-portal/session", http.StatusOK, `{"link":"https://customer.dodopayments.com/session/abc"}`)

	link, err := client.CreatePortalSession(context.Background(), "cus_1", "https://app.example.com/settings/billing")
	if err != nil {
		t.Fatalf("CreatePortalSession() error = %v", err)
	}
	if link != "https://customer.dodopayments.com/session/abc" {
		t.Fatalf("link = %q", link)
	}
	if query := fake.only(t, "POST /customers/cus_1/customer-portal/session").Query; !strings.Contains(query, "return_url=https%3A%2F%2Fapp.example.com%2Fsettings%2Fbilling") {
		t.Fatalf("portal query = %q, want the return URL", query)
	}
}

func TestCancelSubscription(t *testing.T) {
	tests := []struct {
		name        string
		patchStatus int
		getBody     string
		wantErr     bool
	}{
		{name: "cancelled", patchStatus: http.StatusOK},
		{name: "already cancelled", patchStatus: http.StatusConflict, getBody: `{"subscription_id":"sub_1","status":"cancelled"}`},
		{name: "already expired", patchStatus: http.StatusUnprocessableEntity, getBody: `{"subscription_id":"sub_1","status":"expired"}`},
		{name: "refused while still active", patchStatus: http.StatusUnprocessableEntity, getBody: `{"subscription_id":"sub_1","status":"active"}`, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake, client := newFakeDodo(t)
			fake.respond["PATCH /subscriptions/sub_1"] = func(request recordedRequest) (int, string) {
				if request.Body["status"] != "cancelled" {
					t.Errorf("cancel body = %+v", request.Body)
				}
				if test.patchStatus != http.StatusOK {
					return test.patchStatus, `{"code":"INVALID_STATE","message":"subscription cannot be cancelled"}`
				}
				return http.StatusOK, `{"subscription_id":"sub_1","status":"cancelled"}`
			}
			fake.on("GET /subscriptions/sub_1", http.StatusOK, test.getBody)

			err := client.CancelSubscription(context.Background(), "sub_1")
			if (err != nil) != test.wantErr {
				t.Fatalf("CancelSubscription() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestNewRequiresAPIKeyAndValidWebhookKey(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New() error = nil without an API key")
	}
	if _, err := New(Config{APIKey: "key", WebhookKey: "whsec_not base64!"}); err == nil {
		t.Fatal("New() error = nil for a malformed webhook key")
	}
}
