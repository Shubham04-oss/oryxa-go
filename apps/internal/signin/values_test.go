package signin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

func TestFormats(t *testing.T) {
	t.Parallel()
	cases := []struct {
		format string
		good   []string
		bad    []string
	}{
		{integration.FormatHostname,
			[]string{"acme.zendesk.com", "a-b.example.io", "x1.example"},
			[]string{"localhost", "10.0.0.1", "1.2.3.4", "-acme.example.com", "acme..com", "acme.example.com/path", "acme.123", "a_b.example.com", ""}},
		{integration.FormatHTTPSURL,
			[]string{"https://acme.example.com", "https://acme.example.com:8443/api", "https://acme.example.com/"},
			[]string{"http://acme.example.com", "https://127.0.0.1", "https://localhost", "https://user:pass@acme.example.com",
				"https://acme.example.com/?q=1", "https://acme.example.com/#x", "https:acme.example.com", "ftp://acme.example.com"}},
		{integration.FormatUUID,
			[]string{"123e4567-e89b-12d3-a456-426614174000"},
			[]string{"123e4567e89b12d3a456426614174000", "123e4567-e89b-12d3-a456-42661417400g"}},
		{integration.FormatEmail,
			[]string{"ada@acme.example"},
			[]string{"ada", "ada@localhost", "a da@acme.example", "Ada <ada@acme.example>", "@acme.example"}},
	}
	for _, tc := range cases {
		for _, value := range tc.good {
			if !validFormat(tc.format, value) {
				t.Errorf("%s refused %q", tc.format, value)
			}
		}
		for _, value := range tc.bad {
			if validFormat(tc.format, value) {
				t.Errorf("%s accepted %q", tc.format, value)
			}
		}
	}
}

func TestFillEscapesEachPlaceholderForWhereItStands(t *testing.T) {
	t.Parallel()
	values := map[string]string{"instance": "https://acme.example.com/", "team": "a/b c", "q": "x&y=z"}
	got, err := fill("{instance}/teams/{team}/search?q={q}", values, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://acme.example.com/teams/a%2Fb%20c/search?q=x%26y%3Dz"; got != want {
		t.Fatalf("fill = %q, want %q", got, want)
	}
	if _, err := fill("/teams/{missing}", values, false); !errors.Is(err, errMissingValue) {
		t.Fatalf("a placeholder with no value = %v", err)
	}
	// Only a leading placeholder stands for a whole URL.
	if got, _ := fill("/x/{instance}", values, true); got != "/x/https:%2F%2Facme.example.com%2F" {
		t.Fatalf("a placeholder that does not lead = %q", got)
	}
	if _, err := fillURL("{instance}", map[string]string{"instance": "javascript:alert(1)"}); err == nil {
		t.Fatal("fillURL accepted a URL that is not https")
	}
}

func TestPointer(t *testing.T) {
	t.Parallel()
	var document any
	if err := json.Unmarshal([]byte(`{"a":{"b/c":[{"d~e":"found"}]},"n":12345678901234567890,"t":true,"z":""}`), &document); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{"/a/b~1c/0/d~0e": "found", "/t": "true", "/n": "12345678901234567000"} {
		got, ok := scalar(pointer(document, path))
		if !ok || got != want {
			t.Errorf("%s = %q, %v; want %q", path, got, ok, want)
		}
	}
	for _, missing := range []string{"/a/b~1c/1", "/a/b~1c/01", "/a/x", "/z", "a", "/a"} {
		if got, ok := scalar(pointer(document, missing)); ok {
			t.Errorf("%s resolved to %q", missing, got)
		}
	}
}

func TestHint(t *testing.T) {
	t.Parallel()
	for secret, want := range map[secrets.Secret]string{
		"0123456789abcdef": "cdef", "0123456789abcde": "", "": "", "ééééééééééééééé1234": "1234",
	} {
		if got := hint(secret); got != want {
			t.Errorf("hint(%d characters) = %q, want %q", len(secret), got, want)
		}
	}
}

func TestNewRefusesWhatWouldBeUnsafe(t *testing.T) {
	t.Parallel()
	registry := integration.NewRegistry()
	store := newMemStore()
	good := Dependencies{Store: store, Sealer: secrets.Envelope{Keys: &fakeKeys{}, Service: SealService}, Integrations: registry}
	if _, err := New(good, Config{RedirectURI: testRedirectURI}); err != nil {
		t.Fatalf("New() = %v", err)
	}
	wrongService := good
	wrongService.Sealer = &secrets.Envelope{Keys: &fakeKeys{}, Service: "oryxa-auth"}
	if _, err := New(wrongService, Config{RedirectURI: testRedirectURI}); err == nil {
		t.Fatal("New() accepted an envelope bound to another service")
	}
	for _, redirect := range []string{"", "http://api.oryxa.example/cb", "https://api.oryxa.example/cb?x=1", "https://u:p@api.oryxa.example/cb", "/v1/apps/oauth/callback"} {
		if _, err := New(good, Config{RedirectURI: redirect}); err == nil {
			t.Errorf("New() accepted the redirect URI %q", redirect)
		}
	}
	if _, err := New(good, Config{RedirectURI: "http://localhost:8080/v1/apps/oauth/callback"}); err != nil {
		t.Errorf("New() refused a loopback redirect for local development: %v", err)
	}
	if _, err := New(Dependencies{Store: store}, Config{RedirectURI: testRedirectURI}); err == nil {
		t.Fatal("New() accepted missing dependencies")
	}
}

