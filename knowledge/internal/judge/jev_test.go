package judge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/transform"
)

// fakeTypeSafe answers each call with the next scripted status and answer,
// and records every request it was sent.
type fakeTypeSafe struct {
	mu       sync.Mutex
	statuses []int
	answers  []string
	requests []jevRequest
	headers  []http.Header
	paths    []string
}

func (fake *fakeTypeSafe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	var request jevRequest
	_ = json.Unmarshal(body, &request)
	index := len(fake.requests)
	fake.requests = append(fake.requests, request)
	fake.headers = append(fake.headers, r.Header.Clone())
	fake.paths = append(fake.paths, r.URL.Path)
	status := http.StatusOK
	if index < len(fake.statuses) {
		status = fake.statuses[index]
	}
	w.WriteHeader(status)
	if index < len(fake.answers) {
		_, _ = io.WriteString(w, fake.answers[index])
	}
}

func (fake *fakeTypeSafe) calls() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return len(fake.requests)
}

// kindAnswer is Jev choosing choice, with the rest of probability on the kind
// named by rest.
func kindAnswer(choice string, probability float64, rest string) string {
	probabilities := map[string]float64{}
	for _, kind := range kinds {
		probabilities[kind.name] = 0
	}
	probabilities[choice] = probability
	probabilities[rest] += 1 - probability
	encoded, _ := json.Marshal(map[string]any{
		"model": "jev-1.13.0",
		"answers": map[string]any{kindQuestion: map[string]any{
			"type": "choice", "choice": choice, "probabilities": probabilities, "confidence": probability,
		}},
		"usage": map[string]int{"input_tokens": 420, "output_tokens": 0},
	})
	return string(encoded)
}

func testJev(t *testing.T, fake *fakeTypeSafe, escalate judger) *Jev {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	jev, err := NewJev(JevConfig{APIKey: "ts_test", BaseURL: server.URL, Escalate: escalate})
	if err != nil {
		t.Fatal(err)
	}
	jev.wait = func(int) time.Duration { return 0 }
	return jev
}

// recordingJudge stands in for the judge Jev escalates to.
type recordingJudge struct {
	calls   int
	verdict domain.Verdict
}

func (judge *recordingJudge) Judge(context.Context, domain.Candidate) (domain.Verdict, error) {
	judge.calls++
	return judge.verdict, nil
}

func TestJevExcludesWhatItSortsAsNoise(t *testing.T) {
	fake := &fakeTypeSafe{answers: []string{kindAnswer("promotion", 0.96, "conversation")}}
	jev := testJev(t, fake, nil)

	verdict, err := jev.Judge(context.Background(), promotion())
	if err != nil {
		t.Fatalf("Judge() error = %v", err)
	}
	if verdict.Keep || !strings.Contains(verdict.Reason, "promotion") || !strings.Contains(verdict.Reason, "jev-1.13.0") {
		t.Fatalf("verdict = %+v, want excluded as a promotion, naming the model", verdict)
	}

	if fake.paths[0] != "/v1/systemone" || fake.headers[0].Get("Authorization") != "Bearer ts_test" {
		t.Fatalf("called %s with %q", fake.paths[0], fake.headers[0].Get("Authorization"))
	}
	sent := fake.requests[0]
	if sent.Model != DefaultJevModel {
		t.Fatalf("model = %q, want %q", sent.Model, DefaultJevModel)
	}
	if sent.State.Source != "gmail" || sent.State.Title != "50% off everything this weekend" ||
		sent.State.Details["labels"] != "INBOX, CATEGORY_UPDATES" || !strings.Contains(sent.State.Content, "Sale ends Sunday") {
		t.Fatalf("state = %+v", sent.State)
	}
	if _, leaked := sent.State.Details["thread_id"]; leaked {
		t.Fatal("an internal id was shown to Jev")
	}
	question := sent.Questions[kindQuestion]
	if question.Type != "choice" || len(question.Criteria) != len(kinds) || question.Criteria["financial_record"] == nil || question.Criteria["other"] == nil {
		t.Fatalf("question = %+v, want a choice among every kind of document, a catch-all among them", question)
	}
}

func TestJevKeepsWhatItSortsAsWorthKeeping(t *testing.T) {
	// Torn between two kinds worth keeping is still sure it is worth keeping.
	fake := &fakeTypeSafe{answers: []string{kindAnswer("conversation", 0.55, "work_material")}}
	escalate := &recordingJudge{}
	jev := testJev(t, fake, escalate)

	verdict, err := jev.Judge(context.Background(), promotion())
	if err != nil {
		t.Fatalf("Judge() error = %v", err)
	}
	if !verdict.Keep || escalate.calls != 0 {
		t.Fatalf("verdict = %+v after %d escalations, want kept without asking anyone else", verdict, escalate.calls)
	}
}

func TestJevEscalatesWhatItIsUnsureOf(t *testing.T) {
	fake := &fakeTypeSafe{answers: []string{kindAnswer("routine_notification", 0.6, "booking")}}
	escalate := &recordingJudge{verdict: domain.Verdict{Keep: true, Reason: "an order with its details"}}
	jev := testJev(t, fake, escalate)

	verdict, err := jev.Judge(context.Background(), promotion())
	if err != nil {
		t.Fatalf("Judge() error = %v", err)
	}
	if escalate.calls != 1 || !verdict.Keep || verdict.Reason != "an order with its details" {
		t.Fatalf("verdict = %+v after %d escalations, want the escalation's", verdict, escalate.calls)
	}
}

