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
// admission boundary. Issue, pull-request, review, and commit bodies never
// copy here.
type ThinDelivery struct {
	Event      string `json:"event"`
	DeliveryID string `json:"delivery_id"`
	Action     string `json:"action,omitempty"`
	Repository string `json:"repository"`
	Number     int64  `json:"number,omitempty"`
	URL        string `json:"url,omitempty"`
	Ref        string `json:"ref,omitempty"`
	SHA        string `json:"sha,omitempty"`
	ReleaseID  int64  `json:"release_id,omitempty"`
	ReviewID   int64  `json:"review_id,omitempty"`
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
	if !acceptedGitHubEvent(request.Route.TriggerName, event) {
		return drop()
	}
	deliveryID := request.Headers[headerDelivery]
	if !validDeliveryID(deliveryID) {
		return webhooks.RunnerResult{Verified: false}, nil
	}
	delivery, ok := parseThinDelivery(request.Route.TriggerName, event, deliveryID, request.RawBody)
	if !ok {
		return drop()
	}
	return webhooks.RunnerResult{Verified: true, Event: event, ProviderEventID: deliveryID, Payload: delivery}, nil
}

func drop() (webhooks.RunnerResult, error) {
	return webhooks.RunnerResult{Verified: true, Reply: &webhooks.Reply{Status: http.StatusAccepted}}, nil
}

func acceptedGitHubEvent(triggerName, event string) bool {
	return NativeHookEvent(triggerName) == event
}

func parseThinDelivery(triggerName, event, deliveryID string, raw []byte) (ThinDelivery, bool) {
	var payload struct {
		Action     string `json:"action"`
		Ref        string `json:"ref"`
		After      string `json:"after"`
		Compare    string `json:"compare"`
		Repository struct {
			FullName string `json:"full_name"`
			HTMLURL  string `json:"html_url"`
		} `json:"repository"`
		Issue struct {
			Number  int64  `json:"number"`
			HTMLURL string `json:"html_url"`
		} `json:"issue"`
		PullRequest struct {
			Number  int64  `json:"number"`
			HTMLURL string `json:"html_url"`
		} `json:"pull_request"`
		Release struct {
			ID      int64  `json:"id"`
			HTMLURL string `json:"html_url"`
		} `json:"release"`
		Review struct {
			ID      int64  `json:"id"`
			HTMLURL string `json:"html_url"`
		} `json:"review"`
	}
	if json.Unmarshal(raw, &payload) != nil || !validRepositoryFullName(payload.Repository.FullName) {
		return ThinDelivery{}, false
	}
	delivery := ThinDelivery{Event: event, DeliveryID: deliveryID, Action: payload.Action, Repository: payload.Repository.FullName}
	switch triggerName {
	case "new-issue":
		if payload.Action != actionOpened || payload.Issue.Number < 1 || payload.Issue.Number > 2_147_483_647 ||
			!validHTTPSURL(payload.Issue.HTMLURL) {
			return ThinDelivery{}, false
		}
		delivery.Number, delivery.URL = payload.Issue.Number, payload.Issue.HTMLURL
	case "new-pull-request":
		if payload.Action != actionOpened || payload.PullRequest.Number < 1 || payload.PullRequest.Number > 2_147_483_647 ||
			!validHTTPSURL(payload.PullRequest.HTMLURL) {
			return ThinDelivery{}, false
		}
		delivery.Number, delivery.URL = payload.PullRequest.Number, payload.PullRequest.HTMLURL
	case "new-star":
		if payload.Action != "started" || !validHTTPSURL(payload.Repository.HTMLURL) {
			return ThinDelivery{}, false
		}
		delivery.URL = payload.Repository.HTMLURL
	case "new-push":
		if payload.Ref == "" || len(payload.Ref) > 256 || strings.Contains(payload.Ref, "..") ||
			strings.ContainsAny(payload.Ref, "\r\n\t ") {
			return ThinDelivery{}, false
		}
		if payload.After != "" && (len(payload.After) > 64 || strings.ContainsAny(payload.After, "\r\n\t ")) {
			return ThinDelivery{}, false
		}
		if payload.Compare != "" && !validHTTPSURL(payload.Compare) {
			return ThinDelivery{}, false
		}
		delivery.Ref, delivery.SHA, delivery.URL = payload.Ref, payload.After, payload.Compare
	case "new-release":
		if (payload.Action != "published" && payload.Action != "released") || payload.Release.ID < 1 ||
			payload.Release.ID > 2_147_483_647 || !validHTTPSURL(payload.Release.HTMLURL) {
			return ThinDelivery{}, false
		}
		delivery.ReleaseID, delivery.URL = payload.Release.ID, payload.Release.HTMLURL
	case "new-review":
		if payload.Action != "submitted" || payload.PullRequest.Number < 1 || payload.PullRequest.Number > 2_147_483_647 ||
			!validHTTPSURL(payload.PullRequest.HTMLURL) {
			return ThinDelivery{}, false
		}
		if payload.Review.ID < 0 || payload.Review.ID > 2_147_483_647 {
			return ThinDelivery{}, false
		}
		if payload.Review.HTMLURL != "" && !validHTTPSURL(payload.Review.HTMLURL) {
			return ThinDelivery{}, false
		}
		url := payload.Review.HTMLURL
		if url == "" {
			url = payload.PullRequest.HTMLURL
		}
		delivery.Number, delivery.ReviewID, delivery.URL = payload.PullRequest.Number, payload.Review.ID, url
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
