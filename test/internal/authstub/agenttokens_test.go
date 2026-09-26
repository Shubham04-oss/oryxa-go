package authstub

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
	"github.com/Shubham04-oss/oryxa-go/test/identities"
)

// fakeServices lets a test control what Config.Services.Verify answers,
// without touching KMS. TestMintAgentTokenWithRealServiceAuthVerifier below
// exercises the real thing.
type fakeServices struct {
	claims serviceauth.Claims
	err    error
}

func (fake fakeServices) Verify(context.Context, string) (serviceauth.Claims, error) {
	return fake.claims, fake.err
}

func mintRequest(body, serviceToken string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/v1/internal/agent-tokens", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if serviceToken != "" {
		request.Header.Set(serviceauth.Header, serviceToken)
	}
	return request
}

func mint(config Config, body, serviceToken string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	NewHandler(config).ServeHTTP(response, mintRequest(body, serviceToken))
	return response
}

func TestMintAgentTokenSucceeds(t *testing.T) {
	config := Config{Services: fakeServices{claims: serviceauth.Claims{Service: "agents", Purpose: "agent"}}}
	response := mint(config, `{"user_id":"user-member"}`, "svc-token")

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body)
	}
	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.AccessToken != "agent:user-member" {
		t.Errorf("access_token = %q, want %q", body.AccessToken, "agent:user-member")
	}
	if body.ExpiresIn != agentTokenExpiresIn {
		t.Errorf("expires_in = %d, want %d", body.ExpiresIn, agentTokenExpiresIn)
	}
}

