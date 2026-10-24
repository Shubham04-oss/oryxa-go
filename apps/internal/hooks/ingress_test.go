package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// watchedBody records whether anything read it.
type watchedBody struct{ read bool }

func (w *watchedBody) Read([]byte) (int, error) {
	w.read = true
	return 0, io.EOF
}

func errorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var answer struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil {
		t.Fatalf("the answer %q is not an error: %v", response.Body, err)
	}
	return answer.Error.Code
}

func TestUnknownEndpointsAreRefusedBeforeTheirBodyIsRead(t *testing.T) {
	h := newHarness(t)
	expiring := h.connectionEndpoint("github", "apc_1", "mem_1", githubTrigger)
	lapse := testStart.Add(time.Hour)
	if err := h.service.SetRemote(context.Background(), expiring.Endpoint.ID, "hook-1", &lapse); err != nil {
		t.Fatal(err)
	}
	deleted := h.connectionEndpoint("github", "apc_2", "mem_2", githubTrigger)
	if err := h.service.Delete(context.Background(), deleted.Endpoint.ID); err != nil {
		t.Fatal(err)
	}
	h.clock.Set(lapse)

	for name, test := range map[string]struct {
		path          string
		storeLookedUp bool
	}{
		"a malformed id":                              {PathPrefix + "not-an-endpoint", false},
		"an id of the wrong length":                   {PathPrefix + "aph_abc", false},
		"an id in upper case":                         {PathPrefix + strings.ToUpper(expiring.Endpoint.ID), false},
		"a well-formed id nobody made":                {PathPrefix + "aph_aaaaaaaaaaaaaaaaaaaaaaaa", true},
		"a deleted endpoint":                          {PathPrefix + deleted.Endpoint.ID, true},
		"an endpoint whose app's subscription lapsed": {PathPrefix + expiring.Endpoint.ID, true},
	} {
		body := &watchedBody{}
		before := h.store.lookups
		recorder := httptest.NewRecorder()
		h.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, test.path, body))
		if recorder.Code != http.StatusNotFound || errorCode(t, recorder) != "not_found" {
			t.Errorf("%s = %d %s, want 404 not_found", name, recorder.Code, recorder.Body)
		}
		if body.read {
			t.Errorf("%s: the body was read", name)
		}
		if looked := h.store.lookups > before; looked != test.storeLookedUp {
			t.Errorf("%s: the store was asked = %v, want %v", name, looked, test.storeLookedUp)
		}
	}
	// Only POST is served.
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, PathPrefix+expiring.Endpoint.ID, nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET = %d, want 405", recorder.Code)
	}
}

func TestEachEndpointIsRateLimitedOnItsOwn(t *testing.T) {
	h := newHarness(t)
	busy := h.connectionEndpoint("github", "apc_1", "mem_1", githubTrigger)
	quiet := h.connectionEndpoint("github", "apc_2", "mem_2", githubTrigger)
	body := []byte(`{"id":"issue-1"}`)
	signed := func(endpoint Created, i int) *httptest.ResponseRecorder {
		return h.deliver(endpoint.Endpoint.ID, githubDelivery(endpoint.Hook.Secret, fmt.Sprintf("d-%d", i), "issues", body), body)
	}

	for i := range RatePerSecond {
		if response := signed(busy, i); response.Code != http.StatusOK {
			t.Fatalf("delivery %d inside the rate = %d %s, want 200", i+1, response.Code, response.Body)
		}
	}
	response := signed(busy, RatePerSecond)
	if response.Code != http.StatusTooManyRequests || errorCode(t, response) != "rate_limited" {
		t.Fatalf("delivery %d = %d %s, want 429 rate_limited", RatePerSecond+1, response.Code, response.Body)
	}
	if response.Header().Get("Retry-After") != "1" {
		t.Errorf("Retry-After = %q, want 1", response.Header().Get("Retry-After"))
	}
	// Another endpoint has its own budget.
	if response := signed(quiet, 0); response.Code != http.StatusOK {
		t.Fatalf("another endpoint = %d, want 200, not rate limited", response.Code)
	}
	// A second later the bucket is full again.
	h.clock.Advance(time.Second)
	if response := signed(busy, RatePerSecond); response.Code != http.StatusOK {
		t.Fatalf("a second later = %d %s, want 200", response.Code, response.Body)
	}
}

// Unsigned junk is refused before it spends the endpoint's rate, so a flood
// of it at Slack's public URL, which every org shares, cannot starve Slack's
// own deliveries (#475). The flooding address is turned away on its own.
func TestAnUnsignedFloodDoesNotRateLimitSignedSlackDeliveries(t *testing.T) {
	h, surface := surfaceHarness(t)
	junk := mentionBody("EvJunk")
	for i := range 10 * RatePerSecond {
		header := http.Header{}
		if i%2 == 0 {
			header = slackDelivery("not-the-signing-secret-0123456789", h.clock.Now(), junk)
		}
		header.Set("CF-Connecting-IP", "203.0.113.7")
		want := http.StatusUnauthorized
		if i >= RefusalsPerSecond {
			want = http.StatusTooManyRequests
		}
		if response := h.post(eventsPath, header, junk); response.Code != want {
			t.Fatalf("junk %d = %d, want %d", i+1, response.Code, want)
		}
	}
	for i := range RatePerSecond {
		body := mentionBody(fmt.Sprintf("EvReal%d", i))
		header := slackDelivery(surfaceSecret, h.clock.Now(), body)
		header.Set("CF-Connecting-IP", "198.51.100.1")
		if response := h.post(eventsPath, header, body); response.Code != http.StatusOK {
			t.Fatalf("signed delivery %d during the flood = %d %s, want 200", i+1, response.Code, response.Body)
		}
	}
	if len(surface.deliveries()) != RatePerSecond {
		t.Fatalf("the surface was handed %d deliveries, want %d", len(surface.deliveries()), RatePerSecond)
	}
	// A second later the flooding address may try again.
	h.clock.Advance(time.Second)
	if response := h.post(eventsPath, http.Header{"Cf-Connecting-Ip": {"203.0.113.7"}}, junk); response.Code != http.StatusUnauthorized {
		t.Fatalf("junk a second later = %d, want 401", response.Code)
	}
}

