package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

func TestGithubTarget(t *testing.T) {
	tests := []struct {
		name       string
		target     map[string]string
		events     []string
		wantTarget map[string]string
		wantEvents []string
		wantErr    bool
	}{
		{"repository accepted", map[string]string{"repository": "acme/api"}, nil, map[string]string{"repository": "acme/api"}, []string{}, false},
		{"missing repository", map[string]string{}, nil, nil, nil, true},
		{"extra key", map[string]string{"repository": "acme/api", "branch": "main"}, nil, nil, nil, true},
		{"bad pattern", map[string]string{"repository": "not-a-repository"}, nil, nil, nil, true},
		{"star becomes empty", map[string]string{"repository": "acme/api"}, []string{"*"}, map[string]string{"repository": "acme/api"}, []string{}, false},
		{"star with others refused", map[string]string{"repository": "acme/api"}, []string{"*", "push"}, nil, nil, true},
		{"invalid event name", map[string]string{"repository": "acme/api"}, []string{"Issues"}, nil, nil, true},
		{"valid events kept as given", map[string]string{"repository": "acme/api"}, []string{"issues", "push"}, map[string]string{"repository": "acme/api"}, []string{"issues", "push"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target, events, err := githubTarget(tc.target, tc.events)
			if tc.wantErr {
				if !errors.Is(err, domain.ErrInvalid) {
					t.Fatalf("githubTarget(%v, %v) error = %v, want %v", tc.target, tc.events, err, domain.ErrInvalid)
				}
				return
			}
			if err != nil || !maps.Equal(target, tc.wantTarget) || !slices.Equal(events, tc.wantEvents) {
				t.Fatalf("githubTarget(%v, %v) = %v, %v, %v; want %v, %v, nil", tc.target, tc.events, target, events, err, tc.wantTarget, tc.wantEvents)
			}
		})
	}
}

