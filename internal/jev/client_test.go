package jev

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTypeSafe answers each call with the next scripted status and body, and
// records every request it was sent.
type fakeTypeSafe struct {
	mu       sync.Mutex
	statuses []int
	bodies   []string
	requests []wireRequest
	headers  []http.Header
}

func (fake *fakeTypeSafe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	raw, _ := io.ReadAll(r.Body)
	var request wireRequest
	_ = json.Unmarshal(raw, &request)
	index := len(fake.requests)
	fake.requests = append(fake.requests, request)
	fake.headers = append(fake.headers, r.Header.Clone())
	status := http.StatusOK
	if index < len(fake.statuses) {
		status = fake.statuses[index]
	}
	w.WriteHeader(status)
	if index < len(fake.bodies) {
		_, _ = io.WriteString(w, fake.bodies[index])
	}
}

func (fake *fakeTypeSafe) calls() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return len(fake.requests)
}

func testClient(t *testing.T, fake *fakeTypeSafe) *Client {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	client, err := New(Config{APIKey: "ts_test", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	client.wait = func(int) time.Duration { return 0 }
	return client
}

func sampleRequest() Request {
	return Request{
		State: map[string]string{"body": "can someone send the sales deck?"},
		Questions: map[string]Question{
			"answer": {
				Type:         TypeChoice,
				Instructions: "Should an agent answer?",
				Criteria: map[string]Option{
					"needs_an_agent": {What: "asks for something"},
					"small_talk":     {What: "chat with no ask in it"},
				},
			},
			"compact": {
				Type:         TypeNoul,
				Instructions: "Has the room moved past its brief?",
			},
		},
	}
}

func TestAskReturnsANormalAnswer(t *testing.T) {
	fake := &fakeTypeSafe{bodies: []string{`{
		"model": "jev-1.4.0",
		"answers": {
			"answer": {"type": "choice", "choice": "needs_an_agent", "confidence": 0.82,
				"probabilities": {"needs_an_agent": 0.82, "small_talk": 0.18}},
			"compact": {"type": "noul", "noul": 0.12}
		},
		"usage": {"input_tokens": 314}
	}`}}
	client := testClient(t, fake)

	result, err := client.Ask(context.Background(), sampleRequest())
	if err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	if result.Model != "jev-1.4.0" || result.InputTokens != 314 {
		t.Fatalf("result = %+v, want the model and token count TypeSafe sent", result)
	}
	answer := result.Answers["answer"]
	if answer.Choice != "needs_an_agent" || answer.Confidence != 0.82 || answer.Probabilities["small_talk"] != 0.18 {
		t.Fatalf("answers[answer] = %+v", answer)
	}
	if compact := result.Answers["compact"]; compact.Noul != 0.12 {
		t.Fatalf("answers[compact] = %+v", compact)
	}

	if fake.headers[0].Get("Authorization") != "Bearer ts_test" || fake.headers[0].Get("Content-Type") != "application/json" {
		t.Fatalf("headers = %+v", fake.headers[0])
	}
	sent := fake.requests[0]
	if sent.Model != DefaultModel {
		t.Fatalf("model sent = %q, want %q", sent.Model, DefaultModel)
	}
	if sent.Questions["answer"].Type != TypeChoice || sent.Questions["compact"].Type != TypeNoul {
		t.Fatalf("questions sent = %+v", sent.Questions)
	}
}

func TestAskTriesAgainWhenThrottledOrOverloaded(t *testing.T) {
	answer := `{"model": "jev-1.4.0", "answers": {
		"answer": {"type": "choice", "choice": "small_talk", "confidence": 0.9,
			"probabilities": {"needs_an_agent": 0.1, "small_talk": 0.9}},
		"compact": {"type": "noul", "noul": 0.05}
	}}`
	fake := &fakeTypeSafe{
		statuses: []int{http.StatusTooManyRequests, 529, http.StatusOK},
		bodies:   []string{"", "", answer},
	}
	client := testClient(t, fake)

	result, err := client.Ask(context.Background(), sampleRequest())
	if err != nil {
		t.Fatalf("Ask() error = %v, want the third try to answer", err)
	}
	if fake.calls() != 3 || result.Answers["answer"].Choice != "small_talk" {
		t.Fatalf("calls = %d, result = %+v", fake.calls(), result)
	}
}

func TestAskGivesUpOnAStatusItDoesNotRetry(t *testing.T) {
	fake := &fakeTypeSafe{statuses: []int{http.StatusUnauthorized, http.StatusOK}}
	client := testClient(t, fake)

	if _, err := client.Ask(context.Background(), sampleRequest()); err == nil {
		t.Fatal("Ask() error = nil for a refused key")
	}
	if fake.calls() != 1 {
		t.Fatalf("calls = %d, want 1: a refused key will not be accepted on a second try", fake.calls())
	}
}

func TestAskRejectsAMalformedBody(t *testing.T) {
	for name, fake := range map[string]*fakeTypeSafe{
		"not json":                 {bodies: []string{"<html>gateway timeout</html>"}},
		"missing an answer":        {bodies: []string{`{"model":"jev-1.4.0","answers":{}}`}},
		"wrong type":               {bodies: []string{`{"model":"jev-1.4.0","answers":{"answer":{"type":"noul","noul":0.5},"compact":{"type":"noul","noul":0.1}}}`}},
		"no choice at all":         {bodies: []string{`{"model":"jev-1.4.0","answers":{"answer":{"type":"choice","confidence":0.9,"probabilities":{"small_talk":0.9}},"compact":{"type":"noul","noul":0.1}}}`}},
		"probability out of range": {bodies: []string{`{"model":"jev-1.4.0","answers":{"answer":{"type":"choice","choice":"small_talk","confidence":1.4,"probabilities":{"small_talk":1.4}},"compact":{"type":"noul","noul":0.1}}}`}},
	} {
		t.Run(name, func(t *testing.T) {
			client := testClient(t, fake)
			if _, err := client.Ask(context.Background(), sampleRequest()); err == nil {
				t.Fatal("Ask() error = nil for a malformed response")
			}
			if fake.calls() != 1 {
				t.Fatalf("calls = %d, want 1: a body that will not parse is not worth retrying", fake.calls())
			}
		})
	}
}

// TestAskLeavesAnUnrecognisedChoiceToItsCaller documents a deliberate
// choice: Criteria is opaque to this package (any, caller-defined), so
// whether Choice names one of its own options is the caller's own policy to
// check, not this package's. A caller that routes to a seat, say, needs to
// see a choice that no longer matches anything in its own state, rather
// than have this package turn it into an error first.
func TestAskLeavesAnUnrecognisedChoiceToItsCaller(t *testing.T) {
	fake := &fakeTypeSafe{bodies: []string{`{"model":"jev-1.4.0","answers":{
		"answer":{"type":"choice","choice":"a_stale_option","confidence":0.7,"probabilities":{"a_stale_option":0.7,"small_talk":0.3}},
		"compact":{"type":"noul","noul":0.1}
	}}`}}
	client := testClient(t, fake)
	result, err := client.Ask(context.Background(), sampleRequest())
	if err != nil {
		t.Fatalf("Ask() error = %v, want the unrecognised choice handed back rather than rejected", err)
	}
	if result.Answers["answer"].Choice != "a_stale_option" {
		t.Fatalf("answers[answer] = %+v", result.Answers["answer"])
	}
}

func TestNewRefusesANonHTTPSBase(t *testing.T) {
	if _, err := New(Config{APIKey: "ts_test", BaseURL: "http://api.typesafe.ai"}); err == nil {
		t.Fatal("New() error = nil for plain HTTP to another machine")
	}
	if _, err := New(Config{APIKey: ""}); err == nil {
		t.Fatal("New() error = nil without a key")
	}
	client, err := New(Config{APIKey: "ts_test"})
	if err != nil || client.endpoint != DefaultBaseURL+systemOnePath || client.model != DefaultModel {
		t.Fatalf("New() = %+v, %v, want TypeSafe's defaults", client, err)
	}
	if _, err := New(Config{APIKey: "ts_test", BaseURL: "http://localhost:9999"}); err != nil {
		t.Fatalf("New() error = %v, want plain HTTP allowed to this machine", err)
	}
}

func TestAskRequiresAtLeastOneQuestion(t *testing.T) {
	client := testClient(t, &fakeTypeSafe{})
	if _, err := client.Ask(context.Background(), Request{State: "x"}); err == nil {
		t.Fatal("Ask() error = nil for a request with no questions")
	}
}

func TestAskBoundsAnOversizedResponse(t *testing.T) {
	fake := &fakeTypeSafe{bodies: []string{`{"model":"jev","answers":{` + strings.Repeat(" ", 64) + `}}`}}
	client := testClient(t, fake)
	client.maxResponseBytes = 16
	if _, err := client.Ask(context.Background(), sampleRequest()); err == nil {
		t.Fatal("Ask() error = nil for a response larger than the configured cap")
	}
	if fake.calls() != 1 {
		t.Fatalf("calls = %d, want 1: an oversized response is not worth retrying", fake.calls())
	}
}
