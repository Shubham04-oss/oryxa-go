package appsclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter"
)

func samplePost() slackrouter.Post {
	return slackrouter.Post{
		OrganizationID: "org_1", IdempotencyKey: "surfaces:irq_1:answer",
		Target:   slackrouter.Target{ChannelID: "C1", ThreadTS: "1700000000.000100"},
		Template: "answer",
		Params:   map[string]any{"text": "the launch is on the 14th", "owner_name": "Raya", "mode": "summary"},
	}
}

// retryAfterOf reads what slackrouter's worker reads from an error.
func retryAfterOf(err error) time.Duration {
	var hinted interface{ RetryAfter() time.Duration }
	if errors.As(err, &hinted) {
		return hinted.RetryAfter()
	}
	return 0
}

func TestARateLimitIsWaitedOutAndTheSameCallMadeAgain(t *testing.T) {
	fake := newFakeApps(t, func(n int, _ recorded, w http.ResponseWriter) {
		if n <= 2 {
			w.Header().Set("Retry-After", "2")
			refuse(w, http.StatusTooManyRequests, "rate_limited", "too many requests; retry")
			return
		}
		ok(w, map[string]any{"channel_id": "C1", "ts": "1700000000.000400"})
	})
	pause := &waits{}
	signer := &fakeSigner{}
	client := clientFor(t, fake, signer, pause)

	posted, err := client.Send(context.Background(), samplePost())
	if err != nil {
		t.Fatalf("Send() = %v, want the limit waited out", err)
	}
	if posted.TS != "1700000000.000400" {
		t.Fatalf("posted = %+v", posted)
	}
	if got := pause.all(); len(got) != 2 || got[0] != 2*time.Second || got[1] != 2*time.Second {
		t.Fatalf("waits = %v, want two of the 2 seconds apps asked for", got)
	}
	seen := fake.requests()
	if len(seen) != 3 {
		t.Fatalf("got %d requests, want the call made three times", len(seen))
	}
	// The same message under the same key, with a token of its own each time.
	tokens := map[string]bool{}
	for _, request := range seen {
		if string(request.Body) != string(seen[0].Body) {
			t.Fatalf("a retry sent %s, want what was first sent %s", request.Body, seen[0].Body)
		}
		if request.json(t)["idempotency_key"] != "surfaces:irq_1:answer" {
			t.Fatalf("a retry lost its idempotency key: %s", request.Body)
		}
		tokens[request.Token] = true
	}
	if len(tokens) != 3 || len(signer.minted) != 3 {
		t.Fatalf("tokens = %v, minted %d, want a fresh one for each attempt", tokens, len(signer.minted))
	}
}

func TestARateLimitThatDoesNotLiftIsAnErrorThatSaysHowLong(t *testing.T) {
	fake := newFakeApps(t, func(_ int, _ recorded, w http.ResponseWriter) {
		w.Header().Set("Retry-After", "3")
		refuse(w, http.StatusTooManyRequests, "rate_limited", "too many requests; retry")
	})
	pause := &waits{}
	client := clientFor(t, fake, &fakeSigner{}, pause, func(c *Config) { c.Retries = 2 })

	err := client.Post(context.Background(), samplePost())
	var limited *RateLimitedError
	if !errors.As(err, &limited) || limited.After != 3*time.Second {
		t.Fatalf("Post() error = %v, want a RateLimitedError for 3s", err)
	}
	if slackrouter.IsPermanent(err) {
		t.Fatal("a rate limit is never Permanent")
	}
	if retryAfterOf(err) != 3*time.Second {
		t.Fatalf("the router's worker reads %v from it, want 3s", retryAfterOf(err))
	}
	// Made once, and twice more after a wait each: bounded.
	if len(fake.requests()) != 3 || len(pause.all()) != 2 {
		t.Fatalf("made %d requests and waited %v, want 3 requests and 2 waits", len(fake.requests()), pause.all())
	}
}

