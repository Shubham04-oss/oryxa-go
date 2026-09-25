package httpserver

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	realtimev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/realtime/v1"
	"github.com/Shubham04-oss/oryxa-go/realtime/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/realtime/internal/purge"
)

// The golden vectors: what the Worker's tests check its signing against, and
// what both sides must agree on byte for byte, the bodies included. The
// secret is used as the characters it is written in, not decoded from hex.
// The signatures were computed outside Go as well:
//
//	printf '%s' "events.$timestamp.$body" | openssl dgst -sha256 -hmac "$secret"
//
// Each body also carries the purge's signature of it, made without the
// "events." prefix, which must never verify here.
const (
	goldenSecret    = "5f2b8e1c9a7d4036b1e8c2f94a6d0e3b7c1f5a9d2e8b4c6f0a3d7e1b9c5f2a84"
	goldenTimestamp = int64(1790208000) // 2026-09-24T00:00:00Z
)

var goldenVectors = []struct {
	name, body, signature, purgeSignature string
}{
	{
		name:           "a message posted",
		body:           `{"events":[{"id":"evt_golden_posted_1","type":"realtime.message.posted.v1","detail":{"organization_id":"org_1","conversation":{"kind":"direct","id":"org_1~user-a~user-b"},"participant_user_ids":["user-a","user-b"],"message":{"id":"msg_1","seq":1},"author":{"kind":"member","user_id":"user-a"},"posted_at":"2026-09-24T10:00:00.000Z"}}]}`,
		signature:      "v1=75442e7357021586eb3717f2c97b091546d26cc31bcfdecafe571f36e7836cfa",
		purgeSignature: "v1=a8cb24541792769a083259a5396d3d53bbd95c34cd72296d65acaf993e8f583e",
	},
	{
		name:           "a side conversation read",
		body:           `{"events":[{"id":"evt_golden_read_1","type":"realtime.conversation.read.v1","detail":{"organization_id":"org_1","conversation":{"kind":"side","id":"org_1~tsp_1~user-a~user-b","teamspace_id":"tsp_1"},"reader_user_id":"user-b","read_through_seq":7,"read_at":"2026-09-24T10:05:00.123Z"}}]}`,
		signature:      "v1=68488d8dd95e857bc0550ff45cbad1cacb526bd14b30b6cdad7b12216d52adfc",
		purgeSignature: "v1=6478f2e3e872191fa991b20d7569b715ed46a1ea533b5590717ab300e5119d8d",
	},
}

// fakeStore keeps every batch it is asked to accept, answering err instead
// while it is set.
type fakeStore struct {
	mu      sync.Mutex
	batches [][]postgres.Received
	err     error
}

func (fake *fakeStore) Accept(_ context.Context, events []postgres.Received) (int, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.err != nil {
		return 0, fake.err
	}
	fake.batches = append(fake.batches, slices.Clone(events))
	return len(events), nil
}

func (fake *fakeStore) accepted() [][]postgres.Received {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return slices.Clone(fake.batches)
}

// at is a clock stopped at the golden timestamp and offset.
func at(offset time.Duration) func() time.Time {
	return func() time.Time { return time.Unix(goldenTimestamp, 0).Add(offset) }
}

