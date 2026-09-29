package httpserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/guardpolicy"
)

// These tests drive the answer guard's settings handlers through the real
// Server.member, as they are registered in production, on a mux of their own:
// the routes themselves are added to the Server's mux where it is built, and
// registering them here too would collide with that.
const (
	guardGetPattern = "GET /v1/organizations/{organizationID}/answer-guard/preferences"
	guardPutPattern = "PUT /v1/organizations/{organizationID}/answer-guard/preferences"
	guardPath       = "/v1/organizations/org_1/answer-guard/preferences"
)

// fakeOrgPolicies is an OrgPolicies that says what it is told of each
// organization, by id, and nothing of any other.
type fakeOrgPolicies map[string]guardpolicy.OrgPolicy

func (orgs fakeOrgPolicies) Get(_ context.Context, organizationID string) (guardpolicy.OrgPolicy, error) {
	return orgs[organizationID], nil
}

// fakeAnswerGuardPreferences keeps a member's choices in a map, and works out
// what they are told, and what applies to them, from what its organizations'
// policies say with guardpolicy's own rule, so that what is asserted here is
// what a member would be shown, and read at.
type fakeAnswerGuardPreferences struct {
	// orgs says what each organization's policy asks; nil says nothing of any.
	orgs    guardpolicy.OrgPolicies
	ceiling guardpolicy.Cap
	chosen  map[[2]string]guardpolicy.Choices
	// err is what every method fails with. Effective then answers the
	// strictest reading beside it, as the service does.
	err error
	// calls records, in call order, the (organizationID, userID) pair every
	// method was invoked with, so an authorization test can assert the
	// handler always read or wrote the authenticated caller's own settings,
	// never an identity taken from the request path, query, headers or body.
	calls [][2]string
	// changes records what SetPreferences was asked for, as the handler gave it.
	changes []guardChangeAsked
}

type guardChangeAsked struct {
	owner, peer *guardpolicy.Level
}

// policy is what the organization's policy asks: nothing when there is no
// source for it, or it fails, as the service reads one.
func (fake *fakeAnswerGuardPreferences) policy(organizationID string) guardpolicy.OrgPolicy {
	if fake.orgs == nil {
		return guardpolicy.OrgPolicy{}
	}
	policy, err := fake.orgs.Get(context.Background(), organizationID)
	if err != nil {
		return guardpolicy.OrgPolicy{}
	}
	return policy
}

func (fake *fakeAnswerGuardPreferences) view(organizationID, userID string) guardpolicy.View {
	return guardpolicy.Resolve(organizationID, fake.policy(organizationID), fake.chosen[[2]string{organizationID, userID}], fake.ceiling)
}

func (fake *fakeAnswerGuardPreferences) Effective(_ context.Context, organizationID, userID string) (guardpolicy.Effective, error) {
	fake.calls = append(fake.calls, [2]string{organizationID, userID})
	if fake.err != nil {
		strictest := guardpolicy.Choices{Owner: guardpolicy.LevelStrict, Peer: guardpolicy.LevelStrict}
		return guardpolicy.Resolve(organizationID, fake.policy(organizationID), strictest, fake.ceiling).Effective(), fake.err
	}
	return fake.view(organizationID, userID).Effective(), nil
}

func (fake *fakeAnswerGuardPreferences) Preferences(_ context.Context, organizationID, userID string) (guardpolicy.View, error) {
	fake.calls = append(fake.calls, [2]string{organizationID, userID})
	if fake.err != nil {
		return guardpolicy.View{}, fake.err
	}
	return fake.view(organizationID, userID), nil
}

func (fake *fakeAnswerGuardPreferences) SetPreferences(_ context.Context, organizationID, userID string, owner, peer *guardpolicy.Level) (guardpolicy.View, error) {
	fake.calls = append(fake.calls, [2]string{organizationID, userID})
	fake.changes = append(fake.changes, guardChangeAsked{owner: owner, peer: peer})
	if fake.err != nil {
		return guardpolicy.View{}, fake.err
	}
	if fake.chosen == nil {
		fake.chosen = map[[2]string]guardpolicy.Choices{}
	}
	key := [2]string{organizationID, userID}
	chosen := fake.chosen[key]
	if owner != nil {
		chosen.Owner = *owner
	}
	if peer != nil {
		chosen.Peer = *peer
	}
	fake.chosen[key] = chosen
	return fake.view(organizationID, userID), nil
}