func TestAWaitLongerThanTheClientIsWillingToSitOutIsLeftToTheCaller(t *testing.T) {
	fake := newFakeApps(t, func(_ int, _ recorded, w http.ResponseWriter) {
		w.Header().Set("Retry-After", "60")
		refuse(w, http.StatusTooManyRequests, "rate_limited", "too many requests; retry")
	})
	pause := &waits{}
	client := clientFor(t, fake, &fakeSigner{}, pause)

	err := client.Post(context.Background(), samplePost())
	if retryAfterOf(err) != time.Minute {
		t.Fatalf("Post() error = %v, want it to say 60s", err)
	}
	if len(fake.requests()) != 1 || len(pause.all()) != 0 {
		t.Fatalf("made %d requests and waited %v, want the one and no wait: another try would be refused too", len(fake.requests()), pause.all())
	}
}

func TestARateLimitWithoutASayIsWaitedOutForASecondAndNegativeRetriesMeansNone(t *testing.T) {
	fake := newFakeApps(t, func(n int, _ recorded, w http.ResponseWriter) {
		if n == 1 {
			refuse(w, http.StatusTooManyRequests, "rate_limited", "too many requests; retry")
			return
		}
		ok(w, map[string]any{"channel_id": "C1", "ts": "1700000000.000400"})
	})
	pause := &waits{}
	client := clientFor(t, fake, &fakeSigner{}, pause)
	if _, err := client.Send(context.Background(), samplePost()); err != nil {
		t.Fatal(err)
	}
	if got := pause.all(); len(got) != 1 || got[0] != time.Second {
		t.Fatalf("waits = %v, want the default second", got)
	}

	limited := newFakeApps(t, func(_ int, _ recorded, w http.ResponseWriter) {
		w.Header().Set("Retry-After", "1")
		refuse(w, http.StatusTooManyRequests, "rate_limited", "too many requests; retry")
	})
	none := clientFor(t, limited, &fakeSigner{}, &waits{}, func(c *Config) { c.Retries = -1 })
	if err := none.Post(context.Background(), samplePost()); retryAfterOf(err) != time.Second || len(limited.requests()) != 1 {
		t.Fatalf("with no retries: error = %v after %d requests", err, len(limited.requests()))
	}
}

func TestTheContextEndsAWaitForALimit(t *testing.T) {
	fake := newFakeApps(t, func(_ int, _ recorded, w http.ResponseWriter) {
		w.Header().Set("Retry-After", "1")
		refuse(w, http.StatusTooManyRequests, "rate_limited", "too many requests; retry")
	})
	pause := &waits{err: context.Canceled}
	client := clientFor(t, fake, &fakeSigner{}, pause)
	if err := client.Post(context.Background(), samplePost()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Post() error = %v, want the wait ended by its context", err)
	}
	if len(fake.requests()) != 1 {
		t.Fatalf("made %d requests after being told to stop", len(fake.requests()))
	}
}

