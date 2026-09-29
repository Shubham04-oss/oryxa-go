package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoginChallengeRejectsExpiredOrMalformedServerState(t *testing.T) {
	valid := loginChallenge{State: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("s", 32))), Nonce: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("n", 32))), ExpiresAt: time.Now().Add(20 * time.Minute)}
	for _, test := range []struct {
		name string
		edit func(*loginChallenge)
	}{
		{"expired", func(c *loginChallenge) { c.ExpiresAt = time.Now().Add(-time.Second) }},
		{"unbounded", func(c *loginChallenge) { c.ExpiresAt = time.Now().Add(time.Hour) }},
		{"missing_expiry", func(c *loginChallenge) { c.ExpiresAt = time.Time{} }},
		{"short_state", func(c *loginChallenge) { c.State = "fixed" }},
		{"short_nonce", func(c *loginChallenge) { c.Nonce = "fixed" }},
		{"bad_encoding", func(c *loginChallenge) { c.Nonce = strings.Repeat("%", 43) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			challenge := valid
			test.edit(&challenge)
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(challenge) }))
			defer api.Close()
			if _, err := fetchLoginChallenge(context.Background(), api.URL); err == nil || !strings.Contains(err.Error(), "challenge") || strings.Contains(err.Error(), challenge.State) {
				t.Fatalf("invalid challenge result = %v", err)
			}
		})
	}
}

func TestLoginChallengeAndSessionRejectRedirectsAndProviderBodies(t *testing.T) {
	var received atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer other.Close()
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusBadRequest, http.StatusInternalServerError} {
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", other.URL)
			w.WriteHeader(status)
			fmt.Fprint(w, "private-token-and-state")
		}))
		_, challengeErr := fetchLoginChallenge(context.Background(), api.URL)
		_, sessionErr := registerCLISession(context.Background(), api.URL, "access", "id-token", "state")
		api.Close()
		for _, err := range []error{challengeErr, sessionErr} {
			if err == nil || !strings.Contains(err.Error(), fmt.Sprint(status)) || strings.Contains(err.Error(), "private-") {
				t.Fatalf("unsafe login error = %v", err)
			}
		}
	}
	if received.Load() != 0 {
		t.Fatal("login redirected credentials to another authority")
	}
}

func TestRegisteredCLISessionRequiresBoundedServerTTL(t *testing.T) {
	for _, expiry := range []time.Time{{}, time.Now().Add(-time.Second), time.Now().Add(time.Hour)} {
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"expires_at": expiry})
		}))
		_, err := registerCLISession(context.Background(), api.URL, "access", "id", "state")
		api.Close()
		if err == nil || !strings.Contains(err.Error(), "TTL") {
			t.Fatalf("unbounded session = %v", err)
		}
	}
}
