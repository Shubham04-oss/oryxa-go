package hooks

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
)

// fakeGoogle answers Google's discovery document and keys from memory, at
// Google's own URLs, so the real Gmail manifest's issuer is verified through
// go-oidc exactly as it would be against Google.
type fakeGoogle struct {
	key *rsa.PrivateKey

	mu          sync.Mutex
	discoveries int
	down        bool
}

const (
	googleIssuer   = "https://accounts.google.com"
	googleKeysURL  = "https://www.googleapis.com/oauth2/v3/certs"
	pushAccount    = "gmail-push@oryxa-test.iam.gserviceaccount.com"
	googleKeyID    = "test-key-1"
	mailboxAddress = "member@example.com"
)

func newFakeGoogle(t *testing.T) *fakeGoogle {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeGoogle{key: key}
}

func (g *fakeGoogle) RoundTrip(request *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	g.mu.Lock()
	down := g.down
	g.mu.Unlock()
	switch url := request.URL.String(); {
	case down:
		recorder.WriteHeader(http.StatusServiceUnavailable)
	case url == googleIssuer+"/.well-known/openid-configuration":
		g.mu.Lock()
		g.discoveries++
		g.mu.Unlock()
		recorder.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(recorder).Encode(map[string]any{
			"issuer": googleIssuer, "jwks_uri": googleKeysURL,
			"authorization_endpoint": googleIssuer + "/o/oauth2/v2/auth", "token_endpoint": "https://oauth2.googleapis.com/token",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	case url == googleKeysURL:
		recorder.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(recorder).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": googleKeyID, "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(g.key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(g.key.E)).Bytes()),
		}}})
	default:
		recorder.WriteHeader(http.StatusNotFound)
	}
	return recorder.Result(), nil
}

func (g *fakeGoogle) discovered() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.discoveries
}

// signJWT signs claims as an RS256 JWT, written out apart from go-oidc so the
// test does not check go-oidc against itself.
func signJWT(t *testing.T, key *rsa.PrivateKey, keyID string, claims map[string]any) string {
	t.Helper()
	encode := func(value any) string {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(encoded)
	}
	signingInput := encode(map[string]string{"alg": "RS256", "kid": keyID, "typ": "JWT"}) + "." + encode(claims)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// pushClaims are what Pub/Sub's push token says, for an endpoint's URL.
func pushClaims(audience string, now time.Time) map[string]any {
	return map[string]any{
		"iss": googleIssuer, "aud": audience, "azp": "113774264463038321964",
		"email": pushAccount, "email_verified": true, "sub": "113774264463038321964",
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	}
}

// pushBody is a Pub/Sub push of Gmail's notification for a mailbox.
func pushBody(messageID, address string) []byte {
	data := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf(`{"emailAddress":%q,"historyId":9876543}`, address)))
	return []byte(fmt.Sprintf(`{"message":{"data":%q,"messageId":%q,"publishTime":"2026-09-28T12:00:00Z"},"subscription":"projects/oryxa-test/subscriptions/gmail-push"}`, data, messageID))
}

// parsePush reads a Pub/Sub push of Gmail's notification, as the gmail
// integration will.
func parsePush(_ http.Header, body []byte) ([]integration.Event, error) {
	var push struct {
		Message struct {
			Data      []byte `json:"data"`
			MessageID string `json:"messageId"`
		} `json:"message"`
	}
	if err := json.Unmarshal(body, &push); err != nil {
		return nil, err
	}
	var notification struct {
		EmailAddress string `json:"emailAddress"`
		HistoryID    int64  `json:"historyId"`
	}
	if err := json.Unmarshal(push.Message.Data, &notification); err != nil {
		return nil, err
	}
	data, _ := json.Marshal(map[string]int64{"history_id": notification.HistoryID})
	return []integration.Event{{
		ID: push.Message.MessageID, Trigger: gmailTrigger, AccountRef: notification.EmailAddress, Data: data,
	}}, nil
}

// gmailHarness is hooks with Gmail's deployment endpoint, its pushes signed
// by fakeGoogle, and one member's mailbox subscribed.
func gmailHarness(t *testing.T, google *fakeGoogle, values map[string]string) (*harness, Endpoint, string) {
	t.Helper()
	var h *harness
	h = newHarness(t, func(o *harnessOptions) {
		o.config.Values = values
		o.tokens = NewOIDCVerifier(&http.Client{Transport: google}, func() time.Time { return h.clock.Now() })
	})
	h.fakes["gmail"].parse = parsePush
	endpoint, url, err := h.service.DeploymentEndpoint(context.Background(), "gmail")
	if err != nil {
		t.Fatal(err)
	}
	h.subscribeAccount(Subscription{
		TriggerID: "apt_mail", ConnectionID: "apc_mail", OrganizationID: testOrg, MemberID: "mem_mail",
		Toolkit: "gmail", Trigger: gmailTrigger, AccountRef: mailboxAddress,
	})
	return h, endpoint, url
}

func bearerHeader(token string) http.Header {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	header.Set("Content-Type", "application/json")
	return header
}

