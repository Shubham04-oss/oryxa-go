package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations/slack"
)

const (
	surfaceToolkit = "slackbot"
	surfaceSecret  = "signing-secret-0123456789abcdef"
	eventsPath     = "/v1/apps/slack/events"
	actionsPath    = "/v1/apps/slack/interactivity"
)

// fakeSurface is a surface that keeps what it was handed, and records each
// delivery's dedupe key with the store as a real one does.
type fakeSurface struct {
	store *memoryStore
	// webhook is the scheme it verifies with: Slack's v0, as the real one's is.
	webhook integration.Webhook

	mu        sync.Mutex
	err       error
	delivered []surfaceDelivery
}

type surfaceDelivery struct {
	kind, key string
	header    http.Header
	body      []byte
	deadline  time.Time
	hasLimit  bool
}

func (f *fakeSurface) Webhook() integration.Webhook { return f.webhook }

func (f *fakeSurface) Deliver(ctx context.Context, kind, key string, header http.Header, body []byte, receivedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	deadline, limited := ctx.Deadline()
	f.delivered = append(f.delivered, surfaceDelivery{kind, key, header, body, deadline, limited})
	if f.err != nil {
		return f.err
	}
	_, err := f.store.RecordHookDelivery(ctx, Delivery{Key: key, ReceivedAt: receivedAt})
	return err
}

func (f *fakeSurface) Surface(toolkit string) (Surface, bool) {
	if toolkit == surfaceToolkit {
		return f, true
	}
	return nil, false
}

func (f *fakeSurface) deliveries() []surfaceDelivery {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]surfaceDelivery(nil), f.delivered...)
}

// surfaceHarness is hooks with a surface, served at its two static URLs.
func surfaceHarness(t *testing.T) (*harness, *fakeSurface) {
	t.Helper()
	surface := &fakeSurface{webhook: *slack.New().Manifest().Webhook}
	h := newHarness(t, func(o *harnessOptions) { o.surfaces = surface }, withDeploymentSecret(surfaceToolkit, surfaceSecret))
	surface.store = h.store
	h.mux.Handle("POST "+eventsPath, h.service.Alias(surfaceToolkit, "events"))
	h.mux.Handle("POST "+actionsPath, h.service.Alias(surfaceToolkit, "interactivity"))
	return h, surface
}

func (h *harness) surfaceEndpoint() Endpoint {
	h.t.Helper()
	endpoint, err := h.store.ActiveHookEndpoint(context.Background(), integration.HookScopeDeployment, "", surfaceToolkit)
	if err != nil {
		h.t.Fatalf("the surface's endpoint: %v", err)
	}
	return endpoint
}

func mentionBody(eventID string) []byte {
	return []byte(`{"type":"event_callback","team_id":"T0FAKE01","event_id":"` + eventID +
		`","event":{"type":"app_mention","user":"U0ASKER","text":"<@U0BOT> hi","ts":"1.000001","channel":"C1"}}`)
}

