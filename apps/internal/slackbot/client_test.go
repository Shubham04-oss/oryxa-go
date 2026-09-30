package slackbot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
	"github.com/Shubham04-oss/oryxa-go/internal/slackfake"
)

func TestTheAPIBaseURLIsSlackItselfOrALoopbackFake(t *testing.T) {
	for raw, ok := range map[string]bool{
		"http://127.0.0.1:41234/api":  true,
		"http://localhost:8099/api":   true,
		"https://127.0.0.1/api":       true,
		"http://[::1]:9000/api":       true,
		"http://127.8.9.10/api":       true,
		"https://slack.com/api":       false,
		"http://slack.com/api":        false,
		"https://api.slack.com":       false,
		"http://10.0.0.5/api":         false,
		"http://169.254.169.254/":     false,
		"http://localhost.evil.test/": false,
		"http://127.0.0.1.evil.test/": false,
		"http://0.0.0.0:80/":          false,
		"ftp://127.0.0.1/":            false,
		"http://user:pw@127.0.0.1/":   false,
		"http://127.0.0.1/?a=b":       false,
		"127.0.0.1:8080":              false,
		"":                            false,
	} {
		if err := CheckAPIBaseURL(raw); (err == nil) != ok {
			t.Errorf("CheckAPIBaseURL(%q) = %v, want ok = %v", raw, err, ok)
		}
	}
	// An empty base is the real Slack, and any other base must pass the check.
	if client, err := NewClient("", nil); err != nil || client.base != DefaultAPIBaseURL {
		t.Fatalf("NewClient(\"\") = %+v, %v", client, err)
	}
	if _, err := NewClient("https://slack.com/api", nil); err == nil {
		t.Fatal("a client was made for a base that is not loopback")
	}
}

// Whatever a loopback base's host resolves to, the client dials loopback only,
// so a token is never sent to an address that only looks like one.
func TestALoopbackClientReachesLoopbackAndNothingElse(t *testing.T) {
	served := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) }))
	defer served.Close()
	client := loopbackClient()

	response, err := client.Get(served.URL)
	if err != nil {
		t.Fatalf("loopback = %v", err)
	}
	response.Body.Close()

	for _, target := range []string{"http://192.0.2.1:80/", "http://10.255.255.1:80/", "http://[2001:db8::1]:80/"} {
		start := time.Now()
		if _, err := client.Get(target); err == nil || !strings.Contains(err.Error(), "not loopback") {
			t.Errorf("%s = %v, want it refused as not loopback", target, err)
		}
		if time.Since(start) > 2*time.Second {
			t.Errorf("%s: refusing took %v, so it was dialed", target, time.Since(start))
		}
	}

	// A redirect to somewhere else is not followed.
	redirecting := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, "http://192.0.2.1/", http.StatusFound)
	}))
	defer redirecting.Close()
	response, err = client.Get(redirecting.URL)
	if err != nil || response.StatusCode != http.StatusFound {
		t.Fatalf("a redirect = %v, %v", response, err)
	}
	response.Body.Close()
}

func TestTheClientTurnsSlacksAnswersIntoErrorsThatCarryNoSecret(t *testing.T) {
	fake := slackfake.New(t)
	client, err := NewClient(fake.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	token := secrets.Secret(slackfake.DefaultBotToken)
	ctx := context.Background()

	fake.Fail("users.info", "user_not_found", 1)
	_, err = client.User(ctx, token, "U0NOBODY")
	var refused *Error
	if !errors.As(err, &refused) || refused.Code != "user_not_found" || refused.Method != "users.info" {
		t.Fatalf("a refusal = %v", err)
	}
	if strings.Contains(err.Error(), slackfake.DefaultBotToken) {
		t.Fatal("an error names the token")
	}

	fake.RateLimit("chat.postMessage", 3*time.Second, 1)
	_, err = client.Post(ctx, token, Message{Channel: "C0GENERAL", Text: "x"})
	var limited *RateLimitedError
	if !errors.As(err, &limited) || limited.RetryAfter != 3*time.Second || !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("a rate limit = %v", err)
	}

	fake.Down(true)
	if _, err := client.AuthTest(ctx, token); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("Slack down = %v", err)
	}
	fake.Down(false)

	// An answer that is not JSON is Slack being unwell, not a refusal.
	garbled := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write([]byte("<html>")) }))
	defer garbled.Close()
	other, err := NewClient(garbled.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.AuthTest(ctx, token); !errors.Is(err, domain.ErrUnavailable) || strings.Contains(err.Error(), "<html>") {
		t.Fatalf("garbage = %v", err)
	}

	// An unreachable Slack does not put the URL, or the token, in the error.
	unreachable, err := NewClient("http://127.0.0.1:1/api", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = unreachable.AuthTest(ctx, token)
	if !errors.Is(err, domain.ErrUnavailable) || strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), slackfake.DefaultBotToken) {
		t.Fatalf("unreachable = %v", err)
	}
	// Cancelling is the caller's doing, and is said as such.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := client.AuthTest(cancelled, token); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled call = %v", err)
	}
}

// Whatever a caller says, the surface's posts never unfurl links or media.
func TestPostsNeverUnfurl(t *testing.T) {
	fake := slackfake.New(t)
	client, err := NewClient(fake.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	token := secrets.Secret(slackfake.DefaultBotToken)
	if _, err := client.Post(context.Background(), token, Message{Channel: "C0GENERAL", Text: "see https://example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Post(context.Background(), token, Message{Channel: "C0GENERAL", User: "U0ASKER", Text: "psst https://example.com"}); err != nil {
		t.Fatal(err)
	}
	for _, message := range fake.Messages() {
		if message.UnfurlLinks || message.UnfurlMedia {
			t.Fatalf("a message unfurls: %+v", message)
		}
	}
}
