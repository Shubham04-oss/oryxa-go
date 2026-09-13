package microsoftgraph

import (
	"context"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhooks"
)

const clientState = "abcdefghijklmnopqrstuvwxyz012345"

func TestWebhookValidationTokenEchoesPlainTextWithoutQueuePayload(t *testing.T) {
	t.Parallel()
	result, err := (WebhookVerifier{}).VerifyAndParse(context.Background(), webhookRequest(nil, map[string]string{"validationToken": "opaque token"}))
	if err != nil || !result.Verified || result.Reply == nil || result.Reply.Status != 200 || result.Reply.Body != "opaque token" || result.Reply.Headers["content-type"] != "text/plain; charset=utf-8" {
		t.Fatalf("result/error = %+v / %v", result, err)
	}
}

func TestWebhookRejectsMissingOrWrongClientState(t *testing.T) {
	t.Parallel()
	for _, raw := range [][]byte{
		[]byte(`{"value":[{"subscriptionId":"sub-1"}]}`),
		[]byte(`{"value":[{"subscriptionId":"sub-1","clientState":"other"}]}`),
	} {
		result, err := (WebhookVerifier{}).VerifyAndParse(context.Background(), webhookRequest(raw, nil))
		if err != nil || result.Verified {
			t.Fatalf("result/error = %+v / %v", result, err)
		}
	}
}

func TestWebhookUsesSubscriptionAndPayloadForReplaySafeDeliveryID(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"value":[{"subscriptionId":"sub-1","clientState":"abcdefghijklmnopqrstuvwxyz012345","changeType":"created","resourceData":{"id":"message-1"}}]}`)
	first, err := (WebhookVerifier{}).VerifyAndParse(context.Background(), webhookRequest(raw, nil))
	if err != nil || !first.Verified || first.ProviderEventID == "" || !strings.HasPrefix(first.ProviderEventID, "sub-1:") || first.IdentifierValue != "" {
		t.Fatalf("result/error = %+v / %v", first, err)
	}
	second, err := (WebhookVerifier{}).VerifyAndParse(context.Background(), webhookRequest(raw, nil))
	if err != nil || first.ProviderEventID != second.ProviderEventID {
		t.Fatalf("replay IDs/error = %q/%q/%v", first.ProviderEventID, second.ProviderEventID, err)
	}
	changed := []byte(`{"value":[{"subscriptionId":"sub-1","clientState":"abcdefghijklmnopqrstuvwxyz012345","changeType":"updated","resourceData":{"id":"message-1"}}]}`)
	third, err := (WebhookVerifier{}).VerifyAndParse(context.Background(), webhookRequest(changed, nil))
	if err != nil || !third.Verified || third.ProviderEventID == first.ProviderEventID {
		t.Fatalf("changed result/error = %+v / %v", third, err)
	}
}

func TestWebhookRejectsMixedSubscriptionsAndWrongRoute(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"value":[{"subscriptionId":"sub-1","clientState":"abcdefghijklmnopqrstuvwxyz012345"},{"subscriptionId":"sub-2","clientState":"abcdefghijklmnopqrstuvwxyz012345"}]}`)
	result, err := (WebhookVerifier{}).VerifyAndParse(context.Background(), webhookRequest(raw, nil))
	if err != nil || result.Verified {
		t.Fatalf("mixed result/error = %+v / %v", result, err)
	}
	request := webhookRequest(raw, nil)
	request.Route.ConnectionID = ""
	result, err = (WebhookVerifier{}).VerifyAndParse(context.Background(), request)
	if err != nil || result.Verified {
		t.Fatalf("unbound route result/error = %+v / %v", result, err)
	}
}

func TestWebhookRejectsUnsafeSubscriptionID(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"value":[{"subscriptionId":"sub-1?other=true","clientState":"abcdefghijklmnopqrstuvwxyz012345"}]}`)
	result, err := (WebhookVerifier{}).VerifyAndParse(context.Background(), webhookRequest(raw, nil))
	if err != nil || result.Verified {
		t.Fatalf("unsafe subscription result/error = %+v / %v", result, err)
	}
}

func TestWebhookRejectsLifecycleNotificationsAsBusinessEvents(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"value":[{"subscriptionId":"sub-1","clientState":"abcdefghijklmnopqrstuvwxyz012345","lifecycleEvent":"subscriptionRemoved"}]}`)
	result, err := (WebhookVerifier{}).VerifyAndParse(context.Background(), webhookRequest(raw, nil))
	if err != nil || result.Verified || result.Event != "" {
		t.Fatalf("lifecycle result/error = %+v / %v", result, err)
	}
}

func TestWebhookRunnerLeavesOtherProvidersOnTheExistingRunner(t *testing.T) {
	t.Parallel()
	fallback := &webhookFallback{}
	request := webhookRequest([]byte(`{}`), nil)
	request.Route.Provider = "slack"
	result, err := (WebhookRunner{Fallback: fallback}).VerifyAndParse(context.Background(), request)
	if err != nil || !result.Verified || result.Event != "fallback" || !fallback.called {
		t.Fatalf("result/error/fallback = %+v / %v / %+v", result, err, fallback)
	}
}

type webhookFallback struct{ called bool }

func (r *webhookFallback) VerifyAndParse(context.Context, webhooks.RunnerRequest) (webhooks.RunnerResult, error) {
	r.called = true
	return webhooks.RunnerResult{Verified: true, Event: "fallback"}, nil
}

func webhookRequest(raw []byte, query map[string]string) webhooks.RunnerRequest {
	return webhooks.RunnerRequest{
		Method: "POST", Query: query, RawBody: raw, SigningSecret: []byte(clientState),
		Route: domain.WebhookRoute{Provider: "microsoft", Mode: domain.WebhookRoutePerConnection, ConnectionID: "con-1"},
	}
}