func TestASurfaceIsServedAtItsAliasAndTheEndpointIsMadeOnTheFirstRequest(t *testing.T) {
	h, surface := surfaceHarness(t)
	if _, err := h.store.ActiveHookEndpoint(context.Background(), integration.HookScopeDeployment, "", surfaceToolkit); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an endpoint exists before any request: %v", err)
	}

	// Slack's url_verification: signed, answered with the challenge.
	challenge := []byte(`{"token":"legacy","challenge":"3eZbrw1aBm2rZgRNFdxV2595E9CY3gmdALWMmHkvFXO7tYXAYM8P","type":"url_verification"}`)
	response := h.post(eventsPath, slackDelivery(surfaceSecret, h.clock.Now(), challenge), challenge)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"challenge":"3eZbrw1aBm2rZgRNFdxV2595E9CY3gmdALWMmHkvFXO7tYXAYM8P"`) {
		t.Fatalf("url_verification = %d %s", response.Code, response.Body)
	}
	endpoint := h.surfaceEndpoint()
	if endpoint.Scope != integration.HookScopeDeployment || endpoint.ValidatedAt == nil {
		t.Fatalf("endpoint = %+v, want the deployment's, validated by the handshake", endpoint)
	}
	if len(surface.deliveries()) != 0 {
		t.Fatal("a handshake was handed to the surface")
	}
	// Later requests find the same endpoint.
	body := mentionBody("Ev1")
	if response := h.post(eventsPath, slackDelivery(surfaceSecret, h.clock.Now(), body), body); response.Code != http.StatusOK {
		t.Fatalf("delivery = %d %s", response.Code, response.Body)
	}
	if again := h.surfaceEndpoint(); again.ID != endpoint.ID {
		t.Fatalf("a second endpoint was made: %s and %s", endpoint.ID, again.ID)
	}

	delivered := surface.deliveries()
	if len(delivered) != 1 {
		t.Fatalf("deliveries = %d", len(delivered))
	}
	got := delivered[0]
	if got.kind != "events" || got.key != "hook:"+endpoint.ID+":Ev1" || string(got.body) != string(body) {
		t.Fatalf("delivered = %+v", got)
	}
	if !got.hasLimit || got.deadline.After(time.Now().Add(DefaultSurfaceDeadline+time.Second)) {
		t.Fatalf("the surface was given no deadline of its own: %+v", got)
	}
}

func TestASurfaceNeverSeesTheDeliverysCredentials(t *testing.T) {
	h, surface := surfaceHarness(t)
	body := mentionBody("Ev2")
	header := slackDelivery(surfaceSecret, h.clock.Now(), body)
	header.Set("Authorization", "Bearer not-for-the-surface")
	header.Set("Cookie", "session=not-for-the-surface")
	if response := h.post(eventsPath, header, body); response.Code != http.StatusOK {
		t.Fatalf("delivery = %d %s", response.Code, response.Body)
	}
	delivered := surface.deliveries()
	if len(delivered) != 1 || delivered[0].header.Get("Authorization") != "" || delivered[0].header.Get("Cookie") != "" ||
		delivered[0].header.Get("X-Slack-Signature") == "" {
		t.Fatalf("headers = %v", delivered)
	}
}

func TestSurfaceDeliveriesAreVerifiedByTheV0SchemeBeforeTheSurfaceSeesThem(t *testing.T) {
	h, surface := surfaceHarness(t)
	now := h.clock.Now()
	signed := func(secret string, at time.Time, eventID string) (http.Header, []byte) {
		body := mentionBody(eventID)
		return slackDelivery(secret, at, body), body
	}
	type attempt struct {
		header http.Header
		body   []byte
		status int
	}
	attempts := map[string]attempt{}
	add := func(name string, status int, header http.Header, body []byte) {
		attempts[name] = attempt{header, body, status}
	}

	header, body := signed(surfaceSecret, now, "Ev3")
	add("a valid delivery", http.StatusOK, header, body)
	add("no signature", http.StatusUnauthorized, http.Header{"X-Slack-Request-Timestamp": {"1"}}, mentionBody("Ev4"))
	header, body = signed(surfaceSecret, now, "Ev5")
	header.Set("X-Slack-Signature", "v0="+strings.Repeat("0", 64))
	add("a bad signature", http.StatusUnauthorized, header, body)
	header, body = signed("another-secret-0123456789abcdef", now, "Ev6")
	add("signed with another secret", http.StatusUnauthorized, header, body)
	header, body = signed(surfaceSecret, now.Add(-10*time.Minute), "Ev7")
	add("a stale timestamp, signed for it", http.StatusUnauthorized, header, body)
	header, body = signed(surfaceSecret, now.Add(10*time.Minute), "Ev8")
	add("a timestamp from the future", http.StatusUnauthorized, header, body)
	// A captured delivery replayed later carries a fresh timestamp over the old signature.
	header, body = signed(surfaceSecret, now.Add(-10*time.Minute), "Ev9")
	fresh, _ := signed(surfaceSecret, now, "Ev9")
	fresh.Set("X-Slack-Signature", header.Get("X-Slack-Signature"))
	add("a fresh timestamp over a stale signature", http.StatusUnauthorized, fresh, body)
	header, body = signed(surfaceSecret, now, "Ev10")
	add("a body changed after signing", http.StatusUnauthorized, header, append(body[:len(body)-1:len(body)-1], []byte(` }`)...))

	for name, test := range attempts {
		t.Run(name, func(t *testing.T) {
			before := len(surface.deliveries())
			response := h.post(eventsPath, test.header, test.body)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.status, response.Body)
			}
			if handed := len(surface.deliveries()) - before; (test.status == http.StatusOK) != (handed == 1) {
				t.Fatalf("the surface was handed %d deliveries", handed)
			}
		})
	}
	h.assertLogsLack(surfaceSecret, "U0ASKER", "<@U0BOT> hi")
}

func TestAReplayedSurfaceDeliveryIsAcknowledgedAndNotDeliveredTwice(t *testing.T) {
	h, surface := surfaceHarness(t)
	body := mentionBody("Ev8")
	header := slackDelivery(surfaceSecret, h.clock.Now(), body)
	for range 3 {
		if response := h.post(eventsPath, header, body); response.Code != http.StatusOK {
			t.Fatalf("delivery = %d %s", response.Code, response.Body)
		}
	}
	if delivered := surface.deliveries(); len(delivered) != 1 {
		t.Fatalf("deliveries = %d, want the replays deduped", len(delivered))
	}
	// A retry Slack signs afresh is the same delivery: it carries the same event id.
	h.clock.Advance(time.Minute)
	if response := h.post(eventsPath, slackDelivery(surfaceSecret, h.clock.Now(), body), body); response.Code != http.StatusOK {
		t.Fatalf("a re-signed retry = %d", response.Code)
	}
	if delivered := surface.deliveries(); len(delivered) != 1 {
		t.Fatalf("deliveries = %d after a re-signed retry", len(delivered))
	}
}

func TestAFormEncodedClickIsDedupedByItsBodyHash(t *testing.T) {
	h, surface := surfaceHarness(t)
	payload := `{"type":"block_actions","user":{"id":"U0OWNER"},"actions":[{"action_id":"consent_deny","value":"t1.x"}]}`
	body := []byte(url.Values{"payload": {payload}}.Encode())
	header := slackDelivery(surfaceSecret, h.clock.Now(), body)
	header.Set("Content-Type", "application/x-www-form-urlencoded")

	for range 2 {
		if response := h.post(actionsPath, header, body); response.Code != http.StatusOK {
			t.Fatalf("click = %d %s", response.Code, response.Body)
		}
	}
	delivered := surface.deliveries()
	if len(delivered) != 1 || delivered[0].kind != "interactivity" || !strings.HasPrefix(delivered[0].key, "hook:"+h.surfaceEndpoint().ID+":sha256:") {
		t.Fatalf("deliveries = %+v", delivered)
	}
	// A different click is a different delivery.
	other := []byte(url.Values{"payload": {strings.Replace(payload, "consent_deny", "consent_summary", 1)}}.Encode())
	otherHeader := slackDelivery(surfaceSecret, h.clock.Now(), other)
	if response := h.post(actionsPath, otherHeader, other); response.Code != http.StatusOK || len(surface.deliveries()) != 2 {
		t.Fatalf("another click = %d, deliveries = %d", response.Code, len(surface.deliveries()))
	}
}

func TestASurfaceThatCannotRecordADeliveryHasItRetriedByTheApp(t *testing.T) {
	h, surface := surfaceHarness(t)
	body := mentionBody("Ev9")
	header := slackDelivery(surfaceSecret, h.clock.Now(), body)

	surface.mu.Lock()
	surface.err = errors.New("the database went away")
	surface.mu.Unlock()
	if response := h.post(eventsPath, header, body); response.Code != http.StatusInternalServerError || errorCode(t, response) != "internal" {
		t.Fatalf("a failed delivery = %d %s, want 500", response.Code, response.Body)
	}
	surface.mu.Lock()
	surface.err = context.DeadlineExceeded
	surface.mu.Unlock()
	if response := h.post(eventsPath, header, body); response.Code != http.StatusServiceUnavailable || errorCode(t, response) != "unavailable" {
		t.Fatalf("a delivery that ran out of time = %d %s, want 503", response.Code, response.Body)
	}
	// Nothing was marked, so Slack's retry is handled.
	surface.mu.Lock()
	surface.err = nil
	surface.mu.Unlock()
	if response := h.post(eventsPath, header, body); response.Code != http.StatusOK {
		t.Fatalf("the retry = %d %s", response.Code, response.Body)
	}
	if delivered := surface.deliveries(); len(delivered) != 3 {
		t.Fatalf("deliveries = %d, want the two failures and the retry", len(delivered))
	}
}

func TestASurfacesEndpointIdIsNoAddress(t *testing.T) {
	h, _ := surfaceHarness(t)
	challenge := []byte(`{"challenge":"c","type":"url_verification"}`)
	h.post(eventsPath, slackDelivery(surfaceSecret, h.clock.Now(), challenge), challenge)
	endpoint := h.surfaceEndpoint()

	// Even a correctly signed delivery to the generic route is an unknown endpoint's.
	body := mentionBody("Ev10")
	response := h.deliver(endpoint.ID, slackDelivery(surfaceSecret, h.clock.Now(), body), body)
	if response.Code != http.StatusNotFound || errorCode(t, response) != "not_found" {
		t.Fatalf("the generic route = %d %s", response.Code, response.Body)
	}
}

func TestAnAliasForNoSurfaceIsAnUnknownEndpoint(t *testing.T) {
	h := newHarness(t)
	h.mux.Handle("POST /v1/apps/slack/events", h.service.Alias(surfaceToolkit, "events"))
	body := mentionBody("Ev11")
	response := h.post(eventsPath, slackDelivery(surfaceSecret, h.clock.Now(), body), body)
	if response.Code != http.StatusNotFound || errorCode(t, response) != "not_found" {
		t.Fatalf("an alias with no surface = %d %s", response.Code, response.Body)
	}
	if _, err := h.store.ActiveHookEndpoint(context.Background(), integration.HookScopeDeployment, "", surfaceToolkit); err == nil {
		t.Fatal("an endpoint was made for a toolkit that is no surface")
	}
	// An ordinary toolkit is no surface either, whatever the alias says.
	surface := &fakeSurface{webhook: *slack.New().Manifest().Webhook}
	withSurface := newHarness(t, func(o *harnessOptions) { o.surfaces = surface })
	withSurface.mux.Handle("POST /v1/apps/other/events", withSurface.service.Alias("github", "events"))
	if response := withSurface.post("/v1/apps/other/events", slackDelivery(surfaceSecret, withSurface.clock.Now(), body), body); response.Code != http.StatusNotFound {
		t.Fatalf("an alias for an integration's toolkit = %d", response.Code)
	}
}

func TestASurfaceAliasServesPOSTOnly(t *testing.T) {
	h, _ := surfaceHarness(t)
	recorder := httptest.NewRecorder()
	h.service.Alias(surfaceToolkit, "events").ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, eventsPath, nil))
	if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("a GET = %d, Allow %q", recorder.Code, recorder.Header().Get("Allow"))
	}
}

// The scheme a surface verifies with is frozen into its endpoint as any
// endpoint's is, and is one the core's manifest rules accept.
func TestASurfacesSchemeIsFrozenIntoItsEndpointAndIsAValidWebhook(t *testing.T) {
	h, surface := surfaceHarness(t)
	challenge := []byte(`{"challenge":"c","type":"url_verification"}`)
	h.post(eventsPath, slackDelivery(surfaceSecret, h.clock.Now(), challenge), challenge)
	endpoint := h.surfaceEndpoint()

	frozen, _ := json.Marshal(endpoint.Scheme)
	want, _ := json.Marshal(surface.Webhook())
	if string(frozen) != string(want) {
		t.Fatalf("frozen scheme\n got  %s\n want %s", frozen, want)
	}
	valid := testManifest(surfaceToolkit, &endpoint.Scheme)
	if err := valid.Validate(); err != nil {
		t.Fatalf("the surface's webhook breaks the core's rules: %v", err)
	}
}