// guardServer wires the handlers to a real Server's member, and answers the
// directory it asks.
func guardServer(settings AnswerGuardPreferences, logger *slog.Logger) (http.Handler, *fakeDirectory) {
	directory := member()
	server := New(directory, &fakeThreads{}, Config{AllowedOrigins: []string{"https://app.oryxa.test"}}, nil)
	handlers := newGuardPreferenceHandlers(settings, server.member, logger)
	mux := http.NewServeMux()
	mux.HandleFunc(guardGetPattern, handlers.get)
	mux.HandleFunc(guardPutPattern, handlers.put)
	return mux, directory
}

func serveGuard(handler http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	for name, value := range headers {
		if name == "Cookie" {
			request.AddCookie(&http.Cookie{Name: authclient.AccessCookieName, Value: value})
			continue
		}
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func compact(response *httptest.ResponseRecorder) string {
	return strings.TrimSpace(response.Body.String())
}

func TestAMemberReadsTheirOwnAnswerGuardSettings(t *testing.T) {
	settings := &fakeAnswerGuardPreferences{}
	handler, _ := guardServer(settings, nil)

	response := serveGuard(handler, http.MethodGet, guardPath, "", bearer)
	const defaults = `{"owner":{"chosen":null,"default":"secrets","floor":"secrets","effective":"secrets"},` +
		`"peer":{"chosen":null,"default":"strict","floor":"secrets","effective":"strict"},"relevance":"hold","regulated":false}`
	if response.Code != http.StatusOK || compact(response) != defaults {
		t.Fatalf("nothing chosen = %d %s\nwant     %s", response.Code, response.Body.String(), defaults)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}

	// What the organization asks, and what the member chose, are both shown.
	settings.orgs = fakeOrgPolicies{"org_1": {MinimumPeer: guardpolicy.LevelStrict, Relevance: guardpolicy.RelevanceShadow, Regulated: true}}
	settings.chosen = map[[2]string]guardpolicy.Choices{{"org_1", "user_1"}: {Owner: guardpolicy.LevelStrict}}
	const chosen = `{"owner":{"chosen":"strict","default":"secrets","floor":"secrets","effective":"strict"},` +
		`"peer":{"chosen":null,"default":"strict","floor":"strict","effective":"strict"},"relevance":"shadow","regulated":true}`
	if response := serveGuard(handler, http.MethodGet, guardPath, "", bearer); response.Code != http.StatusOK || compact(response) != chosen {
		t.Fatalf("chosen = %d %s\nwant     %s", response.Code, response.Body.String(), chosen)
	}
	if want := [][2]string{{"org_1", "user_1"}, {"org_1", "user_1"}}; fmt.Sprint(settings.calls) != fmt.Sprint(want) {
		t.Errorf("read %v, want the caller's own settings each time: %v", settings.calls, want)
	}
}

func TestAMemberChangesTheirOwnAnswerGuardSettings(t *testing.T) {
	settings := &fakeAnswerGuardPreferences{}
	handler, _ := guardServer(settings, nil)
	put := func(body string) string {
		t.Helper()
		response := serveGuard(handler, http.MethodPut, guardPath, body, bearer)
		if response.Code != http.StatusOK {
			t.Fatalf("PUT %s = %d %s", body, response.Code, response.Body.String())
		}
		return compact(response)
	}
	asked := func() guardChangeAsked { return settings.changes[len(settings.changes)-1] }
	show := func(level *guardpolicy.Level) string {
		if level == nil {
			return "left as it is"
		}
		return fmt.Sprintf("set to %q", *level)
	}

	if got := put(`{"owner":"strict"}`); !strings.HasPrefix(got, `{"owner":{"chosen":"strict","default":"secrets","floor":"secrets","effective":"strict"},"peer":{"chosen":null,`) {
		t.Errorf("owner strict = %s", got)
	}
	if last := asked(); last.owner == nil || *last.owner != guardpolicy.LevelStrict || last.peer != nil {
		t.Errorf("asked for owner %s and peer %s, want owner strict and peer left as it is", show(last.owner), show(last.peer))
	}

	// A key left out leaves that setting as it is.
	got := put(`{"peer":"secrets"}`)
	if !strings.Contains(got, `"owner":{"chosen":"strict"`) || !strings.Contains(got, `"peer":{"chosen":"secrets","default":"strict","floor":"secrets","effective":"secrets"}`) {
		t.Errorf("peer secrets = %s, want the owner's choice kept", got)
	}
	if last := asked(); last.owner != nil || last.peer == nil || *last.peer != guardpolicy.LevelSecrets {
		t.Errorf("asked for owner %s and peer %s, want owner left as it is and peer secrets", show(last.owner), show(last.peer))
	}

	// null clears a setting back to its default.
	got = put(`{"owner":null}`)
	if !strings.Contains(got, `"owner":{"chosen":null,"default":"secrets","floor":"secrets","effective":"secrets"}`) || !strings.Contains(got, `"peer":{"chosen":"secrets"`) {
		t.Errorf("owner cleared = %s, want the owner back to its default and the peer kept", got)
	}
	if last := asked(); last.owner == nil || *last.owner != "" || last.peer != nil {
		t.Errorf("asked for owner %s and peer %s, want owner cleared and peer left as it is", show(last.owner), show(last.peer))
	}

	// Naming both, one as null.
	got = put(`{"owner":"strict","peer":null}`)
	if !strings.Contains(got, `"owner":{"chosen":"strict"`) || !strings.Contains(got, `"peer":{"chosen":null,"default":"strict","floor":"secrets","effective":"strict"}`) {
		t.Errorf("both named = %s", got)
	}

	// An empty object names nothing, and changes nothing.
	before := compact(serveGuard(handler, http.MethodGet, guardPath, "", bearer))
	if got := put(`{}`); got != before {
		t.Errorf("PUT {} = %s, want what was there: %s", got, before)
	}
	if last := asked(); last.owner != nil || last.peer != nil {
		t.Errorf("PUT {} asked for owner %s and peer %s, want both left as they are", show(last.owner), show(last.peer))
	}

	// What was changed is what is read.
	if after := compact(serveGuard(handler, http.MethodGet, guardPath, "", bearer)); after != before {
		t.Errorf("read after = %s, want %s", after, before)
	}
	for _, call := range settings.calls {
		if call != [2]string{"org_1", "user_1"} {
			t.Errorf("a call was made for %v, want only the caller's own", call)
		}
	}
}

func TestAChoiceBelowTheMinimumIsAcceptedAndReported(t *testing.T) {
	settings := &fakeAnswerGuardPreferences{orgs: fakeOrgPolicies{"org_1": {MinimumOwner: guardpolicy.LevelStrict, MinimumPeer: guardpolicy.LevelStrict}}}
	handler, _ := guardServer(settings, nil)

	response := serveGuard(handler, http.MethodPut, guardPath, `{"owner":"secrets","peer":"secrets"}`, bearer)
	const want = `{"owner":{"chosen":"secrets","default":"secrets","floor":"strict","effective":"strict"},` +
		`"peer":{"chosen":"secrets","default":"strict","floor":"strict","effective":"strict"},"relevance":"hold","regulated":false}`
	if response.Code != http.StatusOK || compact(response) != want {
		t.Fatalf("choosing secrets under a strict minimum = %d %s\nwant                               %s", response.Code, response.Body.String(), want)
	}
	if got := settings.chosen[[2]string{"org_1", "user_1"}]; got.Owner != guardpolicy.LevelSecrets || got.Peer != guardpolicy.LevelSecrets {
		t.Errorf("kept %+v, want the member's choices kept as they made them", got)
	}
}

func TestARequestToChangeTheSettingsThatIsNotOneIsRefusedAndChangesNothing(t *testing.T) {
	settings := &fakeAnswerGuardPreferences{}
	handler, _ := guardServer(settings, nil)
	bodies := map[string]string{
		"an empty body":              ``,
		"only spaces":                `  `,
		"null":                       `null`,
		"an array":                   `[]`,
		"a string":                   `"strict"`,
		"a number":                   `1`,
		"a boolean":                  `true`,
		"truncated":                  `{"owner":"strict"`,
		"two objects":                `{"owner":"strict"}{"peer":"strict"}`,
		"a stray brace after it":     `{"owner":"strict"}}`,
		"text after it":              `{"owner":"strict"} x`,
		"an unknown field":           `{"__not_a_real_field__":true}`,
		"a member's id":              `{"user_id":"user_2"}`,
		"an organization's id":       `{"owner":"strict","organization_id":"org_2"}`,
		"the key in another case":    `{"Owner":"strict"}`,
		"an older name":              `{"app_updates":true}`,
		"a level of off":             `{"owner":"off"}`,
		"a level in capitals":        `{"owner":"STRICT"}`,
		"an empty level":             `{"owner":""}`,
		"a level with a space":       `{"peer":" strict"}`,
		"a number for a level":       `{"peer":1}`,
		"a boolean for a level":      `{"peer":true}`,
		"an object for a level":      `{"peer":{}}`,
		"an array for a level":       `{"peer":["strict"]}`,
		"one good level, one bad":    `{"owner":"strict","peer":"everything"}`,
		"a bad level, then a good":   `{"owner":"everything","peer":"strict"}`,
		"more than the limit":        `{"owner":"strict"}` + strings.Repeat(" ", maxGuardPreferencesBytes),
		"a level longer than it is":  `{"owner":"` + strings.Repeat("a", maxGuardPreferencesBytes) + `"}`,
		"a NUL where a level goes":   "{\"owner\":\"str\\u0000ict\"}",
		"bytes that are not UTF-8":   "{\"owner\":\"\xff\"}",
		"a level with a stray slash": `{"owner":"str\ict"}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			response := serveGuard(handler, http.MethodPut, guardPath, body, bearer)
			if response.Code != http.StatusBadRequest || errorCode(t, response) != "invalid_request" {
				t.Errorf("PUT %.60q = %d %.200s, want 400 invalid_request", body, response.Code, response.Body.String())
			}
		})
	}
	if len(settings.calls) != 0 || len(settings.changes) != 0 {
		t.Fatalf("a refused request reached the settings: %v, %v", settings.calls, settings.changes)
	}
}

func TestOnlyAMemberMayReadOrChangeTheirOwnAnswerGuardSettings(t *testing.T) {
	const body = `{"owner":"strict"}`
	crossSite := map[string]string{"Cookie": "cookie-token", "Origin": "https://evil.test"}
	ownSite := map[string]string{"Cookie": "cookie-token", "Origin": "https://app.oryxa.test"}

	t.Run("no credentials", func(t *testing.T) {
		settings := &fakeAnswerGuardPreferences{}
		handler, _ := guardServer(settings, nil)
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			response := serveGuard(handler, method, guardPath, body, nil)
			if response.Code != http.StatusUnauthorized || errorCode(t, response) != "unauthorized" {
				t.Errorf("%s with no credentials = %d %s, want 401", method, response.Code, response.Body.String())
			}
		}
		if len(settings.calls) != 0 {
			t.Errorf("reached the settings: %v", settings.calls)
		}
	})

	t.Run("an agent acting for a member", func(t *testing.T) {
		settings := &fakeAnswerGuardPreferences{}
		handler, directory := guardServer(settings, nil)
		directory.membership.Agent = true
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			response := serveGuard(handler, method, guardPath, body, bearer)
			if response.Code != http.StatusForbidden || errorCode(t, response) != "forbidden" {
				t.Errorf("%s with an agent's token = %d %s, want 403", method, response.Code, response.Body.String())
			}
		}
		if len(settings.calls) != 0 {
			t.Errorf("an agent reached the settings: %v", settings.calls)
		}
	})

	t.Run("somebody who is not in the organization", func(t *testing.T) {
		settings := &fakeAnswerGuardPreferences{}
		handler, directory := guardServer(settings, nil)
		directory.err = authclient.ErrForbidden
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			response := serveGuard(handler, method, guardPath, body, bearer)
			if response.Code != http.StatusForbidden || errorCode(t, response) != "forbidden" {
				t.Errorf("%s by a stranger = %d %s, want 403", method, response.Code, response.Body.String())
			}
		}
		if len(settings.calls) != 0 {
			t.Errorf("a stranger reached the settings: %v", settings.calls)
		}
	})

	t.Run("a cookie", func(t *testing.T) {
		settings := &fakeAnswerGuardPreferences{}
		handler, _ := guardServer(settings, nil)
		if response := serveGuard(handler, http.MethodPut, guardPath, body, crossSite); response.Code != http.StatusForbidden || errorCode(t, response) != "forbidden" {
			t.Errorf("PUT from another site = %d %s, want 403", response.Code, response.Body.String())
		}
		if len(settings.changes) != 0 {
			t.Fatalf("another site changed the settings: %v", settings.changes)
		}
		// Reading needs no origin, as everywhere else; changing needs an allowed one.
		if response := serveGuard(handler, http.MethodGet, guardPath, "", crossSite); response.Code != http.StatusOK {
			t.Errorf("GET with a cookie = %d %s, want 200", response.Code, response.Body.String())
		}
		if response := serveGuard(handler, http.MethodPut, guardPath, body, ownSite); response.Code != http.StatusOK {
			t.Errorf("PUT from the app's own site = %d %s, want 200", response.Code, response.Body.String())
		}
		if len(settings.changes) != 1 {
			t.Errorf("changes = %v, want the one from the app's own site", settings.changes)
		}
	})
}

func TestTheHandlersOnlyUseTheIdentityTheCallerWasProvedToHave(t *testing.T) {
	settings := &fakeAnswerGuardPreferences{}
	handler, _ := guardServer(settings, nil)
	smuggled := map[string]string{"Authorization": "Bearer member-token", "X-User-Id": "user_2", "X-Organization-Id": "org_2", "X-Forwarded-User": "user_2"}

	for _, response := range []*httptest.ResponseRecorder{
		serveGuard(handler, http.MethodGet, guardPath+"?user_id=user_2&organization_id=org_2&userID=user_2", "", smuggled),
		serveGuard(handler, http.MethodPut, guardPath+"?user_id=user_2&organization_id=org_2", `{"owner":"strict"}`, smuggled),
	} {
		if response.Code != http.StatusOK {
			t.Fatalf("= %d %s", response.Code, response.Body.String())
		}
	}
	// Nor can the body name whose settings they are.
	for _, body := range []string{`{"owner":"strict","user_id":"user_2"}`, `{"owner":"strict","member":"user_2"}`, `{"user":"user_2"}`} {
		if response := serveGuard(handler, http.MethodPut, guardPath, body, bearer); response.Code != http.StatusBadRequest {
			t.Errorf("PUT %s = %d, want 400", body, response.Code)
		}
	}
	// The organization is the path's, which the caller was proved a member of.
	if response := serveGuard(handler, http.MethodGet, "/v1/organizations/org_9/answer-guard/preferences", "", bearer); response.Code != http.StatusOK {
		t.Fatalf("GET in org_9 = %d", response.Code)
	}
	want := [][2]string{{"org_1", "user_1"}, {"org_1", "user_1"}, {"org_9", "user_1"}}
	if fmt.Sprint(settings.calls) != fmt.Sprint(want) {
		t.Fatalf("calls = %v, want %v", settings.calls, want)
	}
}

func TestTheAnswerGuardSettingsSayWhenTheyAreNotKept(t *testing.T) {
	handler, _ := guardServer(nil, nil)
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		response := serveGuard(handler, method, guardPath, `{"owner":"strict"}`, bearer)
		if response.Code != http.StatusServiceUnavailable || errorCode(t, response) != "answer_guard_unavailable" {
			t.Errorf("%s with no settings kept = %d %s, want 503 answer_guard_unavailable", method, response.Code, response.Body.String())
		}
		// Who is asking is settled first, whatever is kept.
		if response := serveGuard(handler, method, guardPath, `{"owner":"strict"}`, nil); response.Code != http.StatusUnauthorized {
			t.Errorf("%s with no credentials and no settings kept = %d, want 401", method, response.Code)
		}
	}
}

func TestAFailureIsOursAndARefusedSettingIsTheCallers(t *testing.T) {
	var logs bytes.Buffer
	settings := &fakeAnswerGuardPreferences{err: errors.New("database down at 10.0.0.7")}
	handler, _ := guardServer(settings, slog.New(slog.NewTextHandler(&logs, nil)))

	for _, request := range []struct{ method, body string }{{http.MethodGet, ""}, {http.MethodPut, `{"owner":"strict"}`}} {
		response := serveGuard(handler, request.method, guardPath, request.body, bearer)
		if response.Code != http.StatusInternalServerError || errorCode(t, response) != "internal" {
			t.Errorf("%s with a failing store = %d %s, want 500 internal", request.method, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "10.0.0.7") {
			t.Errorf("%s told the caller what failed: %s", request.method, response.Body.String())
		}
	}
	if text := logs.String(); !strings.Contains(text, "database down at 10.0.0.7") || strings.Count(text, "level=ERROR") != 2 {
		t.Errorf("log = %q, want the two failures, with what they were", text)
	}

	settings.err = fmt.Errorf("%w: peer is \"secrets\" or \"strict\", or empty to clear it", guardpolicy.ErrInvalid)
	response := serveGuard(handler, http.MethodPut, guardPath, `{"peer":"strict"}`, bearer)
	if response.Code != http.StatusBadRequest || errorCode(t, response) != "invalid_request" || strings.Contains(response.Body.String(), "invalid answer guard setting") {
		t.Errorf("a setting the service refused = %d %s, want 400 with its own words", response.Code, response.Body.String())
	}
}

// *guardpolicy.Service is what the settings routes are given in production.
var _ AnswerGuardPreferences = (*guardpolicy.Service)(nil)

// TestASettingsServiceThatWasNeverBuiltIsNoSettingsAtAll: a nil pointer in the
// Config's interface reads as a service to a nil check, and would panic when
// asked; the server takes it for none, so the routes answer 503 as they do
// without one.
func TestASettingsServiceThatWasNeverBuiltIsNoSettingsAtAll(t *testing.T) {
	var neverBuilt *fakeAnswerGuardPreferences
	var neverOpened *guardpolicy.Service
	for name, settings := range map[string]AnswerGuardPreferences{
		"no settings":                     nil,
		"a fake that was never built":     neverBuilt,
		"a service that was never opened": neverOpened,
	} {
		t.Run(name, func(t *testing.T) {
			server := New(member(), &fakeThreads{}, Config{AllowedOrigins: []string{"https://app.oryxa.test"}, AnswerGuardPreferences: settings}, nil)
			for _, method := range []string{http.MethodGet, http.MethodPut} {
				response := serve(server, method, guardPath, `{"owner":"strict"}`, bearer)
				if response.Code != http.StatusServiceUnavailable || errorCode(t, response) != "answer_guard_unavailable" {
					t.Errorf("%s = %d %s, want 503 answer_guard_unavailable", method, response.Code, response.Body.String())
				}
			}
		})
	}
	// A settings service that was built is asked, by the routes New registers.
	settings := &fakeAnswerGuardPreferences{}
	server := New(member(), &fakeThreads{}, Config{AnswerGuardPreferences: settings}, nil)
	if response := serve(server, http.MethodGet, guardPath, "", bearer); response.Code != http.StatusOK || len(settings.calls) != 1 {
		t.Fatalf("GET = %d %s after %d calls, want 200 and one", response.Code, response.Body.String(), len(settings.calls))
	}
}