func TestGithubRegister(t *testing.T) {
	const secret = "shh-secret"
	const callback = "https://callback.example.test/hook"
	credential := domain.Credential{AccessToken: "tok123", Prefix: "Bearer "}
	target := map[string]string{"repository": "acme/api"}

	t.Run("success with no events requested", func(t *testing.T) {
		var gotMethod, gotPath string
		var gotHeader http.Header
		var gotBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			gotMethod, gotPath, gotHeader = request.Method, request.URL.Path, request.Header.Clone()
			if err := json.NewDecoder(request.Body).Decode(&gotBody); err != nil {
				t.Errorf("decode request body: %v", err)
			}
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(writer, `{"id":12345,"name":"web","active":true,"events":["*"],`+
				`"config":{"url":"`+callback+`","content_type":"json","secret":"********","insecure_ssl":"0"},`+
				`"created_at":"2026-09-15T12:00:00Z","updated_at":"2026-09-15T12:00:00Z",`+
				`"url":"https://api.github.com/repos/acme/api/hooks/12345",`+
				`"ping_url":"https://api.github.com/repos/acme/api/hooks/12345/pings",`+
				`"test_url":"https://api.github.com/repos/acme/api/hooks/12345/test",`+
				`"type":"Repository","last_response":{"code":null,"status":"unused","message":null}}`)
		}))
		defer server.Close()

		reg, err := githubProvider(server.URL).Register(context.Background(), server.Client(), RegisterRequest{
			Credential: credential, Target: target, Events: nil, CallbackURL: callback, Secret: secret, Now: fixedNow,
		})
		if err != nil || reg.ID != "12345" {
			t.Fatalf("Register() = %+v, %v; want ID 12345, nil", reg, err)
		}
		if gotMethod != http.MethodPost || gotPath != "/repos/acme/api/hooks" {
			t.Fatalf("request = %s %s, want POST /repos/acme/api/hooks", gotMethod, gotPath)
		}
		if got := gotHeader.Get("Authorization"); got != "Bearer tok123" {
			t.Fatalf("Authorization header = %q, want %q", got, "Bearer tok123")
		}
		if gotHeader.Get("X-GitHub-Api-Version") == "" {
			t.Fatalf("X-GitHub-Api-Version header is missing")
		}
		if gotBody["name"] != "web" || gotBody["active"] != true {
			t.Fatalf("request body = %v, want name \"web\", active true", gotBody)
		}
		if events, _ := gotBody["events"].([]any); len(events) != 1 || events[0] != "*" {
			t.Fatalf("request body events = %v, want [*] when no events are requested", gotBody["events"])
		}
		config, _ := gotBody["config"].(map[string]any)
		if config["url"] != callback || config["content_type"] != "json" || config["secret"] != secret || config["insecure_ssl"] != "0" {
			t.Fatalf("request body config = %v", config)
		}
	})

	t.Run("keeps given events", func(t *testing.T) {
		var gotBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			_ = json.NewDecoder(request.Body).Decode(&gotBody)
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(writer, `{"id":1}`)
		}))
		defer server.Close()

		if _, err := githubProvider(server.URL).Register(context.Background(), server.Client(), RegisterRequest{
			Credential: credential, Target: target, Events: []string{"issues", "push"}, CallbackURL: callback, Secret: secret, Now: fixedNow,
		}); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
		events, _ := gotBody["events"].([]any)
		if len(events) != 2 || events[0] != "issues" || events[1] != "push" {
			t.Fatalf("request body events = %v, want [issues push]", gotBody["events"])
		}
	})

	t.Run("rate limited 403 is provider unavailable", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("X-RateLimit-Remaining", "0")
			writer.WriteHeader(http.StatusForbidden)
		}))
		defer server.Close()
		_, err := githubProvider(server.URL).Register(context.Background(), server.Client(), RegisterRequest{
			Credential: credential, Target: target, CallbackURL: callback, Secret: secret, Now: fixedNow,
		})
		if !errors.Is(err, domain.ErrProviderUnavailable) {
			t.Fatalf("Register() error = %v, want %v", err, domain.ErrProviderUnavailable)
		}
	})

	for _, tc := range []struct {
		name   string
		status int
		want   error
	}{
		{"unauthorized", http.StatusUnauthorized, domain.ErrCredentialRejected},
		{"plain forbidden", http.StatusForbidden, domain.ErrForbidden},
		{"not found", http.StatusNotFound, domain.ErrNotFound},
		{"unprocessable", http.StatusUnprocessableEntity, domain.ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(tc.status)
			}))
			defer server.Close()
			_, err := githubProvider(server.URL).Register(context.Background(), server.Client(), RegisterRequest{
				Credential: credential, Target: target, CallbackURL: callback, Secret: secret, Now: fixedNow,
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Register() error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestGithubUnregister(t *testing.T) {
	credential := domain.Credential{AccessToken: "tok123", Prefix: "Bearer "}
	target := map[string]string{"repository": "acme/api"}

	t.Run("success", func(t *testing.T) {
		var gotMethod, gotPath string
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			gotMethod, gotPath = request.Method, request.URL.Path
			writer.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()
		if err := githubProvider(server.URL).Unregister(context.Background(), server.Client(), UnregisterRequest{
			Credential: credential, RegistrationID: "12345", Target: target,
		}); err != nil {
			t.Fatalf("Unregister() error = %v, want nil", err)
		}
		if gotMethod != http.MethodDelete || gotPath != "/repos/acme/api/hooks/12345" {
			t.Fatalf("request = %s %s, want DELETE /repos/acme/api/hooks/12345", gotMethod, gotPath)
		}
	})

	t.Run("already gone", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()
		if err := githubProvider(server.URL).Unregister(context.Background(), server.Client(), UnregisterRequest{
			Credential: credential, RegistrationID: "12345", Target: target,
		}); err != nil {
			t.Fatalf("Unregister() error = %v, want nil for an already-removed hook", err)
		}
	})

	t.Run("non-numeric registration ID", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			t.Errorf("unexpected request for a non-numeric registration ID")
			writer.WriteHeader(http.StatusOK)
		}))
		defer server.Close()
		err := githubProvider(server.URL).Unregister(context.Background(), server.Client(), UnregisterRequest{
			Credential: credential, RegistrationID: "not-a-number", Target: target,
		})
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("Unregister() error = %v, want %v", err, domain.ErrInvalid)
		}
	})

	t.Run("provider unavailable", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()
		err := githubProvider(server.URL).Unregister(context.Background(), server.Client(), UnregisterRequest{
			Credential: credential, RegistrationID: "12345", Target: target,
		})
		if !errors.Is(err, domain.ErrProviderUnavailable) {
			t.Fatalf("Unregister() error = %v, want %v", err, domain.ErrProviderUnavailable)
		}
	})
}