// An app delivers for many orgs from the same addresses, so one endpoint
// refusing them (an org's wrong secret) does not turn them away from another.
func TestRefusalsAtOneEndpointDoNotBlockAnother(t *testing.T) {
	h := newHarness(t)
	broken := h.connectionEndpoint("github", "apc_1", "mem_1", githubTrigger)
	healthy := h.connectionEndpoint("github", "apc_2", "mem_2", githubTrigger)
	body := []byte(`{"id":"issue-1"}`)
	for range RefusalsPerSecond {
		h.deliver(broken.Endpoint.ID, githubDelivery("a-stale-secret-0123456789abcdef", "d-1", "issues", body), body)
	}
	if response := h.deliver(broken.Endpoint.ID, githubDelivery(broken.Hook.Secret, "d-1", "issues", body), body); response.Code != http.StatusTooManyRequests {
		t.Fatalf("the refusing endpoint = %d, want 429", response.Code)
	}
	if response := h.deliver(healthy.Endpoint.ID, githubDelivery(healthy.Hook.Secret, "d-1", "issues", body), body); response.Code != http.StatusOK {
		t.Fatalf("another endpoint from the same address = %d %s, want 200", response.Code, response.Body)
	}
}

func TestBodiesAreCappedAtOneMebibyte(t *testing.T) {
	h := newHarness(t)
	created := h.connectionEndpoint("github", "apc_1", "mem_1", githubTrigger)
	pad := func(size int) []byte {
		prefix := `{"id":"big","pad":"`
		return []byte(prefix + strings.Repeat("a", size-len(prefix)-2) + `"}`)
	}

	largest := pad(MaxBodyBytes)
	if response := h.deliver(created.Endpoint.ID, githubDelivery(created.Hook.Secret, "d-large", "issues", largest), largest); response.Code != http.StatusOK {
		t.Fatalf("a body of exactly %d bytes = %d %s, want 200", MaxBodyBytes, response.Code, response.Body)
	}
	received := h.received()
	if len(received) != 1 || !received[0].DataOmitted || received[0].Data != nil {
		t.Fatalf("published %+v, want one event flagged data_omitted and carrying no data", received)
	}

	tooLarge := pad(MaxBodyBytes + 1)
	response := h.deliver(created.Endpoint.ID, githubDelivery(created.Hook.Secret, "d-larger", "issues", tooLarge), tooLarge)
	if response.Code != http.StatusRequestEntityTooLarge || errorCode(t, response) != "invalid_request" {
		t.Fatalf("a body a byte over = %d %s, want 413 invalid_request", response.Code, response.Body)
	}
	if h.fakes["github"].parsed() != 1 {
		t.Fatal("Parse saw the oversized delivery")
	}
}

func TestTriggerDataOver128KiBIsLeftOut(t *testing.T) {
	h := newHarness(t)
	created := h.connectionEndpoint("github", "apc_1", "mem_1", githubTrigger)
	sized := func(id string, size int) []byte {
		prefix := `{"id":"` + id + `","pad":"`
		return []byte(prefix + strings.Repeat("b", size-len(prefix)-2) + `"}`)
	}
	atLimit := sized("at-limit", appsv1.MaxTriggerDataBytes)
	overLimit := sized("over-limit", appsv1.MaxTriggerDataBytes+1)
	for i, body := range [][]byte{atLimit, overLimit} {
		if response := h.deliver(created.Endpoint.ID, githubDelivery(created.Hook.Secret, fmt.Sprint("d-", i), "issues", body), body); response.Code != http.StatusOK {
			t.Fatalf("delivery %d = %d", i, response.Code)
		}
	}
	received := h.received()
	if len(received) != 2 {
		t.Fatalf("published %d events, want 2", len(received))
	}
	if received[0].DataOmitted || len(received[0].Data) != appsv1.MaxTriggerDataBytes {
		t.Errorf("data of exactly 128 KiB: omitted = %v, %d bytes; want it carried", received[0].DataOmitted, len(received[0].Data))
	}
	if !received[1].DataOmitted || received[1].Data != nil {
		t.Errorf("data a byte over 128 KiB: omitted = %v, %d bytes; want it left out", received[1].DataOmitted, len(received[1].Data))
	}
}

func TestAVerifiedDeliveryPublishesOnceToItsConnection(t *testing.T) {
	h := newHarness(t)
	created := h.connectionEndpoint("github", "apc_1", "mem_1", githubTrigger)
	body := []byte(`{"id":"issue-opened-1","action":"opened","issue":{"number":7,"title":"Flaky test"}}`)
	header := githubDelivery(created.Hook.Secret, "72d3162e-cc78-11e3-81ab-4c9367dc0958", "issues", body)

	if response := h.deliver(created.Endpoint.ID, header, body); response.Code != http.StatusOK {
		t.Fatalf("a signed delivery = %d %s, want 200", response.Code, response.Body)
	}
	received := h.received()
	want := receivedEvent{
		TriggerID: "apt_apc_1", ConnectionID: "apc_1", OrganizationID: testOrg, MemberID: "mem_1",
		Toolkit: "github", Trigger: githubTrigger, ReceivedAt: testStart,
	}
	if len(received) != 1 {
		t.Fatalf("published %d events, want 1", len(received))
	}
	got := received[0]
	if got.TriggerID != want.TriggerID || got.ConnectionID != want.ConnectionID || got.OrganizationID != want.OrganizationID ||
		got.MemberID != want.MemberID || got.Toolkit != want.Toolkit || got.Trigger != want.Trigger ||
		!got.ReceivedAt.Equal(want.ReceivedAt) || got.DataOmitted {
		t.Fatalf("published %+v, want %+v", got, want)
	}
	if string(got.Data) != string(body) {
		t.Fatalf("data = %s, want the app's payload", got.Data)
	}
	if _, ok := h.store.handled["hook:"+created.Endpoint.ID+":72d3162e-cc78-11e3-81ab-4c9367dc0958"]; !ok {
		t.Fatalf("the delivery was not recorded under hook:{endpoint}:{delivery}: %v", h.store.handled)
	}
	if h.store.counted["apt_apc_1"] != 1 {
		t.Fatalf("the trigger counted %d deliveries, want 1", h.store.counted["apt_apc_1"])
	}

	// The same delivery again is answered, and neither parsed nor published.
	if response := h.deliver(created.Endpoint.ID, header, body); response.Code != http.StatusOK {
		t.Fatalf("a replayed delivery = %d, want 200", response.Code)
	}
	if h.fakes["github"].parsed() != 1 || len(h.received()) != 1 {
		t.Fatalf("a replay was parsed (%d) or published (%d)", h.fakes["github"].parsed(), len(h.received()))
	}
	// Redelivered under a new delivery id, the same event is still one event.
	again := githubDelivery(created.Hook.Secret, "redelivery-2", "issues", body)
	if response := h.deliver(created.Endpoint.ID, again, body); response.Code != http.StatusOK || len(h.received()) != 1 {
		t.Fatalf("the same event under a new delivery id = %d, %d events; want 200 and still one", response.Code, len(h.received()))
	}
}

