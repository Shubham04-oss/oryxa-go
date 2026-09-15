package providers

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// testRSAKey is generated once and reused: RSA key generation is slow enough
// that doing it per test would noticeably slow the suite, and correctness
// here does not depend on a fresh key each time.
var testRSAKey = sync.OnceValue(func() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return key
})

func encodePKCS1(key *rsa.PrivateKey) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

func encodePKCS8(t *testing.T, key crypto.Signer) string {
	t.Helper()
	bytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: bytes}))
}

func TestParsePrivateKey(t *testing.T) {
	key := testRSAKey()
	if err := ParsePrivateKey(encodePKCS1(key)); err != nil {
		t.Fatalf("PKCS1 ParsePrivateKey() error = %v", err)
	}
	if err := ParsePrivateKey(encodePKCS8(t, key)); err != nil {
		t.Fatalf("PKCS8 ParsePrivateKey() error = %v", err)
	}
	if err := ParsePrivateKey(""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty ParsePrivateKey() error = %v, want %v", err, domain.ErrInvalid)
	}
	if err := ParsePrivateKey("not a pem key at all"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("garbage ParsePrivateKey() error = %v, want %v", err, domain.ErrInvalid)
	}
	wrongType := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not-a-cert")})
	if err := ParsePrivateKey(string(wrongType)); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("wrong PEM type ParsePrivateKey() error = %v, want %v", err, domain.ErrInvalid)
	}
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := ParsePrivateKey(encodePKCS8(t, edKey)); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("non-RSA PKCS8 ParsePrivateKey() error = %v, want %v", err, domain.ErrInvalid)
	}
}

// verifyAppJWT checks the shape GitHub requires -- an RS256-signed JWT with a
// fixed header -- and verifies the signature against the app's own public
// key, returning the decoded claims for the caller to check.
func verifyAppJWT(t *testing.T, authorization string, publicKey *rsa.PublicKey) map[string]any {
	t.Helper()
	token := strings.TrimPrefix(authorization, "Bearer ")
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT %q has %d parts, want 3", token, len(parts))
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(header) != `{"alg":"RS256","typ":"JWT"}` {
		t.Fatalf("JWT header = %s", header)
	}
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, sum[:], signature); err != nil {
		t.Fatalf("JWT signature did not verify: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

func TestInstallationAndInstallationToken(t *testing.T) {
	key := testRSAKey()
	app := GitHubApp{AppID: "987654", PrivateKey: encodePKCS1(key)}
	var gotClaims map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotClaims = verifyAppJWT(t, request.Header.Get("Authorization"), &key.PublicKey)
		if request.Header.Get("Accept") != "application/vnd.github+json" || request.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			t.Errorf("request headers = %v", request.Header)
		}
		id, suffix, _ := strings.Cut(strings.TrimPrefix(request.URL.Path, "/app/installations/"), "/")
		switch id {
		case "40400":
			http.NotFound(writer, request)
		case "40300":
			writer.WriteHeader(http.StatusForbidden)
		case "50000":
			writer.WriteHeader(http.StatusBadGateway)
		case "12345":
			switch {
			case suffix == "" && request.Method == http.MethodGet:
				_, _ = io.WriteString(writer, `{"id":12345,"account":{"id":1,"login":"acme-corp"}}`)
			case suffix == "access_tokens" && request.Method == http.MethodPost:
				writer.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(writer, `{"token":"ghs_installtoken","expires_at":"2026-09-14T13:00:00Z"}`)
			default:
				http.NotFound(writer, request)
			}
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	provider := GitHub()
	installation := *provider.Installation
	installation.APIURL = server.URL
	provider.Installation = &installation
	client := testClient(server)

	account, err := client.Installation(context.Background(), provider, app, "12345")
	if err != nil || account != (domain.ProviderAccount{ID: "12345", Name: "acme-corp"}) {
		t.Fatalf("Installation() = %+v %v", account, err)
	}
	if gotClaims["iss"] != "987654" {
		t.Fatalf("JWT iss = %v, want 987654", gotClaims["iss"])
	}
	iat, _ := gotClaims["iat"].(float64)
	exp, _ := gotClaims["exp"].(float64)
	if int64(iat) != testNow.Add(-60*time.Second).Unix() {
		t.Fatalf("JWT iat = %v, want %d", iat, testNow.Add(-60*time.Second).Unix())
	}
	if int64(exp) != testNow.Add(9*time.Minute).Unix() {
		t.Fatalf("JWT exp = %v, want %d", exp, testNow.Add(9*time.Minute).Unix())
	}

	grant, err := client.InstallationToken(context.Background(), provider, app, "12345")
	wantExpiry := time.Date(2026, time.September, 14, 13, 0, 0, 0, time.UTC)
	if err != nil || grant.AccessToken != "ghs_installtoken" || grant.TokenType != "Bearer" || !grant.Expiry.Equal(wantExpiry) {
		t.Fatalf("InstallationToken() = %+v %v", grant, err)
	}

	if _, err := client.Installation(context.Background(), provider, app, "40400"); !errors.Is(err, domain.ErrCredentialRejected) || !errors.Is(err, ErrInstallationNotFound) {
		t.Fatalf("404 Installation() error = %v", err)
	}
	if _, err := client.Installation(context.Background(), provider, app, "40300"); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("403 Installation() error = %v", err)
	}
	if _, err := client.Installation(context.Background(), provider, app, "50000"); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("502 Installation() error = %v", err)
	}
	if _, err := client.InstallationToken(context.Background(), provider, app, "40400"); !errors.Is(err, domain.ErrCredentialRejected) || !errors.Is(err, ErrInstallationNotFound) {
		t.Fatalf("404 InstallationToken() error = %v", err)
	}

	if _, err := client.Installation(context.Background(), provider, app, "not-digits"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("non-digit installation ID error = %v, want %v", err, domain.ErrInvalid)
	}
	if _, err := client.Installation(context.Background(), provider, app, strings.Repeat("1", 21)); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("over-long installation ID error = %v, want %v", err, domain.ErrInvalid)
	}
	if _, err := client.Installation(context.Background(), provider, GitHubApp{AppID: "not-digits", PrivateKey: app.PrivateKey}, "12345"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("non-digit app ID error = %v, want %v", err, domain.ErrInvalid)
	}
	garbageErr := func() error {
		_, err := client.Installation(context.Background(), provider, GitHubApp{AppID: "987654", PrivateKey: "garbage-key-material"}, "12345")
		return err
	}()
	if !errors.Is(garbageErr, domain.ErrInvalid) || strings.Contains(garbageErr.Error(), "garbage-key-material") {
		t.Fatalf("invalid private key error = %v", garbageErr)
	}
	if _, err := client.Installation(context.Background(), Google(), app, "12345"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unsupported provider error = %v, want %v", err, domain.ErrInvalid)
	}
}