// TestSecretsReachNoLogAndNoError runs sign-ins, refreshes, refusals,
// revocations and checks, in success and in failure, and looks for any
// secret in what was logged and in every error answered.
func TestSecretsReachNoLogAndNoError(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.app.set(func(a *stubApp) {
		a.apiToken, a.username, a.password = testToken, "ada@acme.example", "correct horse battery"
	})
	var errs []error
	note := func(_ any, err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	h.connect("con_1", "acme")
	h.untilDue("con_1")
	session, err := h.service.Session(context.Background(), "con_1")
	note(session, err)
	note(get(t, session, h.app.base+"/api/data"))
	// The operator's client refused, then the member's grant refused.
	h.app.set(func(a *stubApp) { a.clients[defaultClientID] = "rotated-at-the-app-0000001" })
	h.untilDue("con_1")
	h.clock.Advance(3 * time.Minute)
	note(h.service.Session(context.Background(), "con_1"))
	h.app.set(func(a *stubApp) { a.clients[defaultClientID] = defaultClientSecret; a.refuseRefresh = true })
	note(h.service.Session(context.Background(), "con_1"))
	note(h.service.CheckDue(context.Background(), 10))
	// A sign-in whose test call is refused, and one the app refuses.
	h.app.set(func(a *stubApp) { a.refuseRefresh = false; a.meStatus = http.StatusUnauthorized })
	started := h.start("con_2", "acme")
	code, state := h.app.authorize(t, started.AuthorizationURL)
	note(h.service.CompleteOAuth(context.Background(), Callback{State: state, CookieState: started.State, Code: code}))
	h.app.set(func(a *stubApp) { a.meStatus = 0 })
	started = h.start("con_3", "acme")
	note(h.service.CompleteOAuth(context.Background(), Callback{State: started.State, CookieState: started.State, Code: "forged"}))
	// Keys and passwords, refused and kept.
	h.store.addConnection("con_4", testOrganization, testMember, "acme", "pending")
	note(h.saveToken("con_4", "pat-refused-0123456789"))
	note(h.saveToken("con_4", testToken))
	h.store.addConnection("con_5", testOrganization, testMember, "acme", "pending")
	note(h.service.SaveCredential(context.Background(), CredentialRequest{
		OrganizationID: testOrganization, ConnectionID: "con_5", Toolkit: "acme", Kind: integration.KindBasic,
		Fields: map[string]string{"username": "ada@acme.example", "password": "wrong horse battery"},
	}))
	// A revocation the app does not answer, then one it does.
	h.app.set(func(a *stubApp) { a.revokeStatus["/oauth/revoke"] = http.StatusInternalServerError })
	h.connect("con_6", "acme")
	note(h.service.Revoke(context.Background(), "con_6"))
	h.app.set(func(a *stubApp) { delete(a.revokeStatus, "/oauth/revoke") })
	note(h.service.Revoke(context.Background(), "con_6"))
	view, _, err := h.service.PutClient(context.Background(), ClientInput{
		OrganizationID: testOrganization, Client: "acme", ClientID: "org-acme", ClientSecret: "org-client-secret-never-shown",
	})
	note(nil, err)
	shown, _ := json.Marshal(view)
	h.logs.Write(shown)

	if len(errs) < 6 {
		t.Fatalf("only %d errors were answered; the failures did not happen", len(errs))
	}
	var everything strings.Builder
	everything.WriteString(h.logs.String())
	for _, err := range errs {
		everything.WriteString(err.Error() + "\n")
	}
	secretPatterns := regexp.MustCompile(`secret-access|secret-refresh|` + regexp.QuoteMeta(defaultClientSecret) +
		`|rotated-at-the-app|` + regexp.QuoteMeta(testToken) + `|pat-refused|horse battery|org-client-secret`)
	if found := secretPatterns.FindString(everything.String()); found != "" {
		t.Fatalf("a secret (%s…) reached a log line or an error:\n%s", found[:4], everything.String())
	}
	if !strings.Contains(h.logs.String(), "level=") {
		t.Fatal("nothing was logged, so nothing was checked")
	}
}