func TestMintAgentTokenRequiresServiceToken(t *testing.T) {
	config := Config{Services: fakeServices{}}
	response := mint(config, `{"user_id":"user-member"}`, "")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestMintAgentTokenRejectsWithoutConfiguredServices(t *testing.T) {
	response := mint(Config{}, `{"user_id":"user-member"}`, "svc-token")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestMintAgentTokenRejectsInvalidServiceToken(t *testing.T) {
	config := Config{Services: fakeServices{err: serviceauth.ErrInvalidToken}}
	response := mint(config, `{"user_id":"user-member"}`, "svc-token")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestMintAgentTokenRejectsDisallowedPurposeFromVerifier(t *testing.T) {
	config := Config{Services: fakeServices{err: serviceauth.ErrPurposeNotAllowed}}
	response := mint(config, `{"user_id":"user-member"}`, "svc-token")
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestMintAgentTokenRejectsWrongPurposeEvenIfCallerAllowsIt(t *testing.T) {
	// The service token verifies fine (its caller is allowed to use purpose
	// "cleanup" in general), but this route only ever accepts "agent".
	config := Config{Services: fakeServices{claims: serviceauth.Claims{Service: "agents", Purpose: "cleanup"}}}
	response := mint(config, `{"user_id":"user-member"}`, "svc-token")
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestMintAgentTokenAnswersServiceUnavailableWhenVerifyFails(t *testing.T) {
	config := Config{Services: fakeServices{err: errors.New("kms unavailable")}}
	response := mint(config, `{"user_id":"user-member"}`, "svc-token")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}

func TestMintAgentTokenRequiresUserID(t *testing.T) {
	config := Config{Services: fakeServices{claims: serviceauth.Claims{Service: "agents", Purpose: "agent"}}}
	response := mint(config, `{}`, "svc-token")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestMintAgentTokenAnswersNotFoundForUnknownUser(t *testing.T) {
	config := Config{Services: fakeServices{claims: serviceauth.Claims{Service: "agents", Purpose: "agent"}}}
	response := mint(config, `{"user_id":"user-ghost"}`, "svc-token")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestMintAgentTokenAnswersNotFoundForUngrantedMember(t *testing.T) {
	config := Config{
		Services:  fakeServices{claims: serviceauth.Claims{Service: "agents", Purpose: "agent"}},
		Ungranted: map[string]bool{"user-outsider": true},
	}
	response := mint(config, `{"user_id":"user-outsider"}`, "svc-token")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestConfigFromEnvWithoutKeyIDLeavesServicesNil(t *testing.T) {
	t.Setenv("AGENTS_PRIMARY_SERVICE_KEY_ID", "")
	config, err := ConfigFromEnv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if config.Services != nil {
		t.Fatal("Services should be nil without AGENTS_PRIMARY_SERVICE_KEY_ID")
	}
}

func TestConfigFromEnvParsesUngrantedMembers(t *testing.T) {
	t.Setenv("AGENTS_PRIMARY_SERVICE_KEY_ID", "")
	t.Setenv("AUTHSTUB_UNGRANTED_MEMBERS", "user-outsider, user-stranger")
	config, err := ConfigFromEnv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !config.Ungranted["user-outsider"] || !config.Ungranted["user-stranger"] {
		t.Fatalf("Ungranted = %v", config.Ungranted)
	}
}

// --- real internal/serviceauth crypto, against a fake KMS backed by a
// real P-256 key, proving this stand-in refuses bad tokens the way auth
// does rather than trusting the header at face value. ---

// fakeSignAPI signs with a real P-256 key the way KMS signs for an
// ECC_NIST_P256 key with ECDSA_SHA_256, mirroring
// internal/serviceauth's own test fake.
type fakeSignAPI struct{ key *ecdsa.PrivateKey }

func (f *fakeSignAPI) Sign(_ context.Context, input *kms.SignInput, _ ...func(*kms.Options)) (*kms.SignOutput, error) {
	digest := sha256.Sum256(input.Message)
	der, err := ecdsa.SignASN1(rand.Reader, f.key, digest[:])
	if err != nil {
		return nil, err
	}
	return &kms.SignOutput{Signature: der}, nil
}

// fakePublicKeyAPI answers GetPublicKey from an in-memory keyset, the way
// KMS answers it: a DER SubjectPublicKeyInfo.
type fakePublicKeyAPI struct{ keys map[string]*ecdsa.PublicKey }

func (f *fakePublicKeyAPI) GetPublicKey(_ context.Context, input *kms.GetPublicKeyInput, _ ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error) {
	key, ok := f.keys[*input.KeyId]
	if !ok {
		return nil, fmt.Errorf("fake kms: no such key %q", *input.KeyId)
	}
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return nil, err
	}
	return &kms.GetPublicKeyOutput{PublicKey: der}, nil
}

func TestMintAgentTokenWithRealServiceAuthVerifier(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := serviceauth.NewVerifier(serviceauth.VerifierConfig{
		Keys:     &fakePublicKeyAPI{keys: map[string]*ecdsa.PublicKey{"key-agents": &key.PublicKey}},
		Audience: serviceAudience,
		Callers:  []serviceauth.Caller{{Service: agentServiceCaller, KeyID: "key-agents", Purposes: []string{agentTokenPurpose}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	config := Config{Services: verifier}

	signer := serviceauth.Signer{Client: &fakeSignAPI{key: key}, KeyID: "key-agents", Service: agentServiceCaller}
	token, err := signer.Token(context.Background(), serviceAudience, agentTokenPurpose)
	if err != nil {
		t.Fatal(err)
	}

	response := mint(config, `{"user_id":"user-admin"}`, token)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body)
	}

	// A token signed by someone else's key must be refused, not trusted:
	// exactly what an attacker without the real KMS key would be stuck
	// with.
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	forgedSigner := serviceauth.Signer{Client: &fakeSignAPI{key: otherKey}, KeyID: "key-agents", Service: agentServiceCaller}
	forged, err := forgedSigner.Token(context.Background(), serviceAudience, agentTokenPurpose)
	if err != nil {
		t.Fatal(err)
	}
	response = mint(config, `{"user_id":"user-admin"}`, forged)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d for a token signed by the wrong key", response.Code, http.StatusUnauthorized)
	}
}

// --- accepting agent tokens wherever a member's own token is accepted ---

func TestOrganizationAcceptsAgentToken(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/organizations/org_sim", nil)
	request.Header.Set("Authorization", "Bearer "+agentTokenPrefix+identities.People["member"].UserID)
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body)
	}
	var body struct {
		Membership struct {
			UserID string `json:"user_id"`
			Role   string `json:"role"`
		} `json:"membership"`
		Agent bool `json:"agent"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Membership.UserID != "user-member" || body.Membership.Role != "member" {
		t.Fatalf("body = %+v", body)
	}
	if !body.Agent {
		t.Fatal("agent = false, want true for an agent token")
	}
}

func TestMembersAcceptsAgentTokenAndOmitsAgentField(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/organizations/org_sim/members", nil)
	request.Header.Set("Authorization", "Bearer "+agentTokenPrefix+identities.People["admin"].UserID)
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body)
	}
	if strings.Contains(response.Body.String(), `"agent"`) {
		t.Fatalf("members response carries an agent field: %s", response.Body)
	}
}

func TestOrganizationRejectsAgentTokenForUnknownUser(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/organizations/org_sim", nil)
	request.Header.Set("Authorization", "Bearer "+agentTokenPrefix+"user-ghost")
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestOrganizationConcealsStrangerAgentToken(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/organizations/org_sim", nil)
	request.Header.Set("Authorization", "Bearer "+agentTokenPrefix+identities.People["stranger"].UserID)
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (agent token is real but stranger is not in the org)", response.Code, http.StatusNotFound)
	}
}

func TestCookieAgentTokenIsAccepted(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/organizations/org_sim", nil)
	request.AddCookie(&http.Cookie{Name: identities.AccessCookie, Value: agentTokenPrefix + identities.People["owner"].UserID})
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body)
	}
}

func TestAgentTokenAllowedPatternsMirrorAuth(t *testing.T) {
	cases := []struct {
		pattern string
		want    bool
	}{
		{"GET /v1/auth/me", true},
		{"GET /v1/organizations", true},
		{"GET /v1/organizations/{organizationID}", true},
		{"GET /v1/organizations/{organizationID}/members", true},
		{"PATCH /v1/organizations/{organizationID}", false},
		{"DELETE /v1/organizations/{organizationID}", false},
		{"POST /v1/organizations/{organizationID}/invitations", false},
		{"DELETE /v1/organizations/{organizationID}/members/{userID}", false},
		{"POST /v1/internal/agent-tokens", false},
	}
	for _, tc := range cases {
		if got := agentTokenAllowed(tc.pattern); got != tc.want {
			t.Errorf("agentTokenAllowed(%q) = %v, want %v", tc.pattern, got, tc.want)
		}
	}
}