func newServer(t *testing.T, store Store, now func() time.Time) (*Server, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	server, err := New(Config{Secret: goldenSecret, Now: now}, store, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return server, &logs
}

// call posts body to the route with the given timestamp and signature
// headers, each left out when empty.
func call(server http.Handler, body, timestamp, signature string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, Path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if timestamp != "" {
		request.Header.Set(TimestampHeader, timestamp)
	}
	if signature != "" {
		request.Header.Set(SignatureHeader, signature)
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	return recorder
}

// signed posts body signed as the Worker signs, now.
func signed(server *Server, body string) *httptest.ResponseRecorder {
	timestamp := server.now().Unix()
	return call(server, body, strconv.FormatInt(timestamp, 10), Sign(goldenSecret, timestamp, []byte(body)))
}

func TestSignMatchesTheGoldenVectors(t *testing.T) {
	for _, vector := range goldenVectors {
		t.Run(vector.name, func(t *testing.T) {
			if got := Sign(goldenSecret, goldenTimestamp, []byte(vector.body)); got != vector.signature {
				t.Fatalf("Sign() = %s, want %s", got, vector.signature)
			}
			// The purge's signature of the same body is the other vector, and
			// never this one.
			if got := purge.Sign(goldenSecret, goldenTimestamp, []byte(vector.body)); got != vector.purgeSignature || got == vector.signature {
				t.Fatalf("purge.Sign() = %s, want %s", got, vector.purgeSignature)
			}
		})
	}
}

// Signed at the golden timestamp, each golden body is accepted whole: its
// event recorded under the Worker's id, as the contract writes it, at the
// time it happened.
func TestTheRouteAcceptsTheGoldenVectors(t *testing.T) {
	want := []postgres.Received{
		{WorkerID: "evt_golden_posted_1", DetailType: realtimev1.MessagePostedDetailType,
			Data: json.RawMessage(`{"organization_id":"org_1","conversation":{"kind":"direct","id":"org_1~user-a~user-b"},` +
				`"participant_user_ids":["user-a","user-b"],"message":{"id":"msg_1","seq":1},` +
				`"author":{"kind":"member","user_id":"user-a"},"posted_at":"2026-09-24T10:00:00Z"}`),
			OccurredAt: time.Date(2026, time.September, 24, 10, 0, 0, 0, time.UTC)},
		{WorkerID: "evt_golden_read_1", DetailType: realtimev1.ConversationReadDetailType,
			Data: json.RawMessage(`{"organization_id":"org_1","conversation":{"kind":"side","id":"org_1~tsp_1~user-a~user-b","teamspace_id":"tsp_1"},` +
				`"reader_user_id":"user-b","read_through_seq":7,"read_at":"2026-09-24T10:05:00.123Z"}`),
			OccurredAt: time.Date(2026, time.September, 24, 10, 5, 0, 123000000, time.UTC)},
	}
	for index, vector := range goldenVectors {
		t.Run(vector.name, func(t *testing.T) {
			store := &fakeStore{}
			server, _ := newServer(t, store, at(time.Second))
			recorder := call(server, vector.body, strconv.FormatInt(goldenTimestamp, 10), vector.signature)
			if recorder.Code != http.StatusNoContent || recorder.Body.Len() != 0 {
				t.Fatalf("answer = %d %s, want 204", recorder.Code, recorder.Body.String())
			}
			batches := store.accepted()
			if len(batches) != 1 || len(batches[0]) != 1 {
				t.Fatalf("accepted = %+v, want the one event", batches)
			}
			got, expected := batches[0][0], want[index]
			if got.WorkerID != expected.WorkerID || got.DetailType != expected.DetailType ||
				string(got.Data) != string(expected.Data) || !got.OccurredAt.Equal(expected.OccurredAt) {
				t.Fatalf("accepted %+v (data %s),\nwant %+v (data %s)", got, got.Data, expected, expected.Data)
			}
		})
	}
}

// A purge's signature of the very same body, at the same time and with the
// same secret, is refused, and nothing is read from the body.
func TestTheRouteRefusesThePurgesSignature(t *testing.T) {
	for _, vector := range goldenVectors {
		t.Run(vector.name, func(t *testing.T) {
			store := &fakeStore{}
			server, _ := newServer(t, store, at(time.Second))
			recorder := call(server, vector.body, strconv.FormatInt(goldenTimestamp, 10), vector.purgeSignature)
			if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), `"code":"unauthorized"`) {
				t.Fatalf("answer = %d %s, want 401", recorder.Code, recorder.Body.String())
			}
			if len(store.accepted()) != 0 {
				t.Fatal("a purge's signature had events accepted")
			}
		})
	}
}