// githubDelivery builds a Delivered signed over body with secret, for the
// given event and delivery ID (left off the X-GitHub-Delivery header when
// empty).
func githubDelivery(secret []byte, event, deliveryID string, body []byte) Delivered {
	header := http.Header{}
	header.Set("X-Hub-Signature-256", "sha256="+hmacHex(secret, body))
	header.Set("X-GitHub-Event", event)
	if deliveryID != "" {
		header.Set("X-GitHub-Delivery", deliveryID)
	}
	return Delivered{Header: header, Body: body}
}

func TestGithubReceive(t *testing.T) {
	secret := []byte("gh-secret")

	t.Run("valid issues opened delivery", func(t *testing.T) {
		body := []byte(`{"action":"opened","issue":{"id":111,"number":7,"title":"Bug found","html_url":"https://github.com/acme/api/issues/7"},` +
			`"repository":{"id":555,"full_name":"acme/api"},"sender":{"id":42,"login":"octocat"}}`)
		reception, err := githubReceive(githubDelivery(secret, "issues", "delivery-1", body), secret, fixedNow)
		if err != nil {
			t.Fatalf("githubReceive() error = %v", err)
		}
		if len(reception.Events) != 1 {
			t.Fatalf("githubReceive() events = %v, want 1", reception.Events)
		}
		event := reception.Events[0]
		if event.Name != "issues.opened" || event.Type != "issues" {
			t.Errorf("Name, Type = %q, %q, want issues.opened, issues", event.Name, event.Type)
		}
		wantResource := integrationsv1.Resource{Type: "issue", ID: "111", Name: "Bug found", URL: "https://github.com/acme/api/issues/7"}
		if event.Resource != wantResource {
			t.Errorf("Resource = %+v, want %+v", event.Resource, wantResource)
		}
		if event.Actor == nil || *event.Actor != (integrationsv1.Actor{ID: "42", Name: "octocat"}) {
			t.Errorf("Actor = %+v, want {42 octocat}", event.Actor)
		}
		if event.Key != "delivery-1" || event.DeliveryID != "delivery-1" {
			t.Errorf("Key, DeliveryID = %q, %q, want delivery-1, delivery-1", event.Key, event.DeliveryID)
		}
		if string(event.Payload) != string(body) {
			t.Errorf("Payload = %s, want %s", event.Payload, body)
		}
	})

	t.Run("missing signature header", func(t *testing.T) {
		header := http.Header{}
		header.Set("X-GitHub-Event", "issues")
		_, err := githubReceive(Delivered{Header: header, Body: []byte(`{}`)}, secret, fixedNow)
		if !errors.Is(err, ErrUnverified) {
			t.Fatalf("githubReceive() error = %v, want %v", err, ErrUnverified)
		}
	})

	t.Run("wrong signature", func(t *testing.T) {
		body := []byte(`{"action":"opened"}`)
		header := http.Header{}
		header.Set("X-Hub-Signature-256", "sha256="+hmacHex([]byte("other-secret"), body))
		header.Set("X-GitHub-Event", "issues")
		_, err := githubReceive(Delivered{Header: header, Body: body}, secret, fixedNow)
		if !errors.Is(err, ErrUnverified) {
			t.Fatalf("githubReceive() error = %v, want %v", err, ErrUnverified)
		}
	})

	t.Run("signature without sha256= prefix", func(t *testing.T) {
		body := []byte(`{"action":"opened"}`)
		header := http.Header{}
		header.Set("X-Hub-Signature-256", hmacHex(secret, body))
		header.Set("X-GitHub-Event", "issues")
		_, err := githubReceive(Delivered{Header: header, Body: body}, secret, fixedNow)
		if !errors.Is(err, ErrUnverified) {
			t.Fatalf("githubReceive() error = %v, want %v", err, ErrUnverified)
		}
	})

	t.Run("ping gives no events and no error", func(t *testing.T) {
		body := []byte(`{"zen":"hello"}`)
		reception, err := githubReceive(githubDelivery(secret, "ping", "delivery-ping", body), secret, fixedNow)
		if err != nil || len(reception.Events) != 0 {
			t.Fatalf("githubReceive(ping) = %+v, %v, want no events and no error", reception, err)
		}
	})

	t.Run("push", func(t *testing.T) {
		body := []byte(`{"ref":"refs/heads/main","after":"abc123","compare":"https://github.com/acme/api/compare/000..abc"}`)
		reception, err := githubReceive(githubDelivery(secret, "push", "delivery-push", body), secret, fixedNow)
		if err != nil || len(reception.Events) != 1 {
			t.Fatalf("githubReceive(push) = %+v, %v", reception, err)
		}
		event := reception.Events[0]
		wantResource := integrationsv1.Resource{Type: "push", ID: "abc123", Name: "refs/heads/main", URL: "https://github.com/acme/api/compare/000..abc"}
		if event.Name != "push" || event.Resource != wantResource {
			t.Fatalf("Name, Resource = %q, %+v, want push, %+v", event.Name, event.Resource, wantResource)
		}
	})

	t.Run("issue_comment", func(t *testing.T) {
		body := []byte(`{"action":"created","issue":{"id":222,"title":"Bug found"},"comment":{"id":333}}`)
		reception, err := githubReceive(githubDelivery(secret, "issue_comment", "delivery-comment", body), secret, fixedNow)
		if err != nil || len(reception.Events) != 1 {
			t.Fatalf("githubReceive(issue_comment) = %+v, %v", reception, err)
		}
		resource := reception.Events[0].Resource
		if resource.Type != "comment" || resource.Name != "Bug found" {
			t.Fatalf("Resource = %+v, want Type comment, Name \"Bug found\"", resource)
		}
	})

	t.Run("create", func(t *testing.T) {
		body := []byte(`{"ref":"my-branch","ref_type":"branch"}`)
		reception, err := githubReceive(githubDelivery(secret, "create", "delivery-create", body), secret, fixedNow)
		if err != nil || len(reception.Events) != 1 {
			t.Fatalf("githubReceive(create) = %+v, %v", reception, err)
		}
		if got := reception.Events[0].Resource.Type; got != "branch" {
			t.Fatalf("Resource.Type = %q, want ref_type \"branch\"", got)
		}
	})

	t.Run("unlisted event falls back to the repository", func(t *testing.T) {
		body := []byte(`{"repository":{"id":777,"full_name":"acme/api","html_url":"https://github.com/acme/api"}}`)
		reception, err := githubReceive(githubDelivery(secret, "star", "delivery-star", body), secret, fixedNow)
		if err != nil || len(reception.Events) != 1 {
			t.Fatalf("githubReceive(star) = %+v, %v", reception, err)
		}
		wantResource := integrationsv1.Resource{Type: "repository", ID: "777", Name: "acme/api", URL: "https://github.com/acme/api"}
		if reception.Events[0].Resource != wantResource {
			t.Fatalf("Resource = %+v, want %+v", reception.Events[0].Resource, wantResource)
		}
	})

	t.Run("no X-GitHub-Delivery gives a digest key", func(t *testing.T) {
		body := []byte(`{"action":"opened"}`)
		reception, err := githubReceive(githubDelivery(secret, "issues", "", body), secret, fixedNow)
		if err != nil || len(reception.Events) != 1 {
			t.Fatalf("githubReceive() = %+v, %v", reception, err)
		}
		event := reception.Events[0]
		if event.DeliveryID != "" {
			t.Errorf("DeliveryID = %q, want empty", event.DeliveryID)
		}
		if !strings.HasPrefix(event.Key, "sha256:") {
			t.Errorf("Key = %q, want a sha256: prefix", event.Key)
		}
	})

	t.Run("valid signature over invalid JSON", func(t *testing.T) {
		body := []byte(`not-json-at-all`)
		_, err := githubReceive(githubDelivery(secret, "issues", "delivery-bad", body), secret, fixedNow)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("githubReceive() error = %v, want %v", err, domain.ErrInvalid)
		}
	})

	t.Run("invalid X-GitHub-Event", func(t *testing.T) {
		body := []byte(`{}`)
		_, err := githubReceive(githubDelivery(secret, "Not Valid!", "delivery-bad-event", body), secret, fixedNow)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("githubReceive() error = %v, want %v", err, domain.ErrInvalid)
		}
	})
}
