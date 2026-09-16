package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

func TestLinearTarget(t *testing.T) {
	if target, _, err := linearTarget(map[string]string{}, nil); err != nil || len(target) != 0 {
		t.Errorf("linearTarget(no target) = %v, %v, want an empty map, nil", target, err)
	}
	if target, _, err := linearTarget(map[string]string{"team": "ENG"}, nil); err != nil || !maps.Equal(target, map[string]string{"team": "ENG"}) {
		t.Errorf("linearTarget(team) = %v, %v, want {team: ENG}, nil", target, err)
	}
	if _, _, err := linearTarget(map[string]string{"foo": "bar"}, nil); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("linearTarget(other key) error = %v, want %v", err, domain.ErrInvalid)
	}
	if _, _, err := linearTarget(map[string]string{}, []string{"NotAResourceType"}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("linearTarget(bad event) error = %v, want %v", err, domain.ErrInvalid)
	}
	if _, events, err := linearTarget(map[string]string{}, []string{"Issue", "Comment"}); err != nil || !slices.Equal(events, []string{"Issue", "Comment"}) {
		t.Errorf("linearTarget(valid events) = %v, %v, want [Issue Comment], nil", events, err)
	}
}

func TestLinearRegister(t *testing.T) {
	const callback = "https://callback.example.test/hook"
	const secret = "webhook-secret"
	// An API key credential is sent as connections placed it: bare, with no
	// prefix, on the Authorization header.
	credential := domain.Credential{In: "header", Name: "Authorization", Prefix: "", AccessToken: "lin_api_key_xyz"}

	t.Run("no team asks for every public team with default resource types", func(t *testing.T) {
		var gotPath, gotAuth string
		var gotBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			gotPath, gotAuth = request.URL.Path, request.Header.Get("Authorization")
			if err := json.NewDecoder(request.Body).Decode(&gotBody); err != nil {
				t.Errorf("decode request body: %v", err)
			}
			_, _ = io.WriteString(writer, `{"data":{"webhookCreate":{"success":true,"webhook":{"id":"wh_123"}}}}`)
		}))
		defer server.Close()

		reg, err := linearProvider(server.URL+"/graphql").Register(context.Background(), server.Client(), RegisterRequest{
			Credential: credential, Target: map[string]string{}, Events: nil, CallbackURL: callback, Secret: secret, Now: fixedNow,
		})
		if err != nil || reg.ID != "wh_123" {
			t.Fatalf("Register() = %+v, %v; want ID wh_123, nil", reg, err)
		}
		if gotPath != "/graphql" {
			t.Fatalf("request path = %q, want /graphql", gotPath)
		}
		if gotAuth != "lin_api_key_xyz" {
			t.Fatalf("Authorization header = %q, want the bare key %q", gotAuth, "lin_api_key_xyz")
		}
		query, _ := gotBody["query"].(string)
		if !strings.Contains(query, "webhookCreate") {
			t.Fatalf("query = %q, want it to contain webhookCreate", query)
		}
		variables, _ := gotBody["variables"].(map[string]any)
		input, _ := variables["input"].(map[string]any)
		if input["url"] != callback || input["secret"] != secret || input["label"] != "Oryxa" || input["enabled"] != true {
			t.Fatalf("variables.input = %v, want url/secret/label Oryxa/enabled true", input)
		}
		resourceTypes, _ := input["resourceTypes"].([]any)
		if len(resourceTypes) != len(linearDefaultResourceTypes) {
			t.Fatalf("resourceTypes = %v, want the %d default resource types", resourceTypes, len(linearDefaultResourceTypes))
		}
		for index, want := range linearDefaultResourceTypes {
			if resourceTypes[index] != want {
				t.Fatalf("resourceTypes[%d] = %v, want %q", index, resourceTypes[index], want)
			}
		}
		if input["allPublicTeams"] != true {
			t.Fatalf("allPublicTeams = %v, want true", input["allPublicTeams"])
		}
		if _, has := input["teamId"]; has {
			t.Fatalf("input = %v, want no teamId when the target names no team", input)
		}
	})

	t.Run("a team key is looked up and the webhook asks for that team with the given events", func(t *testing.T) {
		var bodies []map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			bodies = append(bodies, body)
			if query, _ := body["query"].(string); strings.Contains(query, "teams(") {
				_, _ = io.WriteString(writer, `{"data":{"teams":{"nodes":[{"id":"9cfb482a-81e3-4154-b5b9-2c805e70a02d"}]}}}`)
				return
			}
			_, _ = io.WriteString(writer, `{"data":{"webhookCreate":{"success":true,"webhook":{"id":"wh_456"}}}}`)
		}))
		defer server.Close()

		if _, err := linearProvider(server.URL+"/graphql").Register(context.Background(), server.Client(), RegisterRequest{
			Credential: credential, Target: map[string]string{"team": "ENG"}, Events: []string{"Issue", "Comment"},
			CallbackURL: callback, Secret: secret, Now: fixedNow,
		}); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
		if len(bodies) != 2 {
			t.Fatalf("requests = %d, want the team lookup then webhookCreate", len(bodies))
		}
		if variables, _ := bodies[0]["variables"].(map[string]any); variables["key"] != "ENG" {
			t.Fatalf("team lookup variables = %v, want key ENG", variables)
		}
		variables, _ := bodies[1]["variables"].(map[string]any)
		input, _ := variables["input"].(map[string]any)
		if input["teamId"] != "9cfb482a-81e3-4154-b5b9-2c805e70a02d" {
			t.Fatalf("teamId = %v, want the looked-up team ID", input["teamId"])
		}
		if _, has := input["allPublicTeams"]; has {
			t.Fatalf("input = %v, want no allPublicTeams when the target names a team", input)
		}
		resourceTypes, _ := input["resourceTypes"].([]any)
		if !slices.Equal(anyToStrings(resourceTypes), []string{"Issue", "Comment"}) {
			t.Fatalf("resourceTypes = %v, want [Issue Comment]", resourceTypes)
		}
	})

	t.Run("a team ID is used as it is", func(t *testing.T) {
		var bodies []map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			bodies = append(bodies, body)
			_, _ = io.WriteString(writer, `{"data":{"webhookCreate":{"success":true,"webhook":{"id":"wh_789"}}}}`)
		}))
		defer server.Close()

		if _, err := linearProvider(server.URL+"/graphql").Register(context.Background(), server.Client(), RegisterRequest{
			Credential: credential, Target: map[string]string{"team": "9CFB482A-81E3-4154-B5B9-2C805E70A02D"}, CallbackURL: callback, Secret: secret, Now: fixedNow,
		}); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
		variables, _ := bodies[len(bodies)-1]["variables"].(map[string]any)
		if input, _ := variables["input"].(map[string]any); len(bodies) != 1 || input["teamId"] != "9CFB482A-81E3-4154-B5B9-2C805E70A02D" {
			t.Fatalf("requests = %d, teamId = %v; want webhookCreate alone with the ID", len(bodies), input["teamId"])
		}
	})

	t.Run("a team key Linear does not know creates nothing", func(t *testing.T) {
		requests := 0
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			requests++
			_, _ = io.WriteString(writer, `{"data":{"teams":{"nodes":[]}}}`)
		}))
		defer server.Close()

		_, err := linearProvider(server.URL+"/graphql").Register(context.Background(), server.Client(), RegisterRequest{
			Credential: credential, Target: map[string]string{"team": "NOPE"}, CallbackURL: callback, Secret: secret, Now: fixedNow,
		})
		if !errors.Is(err, domain.ErrInvalid) || requests != 1 {
			t.Fatalf("Register() error = %v after %d requests, want domain.ErrInvalid after the lookup alone", err, requests)
		}
	})

	errorTests := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"authentication error", http.StatusOK, `{"errors":[{"message":"Not authenticated","extensions":{"type":"authentication error"}}]}`, domain.ErrCredentialRejected},
		{"forbidden", http.StatusOK, `{"errors":[{"message":"Not allowed","extensions":{"type":"forbidden"}}]}`, domain.ErrForbidden},
		{"other error", http.StatusOK, `{"errors":[{"message":"Bad input value","extensions":{"type":"SomeOtherType"}}]}`, domain.ErrInvalid},
		{"http 401", http.StatusUnauthorized, ``, domain.ErrCredentialRejected},
		{"success false", http.StatusOK, `{"data":{"webhookCreate":{"success":false,"webhook":{"id":""}}}}`, domain.ErrInvalid},
	}
	for _, tc := range errorTests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(tc.status)
				_, _ = io.WriteString(writer, tc.body)
			}))
			defer server.Close()
			_, err := linearProvider(server.URL+"/graphql").Register(context.Background(), server.Client(), RegisterRequest{
				Credential: credential, Target: map[string]string{}, CallbackURL: callback, Secret: secret, Now: fixedNow,
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Register() error = %v, want %v", err, tc.want)
			}
		})
	}
}