func TestWhatAppsRefusesIsSortedIntoWhatACallerDoes(t *testing.T) {
	type outcome int
	const (
		permanent outcome = iota
		gone
		unreadable
		again // a plain error: tried again by whoever called
	)
	for _, test := range []struct {
		name    string
		status  int
		code    string
		message string
		want    outcome
	}{
		{"a template or params apps will not take", 400, "invalid_request", "text is required", permanent},
		{"a thread apps has not seen", 403, "forbidden", "apps has not seen that conversation in a verified Slack message", permanent},
		{"a purpose apps does not admit", 403, "forbidden", "the service may not use this purpose", permanent},
		{"an install that has ended", 409, "not_connected", "the organization's Slack install has ended", permanent},
		{"a message apps did not post", 404, "not_found", "resource not found", gone},
		{"a purged record", 410, "gone", "gone", gone},
		{"a conversation Slack will not describe", 422, "channel_unreadable", "no scope", unreadable},
		{"a body too large", 413, "invalid_request", "too big", permanent},
		{"a key whose post is being made", 409, "conflict", "that message is being posted; try again shortly", again},
		{"a token that could not be checked", 401, "unauthorized", "the service token is invalid", again},
		{"apps failing", 500, "internal", "internal server error", again},
		{"apps unavailable", 503, "unavailable", "service identity could not be checked; retry", again},
		{"a gateway timing out", 504, "", "", again},
		{"a request timing out", 408, "", "", again},
		{"Slack refusing a message for good: the bot is not in the channel", 502, "slack_error", "slack refused the request: not_in_channel", permanent},
		{"Slack refusing a message for good: an archived channel", 502, "slack_error", "slack refused the request: is_archived", permanent},
		{"Slack refusing for a reason that may pass", 502, "slack_error", "slack refused the request: internal_error", again},
		{"an answer that is not JSON", 502, "", "", again},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeApps(t, func(_ int, _ recorded, w http.ResponseWriter) {
				if test.code == "" && test.message == "" {
					w.WriteHeader(test.status)
					_, _ = w.Write([]byte("<html>bad gateway</html>"))
					return
				}
				refuse(w, test.status, test.code, test.message)
			})
			client := clientFor(t, fake, &fakeSigner{}, &waits{})
			for name, call := range map[string]func() error{
				"post": func() error { return client.Post(context.Background(), samplePost()) },
				"update": func() error {
					return client.Update(context.Background(), Update{OrganizationID: "org_1", ChannelID: "D1", TS: "1700000000.000100", Template: "notice_settled"})
				},
				"channel": func() error { _, err := client.Channel(context.Background(), "org_1", "C1"); return err },
				"inbound": func() error { _, err := client.Inbound(context.Background(), "asi_1"); return err },
			} {
				err := call()
				if err == nil {
					t.Fatalf("%s: error = nil", name)
				}
				switch test.want {
				case permanent:
					if !slackrouter.IsPermanent(err) || errors.Is(err, slackrouter.ErrGone) {
						t.Errorf("%s: error = %v, want Permanent", name, err)
					}
				case gone:
					if !errors.Is(err, slackrouter.ErrGone) || slackrouter.IsPermanent(err) {
						t.Errorf("%s: error = %v, want ErrGone", name, err)
					}
				case unreadable:
					if !errors.Is(err, slackrouter.ErrUnreadable) || slackrouter.IsPermanent(err) {
						t.Errorf("%s: error = %v, want ErrUnreadable", name, err)
					}
				case again:
					if slackrouter.IsPermanent(err) || errors.Is(err, slackrouter.ErrGone) || errors.Is(err, slackrouter.ErrUnreadable) {
						t.Errorf("%s: error = %v, want a plain error to be tried again", name, err)
					}
				}
			}
		})
	}
}

func TestAnErrorAppsGaveKeepsItsStatusCodeAndWords(t *testing.T) {
	fake := newFakeApps(t, func(_ int, _ recorded, w http.ResponseWriter) {
		refuse(w, http.StatusForbidden, "forbidden", "apps has not seen that conversation in a verified Slack message")
	})
	client := clientFor(t, fake, &fakeSigner{}, &waits{})
	err := client.Post(context.Background(), samplePost())
	var refused *Error
	if !errors.As(err, &refused) || refused.Status != 403 || refused.Code != "forbidden" || !strings.Contains(refused.Message, "has not seen") {
		t.Fatalf("error = %v (%+v), want apps' refusal kept", err, refused)
	}
}

func TestUpdateSendsTheMessageItReplacesAndItsNewWords(t *testing.T) {
	fake := newFakeApps(t, func(_ int, _ recorded, w http.ResponseWriter) {
		ok(w, map[string]any{"channel_id": "D9", "ts": "1700000000.000300"})
	})
	client := clientFor(t, fake, &fakeSigner{}, &waits{})
	err := client.Update(context.Background(), Update{
		OrganizationID: "org_1", ChannelID: "D9", TS: "1700000000.000300", Template: "notice_settled",
		Params: map[string]any{"decision": "approved_summary", "question": "when?"},
	})
	if err != nil {
		t.Fatal(err)
	}
	seen := fake.requests()
	if len(seen) != 1 || seen[0].Path != "/v1/internal/apps/slack/update" || seen[0].Method != http.MethodPost {
		t.Fatalf("requests = %+v", seen)
	}
	body := seen[0].json(t)
	if len(body) != 5 || body["organization_id"] != "org_1" || body["channel_id"] != "D9" || body["ts"] != "1700000000.000300" || body["template"] != "notice_settled" {
		t.Fatalf("body = %v, want the five fields apps knows", body)
	}
	if params, _ := body["params"].(map[string]any); params["decision"] != "approved_summary" {
		t.Fatalf("params = %v", body["params"])
	}
}

