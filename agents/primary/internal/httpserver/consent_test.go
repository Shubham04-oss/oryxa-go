package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
)

// The ledger the server is wired with does everything it asks of one.
var _ AgentInteractions = (*interactions.Service)(nil)

// slackServer is the server with a request ledger and, when there is one, a
// store of secrets, as an organization that has the Slack app has them.
func slackServer(ledger AgentInteractions, shares SecretShares) *Server {
	managed := &fakeManagedAgents{definitions: []managedagents.Definition{{ID: "agt_primary", Kind: managedagents.KindPrimary, Name: "Carl", Status: managedagents.StatusActive}}}
	return New(member(), &fakeThreads{}, Config{
		AllowedOrigins: []string{"https://app.oryxa.test"}, RunTimeout: time.Minute,
		Agents: managed, Interactions: ledger, SecretShares: shares,
	}, nil)
}

// TestTheOwnerChoosesWhatToShareInOneCall: a choice reaches the ledger as the
// member's own session, whatever else the body says, and answers with the
// request as it stands and the lease access gave.
func TestTheOwnerChoosesWhatToShareInOneCall(t *testing.T) {
	ledger := &fakeInteractions{}
	server := slackServer(ledger, nil)
	for _, choice := range []string{"summary", "access", "deny"} {
		response := serve(server, http.MethodPost, "/v1/organizations/org_1/agent-requests/irq_7/consent", `{"choice":"`+choice+`"}`, bearer)
		if response.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", choice, response.Code, response.Body.String())
		}
		var body struct {
			Request primaryagents.Interaction `json:"request"`
			Lease   *interactions.Lease       `json:"lease"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Request.ID != "irq_7" || body.Request.Status != "approved" || (body.Lease != nil) != (choice == "access") {
			t.Errorf("%s answered %s", choice, response.Body.String())
		}
		if choice == "access" && (body.Lease.ID != "ils_1" || body.Lease.OwnerUserID != "user_1") {
			t.Errorf("the lease is %+v", body.Lease)
		}
	}
	if strings.Join(ledger.consents, ",") != "summary,access,deny" {
		t.Fatalf("the ledger was given %v", ledger.consents)
	}
	for _, actor := range ledger.actors {
		if actor.Session != primaryagents.SessionMember || actor.UserID != "user_1" || actor.AgentKind != "primary" {
			t.Fatalf("the ledger was called as %+v, want the member in their own session", actor)
		}
	}
}

// TestAChoiceThatLostIs409WithTheWinner: whoever decides first wins, and the
// choice that came second is told who: the winning decision, where it was made,
// and how far it lets the answer go.
func TestAChoiceThatLostIs409WithTheWinner(t *testing.T) {
	decidedAt := time.Date(2026, time.September, 29, 14, 2, 0, 0, time.UTC)
	ledger := &fakeInteractions{
		request: primaryagents.Interaction{
			ID: "irq_7", Status: "approved", Revision: 2,
			Decision: &primaryagents.InteractionDecision{
				Decision: "approved", Consent: "summary", DecidedAt: decidedAt,
				DecidedBy: &primaryagents.InteractionDecider{Kind: "member", UserID: "user_1", Surface: "slack"},
			},
		},
		consentErr: interactions.ErrAlreadyDecided,
	}
	server := slackServer(ledger, nil)
	response := serve(server, http.MethodPost, "/v1/organizations/org_1/agent-requests/irq_7/consent", `{"choice":"access"}`, bearer)
	if response.Code != http.StatusConflict || errorCode(t, response) != "already_decided" {
		t.Fatalf("a choice that lost = %d %s, want 409 already_decided", response.Code, response.Body.String())
	}
	var body struct {
		Winner struct {
			RequestID string `json:"request_id"`
			Status    string `json:"status"`
			Revision  int64  `json:"revision"`
			Decision  string `json:"decision"`
			Consent   string `json:"consent"`
			DecidedBy struct {
				Kind    string `json:"kind"`
				UserID  string `json:"user_id"`
				Surface string `json:"surface"`
			} `json:"decided_by"`
			DecidedAt time.Time `json:"decided_at"`
		} `json:"winner"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	winner := body.Winner
	if winner.RequestID != "irq_7" || winner.Status != "approved" || winner.Revision != 2 || winner.Decision != "approved" || winner.Consent != "summary" ||
		winner.DecidedBy.Kind != "member" || winner.DecidedBy.UserID != "user_1" || winner.DecidedBy.Surface != "slack" || !winner.DecidedAt.Equal(decidedAt) {
		t.Fatalf("winner = %+v", winner)
	}
	// It is nothing of the request's words.
	if strings.Contains(response.Body.String(), "task") || strings.Contains(response.Body.String(), "purpose") {
		t.Fatalf("the loser was told the request's content: %s", response.Body.String())
	}
}

// A choice is refused, with the ledger's own words, as any other call is.
func TestAChoiceThatIsRefusedIsAnsweredWithItsCode(t *testing.T) {
	for _, testCase := range []struct {
		err  error
		want int
		code string
	}{
		{interactions.ErrInvalid, http.StatusBadRequest, "invalid_request"},
		{interactions.ErrForbidden, http.StatusForbidden, "forbidden"},
		{interactions.ErrNotFound, http.StatusNotFound, "not_found"},
		{interactions.ErrExpired, http.StatusConflict, "request_expired"},
		{interactions.ErrConsentUnavailable, http.StatusServiceUnavailable, "consent_unavailable"},
		{errors.New("the database is down"), http.StatusInternalServerError, "internal"},
	} {
		ledger := &fakeInteractions{consentErr: testCase.err}
		response := serve(slackServer(ledger, nil), http.MethodPost, "/v1/organizations/org_1/agent-requests/irq_7/consent", `{"choice":"deny"}`, bearer)
		if response.Code != testCase.want || errorCode(t, response) != testCase.code {
			t.Errorf("%v = %d %s, want %d %s", testCase.err, response.Code, response.Body.String(), testCase.want, testCase.code)
		}
		if strings.Contains(response.Body.String(), "database is down") {
			t.Errorf("an internal failure was told to the caller: %s", response.Body.String())
		}
	}
	// A body that names anything but a choice is refused before the ledger.
	ledger := &fakeInteractions{}
	server := slackServer(ledger, nil)
	for _, body := range []string{`{"choice":"summary","surface":"slack"}`, `{"choice":"summary","decided_by":{"kind":"lease"}}`, `[]`, ``, `{"choice":"summary"} {}`} {
		if response := serve(server, http.MethodPost, "/v1/organizations/org_1/agent-requests/irq_7/consent", body, bearer); response.Code != http.StatusBadRequest {
			t.Errorf("body %q = %d, want 400", body, response.Code)
		}
	}
	if len(ledger.consents) != 0 {
		t.Fatalf("the ledger was asked to decide with a body it should have refused: %v", ledger.consents)
	}
}

// TestActivityIsListedForTheMember: the ledger is asked as the member, with the
// limit, and what it answers is the list.
func TestActivityIsListedForTheMember(t *testing.T) {
	ledger := &fakeInteractions{}
	server := slackServer(ledger, nil)
	response := serve(server, http.MethodGet, "/v1/organizations/org_1/agent-requests/activity?limit=7", "", bearer)
	if response.Code != http.StatusOK || ledger.limit != 7 {
		t.Fatalf("activity = %d %s, limit %d", response.Code, response.Body.String(), ledger.limit)
	}
	var body struct {
		Activity []interactions.ActivityEntry `json:"activity"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || len(body.Activity) != 1 || body.Activity[0].Kind != interactions.ActivityAsked {
		t.Fatalf("activity = %s, %v", response.Body.String(), err)
	}
	for _, limit := range []string{"0", "-1", "many"} {
		if response := serve(server, http.MethodGet, "/v1/organizations/org_1/agent-requests/activity?limit="+limit, "", bearer); response.Code != http.StatusBadRequest {
			t.Errorf("limit %s = %d, want 400", limit, response.Code)
		}
	}
	// It is not a request called "activity".
	if serve(server, http.MethodGet, "/v1/organizations/org_1/agent-requests/activity", "", bearer); ledger.limit != 0 {
		t.Errorf("no limit reached the ledger as %d", ledger.limit)
	}
}

// TestALeaseIsRevokedByItsOwner: the route ends the lease the member names, as
// the member, and answers with it as it stands.
func TestALeaseIsRevokedByItsOwner(t *testing.T) {
	ledger := &fakeInteractions{}
	server := slackServer(ledger, nil)
	response := serve(server, http.MethodDelete, "/v1/organizations/org_1/agent-leases/ils_9", "", bearer)
	if response.Code != http.StatusOK {
		t.Fatalf("revoke = %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Lease interactions.Lease `json:"lease"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Lease.ID != "ils_9" || body.Lease.RevokedAt == nil {
		t.Fatalf("revoke = %s, %v", response.Body.String(), err)
	}
	if len(ledger.revoked) != 1 || ledger.revoked[0] != "ils_9" || ledger.actors[0].Session != primaryagents.SessionMember {
		t.Fatalf("the ledger was asked %v as %+v", ledger.revoked, ledger.actors)
	}
	// A body has no place on it.
	if response := serve(server, http.MethodDelete, "/v1/organizations/org_1/agent-leases/ils_9", `{"lease":"another"}`, bearer); response.Code != http.StatusOK || len(ledger.revoked) != 2 || ledger.revoked[1] != "ils_9" {
		t.Errorf("a body named a lease: %d, revoked %v", response.Code, ledger.revoked)
	}
	// The ledger's refusal is 404: a lease that is not the member's is not there.
	ledger2 := &fakeInteractions{}
	failing := slackServer(&notFoundLeases{ledger2}, nil)
	if response := serve(failing, http.MethodDelete, "/v1/organizations/org_1/agent-leases/ils_x", "", bearer); response.Code != http.StatusNotFound || errorCode(t, response) != "not_found" {
		t.Errorf("revoking a lease that is not there = %d %s", response.Code, response.Body.String())
	}
}

type notFoundLeases struct{ *fakeInteractions }

func (notFoundLeases) RevokeLease(_ context.Context, _ primaryagents.InteractionActor, _ string) (interactions.Lease, error) {
	return interactions.Lease{}, interactions.ErrNotFound
}

// TestListingTakesTheBoxTheOriginAndTheAsk: ?box= picks a side of the member's
// requests, ?origin= where they were made and ?ask_id= the requests of one
// message; each reaches the ledger as the query says, and one that is blank or
// unknown is the caller's mistake.
func TestListingTakesTheBoxTheOriginAndTheAsk(t *testing.T) {
	requests := &teamspaceInteractions{}
	server := agentRequestServer(requests)
	for path, want := range map[string]interactions.Query{
		"/v1/organizations/org_1/agent-requests":                                    {Box: interactions.BoxAll},
		"/v1/organizations/org_1/agent-requests?box=inbox":                          {Box: interactions.BoxInbox},
		"/v1/organizations/org_1/agent-requests?box=SENT":                           {Box: interactions.BoxSent},
		"/v1/organizations/org_1/agent-requests?box=all&origin=slack":               {Box: interactions.BoxAll, Origin: "slack"},
		"/v1/organizations/org_1/agent-requests?ask_id=iak_1&box=inbox&limit=5":     {Box: interactions.BoxInbox, AskID: "iak_1", Limit: 5},
		"/v1/organizations/org_1/agent-requests?origin=agent&teamspace_id=tsp_1":    {Box: interactions.BoxAll, Origin: "agent", TeamspaceID: "tsp_1"},
		"/v1/organizations/org_1/agent-requests/inbox?origin=slack&ask_id=iak_2":    {Box: interactions.BoxInbox, Origin: "slack", AskID: "iak_2"},
		"/v1/organizations/org_1/agent-requests/sent?with_user=user_2&origin=slack": {Box: interactions.BoxSent, WithUserID: "user_2", Origin: "slack"},
	} {
		response := serve(server, http.MethodGet, path, "", bearer)
		if response.Code != http.StatusOK {
			t.Errorf("%s = %d %s", path, response.Code, response.Body.String())
			continue
		}
		if requests.query != want {
			t.Errorf("%s reached the ledger as %+v, want %+v", path, requests.query, want)
		}
	}
	for _, path := range []string{
		"/v1/organizations/org_1/agent-requests?box=everything", "/v1/organizations/org_1/agent-requests?box=",
		"/v1/organizations/org_1/agent-requests?origin=", "/v1/organizations/org_1/agent-requests?ask_id=%20",
	} {
		if response := serve(server, http.MethodGet, path, "", bearer); response.Code != http.StatusBadRequest || errorCode(t, response) != "invalid_request" {
			t.Errorf("%s = %d %s, want 400 invalid_request", path, response.Code, response.Body.String())
		}
	}
}

// putSecret shares a secret as the member, with a body.
func putSecret(server *Server, body string, headers map[string]string) *httptest.ResponseRecorder {
	return serve(server, http.MethodPut, "/v1/organizations/org_1/agent-requests/irq_7/secret", body, headers)
}

func viewSecret(server *Server, headers map[string]string) *httptest.ResponseRecorder {
	return serve(server, http.MethodGet, "/v1/organizations/org_1/agent-requests/irq_7/secret", "", headers)
}

// TestSecretsAreNotKeptWhereNoStoreIsSet: without a store the three routes are
// 503, and say so, after they know who is calling.
func TestSecretsAreNotKeptWhereNoStoreIsSet(t *testing.T) {
	server := slackServer(&fakeInteractions{}, nil)
	for _, response := range []*httptest.ResponseRecorder{
		putSecret(server, `{"value":"s"}`, bearer), viewSecret(server, bearer),
		serve(server, http.MethodDelete, "/v1/organizations/org_1/agent-requests/irq_7/secret", "", bearer),
	} {
		if response.Code != http.StatusServiceUnavailable || errorCode(t, response) != "secrets_unavailable" {
			t.Errorf("no store = %d %s, want 503 secrets_unavailable", response.Code, response.Body.String())
		}
	}
	if response := viewSecret(server, nil); response.Code != http.StatusUnauthorized {
		t.Errorf("no store and no credentials = %d, want 401 first", response.Code)
	}
	if response := viewSecret(New(member(), &fakeThreads{}, Config{Interactions: nil, SecretShares: &fakeSecretShares{}}, nil), bearer); response.Code != http.StatusServiceUnavailable {
		t.Errorf("a store and no ledger = %d, want 503", response.Code)
	}
}

// TestASecretIsSharedForTheRequestTheLedgerNames: the owner's value goes to the
// store under the share the ledger authorized, for the lifetime it allowed, and
// the answer says when it expires and never what it holds.
func TestASecretIsSharedForTheRequestTheLedgerNames(t *testing.T) {
	ledger := &fakeInteractions{}
	shares := &fakeSecretShares{}
	server := slackServer(ledger, shares)
	response := putSecret(server, `{"value":"sk-live-example","ttl_seconds":600}`, bearer)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "sk-live") {
		t.Fatalf("put = %d %s", response.Code, response.Body.String())
	}
	var body struct {
		RequestID string    `json:"request_id"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.RequestID != "irq_7" || body.ExpiresAt.IsZero() {
		t.Fatalf("put answered %s, %v", response.Body.String(), err)
	}
	if shares.values["irq_7"] != "sk-live-example" || len(shares.shares) != 1 || shares.ttls[0] != 10*time.Minute ||
		shares.shares[0].OwnerUserID != "user_1" || shares.shares[0].OrganizationID != "org_1" || shares.shares[0].RequestID != "irq_7" {
		t.Fatalf("the store was given %+v for %v, holds %v", shares.shares, shares.ttls, shares.values)
	}
	// No lifetime asked is the longest the ledger allows.
	if response := putSecret(server, `{"value":"another"}`, bearer); response.Code != http.StatusOK || shares.ttls[1] != interactions.MaxSecretTTL {
		t.Fatalf("put with no ttl = %d, lived %v", response.Code, shares.ttls)
	}
	// The ledger's own authorization is the only one: nothing in the body says
	// who the owner is or whose request it is.
	for _, body := range []string{`{"value":"s","owner_user_id":"user_9"}`, `{"value":"s","request_id":"irq_9"}`} {
		if response := putSecret(server, body, bearer); response.Code != http.StatusBadRequest {
			t.Errorf("body %s = %d, want 400", body, response.Code)
		}
	}
	for name, body := range map[string]string{
		"no value": `{}`, "a blank value": `{"value":"   "}`, "a value too long": `{"value":"` + strings.Repeat("x", interactions.MaxSecretRunes+1) + `"}`,
		"a negative lifetime": `{"value":"s","ttl_seconds":-1}`, "a lifetime past an hour": `{"value":"s","ttl_seconds":3601}`,
		"a NUL in the value": "{\"value\":\"a\\u0000b\"}",
	} {
		before := len(shares.shares)
		if response := putSecret(server, body, bearer); response.Code != http.StatusBadRequest || errorCode(t, response) != "invalid_request" || len(shares.shares) != before {
			t.Errorf("%s = %d %s, stored %d", name, response.Code, response.Body.String(), len(shares.shares)-before)
		}
	}
}

// A secret the ledger does not allow is not kept, and the store is never asked.
func TestASecretTheLedgerDoesNotAllowIsNotKept(t *testing.T) {
	for _, testCase := range []struct {
		err  error
		want int
		code string
	}{
		{interactions.ErrForbidden, http.StatusForbidden, "forbidden"},
		{interactions.ErrNotFound, http.StatusNotFound, "not_found"},
		{interactions.ErrConflict, http.StatusConflict, "invalid_state"},
		{interactions.ErrInvalid, http.StatusBadRequest, "invalid_request"},
		{interactions.ErrExpired, http.StatusConflict, "request_expired"},
	} {
		ledger := &fakeInteractions{secretErr: testCase.err}
		shares := &fakeSecretShares{}
		server := slackServer(ledger, shares)
		for name, response := range map[string]*httptest.ResponseRecorder{
			"put": putSecret(server, `{"value":"s"}`, bearer), "view": viewSecret(server, bearer),
			"revoke": serve(server, http.MethodDelete, "/v1/organizations/org_1/agent-requests/irq_7/secret", "", bearer),
		} {
			if response.Code != testCase.want || errorCode(t, response) != testCase.code {
				t.Errorf("%v on %s = %d %s, want %d %s", testCase.err, name, response.Code, response.Body.String(), testCase.want, testCase.code)
			}
		}
		if len(shares.shares) != 0 {
			t.Errorf("%v: the store was asked %d times", testCase.err, len(shares.shares))
		}
	}
}

// TestASecretIsOpenedOnce: the member who asked opens it, and gets it that once;
// the second time, and any time after it was revoked, it is gone.
func TestASecretIsOpenedOnce(t *testing.T) {
	ledger := &fakeInteractions{}
	shares := &fakeSecretShares{}
	server := slackServer(ledger, shares)
	if response := putSecret(server, `{"value":"sk-live-example"}`, bearer); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	opened := viewSecret(server, bearer)
	if opened.Code != http.StatusOK || opened.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("view = %d %s, cache-control %q", opened.Code, opened.Body.String(), opened.Header().Get("Cache-Control"))
	}
	var body struct {
		Value     string    `json:"value"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(opened.Body.Bytes(), &body); err != nil || body.Value != "sk-live-example" || body.ExpiresAt.IsZero() {
		t.Fatalf("view answered %s, %v", opened.Body.String(), err)
	}
	// The store has nothing left, so a second view finds it gone.
	shares.viewErr = interactions.ErrSecretGone
	again := viewSecret(server, bearer)
	if again.Code != http.StatusGone || errorCode(t, again) != "secret_gone" || strings.Contains(again.Body.String(), "sk-live") {
		t.Fatalf("a second view = %d %s, want 410 secret_gone", again.Code, again.Body.String())
	}
}

// TestOpeningASecretNeedsMoreThanACookie: opening consumes the secret, so a page
// on another site that makes the browser send the member's cookie must not be
// able to trigger it; and neither can a HEAD, which a GET route also answers.
func TestOpeningASecretNeedsMoreThanACookie(t *testing.T) {
	for name, testCase := range map[string]struct {
		headers map[string]string
		want    int
	}{
		"a bearer token":                                     {bearer, http.StatusOK},
		"a bearer token from another site":                   {map[string]string{"Authorization": "Bearer member-token", "Origin": "https://evil.test"}, http.StatusOK},
		"a cookie from the app's own origin":                 {map[string]string{"Cookie": "cookie-token", "Origin": "https://app.oryxa.test"}, http.StatusOK},
		"a cookie in a same-origin fetch":                    {map[string]string{"Cookie": "cookie-token", "Sec-Fetch-Site": "same-origin"}, http.StatusOK},
		"a cookie with nothing to say where it came from":    {map[string]string{"Cookie": "cookie-token"}, http.StatusOK},
		"a cookie from another site":                         {map[string]string{"Cookie": "cookie-token", "Origin": "https://evil.test"}, http.StatusForbidden},
		"a cookie with a null origin":                        {map[string]string{"Cookie": "cookie-token", "Origin": "null"}, http.StatusForbidden},
		"a cookie the browser marked cross-site":             {map[string]string{"Cookie": "cookie-token", "Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		"a cookie the browser marked same-site":              {map[string]string{"Cookie": "cookie-token", "Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		"a cookie from a page the user navigated to by hand": {map[string]string{"Cookie": "cookie-token", "Sec-Fetch-Site": "none"}, http.StatusForbidden},
	} {
		shares := &fakeSecretShares{}
		server := slackServer(&fakeInteractions{}, shares)
		response := viewSecret(server, testCase.headers)
		if response.Code != testCase.want {
			t.Errorf("%s = %d %s, want %d", name, response.Code, response.Body.String(), testCase.want)
		}
		if testCase.want == http.StatusForbidden && len(shares.shares) != 0 {
			t.Errorf("%s: the secret was opened", name)
		}
	}
	shares := &fakeSecretShares{}
	server := slackServer(&fakeInteractions{}, shares)
	head := serve(server, http.MethodHead, "/v1/organizations/org_1/agent-requests/irq_7/secret", "", bearer)
	if head.Code != http.StatusMethodNotAllowed || head.Header().Get("Allow") != http.MethodGet || len(shares.shares) != 0 {
		t.Fatalf("HEAD = %d, allow %q, store asked %d times; want 405 and the secret untouched", head.Code, head.Header().Get("Allow"), len(shares.shares))
	}
}

// TestASecretIsRevokedByItsOwner: revoking answers 204, whether the secret was
// still there or had gone, and only after the ledger says it is the owner's to.
func TestASecretIsRevokedByItsOwner(t *testing.T) {
	ledger := &fakeInteractions{}
	shares := &fakeSecretShares{}
	server := slackServer(ledger, shares)
	if response := putSecret(server, `{"value":"sk-live-example"}`, bearer); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	path := "/v1/organizations/org_1/agent-requests/irq_7/secret"
	if response := serve(server, http.MethodDelete, path, "", bearer); response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("revoke = %d %s", response.Code, response.Body.String())
	}
	if _, kept := shares.values["irq_7"]; kept {
		t.Fatal("the secret is still kept")
	}
	if response := serve(server, http.MethodDelete, path, "", bearer); response.Code != http.StatusNoContent {
		t.Fatalf("revoking what is gone = %d, want 204", response.Code)
	}
}

// The ledger's new refusals are answered with codes of their own, and the ones a
// client acts on differently are told apart from the rest.
func TestTheConsentLoopsErrorsHaveTheirOwnCodes(t *testing.T) {
	for _, testCase := range []struct {
		err    error
		status int
		code   string
	}{
		{interactions.ErrAlreadyDecided, http.StatusConflict, "already_decided"},
		{interactions.ErrAnsweredElsewhere, http.StatusConflict, "answered_elsewhere"},
		{interactions.ErrRevoked, http.StatusConflict, "access_revoked"},
		{interactions.ErrSecretGone, http.StatusGone, "secret_gone"},
		{interactions.ErrConsentUnavailable, http.StatusServiceUnavailable, "consent_unavailable"},
		{fmt.Errorf("the answer was not delivered: %w", interactions.ErrRevoked), http.StatusConflict, "access_revoked"},
		{interactions.ErrConflict, http.StatusConflict, "invalid_state"},
	} {
		if status, code := ErrorCode(testCase.err); status != testCase.status || code != testCase.code {
			t.Errorf("%v = %d %s, want %d %s", testCase.err, status, code, testCase.status, testCase.code)
		}
	}
}
