package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

type fakeCredentialStore struct {
	values      map[string]string
	unavailable bool
}

func (store *fakeCredentialStore) Get(service, account string) (string, error) {
	if store.unavailable {
		return "", errors.New("unavailable")
	}
	value, ok := store.values[service+account]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return value, nil
}
func (store *fakeCredentialStore) Set(service, account, value string) error {
	if store.unavailable {
		return errors.New("unavailable")
	}
	store.values[service+account] = value
	return nil
}
func (store *fakeCredentialStore) Delete(service, account string) error {
	delete(store.values, service+account)
	return nil
}

// Never read the developer's credential store or legacy token cache in tests.
func TestMain(m *testing.M) {
	directory, err := os.MkdirTemp("", "oryxa-cli-tests-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(directory)
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "APPDATA", "LOCALAPPDATA"} {
		os.Setenv(name, directory)
	}
	sessionStore = &fakeCredentialStore{values: map[string]string{}}
	http.DefaultTransport = localTestTransport{http.DefaultTransport}
	code := m.Run()
	os.RemoveAll(directory)
	os.Exit(code)
}

func testSessionStore(t *testing.T) *fakeCredentialStore {
	t.Helper()
	previous := sessionStore
	store := &fakeCredentialStore{values: map[string]string{}}
	sessionStore = store
	memorySessions.values = map[string]cliSession{}
	t.Cleanup(func() { sessionStore = previous; memorySessions.values = map[string]cliSession{} })
	return store
}

func TestSessionCacheReusesOnlyLiveTokenForMatchingAPI(t *testing.T) {
	store := testSessionStore(t)
	directory := filepath.Join(t.TempDir(), "sessions")
	getenv := func(key string) string {
		if key == "ORYXA_SESSION_DIR" {
			return directory
		}
		return ""
	}
	api := "https://api.example.com"
	session := cliSession{AccessToken: "short-lived-token", ExpiresAt: time.Now().Add(15 * time.Minute)}
	if err := saveSession(api, session, getenv); err != nil {
		t.Fatal(err)
	}
	path, _ := sessionCachePath(api, getenv)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("plaintext cache was written: %v", err)
	}
	if len(store.values) != 1 {
		t.Fatal("session not saved in credential store")
	}
	memorySessions.values = map[string]cliSession{} // Simulate a fresh process.
	var stdout, stderr bytes.Buffer
	client, err := signedInClient(context.Background(), options{api: api, noInput: true, json: true}, &stdout, &stderr, getenv)
	if err != nil || client.Token != session.AccessToken {
		t.Fatalf("secure session not reused: %v", err)
	}
	if _, found, err := loadSession("https://other.example.com", getenv); err != nil || found {
		t.Fatalf("cross-API session reuse: %v %v", found, err)
	}
	if _, err := signedInClient(context.Background(), options{api: api, noInput: true, forceLogin: true}, &stdout, &stderr, getenv); err == nil {
		t.Fatal("force-login reused token")
	}
	if err := clearSession(api, getenv); err != nil {
		t.Fatal(err)
	}
	if _, found, err := loadSession(api, getenv); err != nil || found {
		t.Fatalf("logout kept session: %v %v", found, err)
	}
}

func TestLegacySessionIsPurgedWithoutBeingRead(t *testing.T) {
	testSessionStore(t)
	directory := t.TempDir()
	getenv := func(key string) string {
		if key == "ORYXA_SESSION_DIR" {
			return directory
		}
		return ""
	}
	api := "https://api.example.com"
	path, _ := sessionCachePath(api, getenv)
	if err := os.WriteFile(path, []byte(`{"access_token":"plaintext","expires_at":"2099-01-01T00:00:00Z"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := loadSession(api, getenv); err != nil || found {
		t.Fatalf("legacy token reused: %v %v", found, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("legacy plaintext not removed: %v", err)
	}
}

func TestSecureStoreUnavailableUsesOnlyMemory(t *testing.T) {
	store := testSessionStore(t)
	store.unavailable = true
	getenv := func(key string) string {
		if key == "ORYXA_SESSION_DIR" {
			return t.TempDir()
		}
		return ""
	}
	api := "https://api.example.com"
	if err := saveSession(api, cliSession{AccessToken: "memory", ExpiresAt: time.Now().Add(time.Minute)}, getenv); err == nil || !strings.Contains(err.Error(), "memory only") {
		t.Fatalf("secure store failure: %v", err)
	}
	if session, found, err := loadSession(api, getenv); err != nil || !found || session.AccessToken != "memory" {
		t.Fatalf("memory session missing: %v %v", found, err)
	}
	if len(store.values) != 0 {
		t.Fatal("unavailable store persisted credential")
	}
}

func TestLongApplyDoesNotReuseAlmostExpiredSession(t *testing.T) {
	testSessionStore(t)
	getenv := func(string) string { return "" }
	api := "https://api.example.com"
	if err := saveSession(api, cliSession{AccessToken: "short-lived", ExpiresAt: time.Now().Add(time.Minute)}, getenv); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	_, err := signedInClient(context.Background(), options{api: api, noInput: true, minimumSessionTTL: 4 * time.Minute}, &stdout, &stderr, getenv)
	var human *humanError
	if !errors.As(err, &human) {
		t.Fatalf("almost expired token reused for long apply: %v", err)
	}
}

type localTestTransport struct{ http.RoundTripper }

func (transport localTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	hostname := request.URL.Hostname()
	if ip := net.ParseIP(hostname); hostname != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("test blocked non-loopback HTTP request")
	}
	return transport.RoundTripper.RoundTrip(request)
}
func TestCLIIsHermeticWithPopulatedDefaultCache(t *testing.T) {
	testSessionStore(t)
	getenv := func(string) string { return "" }
	if err := saveSession("https://api.oryxa.in", cliSession{AccessToken: "fake-cached-token", ExpiresAt: time.Now().Add(time.Minute)}, getenv); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"whoami", "--no-input", "--json"}, &stdout, &stderr, getenv)
	if code != 1 || !strings.Contains(stdout.String(), "test blocked non-loopback HTTP request") {
		t.Fatalf("test might reach production: exit%d %s %s", code, stdout.String(), stderr.String())
	}
}