func TestAnErrorNeverCarriesWhatWasSentOrTheServiceToken(t *testing.T) {
	const secretQuestion = "the-dev-keys-are-hunter2-hunter2"
	fake := newFakeApps(t, func(_ int, _ recorded, w http.ResponseWriter) {
		refuse(w, http.StatusBadRequest, "invalid_request", "text is required")
	})
	client := clientFor(t, fake, &fakeSigner{}, &waits{})
	post := samplePost()
	post.Params = map[string]any{"text": secretQuestion, "owner_name": "Raya", "mode": "summary"}
	err := client.Post(context.Background(), post)
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), secretQuestion) || strings.Contains(err.Error(), "tok-") {
		t.Fatalf("the error says what it must not: %v", err)
	}

	// Nor does one that never reached apps: the URL is apps' own and the token
	// is not in it, but a transport error's URL is dropped all the same.
	fake.server.Close()
	err = client.Post(context.Background(), post)
	if err == nil {
		t.Fatal("want a transport error")
	}
	if strings.Contains(err.Error(), secretQuestion) || strings.Contains(err.Error(), "tok-") || strings.Contains(err.Error(), fake.server.URL) {
		t.Fatalf("the transport error says what it must not: %v", err)
	}
	if slackrouter.IsPermanent(err) {
		t.Fatalf("a call that never got an answer is not Permanent: %v", err)
	}
}

func TestASignerThatFailsIsAFailureToTryAgain(t *testing.T) {
	fake := newFakeApps(t, func(_ int, _ recorded, w http.ResponseWriter) { ok(w, map[string]any{}) })
	signer := &fakeSigner{err: errors.New("kms is down")}
	client := clientFor(t, fake, signer, &waits{})
	err := client.Post(context.Background(), samplePost())
	if err == nil || slackrouter.IsPermanent(err) {
		t.Fatalf("Post() error = %v, want a plain error", err)
	}
	if len(fake.requests()) != 0 {
		t.Fatal("a request was made without a token")
	}
}

func TestARedirectIsNeverFollowedWithTheServiceToken(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer other.Close()
	fake := newFakeApps(t, func(_ int, _ recorded, w http.ResponseWriter) {
		w.Header().Set("Location", other.URL+"/steal")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	client := clientFor(t, fake, &fakeSigner{}, &waits{})
	if err := client.Post(context.Background(), samplePost()); err == nil {
		t.Fatal("Post() = nil after a redirect, want an error")
	}
	if elsewhere.Load() != 0 {
		t.Fatal("the client followed a redirect to another host")
	}
}

func TestANewClientChecksItsConfiguration(t *testing.T) {
	signer := &fakeSigner{}
	for name, config := range map[string]Config{
		"no URL":            {Signer: signer},
		"plain HTTP":        {BaseURL: "http://apps.example.com", Signer: signer},
		"a path":            {BaseURL: "https://apps.example.com/v1", Signer: signer},
		"credentials":       {BaseURL: "https://user:pass@apps.example.com", Signer: signer},
		"no signer":         {BaseURL: "http://127.0.0.1:8080"},
		"a query":           {BaseURL: "http://127.0.0.1:8080?x=1", Signer: signer},
		"a non-http scheme": {BaseURL: "ftp://127.0.0.1", Signer: signer},
	} {
		if _, err := New(config); err == nil {
			t.Errorf("%s: New() accepted %+v", name, config)
		}
	}
	for name, url := range map[string]string{
		"loopback":       "http://127.0.0.1:8080",
		"loopback name":  "http://localhost:8080/",
		"loopback IPv6":  "http://[::1]:8080",
		"public HTTPS":   "https://apps.example.com",
		"trailing slash": "https://apps.example.com/",
	} {
		if _, err := New(Config{BaseURL: url, Signer: signer}); err != nil {
			t.Errorf("%s: New(%q) = %v", name, url, err)
		}
	}
}

func TestTheBaseURLsTrailingSlashDoesNotDoubleThePath(t *testing.T) {
	fake := newFakeApps(t, func(_ int, _ recorded, w http.ResponseWriter) { ok(w, map[string]any{"channel_id": "C1"}) })
	client, err := New(Config{BaseURL: fake.server.URL + "/", Signer: &fakeSigner{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Channel(context.Background(), "org_1", "C1"); err != nil {
		t.Fatal(err)
	}
	if got := fake.requests()[0].Path; got != "/v1/internal/apps/slack/channels/C1" {
		t.Fatalf("path = %s", got)
	}
}