func TestJevKeepsWhatItIsUnsureOfWithNobodyToAsk(t *testing.T) {
	fake := &fakeTypeSafe{answers: []string{kindAnswer("newsletter", 0.6, "work_material")}}
	jev := testJev(t, fake, nil)

	verdict, err := jev.Judge(context.Background(), promotion())
	if err != nil {
		t.Fatalf("Judge() error = %v", err)
	}
	if !verdict.Keep {
		t.Fatalf("verdict = %+v, want kept: 60%% sure is not sure enough to lose it", verdict)
	}
}

func TestJevHandsAFileItCannotReadToTheJudgeThatCan(t *testing.T) {
	fake := &fakeTypeSafe{}
	escalate := &recordingJudge{verdict: domain.Verdict{Keep: true, Reason: "a real report"}}
	jev := testJev(t, fake, escalate)
	pdf := domain.Candidate{Provider: "googledrive", Title: "scan_0042.pdf", MIME: transform.MIMEPDF, Original: []byte("%PDF-1.4"), Size: 8}

	verdict, err := jev.Judge(context.Background(), pdf)
	if err != nil {
		t.Fatalf("Judge() error = %v", err)
	}
	if fake.calls() != 0 || escalate.calls != 1 || !verdict.Keep {
		t.Fatalf("Jev called %d times, escalation %d, verdict %+v; want the file read by the judge that can", fake.calls(), escalate.calls, verdict)
	}
}

func TestJevJudgesAFileByItsDetailsWithNobodyToHandItTo(t *testing.T) {
	fake := &fakeTypeSafe{answers: []string{kindAnswer("empty", 0.93, "work_material")}}
	jev := testJev(t, fake, nil)
	deck := domain.Candidate{Provider: "googledrive", Title: "Copy of Untitled presentation", MIME: "application/vnd.ms-powerpoint", Original: []byte("PK"), Size: 2}

	verdict, err := jev.Judge(context.Background(), deck)
	if err != nil {
		t.Fatalf("Judge() error = %v", err)
	}
	if verdict.Keep || !strings.Contains(fake.requests[0].State.Content, "cannot be read as text") {
		t.Fatalf("verdict = %+v, content %q", verdict, fake.requests[0].State.Content)
	}
}

func TestJevTriesAgainWhenThrottledOrOverloaded(t *testing.T) {
	fake := &fakeTypeSafe{
		statuses: []int{http.StatusTooManyRequests, 529, http.StatusOK},
		answers:  []string{"", "", kindAnswer("financial_record", 0.97, "routine_notification")},
	}
	jev := testJev(t, fake, nil)

	verdict, err := jev.Judge(context.Background(), promotion())
	if err != nil {
		t.Fatalf("Judge() error = %v, want the third try to answer", err)
	}
	if fake.calls() != 3 || !verdict.Keep {
		t.Fatalf("calls = %d, verdict = %+v", fake.calls(), verdict)
	}
}

func TestJevReportsItselfUnavailable(t *testing.T) {
	for name, fake := range map[string]*fakeTypeSafe{
		"still overloaded": {statuses: []int{529, 529, 529}},
		"key refused":      {statuses: []int{http.StatusUnauthorized}},
		"request refused":  {statuses: []int{http.StatusUnprocessableEntity}},
		"not a kind given": {answers: []string{strings.Replace(kindAnswer("promotion", 0.9, "spam"), `"choice":"promotion"`, `"choice":"banana"`, 1)}},
		"no answer to it":  {answers: []string{`{"model":"jev-1.13.0","answers":{}}`}},
		"not an answer":    {answers: []string{"<html>gateway</html>"}},
	} {
		t.Run(name, func(t *testing.T) {
			jev := testJev(t, fake, nil)
			if _, err := jev.Judge(context.Background(), promotion()); !errors.Is(err, domain.ErrQualityCheckUnavailable) {
				t.Fatalf("Judge() error = %v, want ErrQualityCheckUnavailable", err)
			}
		})
	}
}

func TestJevDoesNotRetryARefusedKey(t *testing.T) {
	fake := &fakeTypeSafe{statuses: []int{http.StatusUnauthorized, http.StatusOK}, answers: []string{"", kindAnswer("financial_record", 0.9, "spam")}}
	jev := testJev(t, fake, nil)
	if _, err := jev.Judge(context.Background(), promotion()); err == nil {
		t.Fatal("Judge() error = nil for a refused key")
	}
	if fake.calls() != 1 {
		t.Fatalf("calls = %d, want 1: asking again with the same key changes nothing", fake.calls())
	}
}

func TestNewJevRefusesToSendTheKeyInTheClear(t *testing.T) {
	if _, err := NewJev(JevConfig{APIKey: "ts_test", BaseURL: "http://api.typesafe.ai"}); err == nil {
		t.Fatal("NewJev() error = nil for plain HTTP to another machine")
	}
	if _, err := NewJev(JevConfig{APIKey: ""}); err == nil {
		t.Fatal("NewJev() error = nil without a key")
	}
	if _, err := NewJev(JevConfig{APIKey: "ts_test", EscalateBelow: 0.3}); err == nil {
		t.Fatal("NewJev() error = nil for a certainty below a coin flip")
	}
	jev, err := NewJev(JevConfig{APIKey: "ts_test"})
	if err != nil || jev.endpoint != DefaultTypeSafeBaseURL+"/v1/systemone" || jev.model != DefaultJevModel || jev.escalateBelow != DefaultEscalateBelow {
		t.Fatalf("NewJev() = %+v, %v, want TypeSafe's defaults", jev, err)
	}
}

func TestDecideCountsOtherAsNeitherKeepNorExclude(t *testing.T) {
	unsure := Classification{Kind: "other", KeepProbability: 0.3, ExcludeProbability: 0.2}
	keep, certainty, _ := Decide(unsure)
	if !keep || certainty != 0.3 {
		t.Fatalf("Decide() = %t, %.2f; want the larger side with its own low certainty", keep, certainty)
	}
}
