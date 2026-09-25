package purge

import (
	"context"
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
// against, and what both sides must agree on byte for byte, the body
// included. The secret is used as the characters it is written in, not
// decoded from hex. The signatures were computed outside Go as well:
//
//	printf '%s' "$timestamp.$body" | openssl dgst -sha256 -hmac "$secret"
//
// The first two are the ones agreed with the Worker's side. The third is the
// shape a real event takes: auth's clock reads nanoseconds, and removed_at
// keeps them.
const (
	goldenSecret    = "5f2b8e1c9a7d4036b1e8c2f94a6d0e3b7c1f5a9d2e8b4c6f0a3d7e1b9c5f2a84"
	goldenTimestamp = int64(1790208000) // 2026-09-24T00:00:00Z
)

var (
	removedAt = time.Date(2026, time.September, 24, 10, 0, 0, 0, time.UTC)
	// India Standard Time, which auth's clock is not in, but which proves the
	// body is written in UTC whatever zone the time arrived in.
	ist = time.FixedZone("IST", 5*60*60+30*60)
)

var goldenVectors = []struct {
	name      string
	request   Request
	body      string
	signature string
}{
	{
		name: "a member removed",
		request: Request{EventID: "evt_Zx8rP0kQ3mB7tY2wN5cV1A", OrganizationID: "org_2fQx9LmR7vTk4WcA",
			Scope: ScopeMember, UserID: "6c1d5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f", RemovedAt: removedAt},
		body:      `{"event_id":"evt_Zx8rP0kQ3mB7tY2wN5cV1A","organization_id":"org_2fQx9LmR7vTk4WcA","scope":"member","user_id":"6c1d5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f","removed_at":"2026-09-24T10:00:00Z"}`,
		signature: "v1=ee12ee93bb00fb5e9901c4a0ac8ed193a85539bfb2c76241f9182f2fcc71b90e",
	},
	{
		name: "an organization deleted",
		request: Request{EventID: "evt_Qm4sT8vY1bN6cX3zL0pK7g", OrganizationID: "org_2fQx9LmR7vTk4WcA",
			Scope: ScopeOrganization, RemovedAt: removedAt},
		body:      `{"event_id":"evt_Qm4sT8vY1bN6cX3zL0pK7g","organization_id":"org_2fQx9LmR7vTk4WcA","scope":"organization","user_id":"","removed_at":"2026-09-24T10:00:00Z"}`,
		signature: "v1=ec0202dfc34ba5b946ce5e384b187d7358b3609549a049b3b3bfb51ef73d2177",
	},
	{
		name: "a member removed within a second, from another zone",
		request: Request{EventID: "evt_Zx8rP0kQ3mB7tY2wN5cV1A", OrganizationID: "org_2fQx9LmR7vTk4WcA",
			Scope: ScopeMember, UserID: "6c1d5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f",
			RemovedAt: time.Date(2026, time.September, 24, 15, 30, 0, 123456789, ist)},
		body:      `{"event_id":"evt_Zx8rP0kQ3mB7tY2wN5cV1A","organization_id":"org_2fQx9LmR7vTk4WcA","scope":"member","user_id":"6c1d5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f","removed_at":"2026-09-24T10:00:00.123456789Z"}`,
		signature: "v1=0828ea12f32328f23603c3f910c57474459a38a6fe79fce8afcfa12bf5780548",
	},
}

func TestSignMatchesTheGoldenVectors(t *testing.T) {
	for _, vector := range goldenVectors {
		t.Run(vector.name, func(t *testing.T) {
			if got := Sign(goldenSecret, goldenTimestamp, []byte(vector.body)); got != vector.signature {
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

// How the Worker's purge route words a refusal of the body: 400 for one it
// cannot read as a purge, 413 for one over 4 KB.
const (
	malformedRefusal = `{"error":{"code":"invalid_request","message":"A purge needs event_id, organization_id, a scope that agrees with user_id, and removed_at."}}`
	tooLargeRefusal  = `{"error":{"code":"too_large","message":"A purge's body is at most 4 KB."}}`
)

// Only 204 is done, and only the Worker refusing the body itself, a 400 or
// 413 carrying its own error, is for good. Everything else may succeed if sent
// again, or once an operator puts right whatever answered in the Worker's
// place: a 401 is the secret or the clock, 408 and 429 are the Worker asking
// for later, a 403, 404 or 405 is a URL that does not reach the Worker's
// route, the old Worker without it, or Cloudflare, and a 5xx is the Worker
// failing.
func TestPurgeTellsARefusalForGoodFromOneWorthRetrying(t *testing.T) {
	const words = "  the Worker's\n  own words  "
	cases := []struct {
		name      string
		status    int
		answer    string
		permanent bool
	}{
		{"the Worker refusing a malformed body", http.StatusBadRequest, malformedRefusal, true},
		{"the Worker refusing a body too large", http.StatusRequestEntityTooLarge, tooLargeRefusal, true},
		{"a 400 without the Worker's refusal", http.StatusBadRequest, words, false},
		{"a 400 page", http.StatusBadRequest, "<html><body>400 Bad Request</body></html>", false},
		{"a 400 with other JSON", http.StatusBadRequest, `{"error":"unknown organization"}`, false},
		{"a 400 with an error but no code", http.StatusBadRequest, `{"error":{"message":"no"}}`, false},
		{"a 413 without the Worker's refusal", http.StatusRequestEntityTooLarge, words, false},
		{"a 403 from Cloudflare", http.StatusForbidden, words, false},
		{"a 403 worded like the Worker", http.StatusForbidden, `{"error":{"code":"forbidden","message":"No."}}`, false},
		{"a 404 from a Worker without the route", http.StatusNotFound, words, false},
		{"a 404 worded like the Worker", http.StatusNotFound, `{"error":{"code":"not_found","message":"Not found."}}`, false},
		{"a 405 from the Worker", http.StatusMethodNotAllowed, `{"error":{"code":"method_not_allowed","message":"Method not allowed."}}`, false},
		{"a 409", http.StatusConflict, words, false},
		{"a 422", http.StatusUnprocessableEntity, words, false},
		{"a 401", http.StatusUnauthorized, words, false},
		{"a 408", http.StatusRequestTimeout, words, false},
		{"a 429", http.StatusTooManyRequests, words, false},
		{"a 500", http.StatusInternalServerError, words, false},
		{"a 502", http.StatusBadGateway, words, false},
		{"a 503 from the Worker", http.StatusServiceUnavailable, `{"error":{"code":"unavailable","message":"The purge did not finish. Try again."}}`, false},
		{"a 504", http.StatusGatewayTimeout, words, false},
		// Answers the contract does not name are no sign the purge happened.
		{"a 200", http.StatusOK, words, false},
		{"a 202", http.StatusAccepted, words, false},
		{"a 307", http.StatusTemporaryRedirect, words, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fake := newWorker(t, test.status, test.answer)
			err := goldenClient(t, fake.URL).Purge(context.Background(), goldenVectors[0].request)
			if err == nil {
				t.Fatalf("Purge() = nil for %d", test.status)
			}
			if errors.Is(err, ErrPermanent) != test.permanent {
				t.Fatalf("Purge() = %v; permanent = %v, want %v", err, errors.Is(err, ErrPermanent), test.permanent)
			}
			said := ": " + strings.Join(strings.Fields(test.answer), " ")
			if !strings.Contains(err.Error(), http.StatusText(test.status)) || !strings.Contains(err.Error(), said) {
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

// Nothing the Worker could read as more than was meant is ever sent: above
// all, an empty user_id is never taken for the whole organization, because
// the scope says which it is.
func TestPurgeRefusesAPurgeWithoutItsScope(t *testing.T) {
	fake := newWorker(t, http.StatusNoContent, "")
	client := goldenClient(t, fake.URL)
	member := Request{EventID: "evt_1", OrganizationID: "org_1", Scope: ScopeMember, UserID: "user-1", RemovedAt: removedAt}
	for name, edit := range map[string]func(*Request){
		"no organization":            func(request *Request) { request.OrganizationID = "" },
		"no event":                   func(request *Request) { request.EventID = "" },
		"no scope":                   func(request *Request) { request.Scope = "" },
		"no scope and no member":     func(request *Request) { request.Scope, request.UserID = "", "" },
		"another scope":              func(request *Request) { request.Scope = "team" },
		"a member's without them":    func(request *Request) { request.UserID = "" },
		"an organization's with one": func(request *Request) { request.Scope = ScopeOrganization },
		"no time":                    func(request *Request) { request.RemovedAt = time.Time{} },
		"nothing":                    func(request *Request) { *request = Request{} },
	} {
		request := member
		edit(&request)
		if err := client.Purge(context.Background(), request); err == nil {
			t.Errorf("%s: Purge(%+v) = nil", name, request)
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
		// The Worker matches Path exactly, so a purge under another path
		// would never reach it.
		"a path":    {WorkerURL: "https://app.oryxa.in/web/", Secret: goldenSecret},
		"a query":   {WorkerURL: "https://app.oryxa.in/?x=1", Secret: goldenSecret},
		"no secret": {WorkerURL: "https://app.oryxa.in"},
	} {
		if _, err := New(config); err == nil {
			t.Errorf("%s: New() = nil error", name)
		}
	}
}