func TestJWTPushVerifiesGooglesTokenForTheEndpointAndAccount(t *testing.T) {
	google := newFakeGoogle(t)
	h, endpoint, url := gmailHarness(t, google, map[string]string{"pubsub_push_account": pushAccount})
	if want := testBaseURL + PathPrefix + endpoint.ID; url != want {
		t.Fatalf("the deployment endpoint's URL = %q, want %q", url, want)
	}

	token := signJWT(t, google.key, googleKeyID, pushClaims(url, testStart))
	if response := h.deliver(endpoint.ID, bearerHeader(token), pushBody("136969346945", mailboxAddress)); response.Code != http.StatusOK {
		t.Fatalf("a genuine push = %d %s, want 200", response.Code, response.Body)
	}
	received := h.received()
	if len(received) != 1 || received[0].MemberID != "mem_mail" || received[0].TriggerID != "apt_mail" ||
		string(received[0].Data) != `{"history_id":9876543}` {
		t.Fatalf("published %+v, want the mailbox owner's one event", received)
	}
	// The integration never sees the delivery's credential.
	for _, header := range h.fakes["gmail"].headers {
		if header.Get("Authorization") != "" {
			t.Fatal("Parse was handed the Authorization header")
		}
	}

	// Google names itself without a scheme in some tokens; go-oidc accepts
	// that for Google alone.
	bare := pushClaims(url, testStart)
	bare["iss"] = "accounts.google.com"
	if response := h.deliver(endpoint.ID, bearerHeader(signJWT(t, google.key, googleKeyID, bare)), pushBody("136969346946", mailboxAddress)); response.Code != http.StatusOK {
		t.Fatalf("a push whose issuer has no scheme = %d, want 200", response.Code)
	}
	if google.discovered() != 1 {
		t.Fatalf("discovered the issuer %d times, want once", google.discovered())
	}

	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	refusedTokens := map[string]string{}
	add := func(name string, change func(map[string]any)) {
		claims := pushClaims(url, testStart)
		change(claims)
		refusedTokens[name] = signJWT(t, google.key, googleKeyID, claims)
	}
	add("another issuer", func(c map[string]any) { c["iss"] = "https://evil.example" })
	add("another audience", func(c map[string]any) { c["aud"] = testBaseURL + PathPrefix + "aph_aaaaaaaaaaaaaaaaaaaaaaaa" })
	add("the audience of another deployment", func(c map[string]any) { c["aud"] = "https://api.elsewhere.test" + PathPrefix + endpoint.ID })
	add("another account", func(c map[string]any) { c["email"] = "attacker@evil-project.iam.gserviceaccount.com" })
	add("an unverified email", func(c map[string]any) { c["email_verified"] = false })
	add("no email", func(c map[string]any) { delete(c, "email") })
	add("an expired token", func(c map[string]any) { c["exp"] = testStart.Add(-time.Minute).Unix() })
	refusedTokens["a token signed by another key"] = signJWT(t, other, googleKeyID, pushClaims(url, testStart))
	refusedTokens["a token naming a key Google never published"] = signJWT(t, other, "test-key-2", pushClaims(url, testStart))
	unsigned := strings.Split(signJWT(t, google.key, googleKeyID, pushClaims(url, testStart)), ".")
	refusedTokens["a token with its signature cut off"] = unsigned[0] + "." + unsigned[1] + "."
	none := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	refusedTokens["an alg none token"] = none + "." + unsigned[1] + "."

	for name, token := range refusedTokens {
		response := h.deliver(endpoint.ID, bearerHeader(token), pushBody("forged-"+name, mailboxAddress))
		if response.Code != http.StatusUnauthorized {
			t.Errorf("%s = %d, want 401", name, response.Code)
		}
		h.assertLogsLack(token)
	}
	for name, header := range map[string]http.Header{
		"no Authorization":      {},
		"a Basic credential":    {"Authorization": []string{"Basic dXNlcjpwYXNz"}},
		"two bearer tokens":     {"Authorization": []string{"Bearer " + token, "Bearer " + token}},
		"a bearer with nothing": {"Authorization": []string{"Bearer "}},
	} {
		if response := h.deliver(endpoint.ID, header, pushBody("m-"+name, mailboxAddress)); response.Code != http.StatusUnauthorized {
			t.Errorf("%s = %d, want 401", name, response.Code)
		}
	}
	if got := len(h.received()); got != 2 {
		t.Fatalf("published %d events, want only the two genuine pushes", got)
	}
}

func TestJWTPushWithoutItsConfiguredAccountAsksTheAppToRetry(t *testing.T) {
	google := newFakeGoogle(t)
	h, endpoint, url := gmailHarness(t, google, nil)
	token := signJWT(t, google.key, googleKeyID, pushClaims(url, testStart))
	response := h.deliver(endpoint.ID, bearerHeader(token), pushBody("1", mailboxAddress))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("a push with pubsub_push_account unset = %d, want 503 so Pub/Sub retries", response.Code)
	}
	if len(h.received()) != 0 {
		t.Fatal("published an event nothing verified")
	}
}

func TestJWTPushRetriesDiscoveryUntilTheIssuerAnswers(t *testing.T) {
	google := newFakeGoogle(t)
	h, endpoint, url := gmailHarness(t, google, map[string]string{"pubsub_push_account": pushAccount})
	google.mu.Lock()
	google.down = true
	google.mu.Unlock()
	token := signJWT(t, google.key, googleKeyID, pushClaims(url, testStart))
	if response := h.deliver(endpoint.ID, bearerHeader(token), pushBody("1", mailboxAddress)); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("a push while Google's keys cannot be fetched = %d, want 503", response.Code)
	}
	google.mu.Lock()
	google.down = false
	google.mu.Unlock()
	if response := h.deliver(endpoint.ID, bearerHeader(token), pushBody("1", mailboxAddress)); response.Code != http.StatusOK {
		t.Fatalf("the same push once Google answers = %d %s, want 200", response.Code, response.Body)
	}
}