func TestAnUnverifiedDeliveryIsRefusedAndNothingInItIsUsed(t *testing.T) {
	h := newHarness(t)
	created := h.connectionEndpoint("github", "apc_1", "mem_1", githubTrigger)
	body := []byte(`{"id":"issue-1","title":"private-title-9f8e7d"}`)
	forged := "sha256=" + hmacHex("not-the-secret", string(body))

	for name, delivery := range map[string]struct {
		header http.Header
		body   []byte
	}{
		"another secret":     {headerOf("X-Hub-Signature-256", forged, "X-GitHub-Delivery", "d-forged"), body},
		"a tampered body":    {githubDelivery(created.Hook.Secret, "d-tampered", "issues", body), []byte(strings.Replace(string(body), "issue-1", "issue-2", 1))},
		"no signature":       {headerOf("X-GitHub-Delivery", "d-bare"), body},
		"an empty body":      {githubDelivery(created.Hook.Secret, "d-empty", "issues", body), nil},
		"an empty signature": {headerOf("X-Hub-Signature-256", "sha256=", "X-GitHub-Delivery", "d-empty-signature"), body},
	} {
		response := h.deliver(created.Endpoint.ID, delivery.header, delivery.body)
		if response.Code != http.StatusUnauthorized || errorCode(t, response) != "unauthorized" {
			t.Errorf("%s = %d %s, want 401 unauthorized", name, response.Code, response.Body)
		}
	}
	if h.fakes["github"].parsed() != 0 || len(h.store.handled) != 0 || len(h.received()) != 0 {
		t.Fatalf("an unverified delivery was used: parsed %d, recorded %d, published %d",
			h.fakes["github"].parsed(), len(h.store.handled), len(h.received()))
	}
	h.assertLogsLack("private-title-9f8e7d", strings.TrimPrefix(forged, "sha256="), "d-forged", created.Hook.Secret)
}

