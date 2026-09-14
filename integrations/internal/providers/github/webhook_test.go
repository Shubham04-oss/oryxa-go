package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhooks"
)

const webhookSecret = "abcdefghijklmnopqrstuvwxyz012345"

func TestWebhookAcceptsOpenedIssueAndRejectsHMACMismatch(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"action":"opened","repository":{"full_name":"oryxa/api"},"issue":{"number":12,"html_url":"https://github.com/oryxa/api/issues/12","title":"must-not-escape","body":"secret-body"}}`)
	request := githubWebhookRequest(raw, "issues", "delivery-1", "new-issue")
	result, err := (WebhookVerifier{}).VerifyAndParse(context.Background(), request)
	if err != nil || !result.Verified || result.Reply != nil || result.Event != "issues" || result.ProviderEventID != "delivery-1" {
		t.Fatalf("opened result/error = %+v / %v", result, err)
	}
	delivery, ok := result.Payload.(ThinDelivery)
	if !ok || delivery.Repository != "oryxa/api" || delivery.Number != 12 || delivery.URL != "https://github.com/oryxa/api/issues/12" ||
		delivery.Action != "opened" || strings.Contains(delivery.URL+delivery.Repository, "secret-body") {
		t.Fatalf("thin delivery = %+v", delivery)
	}

	request.Headers[headerSignature256] = "sha256=" + strings.Repeat("0", 64)
	rejected, err := (WebhookVerifier{}).VerifyAndParse(context.Background(), request)
	if err != nil || rejected.Verified {
		t.Fatalf("bad HMAC result/error = %+v / %v", rejected, err)
	}
}

func TestWebhookPingsWithoutEnqueueAndDropsNonOpenedActions(t *testing.T) {
	t.Parallel()
	ping := githubWebhookRequest([]byte(`{"zen":"Keep it logically awesome."}`), "ping", "delivery-ping", "new-issue")
	result, err := (WebhookVerifier{}).VerifyAndParse(context.Background(), ping)
	if err != nil || !result.Verified || result.Reply == nil || result.Reply.Status != http.StatusOK || result.Payload != nil {
		t.Fatalf("ping result/error = %+v / %v", result, err)
	}

	closed := githubWebhookRequest([]byte(`{"action":"closed","repository":{"full_name":"oryxa/api"},"issue":{"number":12,"html_url":"https://github.com/oryxa/api/issues/12"}}`),
		"issues", "delivery-closed", "new-issue")
	dropped, err := (WebhookVerifier{}).VerifyAndParse(context.Background(), closed)
	if err != nil || !dropped.Verified || dropped.Reply == nil || dropped.Reply.Status != http.StatusAccepted || dropped.Payload != nil {
		t.Fatalf("closed result/error = %+v / %v", dropped, err)
	}

	wrongEvent := githubWebhookRequest([]byte(`{"action":"opened","repository":{"full_name":"oryxa/api"},"pull_request":{"number":3,"html_url":"https://github.com/oryxa/api/pull/3"}}`),
		"pull_request", "delivery-pr", "new-issue")
	mismatched, err := (WebhookVerifier{}).VerifyAndParse(context.Background(), wrongEvent)
	if err != nil || !mismatched.Verified || mismatched.Reply == nil || mismatched.Reply.Status != http.StatusAccepted {
		t.Fatalf("wrong-event result/error = %+v / %v", mismatched, err)
	}
}

func TestWebhookAcceptsOpenedPullRequestOnMatchingTrigger(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"action":"opened","repository":{"full_name":"oryxa/api"},"pull_request":{"number":9,"html_url":"https://github.com/oryxa/api/pull/9","title":"leak"}}`)
	result, err := (WebhookVerifier{}).VerifyAndParse(context.Background(), githubWebhookRequest(raw, "pull_request", "pr-1", "new-pull-request"))
	if err != nil || !result.Verified || result.Event != "pull_request" {
		t.Fatalf("PR result/error = %+v / %v", result, err)
	}
	delivery := result.Payload.(ThinDelivery)
	if delivery.Number != 9 || strings.Contains(delivery.URL, "leak") {
		t.Fatalf("PR delivery = %+v", delivery)
	}
}

func TestWebhookRunnerLeavesOtherProvidersOnTheExistingRunner(t *testing.T) {
	t.Parallel()
	fallback := &webhookFallback{}
	request := githubWebhookRequest([]byte(`{}`), "issues", "d1", "new-issue")
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

func githubWebhookRequest(raw []byte, event, delivery, trigger string) webhooks.RunnerRequest {
	mac := hmac.New(sha256.New, []byte(webhookSecret))
	_, _ = mac.Write(raw)
	return webhooks.RunnerRequest{
		Method: "POST", RawBody: raw, SigningSecret: []byte(webhookSecret),
		Headers: map[string]string{
			headerEvent: event, headerDelivery: delivery,
			headerSignature256: "sha256=" + hex.EncodeToString(mac.Sum(nil)),
		},
		Route: domain.WebhookRoute{Provider: "github", Mode: domain.WebhookRoutePerConnection, ConnectionID: "con-1", TriggerName: trigger},
	}
}