// anyToStrings converts a decoded JSON array of strings back to []string for
// comparison with slices.Equal.
func anyToStrings(values []any) []string {
	out := make([]string, len(values))
	for index, value := range values {
		out[index], _ = value.(string)
	}
	return out
}

func TestLinearUnregister(t *testing.T) {
	credential := domain.Credential{AccessToken: "lin_api_key_xyz"}

	tests := []struct {
		name string
		body string
		want error
	}{
		{"success", `{"data":{"webhookDelete":{"success":true}}}`, nil},
		{"entity not found is not an error", `{"errors":[{"message":"Entity not found: Webhook"}]}`, nil},
		{"success false is provider unavailable", `{"data":{"webhookDelete":{"success":false}}}`, domain.ErrProviderUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(writer, tc.body)
			}))
			defer server.Close()
			err := linearProvider(server.URL+"/graphql").Unregister(context.Background(), server.Client(), UnregisterRequest{
				Credential: credential, RegistrationID: "wh_123",
			})
			if tc.want == nil {
				if err != nil {
					t.Fatalf("Unregister() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("Unregister() error = %v, want %v", err, tc.want)
			}
		})
	}
}

// linearDelivery signs body with secret for Linear-Signature, and sets
// Linear-Delivery when deliveryID is not empty.
func linearDelivery(secret []byte, deliveryID string, body []byte) Delivered {
	header := http.Header{}
	header.Set("Linear-Signature", hmacHex(secret, body))
	if deliveryID != "" {
		header.Set("Linear-Delivery", deliveryID)
	}
	return Delivered{Header: header, Body: body}
}