func TestARotatedSecretKeepsTheOldOneFor24Hours(t *testing.T) {
	h := newHarness(t)
	created := h.connectionEndpoint("github", "apc_1", "mem_1", githubTrigger)
	old := created.Hook.Secret
	rotated, err := h.service.Rotate(context.Background(), created.Endpoint.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	current := rotated.Hook.Secret
	if current == "" || current == old || rotated.URL != created.URL {
		t.Fatalf("the rotation answered %v, want a new secret for the same URL", rotated.Hook)
	}

	deliver := func(secret, id string) int {
		body := []byte(`{"id":"` + id + `"}`)
		return h.deliver(created.Endpoint.ID, githubDelivery(secret, id, "issues", body), body).Code
	}
	if deliver(old, "old-now") != http.StatusOK || deliver(current, "new-now") != http.StatusOK {
		t.Fatal("right after a rotation, both secrets must verify")
	}
	h.clock.Advance(RotationOverlap - time.Second)
	if code := deliver(old, "old-late"); code != http.StatusOK {
		t.Fatalf("the old secret a second before the overlap ends = %d, want 200", code)
	}
	h.clock.Advance(time.Second)
	if code := deliver(old, "old-expired"); code != http.StatusUnauthorized {
		t.Fatalf("the old secret once the overlap ends = %d, want 401", code)
	}
	if code := deliver(current, "new-later"); code != http.StatusOK {
		t.Fatalf("the new secret after the overlap = %d, want 200", code)
	}
	// Rotating again answers another secret; a stale rotation is refused.
	stale := h.store.endpoint(t, created.Endpoint.ID)
	if _, err := h.service.Rotate(context.Background(), created.Endpoint.ID, ""); err != nil {
		t.Fatal(err)
	}
	if ok, _ := h.store.RotateHookSecret(context.Background(), stale.ID, stale.Secret.Version, secrets.SealedData{}, testStart, testStart); ok {
		t.Fatal("a rotation over a secret it did not read went through")
	}
}

func TestSecretsAreOpenedOncePerVersionForAFewMinutes(t *testing.T) {
	h := newHarness(t)
	created := h.connectionEndpoint("github", "apc_1", "mem_1", githubTrigger)
	deliver := func(id string) {
		t.Helper()
		body := []byte(`{"id":"` + id + `"}`)
		if response := h.deliver(created.Endpoint.ID, githubDelivery(created.Hook.Secret, id, "issues", body), body); response.Code != http.StatusOK {
			t.Fatalf("delivery %s = %d", id, response.Code)
		}
	}
	deliver("a")
	deliver("b")
	deliver("c")
	if opened := h.keys.opened(); opened != 1 {
		t.Fatalf("three deliveries opened the secret %d times, want once", opened)
	}
	h.clock.Advance(openedSecretLifetime)
	deliver("d")
	if opened := h.keys.opened(); opened != 2 {
		t.Fatalf("after %s the secret was opened %d times in all, want twice", openedSecretLifetime, opened)
	}
	// A forged delivery opens nothing more than a genuine one: the secret is
	// already open.
	for i := range 10 {
		h.deliver(created.Endpoint.ID, headerOf("X-Hub-Signature-256", "sha256="+strings.Repeat("0", 64)), []byte(fmt.Sprint(i)))
	}
	if opened := h.keys.opened(); opened != 2 {
		t.Fatalf("forged deliveries opened the secret: %d opens in all, want 2", opened)
	}
	// Nothing is opened for a delivery that carries no signature at all.
	fresh := h.connectionEndpoint("github", "apc_2", "mem_2", githubTrigger)
	h.deliver(fresh.Endpoint.ID, nil, []byte(`{}`))
	if opened := h.keys.opened(); opened != 2 {
		t.Fatalf("an unsigned delivery opened a secret: %d opens in all, want 2", opened)
	}
}

func TestKMSFailingAsksTheAppToRetry(t *testing.T) {
	h := newHarness(t)
	created := h.connectionEndpoint("github", "apc_1", "mem_1", githubTrigger)
	h.keys.mu.Lock()
	h.keys.broken = true
	h.keys.mu.Unlock()
	body := []byte(`{"id":"x"}`)
	response := h.deliver(created.Endpoint.ID, githubDelivery(created.Hook.Secret, "d-1", "issues", body), body)
	if response.Code != http.StatusServiceUnavailable || errorCode(t, response) != "unavailable" {
		t.Fatalf("a delivery while KMS is down = %d %s, want 503 unavailable", response.Code, response.Body)
	}
	if len(h.store.handled) != 0 {
		t.Fatal("a delivery that could not be checked was recorded")
	}
}

func TestSlacksDeploymentEndpointVerifiesWithTheConfiguredSecret(t *testing.T) {
	h := newHarness(t, withDeploymentSecret("slack", slackExampleSecret))
	ctx := context.Background()
	endpoint, url, err := h.service.DeploymentEndpoint(ctx, "slack")
	if err != nil {
		t.Fatal(err)
	}
	again, _, err := h.service.DeploymentEndpoint(ctx, "slack")
	if err != nil || again.ID != endpoint.ID {
		t.Fatalf("the deployment endpoint the second time = %s, %v; want %s, the same URL for good", again.ID, err, endpoint.ID)
	}
	if endpoint.Secret.Current != nil {
		t.Fatal("a deployment endpoint stored a secret; it is configured")
	}
	if url != testBaseURL+PathPrefix+endpoint.ID {
		t.Fatalf("URL = %s", url)
	}

	// Slack's own example request passes the ingress, at its own time.
	h.clock.Set(time.Unix(1531420618, 0).UTC())
	h.fakes["slack"].parse = func(http.Header, []byte) ([]integration.Event, error) { return nil, nil }
	example := headerOf("X-Slack-Request-Timestamp", slackExampleTimestamp, "X-Slack-Signature", slackExampleSignature)
	if response := h.deliver(endpoint.ID, example, []byte(slackExampleBody)); response.Code != http.StatusOK {
		t.Fatalf("Slack's example request = %d %s, want 200", response.Code, response.Body)
	}
	// It carries no event_id, so it is known by its body's hash.
	var hashed bool
	for key := range h.store.handled {
		hashed = hashed || strings.HasPrefix(key, "hook:"+endpoint.ID+":sha256:")
	}
	if !hashed {
		t.Fatalf("recorded %v, want the delivery under its body's hash", h.store.handled)
	}
	// Five minutes and a second later, the same request is refused.
	h.clock.Advance(301 * time.Second)
	if response := h.deliver(endpoint.ID, example, []byte(slackExampleBody)); response.Code != http.StatusUnauthorized {
		t.Fatalf("Slack's example request, stale = %d, want 401", response.Code)
	}
}

func TestSlacksURLVerificationIsAnsweredOnlyWhenSigned(t *testing.T) {
	const secret = "slack-signing-secret-0123456789ab"
	h := newHarness(t, withDeploymentSecret("slack", secret))
	endpoint, _, err := h.service.DeploymentEndpoint(context.Background(), "slack")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"token":"Jhj5dZrVaK7ZwHHjRyZWjbDl","challenge":"3eZbrw1aBm2rZgRNFdxV2595E9CY3gmdALWMmHkvFXO7tYXAYM8P","type":"url_verification"}`)

	response := h.deliver(endpoint.ID, slackDelivery(secret, testStart, body), body)
	if response.Code != http.StatusOK {
		t.Fatalf("a signed url_verification = %d %s, want 200", response.Code, response.Body)
	}
	var answer map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil || answer["challenge"] != "3eZbrw1aBm2rZgRNFdxV2595E9CY3gmdALWMmHkvFXO7tYXAYM8P" {
		t.Fatalf("answered %s, want the challenge", response.Body)
	}
	if h.fakes["slack"].parsed() != 0 || len(h.store.handled) != 0 {
		t.Fatal("a handshake was parsed or recorded")
	}
	if h.store.endpoint(t, endpoint.ID).ValidatedAt == nil {
		t.Fatal("the endpoint's first handshake was not recorded")
	}

	unsigned := http.Header{"X-Slack-Request-Timestamp": []string{fmt.Sprint(testStart.Unix())}}
	if response := h.deliver(endpoint.ID, unsigned, body); response.Code != http.StatusUnauthorized {
		t.Fatalf("an unsigned url_verification = %d, want 401", response.Code)
	}
	if response := h.deliver(endpoint.ID, slackDelivery(secret, testStart.Add(-10*time.Minute), body), body); response.Code != http.StatusUnauthorized {
		t.Fatalf("a stale url_verification = %d, want 401", response.Code)
	}
	if response := h.deliver(endpoint.ID, slackDelivery("another-secret-0123456789abcdef", testStart, body), body); response.Code != http.StatusUnauthorized {
		t.Fatalf("a url_verification signed by another app = %d, want 401", response.Code)
	}
}

func TestADeploymentSecretRotatesInConfiguration(t *testing.T) {
	const current, previous = "slack-signing-secret-current-000", "slack-signing-secret-previous-00"
	h := newHarness(t, withDeploymentSecret("slack", current, previous))
	endpoint, _, err := h.service.DeploymentEndpoint(context.Background(), "slack")
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{current, previous} {
		body := []byte(`{"type":"event_callback","id":"Ev-` + secret + `","account":"U_A"}`)
		if response := h.deliver(endpoint.ID, slackDelivery(secret, testStart, body), body); response.Code != http.StatusOK {
			t.Fatalf("a delivery signed with a configured secret = %d", response.Code)
		}
	}
	if _, err := h.service.Rotate(context.Background(), endpoint.ID, "anything-sixteen-chars"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("rotating a deployment endpoint = %v, want invalid: it rotates in configuration", err)
	}

	unconfigured := newHarness(t)
	bare, _, err := unconfigured.service.DeploymentEndpoint(context.Background(), "slack")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"id":"Ev1"}`)
	if response := unconfigured.deliver(bare.ID, slackDelivery(current, testStart, body), body); response.Code != http.StatusUnauthorized {
		t.Fatalf("a delivery to a deployment endpoint with no secret configured = %d, want 401", response.Code)
	}
}

