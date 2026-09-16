package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

func TestMicrosoftTarget(t *testing.T) {
	accepted := []map[string]string{
		{"resource": "me/messages"},
		{"resource": "me/mailFolders('Inbox')/messages"},
		{"resource": "me/events"},
		{"resource": "me/drive/root"},
	}
	for _, target := range accepted {
		if _, _, err := microsoftTarget(target, nil); err != nil {
			t.Errorf("microsoftTarget(%v, nil) error = %v, want nil", target, err)
		}
	}
	if _, _, err := microsoftTarget(map[string]string{"resource": "me/contacts"}, nil); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("microsoftTarget(unknown resource) error = %v, want %v", err, domain.ErrInvalid)
	}
	if _, _, err := microsoftTarget(map[string]string{"resource": "me/messages", "extra": "x"}, nil); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("microsoftTarget(extra key) error = %v, want %v", err, domain.ErrInvalid)
	}
	if _, _, err := microsoftTarget(map[string]string{"resource": "me/drive/root"}, []string{"created"}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("microsoftTarget(me/drive/root, [created]) error = %v, want %v", err, domain.ErrInvalid)
	}
	if _, events, err := microsoftTarget(map[string]string{"resource": "me/drive/root"}, []string{"updated"}); err != nil || !slices.Equal(events, []string{"updated"}) {
		t.Errorf("microsoftTarget(me/drive/root, [updated]) = %v, %v, want [updated], nil", events, err)
	}
	if _, events, err := microsoftTarget(map[string]string{"resource": "me/messages"}, []string{"created", "deleted"}); err != nil || !slices.Equal(events, []string{"created", "deleted"}) {
		t.Errorf("microsoftTarget(me/messages, [created deleted]) = %v, %v, want [created deleted], nil", events, err)
	}
}

