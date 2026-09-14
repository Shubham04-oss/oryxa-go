package google

import (
	"context"
	"net/http"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhooks"
)

func TestCalendarWebhookAcceptsExistsAndDropsSync(t *testing.T) {
	t.Parallel()
	secret := []byte("abcdefghijklmnopqrstuvwxyz012345")
	request := calendarWebhookRequest("exists", "c-channel", "resource-1", "7", secret)
	result, err := (WebhookVerifier{}).VerifyAndParse(context.Background(), request)
	if err != nil || !result.Verified || result.Reply != nil || result.Event != calendarEventName ||
		result.ProviderEventID != "c-channel:7" {
		t.Fatalf("exists result/error = %+v / %v", result, err)
	}

	sync := calendarWebhookRequest("sync", "c-channel", "resource-1", "1", secret)
	replied, err := (WebhookVerifier{}).VerifyAndParse(context.Background(), sync)
	if err != nil || !replied.Verified || replied.Reply == nil || replied.Reply.Status != http.StatusOK || replied.Event != "" {
		t.Fatalf("sync result/error = %+v / %v", replied, err)
	}

	notExists := calendarWebhookRequest("not_exists", "c-channel", "resource-1", "8", secret)
	dropped, err := (WebhookVerifier{}).VerifyAndParse(context.Background(), notExists)
	if err != nil || !dropped.Verified || dropped.Reply == nil || dropped.Reply.Status != http.StatusAccepted {
		t.Fatalf("drop result/error = %+v / %v", dropped, err)
	}
}

func TestCalendarWebhookRejectsTokenMismatch(t *testing.T) {
	t.Parallel()
	request := calendarWebhookRequest("exists", "c-channel", "resource-1", "7", []byte("abcdefghijklmnopqrstuvwxyz012345"))
	request.Headers[headerChannelToken] = "different-token-value-0123456789abcdef"
	result, err := (WebhookVerifier{}).VerifyAndParse(context.Background(), request)
	if err != nil || result.Verified {
		t.Fatalf("mismatch result/error = %+v / %v", result, err)
	}
}

func TestWebhookRunnerLeavesOtherProvidersOnTheExistingRunner(t *testing.T) {
	t.Parallel()
	fallback := &webhookFallback{}
	request := calendarWebhookRequest("exists", "c-channel", "resource-1", "1", []byte("abcdefghijklmnopqrstuvwxyz012345"))
	request.Route.Provider = "github"
	result, err := (WebhookRunner{Fallback: fallback}).VerifyAndParse(context.Background(), request)
	if err != nil || result.Event != "fallback" || !fallback.called {
		t.Fatalf("fallback result/error = %+v / %v", result, err)
	}
}

type webhookFallback struct{ called bool }

func (r *webhookFallback) VerifyAndParse(context.Context, webhooks.RunnerRequest) (webhooks.RunnerResult, error) {
	r.called = true
	return webhooks.RunnerResult{Verified: true, Event: "fallback"}, nil
}

func calendarWebhookRequest(state, channelID, resourceID, messageNumber string, secret []byte) webhooks.RunnerRequest {
	return webhooks.RunnerRequest{
		Route: domain.WebhookRoute{Provider: "google", Mode: domain.WebhookRoutePerConnection,
			ConnectionID: "con-1", TriggerName: "calendar-event-changed"},
		Method: http.MethodPost, SigningSecret: secret,
		Headers: map[string]string{
			headerChannelID: channelID, headerChannelToken: string(secret),
			headerResourceID: resourceID, headerResourceState: state, headerMessageNumber: messageNumber,
		},
	}
}
