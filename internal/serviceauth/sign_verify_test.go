package serviceauth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/kms"
)

// fakeSignAPI signs with a real P-256 key the way KMS signs for an
// ECC_NIST_P256 key with ECDSA_SHA_256: it hashes the message itself and
// returns an ASN.1 DER signature.
type fakeSignAPI struct {
	key *ecdsa.PrivateKey
}

func (f *fakeSignAPI) Sign(_ context.Context, input *kms.SignInput, _ ...func(*kms.Options)) (*kms.SignOutput, error) {
	digest := sha256.Sum256(input.Message)
	der, err := ecdsa.SignASN1(rand.Reader, f.key, digest[:])
	if err != nil {
		return nil, err
	}
	return &kms.SignOutput{Signature: der}, nil
}

// fakePublicKeyAPI answers GetPublicKey from an in-memory keyset, the way
// KMS answers it: a DER SubjectPublicKeyInfo. It counts calls per key ID so
// tests can assert on caching, and can be made to fail like an unavailable
// KMS.
type fakePublicKeyAPI struct {
	keys map[string]*ecdsa.PublicKey
	err  error

	mu    sync.Mutex
	calls map[string]int
}

func newFakePublicKeyAPI(keys map[string]*ecdsa.PublicKey) *fakePublicKeyAPI {
	return &fakePublicKeyAPI{keys: keys, calls: make(map[string]int)}
}