func TestMicrosoftRegister(t *testing.T) {
	const callback = "https://callback.example.test/hook"
	const secret = "webhook-secret"
	credential := domain.Credential{AccessToken: "tok123", Prefix: "Bearer "}

	t.Run("me/messages with no events uses every change type", func(t *testing.T) {
		wantExpiry := fixedNow.Add(10080*time.Minute - time.Hour)
		var gotMethod, gotPath string
		var gotBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			gotMethod, gotPath = request.Method, request.URL.Path
			if err := json.NewDecoder(request.Body).Decode(&gotBody); err != nil {
				t.Errorf("decode request body: %v", err)
			}
			_, _ = io.WriteString(writer, `{"id":"sub-1","expirationDateTime":"`+wantExpiry.Format(time.RFC3339)+`"}`)
		}))
		defer server.Close()

		reg, err := microsoftProvider(server.URL).Register(context.Background(), server.Client(), RegisterRequest{
			Credential: credential, Target: map[string]string{"resource": "me/messages"}, Events: nil,
			CallbackURL: callback, Secret: secret, Now: fixedNow,
		})
		if err != nil || reg.ID != "sub-1" || !reg.ExpiresAt.Equal(wantExpiry) {
			t.Fatalf("Register() = %+v, %v; want ID sub-1, expiry %v", reg, err, wantExpiry)
		}
		if gotMethod != http.MethodPost || gotPath != "/subscriptions" {
			t.Fatalf("request = %s %s, want POST /subscriptions", gotMethod, gotPath)
		}
		if gotBody["changeType"] != "created,updated,deleted" {
			t.Fatalf("changeType = %v, want created,updated,deleted", gotBody["changeType"])
		}
		if gotBody["notificationUrl"] != callback || gotBody["lifecycleNotificationUrl"] != callback {
			t.Fatalf("notification URLs = %v, %v, want both %q", gotBody["notificationUrl"], gotBody["lifecycleNotificationUrl"], callback)
		}
		if gotBody["resource"] != "me/messages" {
			t.Fatalf("resource = %v, want me/messages", gotBody["resource"])
		}
		if gotBody["clientState"] != secret {
			t.Fatalf("clientState = %v, want %q", gotBody["clientState"], secret)
		}
		if gotBody["latestSupportedTlsVersion"] != "v1_2" {
			t.Fatalf("latestSupportedTlsVersion = %v, want v1_2", gotBody["latestSupportedTlsVersion"])
		}
		if gotBody["expirationDateTime"] != wantExpiry.Format(time.RFC3339) {
			t.Fatalf("expirationDateTime = %v, want %v", gotBody["expirationDateTime"], wantExpiry.Format(time.RFC3339))
		}
	})

	t.Run("me/drive/root uses its own lifetime and given events", func(t *testing.T) {
		wantExpiry := fixedNow.Add(42300*time.Minute - time.Hour)
		var gotBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			_ = json.NewDecoder(request.Body).Decode(&gotBody)
			_, _ = io.WriteString(writer, `{"id":"sub-2","expirationDateTime":"`+wantExpiry.Format(time.RFC3339)+`"}`)
		}))
		defer server.Close()

		reg, err := microsoftProvider(server.URL).Register(context.Background(), server.Client(), RegisterRequest{
			Credential: credential, Target: map[string]string{"resource": "me/drive/root"}, Events: []string{"updated"},
			CallbackURL: callback, Secret: secret, Now: fixedNow,
		})
		if err != nil || reg.ID != "sub-2" || !reg.ExpiresAt.Equal(wantExpiry) {
			t.Fatalf("Register() = %+v, %v; want ID sub-2, expiry %v", reg, err, wantExpiry)
		}
		if gotBody["changeType"] != "updated" {
			t.Fatalf("changeType = %v, want updated", gotBody["changeType"])
		}
		if gotBody["expirationDateTime"] != wantExpiry.Format(time.RFC3339) {
			t.Fatalf("expirationDateTime = %v, want %v", gotBody["expirationDateTime"], wantExpiry.Format(time.RFC3339))
		}
	})

	t.Run("answer without an id is provider unavailable", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(writer, `{"expirationDateTime":"2026-09-22T12:00:00Z"}`)
		}))
		defer server.Close()
		_, err := microsoftProvider(server.URL).Register(context.Background(), server.Client(), RegisterRequest{
			Credential: credential, Target: map[string]string{"resource": "me/messages"}, CallbackURL: callback, Secret: secret, Now: fixedNow,
		})
		if !errors.Is(err, domain.ErrProviderUnavailable) {
			t.Fatalf("Register() error = %v, want %v", err, domain.ErrProviderUnavailable)
		}
	})
}

func TestMicrosoftRenew(t *testing.T) {
	credential := domain.Credential{AccessToken: "tok123", Prefix: "Bearer "}
	target := map[string]string{"resource": "me/messages"}

	t.Run("200 gives the answered expiry", func(t *testing.T) {
		wantExpiry := fixedNow.Add(10080*time.Minute - time.Hour)
		var gotMethod, gotPath string
		var gotBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			gotMethod, gotPath = request.Method, request.URL.Path
			_ = json.NewDecoder(request.Body).Decode(&gotBody)
			_, _ = io.WriteString(writer, `{"id":"sub-1","expirationDateTime":"`+wantExpiry.Format(time.RFC3339)+`"}`)
		}))
		defer server.Close()

		reg, err := microsoftProvider(server.URL).Renew(context.Background(), server.Client(), RenewRequest{
			Credential: credential, RegistrationID: "sub-1", Target: target, Now: fixedNow,
		})
		if err != nil || !reg.ExpiresAt.Equal(wantExpiry) {
			t.Fatalf("Renew() = %+v, %v; want expiry %v", reg, err, wantExpiry)
		}
		if gotMethod != http.MethodPatch || gotPath != "/subscriptions/sub-1" {
			t.Fatalf("request = %s %s, want PATCH /subscriptions/sub-1", gotMethod, gotPath)
		}
		if gotBody["expirationDateTime"] != wantExpiry.Format(time.RFC3339) {
			t.Fatalf("expirationDateTime = %v, want %v", gotBody["expirationDateTime"], wantExpiry.Format(time.RFC3339))
		}
	})

	t.Run("404 is not found", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()
		_, err := microsoftProvider(server.URL).Renew(context.Background(), server.Client(), RenewRequest{
			Credential: credential, RegistrationID: "sub-1", Target: target, Now: fixedNow,
		})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("Renew() error = %v, want %v", err, domain.ErrNotFound)
		}
	})
}