func TestGitHubsPingIsAnsweredOnlyWhenSigned(t *testing.T) {
	h := newHarness(t)
	created := h.connectionEndpoint("github", "apc_1", "mem_1", githubTrigger)
	ping := []byte(`{"zen":"Keep it logically awesome.","hook_id":12345678,"hook":{"type":"Repository"}}`)

	if response := h.deliver(created.Endpoint.ID, githubDelivery(created.Hook.Secret, "ping-1", "ping", ping), ping); response.Code != http.StatusOK {
		t.Fatalf("a signed ping = %d %s, want 200", response.Code, response.Body)
	}
	if h.fakes["github"].parsed() != 0 || len(h.store.handled) != 0 || len(h.received()) != 0 {
		t.Fatal("a ping was parsed, recorded or published")
	}
	if h.store.endpoint(t, created.Endpoint.ID).ValidatedAt == nil {
		t.Fatal("the ping was not recorded as the endpoint's first handshake")
	}
	unsigned := headerOf("X-GitHub-Event", "ping", "X-GitHub-Delivery", "ping-2")
	if response := h.deliver(created.Endpoint.ID, unsigned, ping); response.Code != http.StatusUnauthorized {
		t.Fatalf("an unsigned ping = %d, want 401", response.Code)
	}
	// A ping signed for another hook is refused like any other delivery.
	if response := h.deliver(created.Endpoint.ID, githubDelivery("other-hook-secret", "ping-3", "ping", ping), ping); response.Code != http.StatusUnauthorized {
		t.Fatalf("a ping signed with another secret = %d, want 401", response.Code)
	}
}

func driveNotification(token, state, number string) http.Header {
	return headerOf("X-Goog-Channel-ID", "channel-1", "X-Goog-Channel-Token", token,
		"X-Goog-Resource-State", state, "X-Goog-Message-Number", number, "X-Goog-Resource-ID", "resource-1")
}