func (f *fakePublicKeyAPI) GetPublicKey(_ context.Context, input *kms.GetPublicKeyInput, _ ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error) {
	f.mu.Lock()
	f.calls[*input.KeyId]++
	f.mu.Unlock()

	if f.err != nil {
		return nil, f.err
	}
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

func (f *fakePublicKeyAPI) callCount(keyID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[keyID]
}

func generateKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestSignVerifyRoundTrip(t *testing.T) {
	t.Parallel()
	key := generateKey(t)
	fixedNow := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

	signer := Signer{
		Client:  &fakeSignAPI{key: key},
		KeyID:   "key-integrations",
		Service: "integrations",
		Now:     func() time.Time { return fixedNow },
	}
	token, err := signer.Token(context.Background(), "connections", "action")
	if err != nil {
		t.Fatalf("Token: %v", err)
	}

	publicKeys := newFakePublicKeyAPI(map[string]*ecdsa.PublicKey{"key-integrations": &key.PublicKey})
	verifier, err := NewVerifier(VerifierConfig{
		Keys:     publicKeys,
		Audience: "connections",
		Callers: []Caller{
			{Service: "integrations", KeyID: "key-integrations", Purposes: []string{"action", "trigger", "cleanup"}},
		},
		Now: func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	claims, err := verifier.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Service != "integrations" {
		t.Errorf("Service = %q, want integrations", claims.Service)
	}
	if claims.Audience != "connections" {
		t.Errorf("Audience = %q, want connections", claims.Audience)
	}
	if claims.Purpose != "action" {
		t.Errorf("Purpose = %q, want action", claims.Purpose)
	}
	if claims.ID == "" {
		t.Error("ID is empty")
	}
	if !claims.IssuedAt.Equal(fixedNow) {
		t.Errorf("IssuedAt = %v, want %v", claims.IssuedAt, fixedNow)
	}
	if want := fixedNow.Add(MaxLifetime); !claims.Expiry.Equal(want) {
		t.Errorf("Expiry = %v, want %v", claims.Expiry, want)
	}
}

func TestSignerTokenValidatesNames(t *testing.T) {
	t.Parallel()
	key := generateKey(t)

	badService := Signer{Client: &fakeSignAPI{key: key}, KeyID: "k1", Service: "Bad Service"}
	if _, err := badService.Token(context.Background(), "connections", "action"); err == nil {
		t.Error("expected error for invalid service name")
	}

	signer := Signer{Client: &fakeSignAPI{key: key}, KeyID: "k1", Service: "integrations"}
	if _, err := signer.Token(context.Background(), "Bad Audience", "action"); err == nil {
		t.Error("expected error for invalid audience")
	}
	if _, err := signer.Token(context.Background(), "connections", "Bad Purpose"); err == nil {
		t.Error("expected error for invalid purpose")
	}
}

func TestSignerTokenDefaultsNowAndRandom(t *testing.T) {
	t.Parallel()
	key := generateKey(t)
	signer := Signer{Client: &fakeSignAPI{key: key}, KeyID: "k1", Service: "integrations"}

	before := time.Now().Add(-time.Second)
	token, err := signer.Token(context.Background(), "connections", "action")
	after := time.Now().Add(time.Second)
	if err != nil {
		t.Fatal(err)
	}

	parts := strings.Split(token, ".")
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		IssuedAt int64 `json:"iat"`
	}
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		t.Fatal(err)
	}
	issuedAt := time.Unix(claims.IssuedAt, 0)
	if issuedAt.Before(before) || issuedAt.After(after) {
		t.Fatalf("iat = %v, want between %v and %v", issuedAt, before, after)
	}
}

func TestNewVerifierValidation(t *testing.T) {
	t.Parallel()

	if _, err := NewVerifier(VerifierConfig{}); err == nil {
		t.Error("expected error for missing audience")
	}
	if _, err := NewVerifier(VerifierConfig{
		Audience: "connections",
		Callers:  []Caller{{Service: "a", KeyID: "k", Purposes: []string{"x"}}},
	}); err == nil {
		t.Error("expected error when callers are configured without Keys")
	}
	if _, err := NewVerifier(VerifierConfig{
		Audience: "connections",
		Keys:     newFakePublicKeyAPI(nil),
		Callers: []Caller{
			{Service: "a", KeyID: "k1", Purposes: []string{"x"}},
			{Service: "a", KeyID: "k2", Purposes: []string{"y"}},
		},
	}); err == nil {
		t.Error("expected error for duplicate caller service")
	}
	if _, err := NewVerifier(VerifierConfig{Audience: "connections"}); err != nil {
		t.Errorf("verifier with no callers should be valid: %v", err)
	}
}

func TestVerifyCachesPublicKeyAcrossConcurrentVerifications(t *testing.T) {
	t.Parallel()
	key := generateKey(t)
	fixedNow := time.Now()
	signer := Signer{Client: &fakeSignAPI{key: key}, KeyID: "k1", Service: "integrations", Now: func() time.Time { return fixedNow }}
	token, err := signer.Token(context.Background(), "connections", "action")
	if err != nil {
		t.Fatal(err)
	}

	publicKeys := newFakePublicKeyAPI(map[string]*ecdsa.PublicKey{"k1": &key.PublicKey})
	verifier, err := NewVerifier(VerifierConfig{
		Keys:     publicKeys,
		Audience: "connections",
		Callers:  []Caller{{Service: "integrations", KeyID: "k1", Purposes: []string{"action"}}},
		Now:      func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatal(err)
	}

	// One verification up front to prove the cache also holds across
	// sequential calls, then a burst of concurrent ones for -race.
	if _, err := verifier.Verify(context.Background(), token); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if _, err := verifier.Verify(context.Background(), token); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	if got := publicKeys.callCount("k1"); got != 1 {
		t.Fatalf("GetPublicKey calls = %d, want 1", got)
	}
}

func TestVerifyPublicKeyFetchFailureIsNotErrInvalidToken(t *testing.T) {
	t.Parallel()
	key := generateKey(t)
	fixedNow := time.Now()
	signer := Signer{Client: &fakeSignAPI{key: key}, KeyID: "k1", Service: "integrations", Now: func() time.Time { return fixedNow }}
	token, err := signer.Token(context.Background(), "connections", "action")
	if err != nil {
		t.Fatal(err)
	}

	publicKeys := newFakePublicKeyAPI(nil)
	publicKeys.err = errors.New("kms unavailable")
	verifier, err := NewVerifier(VerifierConfig{
		Keys:     publicKeys,
		Audience: "connections",
		Callers:  []Caller{{Service: "integrations", KeyID: "k1", Purposes: []string{"action"}}},
		Now:      func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = verifier.Verify(context.Background(), token)
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, ErrInvalidToken) {
		t.Fatalf("error wraps ErrInvalidToken: %v", err)
	}
	if !strings.Contains(err.Error(), "kms unavailable") {
		t.Fatalf("error = %v, want it to wrap the KMS failure", err)
	}
}

func TestVerifyRejectsSignatureFromAnotherCallersKey(t *testing.T) {
	t.Parallel()
	keyA := generateKey(t)
	keyB := generateKey(t)
	fixedNow := time.Now()

	// signerB claims to be "a" but actually holds "b"'s private key -
	// exactly what an attacker without "a"'s KMS key would be stuck with.
	signerB := Signer{Client: &fakeSignAPI{key: keyB}, KeyID: "key-b", Service: "a", Now: func() time.Time { return fixedNow }}
	token, err := signerB.Token(context.Background(), "connections", "action")
	if err != nil {
		t.Fatal(err)
	}

	publicKeys := newFakePublicKeyAPI(map[string]*ecdsa.PublicKey{
		"key-a": &keyA.PublicKey,
		"key-b": &keyB.PublicKey,
	})
	verifier, err := NewVerifier(VerifierConfig{
		Keys:     publicKeys,
		Audience: "connections",
		Callers: []Caller{
			{Service: "a", KeyID: "key-a", Purposes: []string{"action"}},
			{Service: "b", KeyID: "key-b", Purposes: []string{"action"}},
		},
		Now: func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := verifier.Verify(context.Background(), token); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err = %v, want ErrInvalidToken", err)
	}
}

func TestVerifyRejectsDisallowedPurpose(t *testing.T) {
	t.Parallel()
	key := generateKey(t)
	fixedNow := time.Now()
	signer := Signer{Client: &fakeSignAPI{key: key}, KeyID: "k1", Service: "integrations", Now: func() time.Time { return fixedNow }}
	token, err := signer.Token(context.Background(), "connections", "delete-everything")
	if err != nil {
		t.Fatal(err)
	}

	publicKeys := newFakePublicKeyAPI(map[string]*ecdsa.PublicKey{"k1": &key.PublicKey})
	verifier, err := NewVerifier(VerifierConfig{
		Keys:     publicKeys,
		Audience: "connections",
		Callers:  []Caller{{Service: "integrations", KeyID: "k1", Purposes: []string{"action", "trigger", "cleanup"}}},
		Now:      func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = verifier.Verify(context.Background(), token)
	if !errors.Is(err, ErrPurposeNotAllowed) {
		t.Fatalf("err = %v, want ErrPurposeNotAllowed", err)
	}
	if errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err also wraps ErrInvalidToken: %v", err)
	}
}

func TestVerifyRejectsTamperedClaims(t *testing.T) {
	t.Parallel()
	key := generateKey(t)
	fixedNow := time.Now()
	signer := Signer{Client: &fakeSignAPI{key: key}, KeyID: "k1", Service: "integrations", Now: func() time.Time { return fixedNow }}
	token, err := signer.Token(context.Background(), "connections", "action")
	if err != nil {
		t.Fatal(err)
	}

	parts := strings.Split(token, ".")
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		t.Fatal(err)
	}
	claims["purpose"] = "cleanup" // signed claims said "action"
	tamperedBytes, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	tamperedToken := parts[0] + "." + base64.RawURLEncoding.EncodeToString(tamperedBytes) + "." + parts[2]

	publicKeys := newFakePublicKeyAPI(map[string]*ecdsa.PublicKey{"k1": &key.PublicKey})
	verifier, err := NewVerifier(VerifierConfig{
		Keys:     publicKeys,
		Audience: "connections",
		Callers:  []Caller{{Service: "integrations", KeyID: "k1", Purposes: []string{"action", "cleanup"}}},
		Now:      func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := verifier.Verify(context.Background(), tamperedToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err = %v, want ErrInvalidToken", err)
	}
}

// rawSegment base64url-encodes a literal header or claims string so tests
// can hand-craft tokens the Signer would never produce.
func rawSegment(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

func encodeSegment(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(data)
}

func signSegments(t *testing.T, key *ecdsa.PrivateKey, headerSeg, claimsSeg string) string {
	t.Helper()
	digest := sha256.Sum256([]byte(headerSeg + "." + claimsSeg))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return base64.RawURLEncoding.EncodeToString(sig)
}

func TestVerifyRejectsMalformedAndOutOfPolicyTokens(t *testing.T) {
	t.Parallel()
	key := generateKey(t)
	fixedNow := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

	publicKeys := newFakePublicKeyAPI(map[string]*ecdsa.PublicKey{"k1": &key.PublicKey})
	verifier, err := NewVerifier(VerifierConfig{
		Keys:     publicKeys,
		Audience: "connections",
		Callers:  []Caller{{Service: "integrations", KeyID: "k1", Purposes: []string{"action"}}},
		Now:      func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatal(err)
	}

	validClaims := func(overrides map[string]any) map[string]any {
		claims := map[string]any{
			"iss":     "integrations",
			"aud":     "connections",
			"purpose": "action",
			"iat":     fixedNow.Unix(),
			"exp":     fixedNow.Add(MaxLifetime).Unix(),
			"jti":     "test-id",
		}
		for k, v := range overrides {
			if v == nil {
				delete(claims, k)
				continue
			}
			claims[k] = v
		}
		return claims
	}

	build := func(header string, claims map[string]any, sign bool) string {
		headerSeg := rawSegment(header)
		claimsSeg := encodeSegment(t, claims)
		sigSeg := base64.RawURLEncoding.EncodeToString(make([]byte, 64))
		if sign {
			sigSeg = signSegments(t, key, headerSeg, claimsSeg)
		}
		return headerSeg + "." + claimsSeg + "." + sigSeg
	}

	const validHeader = `{"alg":"ES256","typ":"JWT"}`

	cases := []struct {
		name  string
		token string
	}{
		{
			"expired",
			build(validHeader, validClaims(map[string]any{
				"iat": fixedNow.Add(-2 * time.Minute).Unix(),
				"exp": fixedNow.Add(-31 * time.Second).Unix(),
			}), true),
		},
		{
			"iat in the future beyond leeway",
			build(validHeader, validClaims(map[string]any{
				"iat": fixedNow.Add(40 * time.Second).Unix(),
				"exp": fixedNow.Add(50 * time.Second).Unix(),
			}), true),
		},
		{
			"lifetime over max, hand-signed",
			build(validHeader, validClaims(map[string]any{
				"iat": fixedNow.Unix(),
				"exp": fixedNow.Add(90 * time.Second).Unix(),
			}), true),
		},
		{"wrong audience", build(validHeader, validClaims(map[string]any{"aud": "someone-else"}), true)},
		{"audience as array", build(validHeader, validClaims(map[string]any{"aud": []string{"connections"}}), true)},
		{"unknown service", build(validHeader, validClaims(map[string]any{"iss": "ghost"}), false)},
		{"missing iat", build(validHeader, validClaims(map[string]any{"iat": nil}), true)},
		{"missing exp", build(validHeader, validClaims(map[string]any{"exp": nil}), true)},
		{"alg none", build(`{"alg":"none","typ":"JWT"}`, validClaims(nil), false)},
		{"alg HS256", build(`{"alg":"HS256","typ":"JWT"}`, validClaims(nil), false)},
		{"crit header", build(`{"alg":"ES256","typ":"JWT","crit":["exp"]}`, validClaims(nil), true)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := verifier.Verify(context.Background(), tc.token); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("err = %v, want ErrInvalidToken", err)
			}
		})
	}
}
