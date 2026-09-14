package google

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhooks"
)

const (
	headerChannelID     = "x-goog-channel-id"
	headerChannelToken  = "x-goog-channel-token"
	headerResourceID    = "x-goog-resource-id"
	headerResourceState = "x-goog-resource-state"
	headerMessageNumber = "x-goog-message-number"
	resourceStateSync   = "sync"
	resourceStateExists = "exists"
	calendarEventName   = "channel-notification"
)

func googleChannelTrigger(name string) bool {
	return name == "calendar-event-changed" || name == "drive-file-changed"
}

func acceptedGoogleResourceState(triggerName, state string) bool {
	if triggerName == "calendar-event-changed" {
		return state == resourceStateExists
	}
	if triggerName == "drive-file-changed" {
		switch state {
		case "change", "add", "remove", "update", "trash", "untrash":
			return true
		}
	}
	return false
}

type WebhookVerifier struct{}

// WebhookRunner dispatches Google Calendar and Drive channel notifications to
// native token verification and preserves the existing runner for every other
// provider.
type WebhookRunner struct {
	Fallback webhooks.Runner
}

func (r WebhookRunner) VerifyAndParse(ctx context.Context, request webhooks.RunnerRequest) (webhooks.RunnerResult, error) {
	if request.Route.Provider == "google" {
		return (WebhookVerifier{}).VerifyAndParse(ctx, request)
	}
	if r.Fallback == nil {
		return webhooks.RunnerResult{}, nil
	}
	return r.Fallback.VerifyAndParse(ctx, request)
}

func (WebhookVerifier) VerifyAndParse(_ context.Context, request webhooks.RunnerRequest) (webhooks.RunnerResult, error) {
	if request.Route.Provider != "google" || request.Route.Mode != domain.WebhookRoutePerConnection ||
		request.Route.ConnectionID == "" || !googleChannelTrigger(request.Route.TriggerName) ||
		!strings.EqualFold(request.Method, "POST") {
		return webhooks.RunnerResult{Verified: false}, nil
	}
	if len(request.SigningSecret) < 32 {
		return webhooks.RunnerResult{Verified: false}, nil
	}
	token := request.Headers[headerChannelToken]
	if token == "" || subtle.ConstantTimeCompare([]byte(token), request.SigningSecret) != 1 {
		return webhooks.RunnerResult{Verified: false}, nil
	}
	channelID := request.Headers[headerChannelID]
	resourceID := request.Headers[headerResourceID]
	if !validChannelID(channelID) || !validResourceID(resourceID) {
		return webhooks.RunnerResult{Verified: false}, nil
	}
	state := request.Headers[headerResourceState]
	if state == resourceStateSync {
		return webhooks.RunnerResult{Verified: true, Reply: &webhooks.Reply{Status: http.StatusOK}}, nil
	}
	if !acceptedGoogleResourceState(request.Route.TriggerName, state) {
		return webhooks.RunnerResult{Verified: true, Reply: &webhooks.Reply{Status: http.StatusAccepted}}, nil
	}
	messageNumber := request.Headers[headerMessageNumber]
	if messageNumber == "" || len(messageNumber) > 32 {
		return webhooks.RunnerResult{Verified: false}, nil
	}
	return webhooks.RunnerResult{Verified: true, Event: calendarEventName,
		ProviderEventID: channelID + ":" + messageNumber}, nil
}
