package agentgrant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/secrets"
)

type fakeStore struct {
	flow      domain.LoginFlow
	flowSaved bool

	grant   domain.AgentGrant
	grantOK bool
	getErr  error
	// rotated, when set, is what every read after the first returns: another
	// mint rotating the stored token meanwhile.
	rotated *domain.AgentGrant
	gets    int

	upserted *domain.AgentGrant
	deleted  []string
	replaced *domain.SealedData
}

func (f *fakeStore) SaveLoginFlow(_ context.Context, flow domain.LoginFlow) error {
	f.flow = flow
	f.flowSaved = true
	return nil
}

func (f *fakeStore) ConsumeLoginFlow(_ context.Context, _ string) (domain.LoginFlow, error) {
	if !f.flowSaved {
		return domain.LoginFlow{}, domain.ErrNotFound
	}
	return f.flow, nil
}

func (f *fakeStore) GetAgentGrant(_ context.Context, _ string) (domain.AgentGrant, error) {
	f.gets++
	if f.getErr != nil {
		return domain.AgentGrant{}, f.getErr
	}
	if !f.grantOK {
		return domain.AgentGrant{}, domain.ErrNotFound
	}
	if f.rotated != nil && f.gets > 1 {
		return *f.rotated, nil
	}
	return f.grant, nil
}

func (f *fakeStore) UpsertAgentGrant(_ context.Context, grant domain.AgentGrant) error {
	f.upserted = &grant
	return nil
}

func (f *fakeStore) DeleteAgentGrant(_ context.Context, userID string) error {
	f.deleted = append(f.deleted, userID)
	return nil
}

func (f *fakeStore) ReplaceAgentGrantToken(_ context.Context, _ string, sealed domain.SealedData, _ time.Time) error {
	f.replaced = &sealed
	return nil
}

type fakeIdentityVerifier struct {
	subject       string
	err           error
	expectedNonce string
}

func (f *fakeIdentityVerifier) VerifyAgentIdentityToken(_ context.Context, _ string, nonce string) (string, error) {
	f.expectedNonce = nonce
	if f.err != nil {
		return "", f.err
	}
	return f.subject, nil
}

// fakeSealer "seals" by prefixing plaintext, so tests can see through it
// without depending on real AES-GCM/KMS behavior, which secrets_test.go
// already covers.
type fakeSealer struct{}

func (f *fakeSealer) Seal(_ context.Context, binding secrets.Binding, plaintext []byte) (domain.SealedData, error) {
	return domain.SealedData{Version: 1, Nonce: []byte("nonce"), Ciphertext: append([]byte("sealed:"), plaintext...), EncryptedDataKey: []byte(binding.ResourceID)}, nil
}

func (f *fakeSealer) Open(_ context.Context, _ secrets.Binding, sealed domain.SealedData) ([]byte, error) {
	return bytes.TrimPrefix(sealed.Ciphertext, []byte("sealed:")), nil
}

func testConfig(cognitoDomain string) Config {
	return Config{
		CognitoDomain:   cognitoDomain,
		ClientID:        "agent-client-id",
		ClientSecret:    "agent-client-secret",
		CallbackURL:     "https://auth.example.com/v1/agent-grant/callback",
		AllowedReturnTo: []string{"https://app.example.com"},
	}
}