func TestGoogleChannelsSyncAndRenewOntoAFreshEndpoint(t *testing.T) {
	h := newHarness(t)
	h.fakes["googledrive"].parse = func(header http.Header, _ []byte) ([]integration.Event, error) {
		// Drive's notifications have no body; the change is fetched later.
		data, _ := json.Marshal(map[string]string{"state": header.Get("X-Goog-Resource-State")})
		return []integration.Event{{Trigger: driveTrigger, Data: data}}, nil
	}
	created := h.connectionEndpoint("googledrive", "apc_1", "mem_1", driveTrigger)
	token := created.Hook.Secret

	if response := h.deliver(created.Endpoint.ID, driveNotification(token, "sync", "1"), nil); response.Code != http.StatusOK {
		t.Fatalf("the channel's sync message = %d %s, want 200", response.Code, response.Body)
	}
	if h.fakes["googledrive"].parsed() != 0 || len(h.store.handled) != 0 {
		t.Fatal("the sync message was parsed or recorded")
	}
	if response := h.deliver(created.Endpoint.ID, driveNotification("forged-token", "sync", "1"), nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("a sync message with another token = %d, want 401", response.Code)
	}

	for _, number := range []string{"2", "3", "3"} {
		if response := h.deliver(created.Endpoint.ID, driveNotification(token, "change", number), nil); response.Code != http.StatusOK {
			t.Fatalf("change %s = %d %s", number, response.Code, response.Body)
		}
	}
	if got := len(h.received()); got != 2 {
		t.Fatalf("published %d changes, want 2: message 3 came twice", got)
	}

	// A renewed channel numbers its messages from 1 again, on an endpoint
	// of its own, so nothing is taken for a repeat.
	renewed, err := h.service.Renew(context.Background(), created.Endpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Endpoint.ID == created.Endpoint.ID || renewed.Hook.Secret == "" || renewed.Hook.Secret == token {
		t.Fatalf("the renewal = %+v, want a new endpoint with its own secret", renewed.Endpoint)
	}
	for _, number := range []string{"2", "3"} {
		if response := h.deliver(renewed.Endpoint.ID, driveNotification(renewed.Hook.Secret, "change", number), nil); response.Code != http.StatusOK {
			t.Fatalf("the renewed channel's change %s = %d", number, response.Code)
		}
	}
	if got := len(h.received()); got != 4 {
		t.Fatalf("published %d changes, want 4: the renewed channel's are new", got)
	}
	// Until it is deleted, the old channel still delivers.
	if response := h.deliver(created.Endpoint.ID, driveNotification(token, "change", "4"), nil); response.Code != http.StatusOK {
		t.Fatalf("the replaced channel, still open = %d, want 200", response.Code)
	}
}

func TestGraphValidationIsAnsweredOnceForANewEndpoint(t *testing.T) {
	h := newHarness(t)
	created := h.connectionEndpoint("outlook", "apc_1", "mem_1", graphTrigger)
	const token = "Validation: Testing client application reachability for subscription Request-Id: 25b4ad6a-4ecb-4b5e-9d1c-0f3a2d2c3c1a"
	validate := func(id, token string) *httptest.ResponseRecorder {
		return h.post(PathPrefix+id+"?validationToken="+url.QueryEscape(token), headerOf("Content-Type", "text/plain; charset=utf-8"), nil)
	}

	h.clock.Advance(GraphValidationWindow - time.Second)
	response := validate(created.Endpoint.ID, token)
	if response.Code != http.StatusOK || response.Body.String() != token ||
		!strings.HasPrefix(response.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("a new endpoint's validation = %d %q %q, want 200 echoing the token as text", response.Code, response.Body, response.Header().Get("Content-Type"))
	}
	if response := validate(created.Endpoint.ID, token); response.Code != http.StatusUnauthorized {
		t.Fatalf("a second validation = %d, want 401: it validates once", response.Code)
	}

	old := h.connectionEndpoint("outlook", "apc_2", "mem_2", graphTrigger)
	h.clock.Advance(GraphValidationWindow)
	if response := validate(old.Endpoint.ID, token); response.Code != http.StatusUnauthorized {
		t.Fatalf("validating an endpoint made %s ago = %d, want 401", GraphValidationWindow, response.Code)
	}
	fresh := h.connectionEndpoint("outlook", "apc_3", "mem_3", graphTrigger)
	if response := validate(fresh.Endpoint.ID, "line\nbreak"); response.Code != http.StatusUnauthorized {
		t.Fatalf("a token that is not printable = %d, want 401", response.Code)
	}
	// An endpoint whose app does not validate this way verifies as usual,
	// and the unsigned request fails.
	github := h.connectionEndpoint("github", "apc_4", "mem_4", githubTrigger)
	if response := validate(github.Endpoint.ID, token); response.Code != http.StatusUnauthorized {
		t.Fatalf("a validation request to a GitHub endpoint = %d, want 401", response.Code)
	}
	if len(h.store.handled) != 0 || h.fakes["outlook"].parsed() != 0 {
		t.Fatal("a validation request was recorded or parsed")
	}
}

// graphParse reads a Graph batch, one event per notification.
func graphParse(_ http.Header, body []byte) ([]integration.Event, error) {
	var batch struct {
		Value []struct {
			ID string `json:"id"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &batch); err != nil {
		return nil, err
	}
	var events []integration.Event
	for _, notification := range batch.Value {
		events = append(events, integration.Event{ID: notification.ID, Trigger: graphTrigger, Data: json.RawMessage(`{"id":"` + notification.ID + `"}`)})
	}
	return events, nil
}

func TestGraphBatchesAreVerifiedPerNotificationAndDedupedPerEvent(t *testing.T) {
	h := newHarness(t)
	h.fakes["outlook"].parse = graphParse
	created := h.connectionEndpoint("outlook", "apc_1", "mem_1", graphTrigger)
	state := created.Hook.Secret
	batch := func(ids ...string) []byte {
		var notifications []string
		for _, id := range ids {
			notifications = append(notifications, `{"id":"`+id+`","clientState":"`+state+`","changeType":"created"}`)
		}
		return []byte(`{"value":[` + strings.Join(notifications, ",") + `]}`)
	}

	for _, body := range [][]byte{batch("n1", "n2"), batch("n2", "n3")} {
		if response := h.deliver(created.Endpoint.ID, nil, body); response.Code != http.StatusOK {
			t.Fatalf("a batch = %d %s", response.Code, response.Body)
		}
	}
	var ids []string
	for _, event := range h.received() {
		ids = append(ids, string(event.Data))
	}
	if strings.Join(ids, " ") != `{"id":"n1"} {"id":"n2"} {"id":"n3"}` {
		t.Fatalf("published %v, want n1, n2 and n3 once each", ids)
	}
	forged := []byte(`{"value":[{"id":"n4","clientState":"` + state + `"},{"id":"n5","clientState":"forged"}]}`)
	if response := h.deliver(created.Endpoint.ID, nil, forged); response.Code != http.StatusUnauthorized {
		t.Fatalf("a batch with one forged notification = %d, want 401 for all of it", response.Code)
	}
}

func TestNotionsVerificationTokenIsCapturedOnceAndShownOnce(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	endpoint, _, err := h.service.DeploymentEndpoint(ctx, "notion")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.RevealVerificationToken(ctx, endpoint.ID); !errors.Is(err, ErrNotCaptured) {
		t.Fatalf("revealing before Notion sent anything = %v, want ErrNotCaptured", err)
	}
	// Before the token arrives, nothing verifies.
	event := []byte(`{"id":"evt-1","type":"page.created","account":"bot_a"}`)
	if response := h.deliver(endpoint.ID, headerOf("X-Notion-Signature", "sha256="+hmacHex("guess", string(event))), event); response.Code != http.StatusUnauthorized {
		t.Fatalf("a signed event before the token = %d, want 401", response.Code)
	}
	// A body that merely mentions a token is not Notion's request.
	if response := h.deliver(endpoint.ID, nil, []byte(`{"verification_token":"secret_abcdefgh","id":"x"}`)); response.Code != http.StatusUnauthorized {
		t.Fatalf("a verification request with more in it = %d, want 401", response.Code)
	}

	const token = "secret_tMrlL1qK5vuQAh1b6cZGhFChZTSYJlce98V0pYn7yBl"
	if response := h.deliver(endpoint.ID, nil, []byte(`{"verification_token":"`+token+`"}`)); response.Code != http.StatusOK {
		t.Fatalf("Notion's verification request = %d %s, want 200", response.Code, response.Body)
	}
	stored := h.store.endpoint(t, endpoint.ID)
	sealed, _ := json.Marshal(stored.Secret.Current)
	if stored.Secret.Current == nil || strings.Contains(string(sealed), token) {
		t.Fatal("the token was not kept, or was kept unsealed")
	}
	h.assertLogsLack(token)
	// It is taken once: a second one, whoever sends it, is refused.
	if response := h.deliver(endpoint.ID, nil, []byte(`{"verification_token":"secret_attackerAttackerAttacker"}`)); response.Code != http.StatusUnauthorized {
		t.Fatalf("a second verification request = %d, want 401", response.Code)
	}

	// Events signed with it are verified and routed by the bot's own id.
	h.subscribeAccount(Subscription{
		TriggerID: "apt_n", ConnectionID: "apc_n", OrganizationID: testOrg, MemberID: "mem_n",
		Toolkit: "notion", Trigger: notionTrigger, AccountRef: "bot_a",
	})
	if response := h.deliver(endpoint.ID, headerOf("X-Notion-Signature", "sha256="+hmacHex(token, string(event))), event); response.Code != http.StatusOK {
		t.Fatalf("an event signed with the token = %d %s, want 200", response.Code, response.Body)
	}
	if received := h.received(); len(received) != 1 || received[0].MemberID != "mem_n" {
		t.Fatalf("published %+v, want the bot's member's event", received)
	}

	revealed, err := h.service.RevealVerificationToken(ctx, endpoint.ID)
	if err != nil || revealed.Reveal() != token {
		t.Fatalf("revealing = %v, want the token", err)
	}
	if fmt.Sprint(revealed) == token {
		t.Fatal("the revealed token prints itself")
	}
	if _, err := h.service.RevealVerificationToken(ctx, endpoint.ID); !errors.Is(err, ErrAlreadyRevealed) {
		t.Fatalf("revealing twice = %v, want ErrAlreadyRevealed", err)
	}
	if _, err := h.service.Rotate(ctx, endpoint.ID, ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("rotating Notion's token = %v, want invalid", err)
	}
	github := h.connectionEndpoint("github", "apc_1", "mem_1", githubTrigger)
	if _, err := h.service.RevealVerificationToken(ctx, github.Endpoint.ID); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("revealing a GitHub endpoint's secret = %v, want invalid", err)
	}
}

func TestRoutingReachesOnlyTheMemberAnEventBelongsTo(t *testing.T) {
	memberA := Subscription{TriggerID: "apt_a", ConnectionID: "apc_a", OrganizationID: testOrg, MemberID: "mem_a", Toolkit: "slack", Trigger: slackTrigger, AccountRef: "U_A"}
	// The same Slack user, connected in another organization too.
	memberAElsewhere := Subscription{TriggerID: "apt_a2", ConnectionID: "apc_a2", OrganizationID: "org_2", MemberID: "mem_a2", Toolkit: "slack", Trigger: slackTrigger, AccountRef: "U_A"}
	memberB := Subscription{TriggerID: "apt_b", ConnectionID: "apc_b", OrganizationID: testOrg, MemberID: "mem_b", Toolkit: "slack", Trigger: slackTrigger, AccountRef: "U_B"}
	// What a wrong Subscriptions might offer: another member's, another
	// app's, another trigger's, and one with no member.
	rogue := []Subscription{
		memberB,
		{TriggerID: "apt_x", ConnectionID: "apc_x", OrganizationID: testOrg, MemberID: "mem_x", Toolkit: "github", Trigger: slackTrigger, AccountRef: "U_A"},
		{TriggerID: "apt_y", ConnectionID: "apc_y", OrganizationID: testOrg, MemberID: "mem_y", Toolkit: "slack", Trigger: "SLACK_OTHER", AccountRef: "U_A"},
		{TriggerID: "apt_z", ConnectionID: "apc_z", OrganizationID: testOrg, Toolkit: "slack", Trigger: slackTrigger, AccountRef: "U_A"},
	}
	members := func(h *harness) []string {
		var ids []string
		for _, event := range h.received() {
			ids = append(ids, event.MemberID)
		}
		return ids
	}
	const secret = "slack-signing-secret-for-routing"
	send := func(h *harness, endpointID, body string) {
		t.Helper()
		if response := h.deliver(endpointID, slackDelivery(secret, testStart, []byte(body)), []byte(body)); response.Code != http.StatusOK {
			t.Fatalf("delivery = %d %s", response.Code, response.Body)
		}
	}

	t.Run("the deployment's endpoint, by account", func(t *testing.T) {
		h := newHarness(t, withDeploymentSecret("slack", secret))
		for _, subscription := range []Subscription{memberA, memberAElsewhere, memberB} {
			h.subscribeAccount(subscription)
		}
		h.subscriptions.rogue = rogue
		endpoint, _, err := h.service.DeploymentEndpoint(context.Background(), "slack")
		if err != nil {
			t.Fatal(err)
		}
		send(h, endpoint.ID, `{"id":"Ev1","account":"U_A"}`)
		if got := strings.Join(members(h), ","); got != "mem_a,mem_a2" {
			t.Fatalf("U_A's event reached %s, want only U_A's own connections: mem_a,mem_a2", got)
		}
		send(h, endpoint.ID, `{"id":"Ev2","account":"U_NOBODY"}`)
		send(h, endpoint.ID, `{"id":"Ev3"}`)
		if got := len(h.received()); got != 2 {
			t.Fatalf("events for nobody's account, or none, published %d more", got-2)
		}
		if !strings.Contains(h.logs.String(), "outside a delivery's reach") {
			t.Fatal("dropping another member's subscription was not logged")
		}
	})

	t.Run("an organization's own app, by account inside the organization", func(t *testing.T) {
		h := newHarness(t)
		for _, subscription := range []Subscription{memberA, memberAElsewhere, memberB} {
			h.subscribeAccount(subscription)
		}
		// A wrong Subscriptions offering the same account in another
		// organization is caught too.
		h.subscriptions.rogue = append([]Subscription{memberAElsewhere}, rogue...)
		created := h.create(CreateInput{Toolkit: "slack", Scope: integration.HookScopeOrganization, OrganizationID: testOrg, Secret: secret})
		send(h, created.Endpoint.ID, `{"id":"Ev1","account":"U_A"}`)
		if got := strings.Join(members(h), ","); got != "mem_a" {
			t.Fatalf("U_A's event on org_1's own app reached %s, want only mem_a", got)
		}
	})

	t.Run("a connection's endpoint, by connection", func(t *testing.T) {
		h := newHarness(t)
		created := h.connectionEndpoint("github", "apc_a", "mem_a", githubTrigger)
		h.subscriptions.rogue = []Subscription{
			{TriggerID: "apt_b", ConnectionID: "apc_b", OrganizationID: testOrg, MemberID: "mem_b", Toolkit: "github", Trigger: githubTrigger},
			{TriggerID: "apt_c", ConnectionID: "apc_a", OrganizationID: "org_2", MemberID: "mem_c", Toolkit: "github", Trigger: githubTrigger},
		}
		// An account ref in the event does not widen a connection endpoint's
		// reach.
		body := []byte(`{"id":"issue-1","account":"someone-else"}`)
		if response := h.deliver(created.Endpoint.ID, githubDelivery(created.Hook.Secret, "d-1", "issues", body), body); response.Code != http.StatusOK {
			t.Fatalf("delivery = %d", response.Code)
		}
		if got := strings.Join(members(h), ","); got != "mem_a" {
			t.Fatalf("the connection's event reached %s, want only mem_a", got)
		}
	})
}

func TestParsedEventsTheManifestDoesNotPushAreDropped(t *testing.T) {
	h := newHarness(t)
	h.fakes["github"].parse = func(http.Header, []byte) ([]integration.Event, error) {
		return []integration.Event{
			{ID: "a", Trigger: "GITHUB_NOT_DECLARED", Data: json.RawMessage(`{}`)},
			{ID: "b", Trigger: githubTrigger, Data: json.RawMessage(`not json`)},
			{ID: "c", Trigger: githubTrigger},
			{ID: "d", Trigger: githubTrigger, Data: json.RawMessage(`{"ok":true}`)},
		}, nil
	}
	created := h.connectionEndpoint("github", "apc_1", "mem_1", githubTrigger)
	body := []byte(`{}`)
	if response := h.deliver(created.Endpoint.ID, githubDelivery(created.Hook.Secret, "d-1", "issues", body), body); response.Code != http.StatusOK {
		t.Fatalf("delivery = %d", response.Code)
	}
	if received := h.received(); len(received) != 1 || string(received[0].Data) != `{"ok":true}` {
		t.Fatalf("published %+v, want only the declared trigger's valid event", received)
	}
}

func TestAParseFailureIsRefusedAndLeftForTheRetry(t *testing.T) {
	h := newHarness(t)
	h.fakes["github"].parse = func(http.Header, []byte) ([]integration.Event, error) {
		return nil, errors.New("unknown event shape")
	}
	created := h.connectionEndpoint("github", "apc_1", "mem_1", githubTrigger)
	body := []byte(`{"id":"issue-1"}`)
	header := githubDelivery(created.Hook.Secret, "d-1", "issues", body)
	if response := h.deliver(created.Endpoint.ID, header, body); response.Code != http.StatusBadRequest {
		t.Fatalf("a delivery the integration cannot parse = %d, want 400", response.Code)
	}
	if len(h.store.handled) != 0 {
		t.Fatal("an unparsed delivery was recorded, so its retry would be dropped")
	}
	h.fakes["github"].parse = nil
	if response := h.deliver(created.Endpoint.ID, header, body); response.Code != http.StatusOK || len(h.received()) != 1 {
		t.Fatalf("the retry once the integration reads it = %d, %d events; want 200 and one", response.Code, len(h.received()))
	}
}

func TestAToolkitWithoutAPusherIsAcknowledged(t *testing.T) {
	const secret = "zendesk-deployment-secret-0000"
	h := newHarness(t, withDeploymentSecret("zendesk", secret))
	endpoint, _, err := h.service.DeploymentEndpoint(context.Background(), "zendesk")
	if err != nil {
		t.Fatal(err)
	}
	body := `{"id":"t-1"}`
	timestamp := fmt.Sprint(testStart.Unix())
	header := headerOf("webhook-id", "msg_1", "webhook-timestamp", timestamp,
		"webhook-signature", "v1,"+hmacBase64(secret, "msg_1", ".", timestamp, ".", body))
	if response := h.deliver(endpoint.ID, header, []byte(body)); response.Code != http.StatusOK {
		t.Fatalf("a verified delivery nothing parses yet = %d, want 200", response.Code)
	}
	if _, ok := h.store.handled["hook:"+endpoint.ID+":msg_1"]; !ok || len(h.received()) != 0 {
		t.Fatalf("recorded %v, published %d; want it recorded and nothing published", h.store.handled, len(h.received()))
	}
	if !strings.Contains(h.logs.String(), "no integration parses this toolkit's deliveries yet") {
		t.Fatal("dropping a delivery nothing parses was not logged")
	}
}

func TestFailuresOnOryxasSideAskTheAppToRetry(t *testing.T) {
	h := newHarness(t)
	created := h.connectionEndpoint("github", "apc_1", "mem_1", githubTrigger)
	body := []byte(`{"id":"issue-1"}`)
	header := githubDelivery(created.Hook.Secret, "d-1", "issues", body)

	h.store.failNext = errors.New("connection reset")
	if response := h.deliver(created.Endpoint.ID, header, body); response.Code != http.StatusInternalServerError {
		t.Fatalf("the store failing to find the endpoint = %d, want 500", response.Code)
	}
	h.subscriptions.err = errors.New("apps' store is down")
	if response := h.deliver(created.Endpoint.ID, header, body); response.Code != http.StatusInternalServerError {
		t.Fatalf("Subscriptions failing = %d, want 500", response.Code)
	}
	h.subscriptions.err = nil
	if len(h.store.handled) != 0 {
		t.Fatal("a delivery that failed was recorded")
	}
	if response := h.deliver(created.Endpoint.ID, header, body); response.Code != http.StatusOK || len(h.received()) != 1 {
		t.Fatalf("the retry = %d, %d events", response.Code, len(h.received()))
	}
}

func TestALinearDeliveryEndToEnd(t *testing.T) {
	h := newHarness(t)
	created := h.connectionEndpoint("linear", "apc_1", "mem_1", linearTrigger)
	body := []byte(fmt.Sprintf(`{"action":"create","type":"Issue","id":"issue-1","webhookTimestamp":%d}`, testStart.UnixMilli()))
	header := headerOf("Linear-Signature", hmacHex(created.Hook.Secret, string(body)), "Linear-Delivery", "ld-1")
	h.clock.Advance(59 * time.Second)
	if response := h.deliver(created.Endpoint.ID, header, body); response.Code != http.StatusOK {
		t.Fatalf("a Linear delivery 59s after it was sent = %d %s", response.Code, response.Body)
	}
	h.clock.Advance(2 * time.Second)
	header.Set("Linear-Delivery", "ld-2")
	if response := h.deliver(created.Endpoint.ID, header, body); response.Code != http.StatusUnauthorized {
		t.Fatalf("the same delivery 61s after it was sent = %d, want 401", response.Code)
	}
}
