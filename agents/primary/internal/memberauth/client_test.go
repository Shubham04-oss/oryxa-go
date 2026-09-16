package memberauth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// serviceKey stands in for agents' KMS asymmetric key: it signs like
// kms:Sign and publishes its public key like kms:GetPublicKey, exactly as
// connections/internal/e2e does for the services it fakes.
type serviceKey struct{ key *ecdsa.PrivateKey }

func (key serviceKey) Sign(_ context.Context, input *kms.SignInput, _ ...func(*kms.Options)) (*kms.SignOutput, error) {
	digest := sha256.Sum256(input.Message)
	signature, err := ecdsa.SignASN1(rand.Reader, key.key, digest[:])
	return &kms.SignOutput{Signature: signature, KeyId: input.KeyId, SigningAlgorithm: input.SigningAlgorithm}, err
}

func (key serviceKey) GetPublicKey(_ context.Context, input *kms.GetPublicKeyInput, _ ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error) {
	der, err := x509.MarshalPKIXPublicKey(&key.key.PublicKey)
	return &kms.GetPublicKeyOutput{PublicKey: der, KeyId: input.KeyId}, err
}

func newTestSigner(t *testing.T) (serviceauth.Signer, *serviceauth.Verifier) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key := serviceKey{key: privateKey}
	verifier, err := serviceauth.NewVerifier(serviceauth.VerifierConfig{
		Keys:     key,
		Audience: "auth",
		Callers:  []serviceauth.Caller{{Service: "agents", KeyID: "alias/agents-identity", Purposes: []string{"agent"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return serviceauth.Signer{Client: key, KeyID: "alias/agents-identity", Service: "agents"}, verifier
}

// fakeAuth is a minimal stand-in for auth's POST /v1/internal/agent-tokens,
// recording every request it answers and letting a test script its
// responses.
type fakeAuth struct {
	t        *testing.T
	verifier *serviceauth.Verifier

	mu       sync.Mutex
	requests []fakeAuthRequest
	status   int
	body     any
}

type fakeAuthRequest struct {
	path        string
	userID      string
	serviceAuth string
}

func newFakeAuth(t *testing.T, verifier *serviceauth.Verifier) *fakeAuth {
	return &fakeAuth{t: t, verifier: verifier, status: http.StatusOK, body: map[string]any{"access_token": "minted-token", "expires_in": 3600}}
}

func (f *fakeAuth) setResponse(status int, body any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body = status, body
}

func (f *fakeAuth) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeAuth) lastRequest() fakeAuthRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[len(f.requests)-1]
}

func (f *fakeAuth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	var decoded struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		f.t.Fatal(err)
	}

	serviceToken := r.Header.Get(serviceauth.Header)
	claims, err := f.verifier.Verify(r.Context(), serviceToken)
	if err != nil {
		f.t.Errorf("service token did not verify: %v", err)
	} else if claims.Service != "agents" || claims.Audience != "auth" || claims.Purpose != "agent" {
		f.t.Errorf("service token claims = %+v, want service agents, audience auth, purpose agent", claims)
	}

	f.mu.Lock()
	f.requests = append(f.requests, fakeAuthRequest{path: r.URL.Path, userID: decoded.UserID, serviceAuth: serviceToken})
	status, respBody := f.status, f.body
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if respBody != nil {
		_ = json.NewEncoder(w).Encode(respBody)
	}
}

func TestAuthClientRequestShape(t *testing.T) {
	signer, verifier := newTestSigner(t)
	fake := newFakeAuth(t, verifier)
	server := httptest.NewServer(fake)
	defer server.Close()

	client := NewAuthClient(AuthClientConfig{BaseURL: server.URL, Signer: signer})
	token, expiresAt, err := client.AgentToken(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("AgentToken() error = %v", err)
	}
	if token != "minted-token" {
		t.Errorf("token = %q, want %q", token, "minted-token")
	}
	if expiresAt.Before(time.Now().Add(59 * time.Minute)) {
		t.Errorf("expiresAt = %v, want about an hour out", expiresAt)
	}

	if fake.requestCount() != 1 {
		t.Fatalf("requests = %d, want 1", fake.requestCount())
	}
	got := fake.lastRequest()
	if got.path != "/v1/internal/agent-tokens" {
		t.Errorf("path = %q, want /v1/internal/agent-tokens", got.path)
	}
	if got.userID != "user-1" {
		t.Errorf("body user_id = %q, want user-1", got.userID)
	}
	if got.serviceAuth == "" {
		t.Error("no service token header was sent")
	}
}

func TestAuthClientMapsGrantRequired(t *testing.T) {
	signer, verifier := newTestSigner(t)
	for _, status := range []int{http.StatusNotFound, http.StatusConflict} {
		fake := newFakeAuth(t, verifier)
		fake.setResponse(status, map[string]string{"error": "no grant"})
		server := httptest.NewServer(fake)

		client := NewAuthClient(AuthClientConfig{BaseURL: server.URL, Signer: signer})
		if _, _, err := client.AgentToken(context.Background(), "user-1"); !errors.Is(err, ErrGrantRequired) {
			t.Errorf("status %d: AgentToken() error = %v, want ErrGrantRequired", status, err)
		}
		server.Close()
	}
}

func TestAuthClientMapsServerErrorsToUnavailable(t *testing.T) {
	signer, verifier := newTestSigner(t)
	fake := newFakeAuth(t, verifier)
	fake.setResponse(http.StatusServiceUnavailable, map[string]string{"error": "try later"})
	server := httptest.NewServer(fake)
	defer server.Close()

	client := NewAuthClient(AuthClientConfig{BaseURL: server.URL, Signer: signer})
	if _, _, err := client.AgentToken(context.Background(), "user-1"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("AgentToken() error = %v, want ErrUnavailable", err)
	}
}

func TestAuthClientUnreachableIsUnavailable(t *testing.T) {
	signer, _ := newTestSigner(t)
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Close() // closed before any request reaches it

	client := NewAuthClient(AuthClientConfig{BaseURL: server.URL, Signer: signer})
	if _, _, err := client.AgentToken(context.Background(), "user-1"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("AgentToken() error = %v, want ErrUnavailable", err)
	}
}

func TestAuthClientCachesUntilTheRefreshWindow(t *testing.T) {
	signer, verifier := newTestSigner(t)
	fake := newFakeAuth(t, verifier)
	fake.setResponse(http.StatusOK, map[string]any{"access_token": "token-1", "expires_in": 600}) // 10 minutes
	server := httptest.NewServer(fake)
	defer server.Close()

	now := time.Now()
	client := NewAuthClient(AuthClientConfig{BaseURL: server.URL, Signer: signer, Now: func() time.Time { return now }})

	first, _, err := client.AgentToken(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("AgentToken() error = %v", err)
	}
	if first != "token-1" {
		t.Fatalf("token = %q, want token-1", first)
	}

	// Still well inside the window: no second request.
	now = now.Add(2 * time.Minute)
	second, _, err := client.AgentToken(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("AgentToken() error = %v", err)
	}
	if second != "token-1" || fake.requestCount() != 1 {
		t.Fatalf("a call inside the window refreshed: token = %q, requests = %d", second, fake.requestCount())
	}

	// Past 2 minutes before the 10-minute expiry (i.e. past the 8-minute mark):
	// the cache must refresh.
	fake.setResponse(http.StatusOK, map[string]any{"access_token": "token-2", "expires_in": 600})
	now = now.Add(7 * time.Minute) // t+9m, inside the last 2 minutes before t+10m
	third, _, err := client.AgentToken(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("AgentToken() error = %v", err)
	}
	if third != "token-2" {
		t.Fatalf("token past the refresh window = %q, want token-2 (a refresh)", third)
	}
	if fake.requestCount() != 2 {
		t.Fatalf("requests = %d, want 2 (one refresh)", fake.requestCount())
	}
}

func TestAuthClientConcurrentCallersShareOneColdRequest(t *testing.T) {
	signer, verifier := newTestSigner(t)
	fake := newFakeAuth(t, verifier)
	server := httptest.NewServer(fake)
	defer server.Close()

	client := NewAuthClient(AuthClientConfig{BaseURL: server.URL, Signer: signer})

	const callers = 20
	var wg sync.WaitGroup
	var failures atomic.Int64
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			token, _, err := client.AgentToken(context.Background(), "user-1")
			if err != nil || token != "minted-token" {
				failures.Add(1)
			}
		}()
	}
	wg.Wait()

	if failures.Load() != 0 {
		t.Fatalf("%d of %d concurrent callers failed", failures.Load(), callers)
	}
	if fake.requestCount() != 1 {
		t.Fatalf("requests = %d, want 1: concurrent callers for an uncached user should share one request", fake.requestCount())
	}
}

func TestAuthClientConcurrentCallersForDifferentUsers(t *testing.T) {
	signer, verifier := newTestSigner(t)
	fake := newFakeAuth(t, verifier)
	server := httptest.NewServer(fake)
	defer server.Close()

	client := NewAuthClient(AuthClientConfig{BaseURL: server.URL, Signer: signer})

	const users = 10
	var wg sync.WaitGroup
	var failures atomic.Int64
	wg.Add(users)
	for i := 0; i < users; i++ {
		userID := "user-" + string(rune('a'+i))
		go func() {
			defer wg.Done()
			if _, _, err := client.AgentToken(context.Background(), userID); err != nil {
				failures.Add(1)
			}
		}()
	}
	wg.Wait()

	if failures.Load() != 0 {
		t.Fatalf("%d of %d concurrent callers failed", failures.Load(), users)
	}
	if fake.requestCount() != users {
		t.Fatalf("requests = %d, want %d (one per distinct user)", fake.requestCount(), users)
	}
}
