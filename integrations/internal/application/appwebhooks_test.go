package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
)

func TestPutAppWebhookSetsTheSecretAndKeepsTheURL(t *testing.T) {
	h := newHarness(t)

	view, err := h.service.PutAppWebhook(context.Background(), bearer(adminToken), testOrganization, "app_chat", "  chat-signing-secret-1234 ")

	if err != nil {
		t.Fatal(err)
	}
	route := strings.TrimPrefix(view.URL, testPublicURL+"/v1/webhooks/chat/")
	if len(route) != 43 || view.Route != route || view.Provider != "chat" || view.UpdatedBy != "user_admin" || !view.CreatedAt.Equal(testNow) {
		t.Fatalf("PutAppWebhook() = %+v", view)
	}
	binding := secrets.Binding{OrganizationID: testOrganization, ResourceType: secrets.ResourceAppWebhook, ResourceID: "app_chat"}
	if opened, err := (fakeSealer{}).Open(context.Background(), binding, h.store.webhooks["app_chat"].EncryptedSecret); err != nil || string(opened) != "chat-signing-secret-1234" {
		t.Fatalf("stored secret opens to %q, %v, want the trimmed secret", opened, err)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if text := string(encoded); strings.Count(text, route) != 1 || !strings.Contains(text, `"url":"`+view.URL+`"`) || strings.Contains(text, "secret") || strings.Contains(text, "route") {
		t.Fatalf("view JSON = %s, want the URL and neither the route alone nor the secret", text)
	}

	h.now = testNow.Add(time.Hour)
	replaced, err := h.service.PutAppWebhook(context.Background(), bearer(adminToken), testOrganization, "app_chat", "rotated-signing-secret-5678")
	if err != nil {
		t.Fatal(err)
	}
	if replaced.URL != view.URL || !replaced.CreatedAt.Equal(testNow) || !replaced.UpdatedAt.Equal(h.now) {
		t.Fatalf("PutAppWebhook() again = %+v, want the same URL", replaced)
	}
	if opened, _ := (fakeSealer{}).Open(context.Background(), binding, h.store.webhooks["app_chat"].EncryptedSecret); string(opened) != "rotated-signing-secret-5678" {
		t.Fatalf("stored secret opens to %q, want the new secret", opened)
	}
}

func TestPutAppWebhookRefusals(t *testing.T) {
	tests := []struct {
		name        string
		credentials authclient.Credentials
		appID       string
		secret      string
		want        error
	}{
		{name: "no credentials", appID: "app_chat", secret: "chat-signing-secret-1234", want: domain.ErrUnauthorized},
		{name: "a member", credentials: bearer(memberToken), appID: "app_chat", secret: "chat-signing-secret-1234", want: domain.ErrForbidden},
		{name: "a short secret", credentials: bearer(adminToken), appID: "app_chat", secret: "short", want: domain.ErrInvalid},
		{name: "an unknown app", credentials: bearer(adminToken), appID: "app_missing", secret: "chat-signing-secret-1234", want: domain.ErrNotFound},
		{name: "an app whose subscriptions register webhooks", credentials: bearer(adminToken), appID: "app_hooks", secret: "chat-signing-secret-1234", want: domain.ErrInvalid},
		{name: "an app of a provider without events", credentials: bearer(adminToken), appID: "app_other", secret: "chat-signing-secret-1234", want: domain.ErrInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			if _, err := h.service.PutAppWebhook(context.Background(), test.credentials, testOrganization, test.appID, test.secret); !errors.Is(err, test.want) {
				t.Fatalf("PutAppWebhook() error = %v, want %v", err, test.want)
			}
			if len(h.store.webhooks) != 0 {
				t.Fatal("a refused webhook was stored")
			}
		})
	}
}

func TestGetAndDeleteAppWebhook(t *testing.T) {
	h := newHarness(t)
	if _, err := h.service.GetAppWebhook(context.Background(), bearer(adminToken), testOrganization, "app_chat"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetAppWebhook() before it is set error = %v, want domain.ErrNotFound", err)
	}
	set := h.setAppWebhook(t)

	got, err := h.service.GetAppWebhook(context.Background(), bearer(adminToken), testOrganization, "app_chat")
	if err != nil || got.URL != set.URL {
		t.Fatalf("GetAppWebhook() = %+v, %v", got, err)
	}
	if _, err := h.service.GetAppWebhook(context.Background(), bearer(memberToken), testOrganization, "app_chat"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("GetAppWebhook() by a member error = %v, want domain.ErrForbidden", err)
	}
	if err := h.service.DeleteAppWebhook(context.Background(), bearer(memberToken), testOrganization, "app_chat"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("DeleteAppWebhook() by a member error = %v, want domain.ErrForbidden", err)
	}
	if err := h.service.DeleteAppWebhook(context.Background(), bearer(adminToken), testOrganization, "app_chat"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.GetAppWebhook(context.Background(), bearer(adminToken), testOrganization, "app_chat"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetAppWebhook() after deletion error = %v, want domain.ErrNotFound", err)
	}
}
