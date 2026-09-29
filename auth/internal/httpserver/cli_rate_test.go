package httpserver

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCLIChallengeRateLimitCannotBeBypassedWithForwardingHeaders(t *testing.T) {
	server, _ := setupServer(newFakeCLISessions())
	for i := 0; i < 11; i++ {
		r := httptest.NewRequest(http.MethodPost, "/v1/auth/cli/challenge", nil)
		r.RemoteAddr = "192.0.2.1:12345"
		r.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d", i))
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		if i < 10 && w.Code != 200 {
			t.Fatal(w.Code)
		}
		if i == 10 && (w.Code != 429 || w.Header().Get("Retry-After") == "") {
			t.Fatal("unbounded challenge issuance", w.Code)
		}
	}
}
func TestCLIChallengeLimiterBoundsPeerMemoryAndRecovers(t *testing.T) {
	var limiter cliChallengeLimiter
	now := time.Now()
	for i := 0; i < 120; i++ {
		if !limiter.allow(fmt.Sprintf("192.0.2.%d:1234", i), now) {
			t.Fatal("early global denial")
		}
	}
	if limiter.allow("203.0.113.1:1", now) {
		t.Fatal("global budget bypassed")
	}
	if len(limiter.peers) != 120 {
		t.Fatal("unexpected peer count", len(limiter.peers))
	}
	if !limiter.allow("203.0.113.1:1", now.Add(time.Minute)) || len(limiter.peers) != 1 {
		t.Fatal("expired peer records retained")
	}
}