func TestUserInstallations(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/user/installations" || request.Header.Get("Authorization") != "Bearer user-token" {
			t.Errorf("request = %s %v", request.URL.Path, request.Header)
		}
		var ids []string
		switch request.URL.Query().Get("page") {
		case "1":
			for i := 1; i <= 100; i++ {
				ids = append(ids, fmt.Sprintf(`{"id":%d}`, i))
			}
		case "2":
			ids = []string{`{"id":101}`}
		default:
			t.Errorf("unexpected page %q", request.URL.Query().Get("page"))
		}
		_, _ = io.WriteString(writer, `{"installations":[`+strings.Join(ids, ",")+`]}`)
	}))
	defer server.Close()

	provider := GitHub()
	installation := *provider.Installation
	installation.APIURL = server.URL
	provider.Installation = &installation
	client := testClient(server)

	ids, err := client.UserInstallations(context.Background(), provider, Grant{AccessToken: "user-token"})
	if err != nil || len(ids) != 101 || ids[0] != "1" || ids[100] != "101" {
		t.Fatalf("UserInstallations() = %v %v", ids, err)
	}
}

func TestUserInstallationsStopsAfterTenPages(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		atomic.AddInt32(&requests, 1)
		var ids []string
		for i := 1; i <= 100; i++ {
			ids = append(ids, fmt.Sprintf(`{"id":%d}`, i))
		}
		_, _ = io.WriteString(writer, `{"installations":[`+strings.Join(ids, ",")+`]}`)
	}))
	defer server.Close()

	provider := GitHub()
	installation := *provider.Installation
	installation.APIURL = server.URL
	provider.Installation = &installation
	client := testClient(server)

	ids, err := client.UserInstallations(context.Background(), provider, Grant{AccessToken: "user-token"})
	if err != nil || len(ids) != 1000 {
		t.Fatalf("UserInstallations() = %d ids, %v, want 1000 ids", len(ids), err)
	}
	if got := atomic.LoadInt32(&requests); got != 10 {
		t.Fatalf("requests = %d, want 10", got)
	}
}

func TestUserInstallationsRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	provider := GitHub()
	installation := *provider.Installation
	installation.APIURL = server.URL
	provider.Installation = &installation

	if _, err := testClient(server).UserInstallations(context.Background(), provider, Grant{AccessToken: "bad"}); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("UserInstallations() error = %v, want %v", err, domain.ErrCredentialRejected)
	}
}

func TestInstallationURL(t *testing.T) {
	provider := GitHub()
	value, err := (Client{}).InstallationURL(provider, InstallationRequest{AppSlug: "oryxa-app", State: "state-1"})
	if err != nil {
		t.Fatalf("InstallationURL() error = %v", err)
	}
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/apps/oryxa-app/installations/new" || parsed.Query().Get("state") != "state-1" {
		t.Fatalf("InstallationURL() = %q", value)
	}
	for _, slug := range []string{"", "Oryxa", "-oryxa", "oryxa_app", strings.Repeat("a", 101)} {
		if _, err := (Client{}).InstallationURL(provider, InstallationRequest{AppSlug: slug, State: "s"}); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("InstallationURL(%q) error = %v, want %v", slug, err, domain.ErrInvalid)
		}
	}
	if _, err := (Client{}).InstallationURL(Google(), InstallationRequest{AppSlug: "x", State: "s"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unsupported provider error = %v, want %v", err, domain.ErrInvalid)
	}
}