func TestTheRouteRefusesACallTheWorkerDidNotSignNow(t *testing.T) {
	body := goldenVectors[0].body
	timestamp := strconv.FormatInt(goldenTimestamp, 10)
	signature := goldenVectors[0].signature
	for name, test := range map[string]struct {
		timestamp, signature string
		now                  time.Duration
		header               http.Header
	}{
		"no signature":              {timestamp: timestamp},
		"no timestamp":              {signature: signature},
		"neither":                   {},
		"two signatures":            {timestamp: timestamp, signature: signature, header: http.Header{SignatureHeader: {signature}}},
		"two timestamps":            {timestamp: timestamp, signature: signature, header: http.Header{TimestampHeader: {timestamp}}},
		"signed too long ago":       {timestamp: timestamp, signature: signature, now: Window + time.Second},
		"signed too far ahead":      {timestamp: timestamp, signature: signature, now: -Window - time.Second},
		"a timestamp in ms":         {timestamp: timestamp + "000", signature: Sign(goldenSecret, goldenTimestamp*1000, []byte(body))},
		"a signed timestamp":        {timestamp: "+" + timestamp, signature: signature},
		"a timestamp with a space":  {timestamp: timestamp + " 1", signature: signature},
		"no version":                {timestamp: timestamp, signature: strings.TrimPrefix(signature, "v1=")},
		"another version":           {timestamp: timestamp, signature: "v2=" + strings.TrimPrefix(signature, "v1=")},
		"a signature cut short":     {timestamp: timestamp, signature: signature[:len(signature)-2]},
		"not hex":                   {timestamp: timestamp, signature: "v1=" + strings.Repeat("zz", 32)},
		"another secret":            {timestamp: timestamp, signature: Sign(goldenSecret+"x", goldenTimestamp, []byte(body))},
		"another timestamp signed":  {timestamp: timestamp, signature: Sign(goldenSecret, goldenTimestamp+1, []byte(body))},
		"another body signed":       {timestamp: timestamp, signature: Sign(goldenSecret, goldenTimestamp, []byte(body+" "))},
		"the other vector's":        {timestamp: timestamp, signature: goldenVectors[1].signature},
		"the prefix without a dot":  {timestamp: timestamp, signature: hmacOf("events" + timestamp + "." + body)},
		"the prefix at the end":     {timestamp: timestamp, signature: hmacOf(timestamp + "." + body + "events.")},
		"an empty body's signature": {timestamp: timestamp, signature: Sign(goldenSecret, goldenTimestamp, nil)},
	} {
		t.Run(name, func(t *testing.T) {
			store := &fakeStore{}
			server, logs := newServer(t, store, at(test.now))
			request := httptest.NewRequest(http.MethodPost, Path, strings.NewReader(body))
			if test.timestamp != "" {
				request.Header.Set(TimestampHeader, test.timestamp)
			}
			if test.signature != "" {
				request.Header.Set(SignatureHeader, test.signature)
			}
			for key, values := range test.header {
				for _, value := range values {
					request.Header.Add(key, value)
				}
			}
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), `"code":"unauthorized"`) {
				t.Fatalf("answer = %d %s, want 401", recorder.Code, recorder.Body.String())
			}
			if len(store.accepted()) != 0 {
				t.Fatal("events were accepted")
			}
			if strings.Contains(logs.String(), goldenSecret) || !strings.Contains(logs.String(), "the Worker's events refused") {
				t.Fatalf("logs = %s; want the refusal, without the secret", logs.String())
			}
		})
	}
}

// hmacOf is a v1 signature of exactly message, for signing strings the
// Worker must not use.
func hmacOf(message string) string {
	hash := hmac.New(sha256.New, []byte(goldenSecret))
	hash.Write([]byte(message))
	return "v1=" + hex.EncodeToString(hash.Sum(nil))
}

// The window is 300 seconds either way, both ends included.
func TestTheRouteTakesACallSignedWithinTheWindow(t *testing.T) {
	for _, offset := range []time.Duration{-Window, 0, Window} {
		store := &fakeStore{}
		server, _ := newServer(t, store, at(offset))
		recorder := call(server, goldenVectors[0].body, strconv.FormatInt(goldenTimestamp, 10), goldenVectors[0].signature)
		if recorder.Code != http.StatusNoContent || len(store.accepted()) != 1 {
			t.Errorf("signed %s from this clock: answer = %d %s, want 204", -offset, recorder.Code, recorder.Body.String())
		}
	}
}

// The secret is trimmed of what surrounds it, as the Worker trims its own.
func TestTheSecretIsTrimmed(t *testing.T) {
	store := &fakeStore{}
	server, err := New(Config{Secret: "\n " + goldenSecret + " \n", Now: at(0)}, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if recorder := call(server, goldenVectors[1].body, strconv.FormatInt(goldenTimestamp, 10), goldenVectors[1].signature); recorder.Code != http.StatusNoContent {
		t.Fatalf("answer = %d %s, want 204", recorder.Code, recorder.Body.String())
	}
	if _, err := New(Config{Secret: " \n"}, store, nil); err == nil {
		t.Fatal("New() without a secret = nil error")
	}
	if _, err := New(Config{Secret: goldenSecret}, nil, nil); err == nil {
		t.Fatal("New() without a store = nil error")
	}
}

// Too much is refused before the body is read, when it says how long it is,
// and as soon as it runs over when it does not.
func TestTheRouteRefusesTooMuch(t *testing.T) {
	store := &fakeStore{}
	server, _ := newServer(t, store, at(0))
	large := `{"events":[],"padding":"` + strings.Repeat("x", MaxBodyBytes) + `"}`

	// Unsigned, it is still refused for its size: nothing of it is read.
	recorder := call(server, large, "", "")
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a declared body over the limit = %d %s, want 413", recorder.Code, recorder.Body.String())
	}
	request := httptest.NewRequest(http.MethodPost, Path, io.MultiReader(strings.NewReader(large)))
	request.ContentLength = -1
	request.Header.Set(TimestampHeader, strconv.FormatInt(goldenTimestamp, 10))
	request.Header.Set(SignatureHeader, Sign(goldenSecret, goldenTimestamp, []byte(large)))
	chunked := httptest.NewRecorder()
	server.ServeHTTP(chunked, request)
	if chunked.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("an undeclared body over the limit = %d %s, want 413", chunked.Code, chunked.Body.String())
	}

	// A body of exactly the limit is read whole.
	exact := `{"events":[],"padding":"` + strings.Repeat("x", MaxBodyBytes-len(`{"events":[],"padding":""}`)) + `"}`
	if recorder := signed(server, exact); recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "events holds no event") {
		t.Fatalf("a body of exactly the limit = %d %s, want it read, and refused for holding no event", recorder.Code, recorder.Body.String())
	}

	if recorder := signed(server, batchOf(MaxEvents+1)); recorder.Code != http.StatusRequestEntityTooLarge ||
		!strings.Contains(recorder.Body.String(), "at most 100 events") {
		t.Fatalf("%d events = %d %s, want 413", MaxEvents+1, recorder.Code, recorder.Body.String())
	}
	if len(store.accepted()) != 0 {
		t.Fatal("events were accepted")
	}
	if recorder := signed(server, batchOf(MaxEvents)); recorder.Code != http.StatusNoContent {
		t.Fatalf("%d events = %d %s, want 204", MaxEvents, recorder.Code, recorder.Body.String())
	}
	if batches := store.accepted(); len(batches) != 1 || len(batches[0]) != MaxEvents {
		t.Fatalf("accepted %d batches, want one of %d", len(batches), MaxEvents)
	}
}