func TestMicrosoftUnregister(t *testing.T) {
	credential := domain.Credential{AccessToken: "tok123", Prefix: "Bearer "}

	tests := []struct {
		name   string
		status int
		want   error
	}{
		{"success", http.StatusNoContent, nil},
		{"already gone", http.StatusNotFound, nil},
		{"forbidden", http.StatusForbidden, domain.ErrForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath string
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				gotMethod, gotPath = request.Method, request.URL.Path
				writer.WriteHeader(tc.status)
			}))
			defer server.Close()
			err := microsoftProvider(server.URL).Unregister(context.Background(), server.Client(), UnregisterRequest{
				Credential: credential, RegistrationID: "sub-1",
			})
			if gotMethod != http.MethodDelete || gotPath != "/subscriptions/sub-1" {
				t.Fatalf("request = %s %s, want DELETE /subscriptions/sub-1", gotMethod, gotPath)
			}
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

func TestMicrosoftReceive(t *testing.T) {
	secret := []byte("graph-secret")

	t.Run("validation token is echoed without needing a body", func(t *testing.T) {
		query := url.Values{"validationToken": {"abc def"}}
		reception, err := microsoftReceive(Delivered{Query: query}, secret, fixedNow)
		if err != nil {
			t.Fatalf("microsoftReceive() error = %v", err)
		}
		if reception.Reply == nil || reception.Reply.Status != http.StatusOK || reception.Reply.ContentType != "text/plain" || string(reception.Reply.Body) != "abc def" {
			t.Fatalf("Reply = %+v, want {200 text/plain \"abc def\"}", reception.Reply)
		}
	})

	t.Run("batch with matching clientState", func(t *testing.T) {
		body := []byte(`{"value":[` +
			`{"subscriptionId":"sub-1","changeType":"created","resource":"me/messages/AAA","clientState":"graph-secret",` +
			`"resourceData":{"@odata.type":"#Microsoft.Graph.Message","@odata.etag":"etag1","id":"AAA"}},` +
			`{"subscriptionId":"sub-1","changeType":"created","resource":"me/messages/AAA","clientState":"graph-secret",` +
			`"resourceData":{"@odata.type":"#Microsoft.Graph.Message","@odata.etag":"etag1","id":"AAA"}},` +
			`{"subscriptionId":"sub-1","changeType":"created","resource":"me/messages/AAA","clientState":"graph-secret",` +
			`"resourceData":{"@odata.type":"#Microsoft.Graph.Message","@odata.etag":"etag2","id":"AAA"}}` +
			`]}`)
		reception, err := microsoftReceive(Delivered{Body: body}, secret, fixedNow)
		if err != nil {
			t.Fatalf("microsoftReceive() error = %v", err)
		}
		if reception.Reply == nil || reception.Reply.Status != http.StatusAccepted {
			t.Fatalf("Reply = %+v, want status 202", reception.Reply)
		}
		if len(reception.Events) != 3 {
			t.Fatalf("Events = %v, want 3", reception.Events)
		}
		first := reception.Events[0]
		if first.Name != "message.created" {
			t.Errorf("Name = %q, want message.created", first.Name)
		}
		wantResource := integrationsv1.Resource{Type: "message", ID: "AAA"}
		if first.Resource != wantResource {
			t.Errorf("Resource = %+v, want %+v", first.Resource, wantResource)
		}
		if first.RegistrationID != "sub-1" {
			t.Errorf("RegistrationID = %q, want sub-1", first.RegistrationID)
		}
		if strings.Contains(string(first.Payload), "clientState") {
			t.Errorf("Payload still carries clientState: %s", first.Payload)
		}
		if reception.Events[0].Key != reception.Events[1].Key {
			t.Errorf("identical items got different keys: %q != %q", reception.Events[0].Key, reception.Events[1].Key)
		}
		if reception.Events[0].Key == reception.Events[2].Key {
			t.Errorf("items with different @odata.etag got the same key: %q", reception.Events[0].Key)
		}
	})

	t.Run("wrong clientState fails the whole delivery", func(t *testing.T) {
		body := []byte(`{"value":[` +
			`{"subscriptionId":"sub-1","changeType":"created","resource":"me/messages/AAA","clientState":"graph-secret",` +
			`"resourceData":{"@odata.type":"#Microsoft.Graph.Message","id":"AAA"}},` +
			`{"subscriptionId":"sub-1","changeType":"created","resource":"me/messages/BBB","clientState":"wrong-secret",` +
			`"resourceData":{"@odata.type":"#Microsoft.Graph.Message","id":"BBB"}}` +
			`]}`)
		if _, err := microsoftReceive(Delivered{Body: body}, secret, fixedNow); !errors.Is(err, ErrUnverified) {
			t.Fatalf("microsoftReceive() error = %v, want %v", err, ErrUnverified)
		}
	})

	t.Run("empty value is invalid", func(t *testing.T) {
		if _, err := microsoftReceive(Delivered{Body: []byte(`{"value":[]}`)}, secret, fixedNow); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("microsoftReceive() error = %v, want %v", err, domain.ErrInvalid)
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		if _, err := microsoftReceive(Delivered{Body: []byte(`not json`)}, secret, fixedNow); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("microsoftReceive() error = %v, want %v", err, domain.ErrInvalid)
		}
	})

	t.Run("reauthorizationRequired renews", func(t *testing.T) {
		body := []byte(`{"value":[{"subscriptionId":"sub-1","clientState":"graph-secret","lifecycleEvent":"reauthorizationRequired"}]}`)
		reception, err := microsoftReceive(Delivered{Body: body}, secret, fixedNow)
		if err != nil {
			t.Fatalf("microsoftReceive() error = %v", err)
		}
		if reception.Lifecycle != LifecycleRenew || len(reception.Events) != 0 {
			t.Fatalf("Lifecycle, Events = %v, %v, want renew, none", reception.Lifecycle, reception.Events)
		}
	})

	t.Run("subscriptionRemoved wins over a later reauthorizationRequired", func(t *testing.T) {
		body := []byte(`{"value":[` +
			`{"subscriptionId":"sub-1","clientState":"graph-secret","lifecycleEvent":"subscriptionRemoved"},` +
			`{"subscriptionId":"sub-1","clientState":"graph-secret","lifecycleEvent":"reauthorizationRequired"}` +
			`]}`)
		reception, err := microsoftReceive(Delivered{Body: body}, secret, fixedNow)
		if err != nil {
			t.Fatalf("microsoftReceive() error = %v", err)
		}
		if reception.Lifecycle != LifecycleRemoved || len(reception.Events) != 0 {
			t.Fatalf("Lifecycle, Events = %v, %v, want removed, none", reception.Lifecycle, reception.Events)
		}
	})

	t.Run("missed gives no lifecycle and no event", func(t *testing.T) {
		body := []byte(`{"value":[{"subscriptionId":"sub-1","clientState":"graph-secret","lifecycleEvent":"missed"}]}`)
		reception, err := microsoftReceive(Delivered{Body: body}, secret, fixedNow)
		if err != nil {
			t.Fatalf("microsoftReceive() error = %v", err)
		}
		if reception.Lifecycle != LifecycleNone || len(reception.Events) != 0 {
			t.Fatalf("Lifecycle, Events = %v, %v, want none, none", reception.Lifecycle, reception.Events)
		}
	})
}

func TestMicrosoftKind(t *testing.T) {
	tests := []struct{ in, want string }{
		{"#Microsoft.Graph.Message", "message"},
		{"#microsoft.graph.driveItem", "drive_item"},
		{"", "resource"},
	}
	for _, tc := range tests {
		if got := microsoftKind(tc.in); got != tc.want {
			t.Errorf("microsoftKind(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