func TestLinearReceive(t *testing.T) {
	secret := []byte("linear-secret")

	t.Run("full issue create delivery", func(t *testing.T) {
		timestamp := fixedNow.Add(-30 * time.Second).UnixMilli()
		body := []byte(fmt.Sprintf(`{"action":"create","type":"Issue","data":{"id":"issue-1","title":"Fix bug"},`+
			`"url":"https://linear.app/oryxa/issue/ENG-1","createdAt":"2026-09-15T12:00:00Z",`+
			`"webhookTimestamp":%d,"webhookId":"wh_123","actor":{"id":"user-1","name":"Ada"}}`, timestamp))
		reception, err := linearReceive(linearDelivery(secret, "delivery-1", body), secret, fixedNow)
		if err != nil {
			t.Fatalf("linearReceive() error = %v", err)
		}
		if len(reception.Events) != 1 {
			t.Fatalf("Events = %v, want 1", reception.Events)
		}
		event := reception.Events[0]
		if event.Name != "issue.create" {
			t.Errorf("Name = %q, want issue.create", event.Name)
		}
		wantResource := integrationsv1.Resource{Type: "issue", ID: "issue-1", Name: "Fix bug", URL: "https://linear.app/oryxa/issue/ENG-1"}
		if event.Resource != wantResource {
			t.Errorf("Resource = %+v, want %+v", event.Resource, wantResource)
		}
		if event.Actor == nil || *event.Actor != (integrationsv1.Actor{ID: "user-1", Name: "Ada"}) {
			t.Errorf("Actor = %+v, want {user-1 Ada}", event.Actor)
		}
		wantCreatedAt := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
		if !event.OccurredAt.Equal(wantCreatedAt) {
			t.Errorf("OccurredAt = %v, want %v", event.OccurredAt, wantCreatedAt)
		}
		if event.RegistrationID != "wh_123" {
			t.Errorf("RegistrationID = %q, want wh_123", event.RegistrationID)
		}
		if event.Key != "delivery-1" || event.DeliveryID != "delivery-1" {
			t.Errorf("Key, DeliveryID = %q, %q, want delivery-1, delivery-1", event.Key, event.DeliveryID)
		}
	})

	t.Run("IssueLabel update", func(t *testing.T) {
		timestamp := fixedNow.Add(-10 * time.Second).UnixMilli()
		body := []byte(fmt.Sprintf(`{"action":"update","type":"IssueLabel","data":{"id":"label-1","name":"bug"},"webhookTimestamp":%d,"webhookId":"wh_123"}`, timestamp))
		reception, err := linearReceive(linearDelivery(secret, "delivery-2", body), secret, fixedNow)
		if err != nil || len(reception.Events) != 1 {
			t.Fatalf("linearReceive() = %+v, %v", reception, err)
		}
		if got := reception.Events[0].Name; got != "issue_label.update" {
			t.Errorf("Name = %q, want issue_label.update", got)
		}
	})

	t.Run("2 minutes old is unverified", func(t *testing.T) {
		timestamp := fixedNow.Add(-2 * time.Minute).UnixMilli()
		body := []byte(fmt.Sprintf(`{"action":"create","type":"Issue","data":{"id":"issue-1"},"webhookTimestamp":%d,"webhookId":"wh_123"}`, timestamp))
		if _, err := linearReceive(linearDelivery(secret, "delivery-old", body), secret, fixedNow); !errors.Is(err, ErrUnverified) {
			t.Fatalf("linearReceive() error = %v, want %v", err, ErrUnverified)
		}
	})

	t.Run("2 minutes ahead is unverified", func(t *testing.T) {
		timestamp := fixedNow.Add(2 * time.Minute).UnixMilli()
		body := []byte(fmt.Sprintf(`{"action":"create","type":"Issue","data":{"id":"issue-1"},"webhookTimestamp":%d,"webhookId":"wh_123"}`, timestamp))
		if _, err := linearReceive(linearDelivery(secret, "delivery-ahead", body), secret, fixedNow); !errors.Is(err, ErrUnverified) {
			t.Fatalf("linearReceive() error = %v, want %v", err, ErrUnverified)
		}
	})

	t.Run("missing timestamp is unverified", func(t *testing.T) {
		body := []byte(`{"action":"create","type":"Issue","data":{"id":"issue-1"},"webhookId":"wh_123"}`)
		if _, err := linearReceive(linearDelivery(secret, "delivery-missing", body), secret, fixedNow); !errors.Is(err, ErrUnverified) {
			t.Fatalf("linearReceive() error = %v, want %v", err, ErrUnverified)
		}
	})

	t.Run("wrong signature", func(t *testing.T) {
		timestamp := fixedNow.Add(-10 * time.Second).UnixMilli()
		body := []byte(fmt.Sprintf(`{"action":"create","type":"Issue","data":{"id":"issue-1"},"webhookTimestamp":%d,"webhookId":"wh_123"}`, timestamp))
		delivery := linearDelivery([]byte("wrong-secret"), "delivery-x", body)
		if _, err := linearReceive(delivery, secret, fixedNow); !errors.Is(err, ErrUnverified) {
			t.Fatalf("linearReceive() error = %v, want %v", err, ErrUnverified)
		}
	})

	t.Run("missing type", func(t *testing.T) {
		timestamp := fixedNow.Add(-10 * time.Second).UnixMilli()
		body := []byte(fmt.Sprintf(`{"action":"create","webhookTimestamp":%d,"webhookId":"wh_123"}`, timestamp))
		if _, err := linearReceive(linearDelivery(secret, "delivery-3", body), secret, fixedNow); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("linearReceive() error = %v, want %v", err, domain.ErrInvalid)
		}
	})

	t.Run("missing action", func(t *testing.T) {
		timestamp := fixedNow.Add(-10 * time.Second).UnixMilli()
		body := []byte(fmt.Sprintf(`{"type":"Issue","webhookTimestamp":%d,"webhookId":"wh_123"}`, timestamp))
		if _, err := linearReceive(linearDelivery(secret, "delivery-4", body), secret, fixedNow); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("linearReceive() error = %v, want %v", err, domain.ErrInvalid)
		}
	})
}

func TestSnakeCase(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Issue", "issue"},
		{"IssueLabel", "issue_label"},
		{"IssueSLA", "issue_sla"},
		{"ProjectUpdate", "project_update"},
		{"CustomerNeed", "customer_need"},
	}
	for _, tc := range tests {
		if got := snakeCase(tc.in); got != tc.want {
			t.Errorf("snakeCase(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
