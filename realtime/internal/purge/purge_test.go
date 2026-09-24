package purge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The golden vectors: what the Worker's own tests check its verification
// against, and what both sides must agree on byte for byte. The secret is
// used as the characters it is written in, not decoded from hex. The
// signatures were computed outside Go as well:
//
//	printf '%s' "$timestamp.$body" | openssl dgst -sha256 -hmac "$secret"
const (
	goldenSecret    = "5f2b8e1c9a7d4036b1e8c2f94a6d0e3b7c1f5a9d2e8b4c6f0a3d7e1b9c5f2a84"
	goldenTimestamp = int64(1790208000) // 2026-09-24T00:00:00Z
)

var goldenVectors = []struct {
	name      string
	request   Request
	body      string
	signature string
}{
	{
		name:      "a member removed",
		request:   Request{EventID: "evt_Zx8rP0kQ3mB7tY2wN5cV1A", OrganizationID: "org_2fQx9LmR7vTk4WcA", UserID: "6c1d5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f"},
		body:      `{"event_id":"evt_Zx8rP0kQ3mB7tY2wN5cV1A","organization_id":"org_2fQx9LmR7vTk4WcA","user_id":"6c1d5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f"}`,
		signature: "v1=a6f400c72318e7a142e5fcc51f04f98fad8ffae7864549a902cadfa96b584067",
	},
	{
		name:      "an organization deleted",
		request:   Request{EventID: "evt_Qm4sT8vY1bN6cX3zL0pK7g", OrganizationID: "org_2fQx9LmR7vTk4WcA"},
		body:      `{"event_id":"evt_Qm4sT8vY1bN6cX3zL0pK7g","organization_id":"org_2fQx9LmR7vTk4WcA","user_id":""}`,
		signature: "v1=c35da0ee0a92d26cf07903fcfd3118d42b0517920abe31cf13a0f818b9004314",
	},
}

func TestSignMatchesTheGoldenVectors(t *testing.T) {
	for _, vector := range goldenVectors {
		t.Run(vector.name, func(t *testing.T) {
			body, err := json.Marshal(vector.request)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != vector.body {
				t.Fatalf("body = %s, want %s", body, vector.body)
			}
			if got := Sign(goldenSecret, goldenTimestamp, body); got != vector.signature {
				t.Fatalf("Sign() = %s, want %s", got, vector.signature)
			}
		})
	}
	// Any change to what is signed changes the signature.
	body := []byte(goldenVectors[0].body)
	for name, signature := range map[string]string{
		"another secret":    Sign(goldenSecret+"x", goldenTimestamp, body),
		"another timestamp": Sign(goldenSecret, goldenTimestamp+1, body),
		"another body":      Sign(goldenSecret, goldenTimestamp, append([]byte(" "), body...)),
	} {
		if signature == goldenVectors[0].signature {
			t.Errorf("%s signed the same", name)
		}
	}
}

// worker is an httptest Worker that records the last request it was sent and
// answers every one with status.
type worker struct {
	*httptest.Server
	calls atomic.Int32
	mu    sync.Mutex
	last  *http.Request
	body  []byte
}

func newWorker(t *testing.T, status int, answer string) *worker {
	t.Helper()
	fake := &worker{}
	fake.Server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		fake.mu.Lock()
		fake.last, fake.body = request, body
		fake.mu.Unlock()
		fake.calls.Add(1)
		writer.WriteHeader(status)
		_, _ = io.WriteString(writer, answer)
	}))
	t.Cleanup(fake.Close)
	return fake
}

// received is the last request the Worker was sent, and its body.
func (fake *worker) received() (*http.Request, []byte) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.last, fake.body
}

