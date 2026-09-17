package connectionsclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

type fakeSigner struct{ signed int }

func (signer *fakeSigner) Token(_ context.Context, audience, purpose string) (string, error) {
	signer.signed++
	return "service-token/" + audience + "/" + purpose, nil
}

var testClock = time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)

// asked is one call connections received, read while the request is still
// open so a test can look at it afterwards.
type asked struct {
	header http.Header
	body   map[string]string
}

// tokenServer answers a token that lapses at expiresAt, recording every call.
func tokenServer(t *testing.T, expiresAt *time.Time) (*httptest.Server, *[]asked) {
	t.Helper()
	calls := []asked{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		call := asked{header: request.Header.Clone(), body: map[string]string{}}
		_ = json.NewDecoder(request.Body).Decode(&call.body)
		calls = append(calls, call)
		answer := map[string]any{
			"access_token": "provider-token",
			"placement":    map[string]string{"in": "header", "name": "Authorization", "prefix": "Bearer "},
		}
		if expiresAt != nil {
			answer["expires_at"] = expiresAt.Format(time.RFC3339Nano)
		}
		if err := json.NewEncoder(writer).Encode(answer); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func testClient(t *testing.T, server *httptest.Server) (*Client, *fakeSigner) {
	t.Helper()
	signer := &fakeSigner{}
	client := New(server.URL, signer)
	client.clock = func() time.Time { return testClock }
	return client, signer
}

func tokenRequest() TokenRequest {
	return TokenRequest{
		OrganizationID: "org_1", ConnectionID: "con_1",
		Purpose: domain.PurposeAction, ActingMemberID: "user-1",
	}
}

func TestTokenSignsKnowledgesOwnIdentity(t *testing.T) {
	expires := testClock.Add(time.Hour)
	server, calls := tokenServer(t, &expires)
	client, signer := testClient(t, server)

	credential, err := client.Token(context.Background(), tokenRequest())
	if err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	if credential.AccessToken != "provider-token" || credential.Prefix != "Bearer " {
		t.Fatalf("Token() = %+v", credential)
	}
	if signer.signed != 1 {
		t.Fatalf("signed %d times, want 1", signer.signed)
	}
	if got := (*calls)[0].header.Get(serviceauth.Header); got != "service-token/connections/action" {
		t.Fatalf("%s = %q, want the signed service identity", serviceauth.Header, got)
	}
}

func TestTokenNamesTheMemberItActsForWhenNoneIsPresent(t *testing.T) {
	expires := testClock.Add(time.Hour)
	server, calls := tokenServer(t, &expires)
	client, _ := testClient(t, server)

	if _, err := client.Token(context.Background(), tokenRequest()); err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	if got := (*calls)[0].body["acting_member_id"]; got != "user-1" {
		t.Fatalf("acting_member_id = %q, want user-1", got)
	}
	if (*calls)[0].header.Get("Authorization") != "" {
		t.Fatal("a bare service call must not carry a member's bearer token")
	}
}

func TestTokenForwardsAMembersOwnTokenWhenOneIsActing(t *testing.T) {
	expires := testClock.Add(time.Hour)
	server, calls := tokenServer(t, &expires)
	client, _ := testClient(t, server)

	request := tokenRequest()
	request.AccessToken = "member-token"
	if _, err := client.Token(context.Background(), request); err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	if got := (*calls)[0].header.Get("Authorization"); got != "Bearer member-token" {
		t.Fatalf("Authorization = %q, want the member's own token", got)
	}
	if _, named := (*calls)[0].body["acting_member_id"]; named {
		t.Fatal("a call carrying a member must not also name one")
	}
}

func TestTokenIsHeldRatherThanAskedForEveryPage(t *testing.T) {
	expires := testClock.Add(time.Hour)
	server, calls := tokenServer(t, &expires)
	client, _ := testClient(t, server)

	for range 5 {
		if _, err := client.Token(context.Background(), tokenRequest()); err != nil {
			t.Fatalf("Token() error = %v", err)
		}
	}
	if len(*calls) != 1 {
		t.Fatalf("asked connections %d times, want 1: a backfill must not ask per call", len(*calls))
	}
}

func TestTokenIsAskedAgainOnceTheHeldOneIsNearlyDue(t *testing.T) {
	expires := testClock.Add(time.Hour)
	server, calls := tokenServer(t, &expires)
	client, _ := testClient(t, server)

	if _, err := client.Token(context.Background(), tokenRequest()); err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	// Connections renews inside the last two minutes, so a caller must stop
	// using what it holds before then.
	client.clock = func() time.Time { return expires.Add(-domain.RenewBefore) }
	if _, err := client.Token(context.Background(), tokenRequest()); err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	if len(*calls) != 2 {
		t.Fatalf("asked connections %d times, want 2", len(*calls))
	}
}

func TestTokenIsNeverHeldWhenItDoesNotLapse(t *testing.T) {
	// A credential with no expiry is a key-based secret, which connections
	// says is never cached.
	server, calls := tokenServer(t, nil)
	client, _ := testClient(t, server)

	for range 3 {
		if _, err := client.Token(context.Background(), tokenRequest()); err != nil {
			t.Fatalf("Token() error = %v", err)
		}
	}
	if len(*calls) != 3 {
		t.Fatalf("asked connections %d times, want 3: a key-based secret is never kept", len(*calls))
	}
}

func TestForgetDropsWhatIsHeldForAConnection(t *testing.T) {
	expires := testClock.Add(time.Hour)
	server, calls := tokenServer(t, &expires)
	client, _ := testClient(t, server)

	if _, err := client.Token(context.Background(), tokenRequest()); err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	client.Forget("con_1")
	if _, err := client.Token(context.Background(), tokenRequest()); err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	if len(*calls) != 2 {
		t.Fatalf("asked connections %d times, want 2", len(*calls))
	}
}

func TestConnectionsAnswersTellACallerWhatToDo(t *testing.T) {
	for _, refusal := range []struct {
		status int
		code   string
		want   error
	}{
		{http.StatusConflict, "needs_reconnect", domain.ErrNeedsReconnect},
		{http.StatusForbidden, "scope_required", domain.ErrScopeRequired},
		{http.StatusUnprocessableEntity, "credential_rejected", domain.ErrCredentialRejected},
		{http.StatusForbidden, "app_disabled", domain.ErrForbidden},
		{http.StatusNotFound, "", domain.ErrNotFound},
		{http.StatusUnauthorized, "", domain.ErrUnauthorized},
		{http.StatusBadRequest, "", domain.ErrInvalid},
		{http.StatusBadGateway, "", domain.ErrProviderUnavailable},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(refusal.status)
			_, _ = fmt.Fprintf(writer, `{"error":{"code":%q}}`, refusal.code)
		}))
		client, _ := testClient(t, server)
		_, err := client.Token(context.Background(), tokenRequest())
		server.Close()
		if !errors.Is(err, refusal.want) {
			t.Fatalf("Token() on %d %q error = %v, want %v", refusal.status, refusal.code, err, refusal.want)
		}
	}
}

func TestTokenRefusesAnAnswerWithNoCredentialInIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"placement":{"in":"header"}}`))
	}))
	defer server.Close()
	client, _ := testClient(t, server)

	if _, err := client.Token(context.Background(), tokenRequest()); err == nil {
		t.Fatal("Token() error = nil, want a refusal")
	}
}

func TestConnectionRefusesAnAnswerAboutAnotherConnection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"id":"con_2","organization_id":"org_1"}`))
	}))
	defer server.Close()
	client, _ := testClient(t, server)

	if _, err := client.Connection(context.Background(), "org_1", "con_1", ""); err == nil {
		t.Fatal("Connection() error = nil, want a refusal")
	}
}