func TestAuthorizeStoresBoundFlowAndBuildsAgentAuthorizationURL(t *testing.T) {
	store := &fakeStore{}
	manager, err := New(testConfig("https://identity.example.com"), store, &fakeIdentityVerifier{}, &fakeSealer{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	authorizationURL, state, err := manager.Authorize(context.Background(), "member-1", "https://app.example.com")
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	if !strings.HasPrefix(state, "member-1.") {
		t.Fatalf("state = %q, want it bound to member-1", state)
	}
	if !store.flowSaved || store.flow.CodeVerifier == "" || store.flow.Nonce == "" || store.flow.ReturnTo != "https://app.example.com" {
		t.Fatalf("incomplete flow: %+v", store.flow)
	}
	parsed, err := url.Parse(authorizationURL)
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}
	query := parsed.Query()
	if query.Get("client_id") != "agent-client-id" || query.Get("scope") != Scope || query.Get("code_challenge_method") != "S256" ||
		query.Get("state") != state || query.Get("response_type") != "code" {
		t.Fatalf("unexpected authorization query: %v", query)
	}
	// A member who signs in with a password must be able to grant as well as
	// one who signs in with Google.
	if query.Has("identity_provider") {
		t.Fatalf("authorization query forces an identity provider: %v", query)
	}
}

func TestAuthorizeRejectsUnlistedReturnURL(t *testing.T) {
	manager, err := New(testConfig("https://identity.example.com"), &fakeStore{}, &fakeIdentityVerifier{}, &fakeSealer{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, _, err := manager.Authorize(context.Background(), "member-1", "https://attacker.example.com"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Authorize() error = %v, want invalid input", err)
	}
}

func TestCompleteExchangesCodeStoresSealedGrantAndReturnsReturnTo(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		username, password, ok := request.BasicAuth()
		if !ok || username != "agent-client-id" || password != "agent-client-secret" {
			t.Fatalf("token request did not use HTTP Basic client authentication: ok=%v user=%q", ok, username)
		}
		if err := request.ParseForm(); err != nil {
			t.Fatalf("ParseForm() error = %v", err)
		}
		if request.Form.Get("grant_type") != "authorization_code" || request.Form.Get("code_verifier") != "verifier" ||
			request.Form.Get("code") != "code-1" || request.Form.Get("client_id") != "agent-client-id" {
			t.Fatalf("unexpected token form: %v", request.Form)
		}
		_ = json.NewEncoder(writer).Encode(tokenResponse{
			AccessToken: "access-token", IDToken: "identity-token", RefreshToken: "refresh-token", ExpiresIn: 3600,
		})
	}))
	defer tokenServer.Close()

	store := &fakeStore{flow: domain.LoginFlow{
		CodeVerifier: "verifier", Nonce: "nonce-1", ReturnTo: "https://app.example.com",
	}, flowSaved: true}
	verifier := &fakeIdentityVerifier{subject: "member-1"}
	manager, err := New(testConfig(tokenServer.URL), store, verifier, &fakeSealer{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	state := "member-1.random-suffix"
	returnTo, err := manager.Complete(context.Background(), state, state, "code-1")
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if returnTo != "https://app.example.com" {
		t.Fatalf("returnTo = %q", returnTo)
	}
	if verifier.expectedNonce != "nonce-1" {
		t.Fatalf("nonce = %q, want nonce-1", verifier.expectedNonce)
	}
	if store.upserted == nil || store.upserted.UserID != "member-1" || store.upserted.Scope != Scope {
		t.Fatalf("upserted grant = %+v", store.upserted)
	}
	if string(store.upserted.EncryptedRefreshToken.Ciphertext) != "sealed:refresh-token" {
		t.Fatalf("sealed ciphertext = %q", store.upserted.EncryptedRefreshToken.Ciphertext)
	}
}

func TestCompleteRejectsMismatchedStateBeforeExchange(t *testing.T) {
	manager, err := New(testConfig("https://identity.example.com"), &fakeStore{}, &fakeIdentityVerifier{}, &fakeSealer{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := manager.Complete(context.Background(), "member-1.a", "member-1.b", "code"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("Complete() error = %v, want unauthorized", err)
	}
}

func TestCompleteRejectsUnknownOrExpiredState(t *testing.T) {
	manager, err := New(testConfig("https://identity.example.com"), &fakeStore{}, &fakeIdentityVerifier{}, &fakeSealer{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	state := "member-1.random-suffix"
	if _, err := manager.Complete(context.Background(), state, state, "code"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("Complete() error = %v, want unauthorized", err)
	}
}

func TestCompleteRejectsAnotherMembersCodeAndStoresNothing(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(writer).Encode(tokenResponse{
			AccessToken: "access-token", IDToken: "identity-token", RefreshToken: "refresh-token", ExpiresIn: 3600,
		})
	}))
	defer tokenServer.Close()

	store := &fakeStore{flow: domain.LoginFlow{CodeVerifier: "verifier", Nonce: "nonce", ReturnTo: "https://app.example.com"}, flowSaved: true}
	verifier := &fakeIdentityVerifier{subject: "someone-else"}
	manager, err := New(testConfig(tokenServer.URL), store, verifier, &fakeSealer{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	state := "member-1.random-suffix"
	if _, err := manager.Complete(context.Background(), state, state, "code"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("Complete() error = %v, want unauthorized", err)
	}
	if store.upserted != nil {
		t.Fatalf("grant stored despite subject mismatch: %+v", store.upserted)
	}
}

func TestCompleteRejectsReturnURLNoLongerAllowedAndStoresNothing(t *testing.T) {
	store := &fakeStore{flow: domain.LoginFlow{CodeVerifier: "verifier", Nonce: "nonce", ReturnTo: "https://attacker.example.com"}, flowSaved: true}
	manager, err := New(testConfig("https://identity.example.com"), store, &fakeIdentityVerifier{}, &fakeSealer{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	state := "member-1.random-suffix"
	if _, err := manager.Complete(context.Background(), state, state, "code"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Complete() error = %v, want invalid input", err)
	}
	if store.upserted != nil {
		t.Fatal("grant stored despite a disallowed return URL")
	}
}

func TestRevokeDeletesGrantEvenWhenCognitoRevokeFails(t *testing.T) {
	revokeServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			t.Fatalf("ParseForm() error = %v", err)
		}
		if request.Form.Get("token") != "refresh-token" {
			t.Fatalf("revoked token = %q, want refresh-token", request.Form.Get("token"))
		}
		username, password, ok := request.BasicAuth()
		if !ok || username != "agent-client-id" || password != "agent-client-secret" {
			t.Fatalf("revoke did not use HTTP Basic client authentication: ok=%v user=%q", ok, username)
		}
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer revokeServer.Close()

	store := &fakeStore{
		grant:   domain.AgentGrant{UserID: "member-1", EncryptedRefreshToken: domain.SealedData{Ciphertext: []byte("sealed:refresh-token")}},
		grantOK: true,
	}
	manager, err := New(testConfig(revokeServer.URL), store, &fakeIdentityVerifier{}, &fakeSealer{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := manager.Revoke(context.Background(), "member-1"); err != nil {
		t.Fatalf("Revoke() error = %v, want nil despite Cognito failure", err)
	}
	if len(store.deleted) != 1 || store.deleted[0] != "member-1" {
		t.Fatalf("deleted = %v, want [member-1]", store.deleted)
	}
}

func TestRevokeWithNoGrantIsNotAnError(t *testing.T) {
	store := &fakeStore{}
	manager, err := New(testConfig("https://identity.example.com"), store, &fakeIdentityVerifier{}, &fakeSealer{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := manager.Revoke(context.Background(), "member-1"); err != nil {
		t.Fatalf("Revoke() error = %v, want nil", err)
	}
	if len(store.deleted) != 0 {
		t.Fatalf("deleted = %v, want none", store.deleted)
	}
}

func TestMintTokenSavesRotatedRefreshTokenAndReturnsAccessToken(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			t.Fatalf("ParseForm() error = %v", err)
		}
		if request.Form.Get("grant_type") != "refresh_token" || request.Form.Get("refresh_token") != "refresh-token" {
			t.Fatalf("unexpected refresh form: %v", request.Form)
		}
		_ = json.NewEncoder(writer).Encode(tokenResponse{AccessToken: "new-access-token", RefreshToken: "rotated-refresh-token", ExpiresIn: 3600})
	}))
	defer tokenServer.Close()

	store := &fakeStore{
		grant:   domain.AgentGrant{UserID: "member-1", EncryptedRefreshToken: domain.SealedData{Ciphertext: []byte("sealed:refresh-token")}},
		grantOK: true,
	}
	manager, err := New(testConfig(tokenServer.URL), store, &fakeIdentityVerifier{}, &fakeSealer{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	accessToken, expiresIn, err := manager.MintToken(context.Background(), "member-1")
	if err != nil {
		t.Fatalf("MintToken() error = %v", err)
	}
	if accessToken != "new-access-token" || expiresIn != 3600 {
		t.Fatalf("access token = %q, expires in = %d", accessToken, expiresIn)
	}
	if store.replaced == nil || string(store.replaced.Ciphertext) != "sealed:rotated-refresh-token" {
		t.Fatalf("replaced token = %v", store.replaced)
	}
}

func TestMintTokenAnswersNotFoundWithoutGrant(t *testing.T) {
	manager, err := New(testConfig("https://identity.example.com"), &fakeStore{}, &fakeIdentityVerifier{}, &fakeSealer{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, _, err := manager.MintToken(context.Background(), "member-1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("MintToken() error = %v, want not found", err)
	}
}

func TestMintTokenDeletesGrantWhenCognitoRefusesTheRefreshToken(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(writer).Encode(tokenResponse{Error: "invalid_grant"})
	}))
	defer tokenServer.Close()

	store := &fakeStore{
		grant:   domain.AgentGrant{UserID: "member-1", EncryptedRefreshToken: domain.SealedData{Ciphertext: []byte("sealed:refresh-token")}},
		grantOK: true,
	}
	manager, err := New(testConfig(tokenServer.URL), store, &fakeIdentityVerifier{}, &fakeSealer{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, _, err := manager.MintToken(context.Background(), "member-1"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("MintToken() error = %v, want conflict", err)
	}
	if len(store.deleted) != 1 || store.deleted[0] != "member-1" {
		t.Fatalf("deleted = %v, want [member-1]", store.deleted)
	}
}

func TestMintTokenReturnsUnavailableWhenCognitoDoesNotAnswer(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	unreachable := tokenServer.URL
	tokenServer.Close() // closed before use, so the client fails to connect

	store := &fakeStore{
		grant:   domain.AgentGrant{UserID: "member-1", EncryptedRefreshToken: domain.SealedData{Ciphertext: []byte("sealed:refresh-token")}},
		grantOK: true,
	}
	manager, err := New(testConfig(unreachable), store, &fakeIdentityVerifier{}, &fakeSealer{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, _, err := manager.MintToken(context.Background(), "member-1"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("MintToken() error = %v, want unavailable", err)
	}
	if len(store.deleted) != 0 {
		t.Fatalf("deleted = %v, want none", store.deleted)
	}
}

func TestMintTokenRetriesWithATokenRotatedMeanwhileAndKeepsTheGrant(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			t.Fatalf("ParseForm() error = %v", err)
		}
		// Another mint already rotated refresh-token: Cognito refuses it and
		// accepts the token that mint stored.
		if request.Form.Get("refresh_token") != "rotated-refresh-token" {
			writer.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(writer).Encode(tokenResponse{Error: "invalid_grant"})
			return
		}
		_ = json.NewEncoder(writer).Encode(tokenResponse{AccessToken: "new-access-token", ExpiresIn: 3600})
	}))
	defer tokenServer.Close()

	store := &fakeStore{
		grant:   domain.AgentGrant{UserID: "member-1", EncryptedRefreshToken: domain.SealedData{Nonce: []byte("n1"), Ciphertext: []byte("sealed:refresh-token")}},
		grantOK: true,
		rotated: &domain.AgentGrant{UserID: "member-1", EncryptedRefreshToken: domain.SealedData{Nonce: []byte("n2"), Ciphertext: []byte("sealed:rotated-refresh-token")}},
	}
	manager, err := New(testConfig(tokenServer.URL), store, &fakeIdentityVerifier{}, &fakeSealer{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	accessToken, _, err := manager.MintToken(context.Background(), "member-1")
	if err != nil || accessToken != "new-access-token" {
		t.Fatalf("MintToken() = %q, %v, want the token the rotated grant mints", accessToken, err)
	}
	if len(store.deleted) != 0 {
		t.Fatalf("deleted = %v, want the rotated grant kept", store.deleted)
	}
}

func TestMintTokenKeepsTheGrantUnlessCognitoRefusesIt(t *testing.T) {
	for name, test := range map[string]struct {
		status int
		body   string
		want   error
	}{
		"Cognito fails":                    {http.StatusInternalServerError, `{"error":"internal_error"}`, domain.ErrUnavailable},
		"Cognito throttles":                {http.StatusTooManyRequests, `{"error":"too_many_requests"}`, domain.ErrUnavailable},
		"Cognito answers with no token":    {http.StatusOK, `{"expires_in":3600}`, domain.ErrUnavailable},
		"Cognito rejects the agent client": {http.StatusBadRequest, `{"error":"invalid_client"}`, nil},
	} {
		t.Run(name, func(t *testing.T) {
			tokenServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(test.body))
			}))
			defer tokenServer.Close()

			store := &fakeStore{
				grant:   domain.AgentGrant{UserID: "member-1", EncryptedRefreshToken: domain.SealedData{Ciphertext: []byte("sealed:refresh-token")}},
				grantOK: true,
			}
			manager, err := New(testConfig(tokenServer.URL), store, &fakeIdentityVerifier{}, &fakeSealer{})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			_, _, err = manager.MintToken(context.Background(), "member-1")
			if err == nil || errors.Is(err, domain.ErrConflict) || (test.want != nil && !errors.Is(err, test.want)) {
				t.Fatalf("MintToken() error = %v, want %v and never a conflict", err, test.want)
			}
			if len(store.deleted) != 0 {
				t.Fatalf("deleted = %v, want the grant kept", store.deleted)
			}
		})
	}
}
