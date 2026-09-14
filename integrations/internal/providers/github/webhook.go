package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhooks"
)

const (
	headerSignature256 = "x-hub-signature-256"
	headerEvent        = "x-github-event"
	headerDelivery     = "x-github-delivery"
	eventPing          = "ping"
	actionOpened       = "opened"
)

// ThinDelivery is the only GitHub webhook material that may cross the
// admission boundary. Issue and pull-request title, body, and diffs are
// never copied here.
type ThinDelivery struct {
	Event      string `json:"event"`
	DeliveryID string `json:"delivery_id"`
	Action     string `json:"action"`
	Repository string `json:"repository"`
	Number     int64  `json:"number"`
	URL        string `json:"url"`
}

type WebhookVerifier struct{}

// WebhookRunner dispatches GitHub routes to native HMAC verification and
// preserves the existing runner for every other provider.
type WebhookRunner struct {
	Fallback webhooks.Runner
}

func (r WebhookRunner) VerifyAndParse(ctx context.Context, request webhooks.RunnerRequest) (webhooks.RunnerResult, error) {
	if request.Route.Provider == "github" {
		return (WebhookVerifier{}).VerifyAndParse(ctx, request)
	}
	if r.Fallback == nil {
		return webhooks.RunnerResult{}, nil
	}
	return r.Fallback.VerifyAndParse(ctx, request)
}

func (WebhookVerifier) VerifyAndParse(_ context.Context, request webhooks.RunnerRequest) (webhooks.RunnerResult, error) {
	if request.Route.Provider != "github" || request.Route.Mode != domain.WebhookRoutePerConnection ||
		request.Route.ConnectionID == "" || !strings.EqualFold(request.Method, "POST") {
		return webhooks.RunnerResult{Verified: false}, nil
	}
	if len(request.SigningSecret) < 32 || !validSignature(request.SigningSecret, request.RawBody, request.Headers[headerSignature256]) {
		return webhooks.RunnerResult{Verified: false}, nil
	}
	event := request.Headers[headerEvent]
	if event == eventPing {
		return webhooks.RunnerResult{Verified: true, Reply: &webhooks.Reply{Status: http.StatusOK}}, nil
	}
	if !acceptedOpenedEvent(request.Route.TriggerName, event) {
		return drop()
	}
	deliveryID := request.Headers[headerDelivery]
	if !validDeliveryID(deliveryID) {
		return webhooks.RunnerResult{Verified: false}, nil
	}
	delivery, ok := parseOpenedDelivery(event, deliveryID, request.RawBody)
	if !ok {
		return drop()
	}
	return webhooks.RunnerResult{Verified: true, Event: event, ProviderEventID: deliveryID, Payload: delivery}, nil
}

func drop() (webhooks.RunnerResult, error) {
	return webhooks.RunnerResult{Verified: true, Reply: &webhooks.Reply{Status: http.StatusAccepted}}, nil
}

func acceptedOpenedEvent(triggerName, event string) bool {
	switch triggerName {
	case "new-issue":
		return event == EventIssues
	case "new-pull-request":
		return event == EventPullRequest
	default:
		return false
	}
}

func parseOpenedDelivery(event, deliveryID string, raw []byte) (ThinDelivery, bool) {
	var payload struct {
		Action     string `json:"action"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		Issue struct {
			Number  int64  `json:"number"`
			HTMLURL string `json:"html_url"`
		} `json:"issue"`
		PullRequest struct {
			Number  int64  `json:"number"`
			HTMLURL string `json:"html_url"`
		} `json:"pull_request"`
	}
	if json.Unmarshal(raw, &payload) != nil || payload.Action != actionOpened || !validRepositoryFullName(payload.Repository.FullName) {
		return ThinDelivery{}, false
	}
	delivery := ThinDelivery{Event: event, DeliveryID: deliveryID, Action: payload.Action, Repository: payload.Repository.FullName}
	switch event {
	case EventIssues:
		if payload.Issue.Number < 1 || payload.Issue.Number > 2_147_483_647 || !validHTTPSURL(payload.Issue.HTMLURL) {
			return ThinDelivery{}, false
		}
		delivery.Number, delivery.URL = payload.Issue.Number, payload.Issue.HTMLURL
	case EventPullRequest:
		if payload.PullRequest.Number < 1 || payload.PullRequest.Number > 2_147_483_647 || !validHTTPSURL(payload.PullRequest.HTMLURL) {
			return ThinDelivery{}, false
		}
		delivery.Number, delivery.URL = payload.PullRequest.Number, payload.PullRequest.HTMLURL
	default:
		return ThinDelivery{}, false
	}
	return delivery, true
}

func validSignature(secret, body []byte, header string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) || len(header) != len(prefix)+sha256.Size*2 {
		return false
	}
	provided, err := hex.DecodeString(header[len(prefix):])
	if err != nil || len(provided) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return hmac.Equal(mac.Sum(nil), provided)
}

func validDeliveryID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func validRepositoryFullName(value string) bool {
	owner, repo, ok := strings.Cut(value, "/")
	return ok && validGitHubName(owner) && validGitHubName(repo) && !strings.Contains(repo, "/")
}
