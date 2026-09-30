package hooks

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations/slack"
)

// answeringSurface is a surface whose deliveries are answered with what it
// says: the fakeSurface's recording, and an answer for the response's body.
type answeringSurface struct {
	*fakeSurface
	answer json.RawMessage
}

func (a *answeringSurface) DeliverAndAnswer(ctx context.Context, kind, key string, header http.Header, body []byte, receivedAt time.Time) (json.RawMessage, error) {
	if err := a.Deliver(ctx, kind, key, header, body, receivedAt); err != nil {
		return nil, err
	}
	return a.answer, nil
}

func (a *answeringSurface) Surface(toolkit string) (Surface, bool) {
	if toolkit == surfaceToolkit {
		return a, true
	}
	return nil, false
}

// TestASurfacesAnswerIsTheResponsesBody: a surface that answers a delivery, as
// Slack's modal submissions are answered with what to show in the modal, has
// its answer sent as the 200's JSON body; with no answer the 200 is empty, as
// every delivery's is.
func TestASurfacesAnswerIsTheResponsesBody(t *testing.T) {
	surface := &answeringSurface{fakeSurface: &fakeSurface{webhook: *slack.New().Manifest().Webhook}}
	h := newHarness(t, func(o *harnessOptions) { o.surfaces = surface }, withDeploymentSecret(surfaceToolkit, surfaceSecret))
	surface.store = h.store
	h.mux.Handle("POST "+actionsPath, h.service.Alias(surfaceToolkit, "interactivity"))

	surface.answer = json.RawMessage(`{"response_action":"errors","errors":{"reply":"Write your reply first."}}`)
	body := []byte("payload=%7B%22type%22%3A%22view_submission%22%7D")
	header := slackDelivery(surfaceSecret, h.clock.Now(), body)
	header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := h.post(actionsPath, header, body)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" || response.Body.String() != string(surface.answer) {
		t.Fatalf("an answered delivery = %d %s %q", response.Code, response.Header().Get("Content-Type"), response.Body)
	}
	if len(surface.deliveries()) != 1 {
		t.Fatalf("deliveries = %d, want the one", len(surface.deliveries()))
	}

	surface.answer = nil
	other := []byte("payload=%7B%22type%22%3A%22view_submission%22%2C%22n%22%3A2%7D")
	header = slackDelivery(surfaceSecret, h.clock.Now(), other)
	header.Set("Content-Type", "application/x-www-form-urlencoded")
	if response := h.post(actionsPath, header, other); response.Code != http.StatusOK || response.Body.Len() != 0 {
		t.Fatalf("a delivery with no answer = %d %q, want an empty 200", response.Code, response.Body)
	}
}