// batchOf is a batch of count reads, each its own event.
func batchOf(count int) string {
	events := make([]string, count)
	for index := range events {
		events[index] = `{"id":"evt_` + strconv.Itoa(index) + `","type":"realtime.conversation.read.v1","detail":{"organization_id":"org_1",` +
			`"conversation":{"kind":"direct","id":"org_1~user-a~user-b"},"reader_user_id":"user-a","read_through_seq":` +
			strconv.Itoa(index+1) + `,"read_at":"2026-09-24T00:00:00.000Z"}}`
	}
	return `{"events":[` + strings.Join(events, ",") + `]}`
}

// Any event realtime cannot publish refuses the batch, saying which and why,
// and nothing of it is recorded.
func TestTheRouteRefusesABatchWithAnyInvalidEvent(t *testing.T) {
	store := &fakeStore{}
	server, logs := newServer(t, store, at(0))
	valid := `{"id":"evt_ok","type":"realtime.conversation.read.v1","detail":{"organization_id":"org_1",` +
		`"conversation":{"kind":"direct","id":"org_1~user-a~user-b"},"reader_user_id":"user-a","read_through_seq":1,"read_at":"2026-09-24T00:00:00.000Z"}}`
	invalid := `{"id":"evt_bad","type":"realtime.conversation.read.v1","detail":{"organization_id":"org_2",` +
		`"conversation":{"kind":"direct","id":"org_1~user-a~user-b"},"reader_user_id":"user-a","read_through_seq":1,"read_at":"2026-09-24T00:00:00.000Z"}}`
	recorder := signed(server, `{"events":[`+valid+`,`+invalid+`]}`)
	var answer struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil || recorder.Code != http.StatusBadRequest ||
		answer.Error.Code != "invalid_request" || !strings.HasPrefix(answer.Error.Message, "events[1] (evt_bad): detail: conversation.id must start with organization_id") {
		t.Fatalf("answer = %d %s, want 400 naming the second event", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q", recorder.Header().Get("Content-Type"))
	}
	if len(store.accepted()) != 0 {
		t.Fatal("the valid event was accepted with the invalid one")
	}
	if !strings.Contains(logs.String(), "evt_bad") {
		t.Fatalf("the refusal was not logged with its reason:\n%s", logs.String())
	}
}

// A batch that could not be recorded is answered so the Worker sends it again.
func TestTheRouteAsksForTheBatchAgainWhenItCannotRecordIt(t *testing.T) {
	store := &fakeStore{err: errors.New("the database is unreachable")}
	server, logs := newServer(t, store, at(0))
	recorder := signed(server, batchOf(2))
	if recorder.Code != http.StatusInternalServerError || strings.Contains(recorder.Body.String(), "unreachable") {
		t.Fatalf("answer = %d %s, want 500 without the internal error", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(logs.String(), "unreachable") {
		t.Fatalf("the failure was not logged:\n%s", logs.String())
	}
}

func TestTheRouteIsPostAlone(t *testing.T) {
	server, _ := newServer(t, &fakeStore{}, at(0))
	if got := server.Patterns(); !slices.Equal(got, []string{"POST /v1/realtime/events"}) {
		t.Fatalf("Patterns() = %v", got)
	}
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(method, Path, nil))
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want 405", method, recorder.Code)
		}
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, Path+"/", nil))
	if recorder.Code != http.StatusNotFound {
		t.Errorf("POST %s/ = %d, want 404", Path, recorder.Code)
	}
}