func goldenClient(t *testing.T, workerURL string) *Client {
	t.Helper()
	client, err := New(Config{
		WorkerURL: workerURL,
		Secret:    goldenSecret,
		Now:       func() time.Time { return time.Unix(goldenTimestamp, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestPurgeSendsTheSignedRequestTheContractNames(t *testing.T) {
	for _, vector := range goldenVectors {
		t.Run(vector.name, func(t *testing.T) {
			fake := newWorker(t, http.StatusNoContent, "")
			if err := goldenClient(t, fake.URL+"/").Purge(context.Background(), vector.request); err != nil {
				t.Fatalf("Purge() = %v", err)
			}
			request, body := fake.received()
			if request.Method != http.MethodPost || request.URL.Path != "/internal/realtime/purge" || request.URL.RawQuery != "" {
				t.Fatalf("request = %s %s", request.Method, request.URL)
			}
			if got := request.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q", got)
			}
			if got := request.Header.Get("X-Oryxa-Timestamp"); got != "1790208000" {
				t.Errorf("X-Oryxa-Timestamp = %q", got)
			}
			if got := request.Header.Get("X-Oryxa-Signature"); got != vector.signature {
				t.Errorf("X-Oryxa-Signature = %q, want %q", got, vector.signature)
			}
			if got := request.Header.Get("User-Agent"); got != "oryxa-realtime/1" {
				t.Errorf("User-Agent = %q", got)
			}
			if string(body) != vector.body {
				t.Errorf("body = %s, want %s", body, vector.body)
			}
		})
	}
}

func TestPurgeSignsEachAttemptWhenItIsSent(t *testing.T) {
	fake := newWorker(t, http.StatusNoContent, "")
	now := time.Unix(goldenTimestamp, 0)
	client, err := New(Config{WorkerURL: fake.URL, Secret: goldenSecret, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	request := goldenVectors[0].request
	if err := client.Purge(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Minute)
	if err := client.Purge(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	second, body := fake.received()
	if got, want := second.Header.Get("X-Oryxa-Timestamp"), "1790208600"; got != want {
		t.Fatalf("second attempt signed at %s, want %s", got, want)
	}
	if got, want := second.Header.Get("X-Oryxa-Signature"), Sign(goldenSecret, goldenTimestamp+600, body); got != want {
		t.Fatalf("second attempt signature = %s, want %s", got, want)
	}
}

func TestPurgeKeepsAPathTheWorkerURLCarries(t *testing.T) {
	fake := newWorker(t, http.StatusNoContent, "")
	if err := goldenClient(t, fake.URL+"/web/").Purge(context.Background(), goldenVectors[0].request); err != nil {
		t.Fatal(err)
	}
	if request, _ := fake.received(); request.URL.Path != "/web/internal/realtime/purge" {
		t.Fatalf("path = %q", request.URL.Path)
	}
}

// Only 204 is done. A 4xx is the Worker refusing this purge for good, except
// a 401, which is the secret or the clock, and 408 and 429, which are the
// Worker asking for later. Everything else may succeed if sent again.
func TestPurgeTellsARefusalForGoodFromOneWorthRetrying(t *testing.T) {
	cases := []struct {
		status    int
		permanent bool
	}{
		{http.StatusBadRequest, true},
		{http.StatusForbidden, true},
		{http.StatusNotFound, true},
		{http.StatusMethodNotAllowed, true},
		{http.StatusConflict, true},
		{http.StatusUnprocessableEntity, true},
		{http.StatusUnauthorized, false},
		{http.StatusRequestTimeout, false},
		{http.StatusTooManyRequests, false},
		{http.StatusInternalServerError, false},
		{http.StatusBadGateway, false},
		{http.StatusServiceUnavailable, false},
		{http.StatusGatewayTimeout, false},
		// Answers the contract does not name are no sign the purge happened.
		{http.StatusOK, false},
		{http.StatusAccepted, false},
		{http.StatusTemporaryRedirect, false},
	}
	for _, test := range cases {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			fake := newWorker(t, test.status, "  the Worker's\n  own words  ")
			err := goldenClient(t, fake.URL).Purge(context.Background(), goldenVectors[0].request)
			if err == nil {
				t.Fatalf("Purge() = nil for %d", test.status)
			}
			if errors.Is(err, ErrPermanent) != test.permanent {
				t.Fatalf("Purge() = %v; permanent = %v, want %v", err, errors.Is(err, ErrPermanent), test.permanent)
			}
			if !strings.Contains(err.Error(), http.StatusText(test.status)) || !strings.Contains(err.Error(), ": the Worker's own words") {
				t.Fatalf("Purge() = %q, want the status and what the Worker said on one line", err)
			}
		})
	}
}

func TestPurgeSaysWhatA401Means(t *testing.T) {
	fake := newWorker(t, http.StatusUnauthorized, "")
	err := goldenClient(t, fake.URL).Purge(context.Background(), goldenVectors[0].request)
	if err == nil || errors.Is(err, ErrPermanent) || !strings.Contains(err.Error(), "REALTIME_PURGE_SECRET") ||
		!strings.Contains(err.Error(), "clock") {
		t.Fatalf("Purge() = %v, want a retryable error naming the secret and the clock", err)
	}
}

func TestPurgeShortensALongAnswer(t *testing.T) {
	fake := newWorker(t, http.StatusBadRequest, strings.Repeat("<p>not found</p> ", 500))
	err := goldenClient(t, fake.URL).Purge(context.Background(), goldenVectors[0].request)
	if err == nil || len(err.Error()) > 400 || !strings.HasSuffix(err.Error(), "…") {
		t.Fatalf("Purge() = %q (%d bytes), want the answer cut short", err, len(err.Error()))
	}
}

func TestPurgeRetriesWhatNeverReachedTheWorker(t *testing.T) {
	gone := newWorker(t, http.StatusNoContent, "")
	gone.Close()
	if err := goldenClient(t, gone.URL).Purge(context.Background(), goldenVectors[0].request); err == nil || errors.Is(err, ErrPermanent) {
		t.Fatalf("Purge() to a Worker that is down = %v, want a retryable error", err)
	}

	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		<-release
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer slow.Close()
	defer close(release)
	client, err := New(Config{WorkerURL: slow.URL, Secret: goldenSecret, HTTP: &http.Client{Timeout: 50 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Purge(context.Background(), goldenVectors[0].request); err == nil || errors.Is(err, ErrPermanent) {
		t.Fatalf("Purge() to a Worker that does not answer = %v, want a retryable error", err)
	}
}

func TestPurgeDoesNotFollowARedirect(t *testing.T) {
	var elsewhere atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/elsewhere" {
			elsewhere.Add(1)
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		http.Redirect(writer, request, "/elsewhere", http.StatusPermanentRedirect)
	}))
	defer server.Close()
	err := goldenClient(t, server.URL).Purge(context.Background(), goldenVectors[0].request)
	if err == nil || errors.Is(err, ErrPermanent) || elsewhere.Load() != 0 {
		t.Fatalf("Purge() = %v, redirect followed %d times; want a retryable error and none", err, elsewhere.Load())
	}
}

func TestPurgeRefusesAPurgeWithoutItsScope(t *testing.T) {
	fake := newWorker(t, http.StatusNoContent, "")
	client := goldenClient(t, fake.URL)
	for name, request := range map[string]Request{
		"no organization": {EventID: "evt_1", UserID: "user-1"},
		"no event":        {OrganizationID: "org_1", UserID: "user-1"},
		"nothing":         {},
	} {
		if err := client.Purge(context.Background(), request); err == nil {
			t.Errorf("%s: Purge() = nil", name)
		}
	}
	if fake.calls.Load() != 0 {
		t.Fatalf("the Worker was called %d times", fake.calls.Load())
	}
}

func TestNewRefusesWhatItCannotReachOrSignWith(t *testing.T) {
	for name, config := range map[string]Config{
		"no URL":         {Secret: goldenSecret},
		"a relative URL": {WorkerURL: "/internal", Secret: goldenSecret},
		"another scheme": {WorkerURL: "ftp://app.oryxa.in", Secret: goldenSecret},
		"no host":        {WorkerURL: "https://", Secret: goldenSecret},
		"no secret":      {WorkerURL: "https://app.oryxa.in"},
	} {
		if _, err := New(config); err == nil {
			t.Errorf("%s: New() = nil error", name)
		}
	}
}
