package requests

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
// against (web-next/src/realtime-requests.test.ts), and what both sides must
// agree on byte for byte, the body included. The secret is used as the
// characters it is written in, not decoded from hex. The signatures were
// computed outside Go:
//
//	printf '%s' "requests.$timestamp.$body" | openssl dgst -sha256 -hmac "$secret"
//
// Each vector also carries what a purge would have signed the same body
// with (`$timestamp.$body`) and what the Worker's events to Go would have
// (`events.$timestamp.$body`): a signature is only ever good for the
// direction it was made for.
const (
	goldenSecret    = "5f2b8e1c9a7d4036b1e8c2f94a6d0e3b7c1f5a9d2e8b4c6f0a3d7e1b9c5f2a84"
	goldenTimestamp = int64(1790208000) // 2026-09-24T00:00:00Z
)

var changedAt = time.Date(2026, time.September, 24, 10, 0, 0, 0, time.UTC)

var goldenVectors = []struct {
	name         string
	note         Note
	body         string
	signature    string
	purgeSigned  string
	eventsSigned string
}{
	{
		name: "a request just made",
		note: Note{OrganizationID: "org_1", RequestID: "irq_golden_1", Revision: 1, AskerUserID: "user-a", OwnerUserID: "user-b",
			Origin: OriginAgent, State: StateWaiting, ChangedAt: changedAt},
		body:         `{"organization_id":"org_1","request_id":"irq_golden_1","revision":1,"asker_user_id":"user-a","owner_user_id":"user-b","origin":"agent","state":"waiting","changed_at":"2026-09-24T10:00:00Z"}`,
		signature:    "v1=6efcec28a265a8e2c70d1a03881c12b7c43808663c62263cf23bbe8a91ad51f7",
		purgeSigned:  "v1=16be9eafa65062c650af54c73a245257e0a0fa7de9e04a9ddb72c0321dbd8b7b",
		eventsSigned: "v1=fdd14f29a48994bbf995dae4c418db61aa81613c296f801a447ce64950869a7c",
	},
	{
		// The same request, a revision on, from Slack, with a fraction of a
		// second and from another zone: the body is written in UTC whatever
		// zone the time arrived in.
		name: "the request approved, from Slack, within a second",
		note: Note{OrganizationID: "org_1", RequestID: "irq_golden_1", Revision: 2, AskerUserID: "user-a", OwnerUserID: "user-b",
			Origin: OriginSlack, State: StateApproved,
			ChangedAt: time.Date(2026, time.September, 24, 15, 30, 1, 123000000, time.FixedZone("IST", 5*60*60+30*60))},
		body:         `{"organization_id":"org_1","request_id":"irq_golden_1","revision":2,"asker_user_id":"user-a","owner_user_id":"user-b","origin":"slack","state":"approved","changed_at":"2026-09-24T10:00:01.123Z"}`,
		signature:    "v1=90d5150be5363039161c4ea3eef17f3817e4129925c3caa98aa8ba1051b7656b",
		purgeSigned:  "v1=5932e4b56b95ba214b3936588b1e0f724f2f03583ac9c2debc456896e37f5cc0",
		eventsSigned: "v1=c4879ba81942450723e44d9f58d3c1797c5db3db735cb18c122c57e87c84de81",
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

// The three directions between Go and the Worker sign over strings that begin
// differently, so a signature made for one never verifies as another's: a
// purge's (timestamp first), a request note's ("requests." first) and the
// Worker's events to Go ("events." first).
func TestANotesSignatureIsNeitherAPurgesNorAnEventsAndTheyAreNotIt(t *testing.T) {
	for _, vector := range goldenVectors {
		t.Run(vector.name, func(t *testing.T) {
			signature := Sign(goldenSecret, goldenTimestamp, []byte(vector.body))
			for what, other := range map[string]string{"a purge's": vector.purgeSigned, "an events request's": vector.eventsSigned} {
				if signature == other {
					t.Errorf("a note's signature is %s", what)
				}
			}
			if vector.purgeSigned == vector.eventsSigned {
				t.Error("a purge's and an events request's signatures of one body are the same")
			}
		})
	}
	if SignaturePrefix != "requests." {
		t.Fatalf("SignaturePrefix = %q, want \"requests.\" (the contract's)", SignaturePrefix)
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

func TestNoteSendsTheSignedRequestTheContractNames(t *testing.T) {
	for _, vector := range goldenVectors {
		t.Run(vector.name, func(t *testing.T) {
			fake := newWorker(t, http.StatusNoContent, "")
			if err := goldenClient(t, fake.URL+"/").Note(context.Background(), vector.note); err != nil {
				t.Fatalf("Note() = %v", err)
			}
			request, body := fake.received()
			if request.Method != http.MethodPost || request.URL.Path != "/internal/realtime/requests" || request.URL.RawQuery != "" {
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

func TestNoteSignsEachAttemptWhenItIsSent(t *testing.T) {
	fake := newWorker(t, http.StatusNoContent, "")
	now := time.Unix(goldenTimestamp, 0)
	client, err := New(Config{WorkerURL: fake.URL, Secret: goldenSecret, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	note := goldenVectors[0].note
	if err := client.Note(context.Background(), note); err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Minute)
	if err := client.Note(context.Background(), note); err != nil {
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

// How the Worker's route words a refusal of the body: 400 for one it cannot
// read as a note, 413 for one over its limit.
const (
	malformedRefusal = `{"error":{"code":"invalid_request","message":"A request note needs organization_id, request_id, revision, asker_user_id, owner_user_id, origin, state and changed_at."}}`
	tooLargeRefusal  = `{"error":{"code":"too_large","message":"A request note's body is at most 4 KB."}}`
)

// Only 204 is done, and only the Worker refusing the body itself, a 400 or
// 413 carrying its own error, is for good. Everything else may succeed if sent
// again, or once an operator puts right whatever answered in the Worker's
// place: a 401 is the secret or the clock, 408 and 429 are the Worker asking
// for later, a 403, 404 or 405 is a URL that does not reach the Worker's
// route, the old Worker without it, or Cloudflare, and a 5xx is the Worker
// failing.
func TestNoteTellsARefusalForGoodFromOneWorthRetrying(t *testing.T) {
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
		{"a 404 from a Worker without the route", http.StatusNotFound, words, false},
		{"a 404 worded like the Worker", http.StatusNotFound, `{"error":{"code":"not_found","message":"Not found."}}`, false},
		{"a 405 from the Worker", http.StatusMethodNotAllowed, `{"error":{"code":"method_not_allowed","message":"Method not allowed."}}`, false},
		{"a 401", http.StatusUnauthorized, words, false},
		{"a 408", http.StatusRequestTimeout, words, false},
		{"a 429", http.StatusTooManyRequests, words, false},
		{"a 500", http.StatusInternalServerError, words, false},
		{"a 503 from the Worker", http.StatusServiceUnavailable, `{"error":{"code":"unavailable","message":"The request note did not finish. Try again."}}`, false},
		{"a 504", http.StatusGatewayTimeout, words, false},
		// Answers the contract does not name are no sign the note was taken.
		{"a 200", http.StatusOK, words, false},
		{"a 202", http.StatusAccepted, words, false},
		{"a 307", http.StatusTemporaryRedirect, words, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fake := newWorker(t, test.status, test.answer)
			err := goldenClient(t, fake.URL).Note(context.Background(), goldenVectors[0].note)
			if err == nil {
				t.Fatalf("Note() = nil for %d", test.status)
			}
			if errors.Is(err, ErrPermanent) != test.permanent {
				t.Fatalf("Note() = %v; permanent = %v, want %v", err, errors.Is(err, ErrPermanent), test.permanent)
			}
			said := ": " + strings.Join(strings.Fields(test.answer), " ")
			if !strings.Contains(err.Error(), http.StatusText(test.status)) || !strings.Contains(err.Error(), said) {
				t.Fatalf("Note() = %q, want the status and what the Worker said on one line", err)
			}
		})
	}
}

func TestNoteSaysWhatA401Means(t *testing.T) {
	fake := newWorker(t, http.StatusUnauthorized, "")
	err := goldenClient(t, fake.URL).Note(context.Background(), goldenVectors[0].note)
	if err == nil || errors.Is(err, ErrPermanent) || !strings.Contains(err.Error(), "REALTIME_PURGE_SECRET") ||
		!strings.Contains(err.Error(), "clock") {
		t.Fatalf("Note() = %v, want a retryable error naming the secret and the clock", err)
	}
}

func TestNoteShortensALongAnswer(t *testing.T) {
	fake := newWorker(t, http.StatusBadRequest, strings.Repeat("<p>not found</p> ", 500))
	err := goldenClient(t, fake.URL).Note(context.Background(), goldenVectors[0].note)
	if err == nil || len(err.Error()) > 400 || !strings.HasSuffix(err.Error(), "…") {
		t.Fatalf("Note() = %q (%d bytes), want the answer cut short", err, len(err.Error()))
	}
}

func TestNoteRetriesWhatNeverReachedTheWorker(t *testing.T) {
	gone := newWorker(t, http.StatusNoContent, "")
	gone.Close()
	if err := goldenClient(t, gone.URL).Note(context.Background(), goldenVectors[0].note); err == nil || errors.Is(err, ErrPermanent) {
		t.Fatalf("Note() to a Worker that is down = %v, want a retryable error", err)
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
	if err := client.Note(context.Background(), goldenVectors[0].note); err == nil || errors.Is(err, ErrPermanent) {
		t.Fatalf("Note() to a Worker that does not answer = %v, want a retryable error", err)
	}
}

// roundTripper answers a call without a network.
type roundTripper func(*http.Request) (*http.Response, error)

func (answer roundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return answer(request)
}

// A call the Worker never answers must give up within Timeout, so a batch of
// them stays inside the queue's visibility timeout.
func TestNoteGivesTheWorkerAtMostItsTimeout(t *testing.T) {
	var given time.Duration
	client, err := New(Config{WorkerURL: "https://app.oryxa.in", Secret: goldenSecret, HTTP: &http.Client{
		Transport: roundTripper(func(request *http.Request) (*http.Response, error) {
			deadline, ok := request.Context().Deadline()
			if !ok {
				return nil, errors.New("the call has no deadline")
			}
			given = time.Until(deadline)
			return &http.Response{StatusCode: http.StatusNoContent, Header: http.Header{}, Body: http.NoBody, Request: request}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Note(context.Background(), goldenVectors[0].note); err != nil {
		t.Fatal(err)
	}
	if given > Timeout || given <= Timeout-time.Second {
		t.Fatalf("the call was given %s, want %s", given, Timeout)
	}
}

func TestNoteDoesNotFollowARedirect(t *testing.T) {
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
	err := goldenClient(t, server.URL).Note(context.Background(), goldenVectors[0].note)
	if err == nil || errors.Is(err, ErrPermanent) || elsewhere.Load() != 0 {
		t.Fatalf("Note() = %v, redirect followed %d times; want a retryable error and none", err, elsewhere.Load())
	}
}

// Nothing the Worker would refuse for good is ever sent: a conversation needs
// two different people and ids that can name it, and a note says which
// revision of which request it is, where it stands, and when it got there.
func TestNoteRefusesWhatTheWorkerWouldRefuseForGood(t *testing.T) {
	fake := newWorker(t, http.StatusNoContent, "")
	client := goldenClient(t, fake.URL)
	good := goldenVectors[0].note
	for name, edit := range map[string]func(*Note){
		"no organization":            func(note *Note) { note.OrganizationID = "" },
		"an organization with a dot": func(note *Note) { note.OrganizationID = "org.1" },
		"no request":                 func(note *Note) { note.RequestID = "" },
		"a request with a tilde":     func(note *Note) { note.RequestID = "irq~1" },
		"no asker":                   func(note *Note) { note.AskerUserID = "" },
		"no owner":                   func(note *Note) { note.OwnerUserID = "" },
		"an id too long":             func(note *Note) { note.OwnerUserID = strings.Repeat("u", 129) },
		"the same person twice":      func(note *Note) { note.OwnerUserID = note.AskerUserID },
		"revision zero":              func(note *Note) { note.Revision = 0 },
		"a negative revision":        func(note *Note) { note.Revision = -1 },
		"no origin":                  func(note *Note) { note.Origin = "" },
		"an origin in capitals":      func(note *Note) { note.Origin = "Slack" },
		"a long origin":              func(note *Note) { note.Origin = strings.Repeat("a", 33) },
		"no state":                   func(note *Note) { note.State = "" },
		"a state nobody says":        func(note *Note) { note.State = "expired" },
		"no time":                    func(note *Note) { note.ChangedAt = time.Time{} },
		"nothing":                    func(note *Note) { *note = Note{} },
	} {
		note := good
		edit(&note)
		if err := client.Note(context.Background(), note); err == nil {
			t.Errorf("%s: Note(%+v) = nil", name, note)
		}
	}
	if fake.calls.Load() != 0 {
		t.Fatalf("the Worker was called %d times", fake.calls.Load())
	}
}

// An origin the platform adds later is passed on as it is, and every state
// the ledger's statuses map to is one a note may carry.
func TestNoteTakesEveryStateAndAnyShortOrigin(t *testing.T) {
	fake := newWorker(t, http.StatusNoContent, "")
	client := goldenClient(t, fake.URL)
	for _, state := range States {
		note := goldenVectors[0].note
		note.State = state
		if err := client.Note(context.Background(), note); err != nil {
			t.Errorf("Note(%s) = %v", state, err)
		}
	}
	note := goldenVectors[0].note
	note.Origin = "teams"
	if err := client.Note(context.Background(), note); err != nil {
		t.Errorf("Note(origin teams) = %v", err)
	}
	if got, want := fake.calls.Load(), int32(len(States)+1); got != want {
		t.Fatalf("the Worker was called %d times, want %d", got, want)
	}
}

func TestNewRefusesWhatItCannotReachOrSignWith(t *testing.T) {
	for name, config := range map[string]Config{
		"no URL":         {Secret: goldenSecret},
		"a relative URL": {WorkerURL: "/internal", Secret: goldenSecret},
		"another scheme": {WorkerURL: "ftp://app.oryxa.in", Secret: goldenSecret},
		"no host":        {WorkerURL: "https://", Secret: goldenSecret},
		// The Worker matches Path exactly, so a note under another path would
		// never reach it.
		"a path":    {WorkerURL: "https://app.oryxa.in/web/", Secret: goldenSecret},
		"a query":   {WorkerURL: "https://app.oryxa.in/?x=1", Secret: goldenSecret},
		"no secret": {WorkerURL: "https://app.oryxa.in"},
	} {
		if _, err := New(config); err == nil {
			t.Errorf("%s: New() = nil error", name)
		}
	}
}

func TestIsIDIsWhatAConversationsNameCanHold(t *testing.T) {
	for _, id := range []string{"a", "user-a", "6c1d5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f", "irq_JixqQoilaC2tgma5jqRHtg", "A_-0", strings.Repeat("x", 128)} {
		if !IsID(id) {
			t.Errorf("IsID(%q) = false", id)
		}
	}
	for _, id := range []string{"", " ", "a b", "a~b", "a.b", "a/b", "a\n", "é", strings.Repeat("x", 129)} {
		if IsID(id) {
			t.Errorf("IsID(%q) = true", id)
		}
	}
}
