package microsoftgraph

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhooks"
)

// WebhookVerifier implements Graph's notification contract. Graph does not
// supply a request signature: each notification is authenticated by comparing
// clientState to the random secret sent when its subscription was created.
// The integrations service stores that secret encrypted in the opaque route.
type WebhookVerifier struct{}

// WebhookRunner dispatches only Microsoft routes to Graph verification and
// preserves the existing runner's behavior for every other provider. It is
// intentionally a narrow composition point for main's webhook wiring.
type WebhookRunner struct {
	Fallback webhooks.Runner
}

func (r WebhookRunner) VerifyAndParse(ctx context.Context, request webhooks.RunnerRequest) (webhooks.RunnerResult, error) {
	if request.Route.Provider == "microsoft" {
		return (WebhookVerifier{}).VerifyAndParse(ctx, request)
	}
	if r.Fallback == nil {
		return webhooks.RunnerResult{}, nil
	}
	return r.Fallback.VerifyAndParse(ctx, request)
}

func (WebhookVerifier) VerifyAndParse(_ context.Context, request webhooks.RunnerRequest) (webhooks.RunnerResult, error) {
	if request.Route.Provider != "microsoft" || request.Route.Mode != domain.WebhookRoutePerConnection ||
		request.Route.ConnectionID == "" || !strings.EqualFold(request.Method, "POST") {
		return webhooks.RunnerResult{Verified: false}, nil
	}
	if token, ok := request.Query["validationToken"]; ok {
		// Graph sends the validation token URL-decoded in the query and requires
		// exactly that plain text token within ten seconds. The opaque route is
		// the only routing input; the token is never accepted as an identifier.
		if token == "" || len(token) > 4096 || strings.ContainsAny(token, "\r\n") {
			return webhooks.RunnerResult{Verified: false}, nil
		}
		return webhooks.RunnerResult{Verified: true, Reply: &webhooks.Reply{
			Status: 200, Headers: map[string]string{"content-type": "text/plain; charset=utf-8"}, Body: token,
		}}, nil
	}
	var payload struct {
		Value []graphNotification `json:"value"`
	}
	if json.Unmarshal(request.RawBody, &payload) != nil || len(payload.Value) == 0 || len(payload.Value) > 100 || len(request.SigningSecret) < 32 {
		return webhooks.RunnerResult{Verified: false}, nil
	}
	subscriptionID := ""
	for _, notification := range payload.Value {
		// Lifecycle notifications are control-plane signals, never business
		// mailbox/calendar changes. This deployment has no lifecycle URL, so
		// reject them rather than publishing a misleading product event.
		if notification.LifecycleEvent != "" {
			return webhooks.RunnerResult{Verified: false}, nil
		}
		if !validSubscriptionID(notification.SubscriptionID) || notification.ClientState == "" ||
			subtle.ConstantTimeCompare([]byte(notification.ClientState), request.SigningSecret) != 1 {
			return webhooks.RunnerResult{Verified: false}, nil
		}
		if subscriptionID == "" {
			subscriptionID = notification.SubscriptionID
		} else if subtle.ConstantTimeCompare([]byte(subscriptionID), []byte(notification.SubscriptionID)) != 1 {
			// A per-connection opaque endpoint is bound to exactly one Graph
			// subscription. Do not permit a valid state for one subscription to
			// fan out a mixed delivery into another trigger.
			return webhooks.RunnerResult{Verified: false}, nil
		}
	}
	digest := sha256.Sum256(request.RawBody)
	// Graph has no delivery ID. Combining its subscription identity with the
	// immutable delivered body makes provider retries replay-safe while still
	// allowing later changes to the same resource through the same subscription.
	return webhooks.RunnerResult{Verified: true, Event: "change-notification",
		ProviderEventID: subscriptionID + ":" + hex.EncodeToString(digest[:])}, nil
}

type graphNotification struct {
	SubscriptionID string `json:"subscriptionId"`
	ClientState    string `json:"clientState"`
	LifecycleEvent string `json:"lifecycleEvent"`
}
