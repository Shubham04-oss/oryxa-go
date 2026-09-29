package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionCacheReusesOnlyLiveTokenForMatchingAPI(t *testing.T) {
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
	path, err := sessionCachePath(api, getenv)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("session permissions: %v, %v", info, err)
	}
	var stdout, stderr bytes.Buffer
	client, err := signedInClient(context.Background(), options{api: api, noInput: true, json: true}, &stdout, &stderr, getenv)
	if err != nil || client.Token != session.AccessToken || !strings.Contains(stdout.String(), `"source":"cache"`) {
		t.Fatalf("cached session: client=%v, err=%v, stdout=%q, stderr=%q", client, err, stdout.String(), stderr.String())
	}
	_, otherFound, err := loadSession("https://other.example.com", getenv)
	if err != nil || otherFound {
		t.Fatalf("other API reused token: found=%v, err=%v", otherFound, err)
	}
	if _, err := signedInClient(context.Background(), options{api: api, noInput: true, forceLogin: true}, &stdout, &stderr, getenv); err == nil {
		t.Fatal("force-login reused the cached token")
	}
	if err := clearSession(api, getenv); err != nil {
		t.Fatal(err)
	}
	if _, found, err := loadSession(api, getenv); err != nil || found {
		t.Fatalf("logout kept session: found=%v, err=%v", found, err)
	}
}

func TestExpiredSessionRequiresSignInAndUnsafeFileIsRejected(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "sessions")
	getenv := func(key string) string {
		if key == "ORYXA_SESSION_DIR" {
			return directory
		}
		return ""
	}
	api := "https://api.example.com"
	if err := saveSession(api, cliSession{AccessToken: "soon-expired", ExpiresAt: time.Now().Add(time.Minute)}, getenv); err != nil {
		t.Fatal(err)
	}
	path, _ := sessionCachePath(api, getenv)
	if err := os.WriteFile(path, []byte(`{"access_token":"expired","expires_at":"2020-01-01T00:00:00Z"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := loadSession(api, getenv); err != nil || found {
		t.Fatalf("expired session accepted: found=%v, err=%v", found, err)
	}
	if err := os.WriteFile(path, []byte(`{"access_token":"unsafe","expires_at":"2099-01-01T00:00:00Z"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadSession(api, getenv); err == nil {
		t.Fatal("world-readable session file accepted")
	}
}
