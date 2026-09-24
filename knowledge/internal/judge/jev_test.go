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
	unsure := Classification{Kind: "other", Probabilities: map[string]float64{"other": 0.5, "conversation": 0.3, "promotion": 0.2}}
	keep, certainty, _ := Decide(unsure, domain.IncludedKinds(nil))
	if !keep || certainty != 0.3 {
		t.Fatalf("Decide() = %t, %.2f; want the larger side with its own low certainty", keep, certainty)
	}
}

func TestDecideIndexesWhatTheSourceChose(t *testing.T) {
	update := Classification{Kind: "event_update", Probabilities: map[string]float64{"event_update": 0.95, "promotion": 0.05}}
	if keep, _, _ := Decide(update, domain.IncludedKinds(nil)); keep {
		t.Fatal("Decide() kept an event update, which a source does not index by default")
	}
	if keep, certainty, _ := Decide(update, domain.IncludedKinds([]string{"event_update"})); !keep || certainty != 0.95 {
		t.Fatalf("Decide() = %t, %.2f; want an event update kept where the member chose them", keep, certainty)
	}
}

func TestJevKeepsItsAnswerAsTheJudgment(t *testing.T) {
	fake := &fakeTypeSafe{answers: []string{kindAnswer("promotion", 0.96, "conversation")}}
	jev := testJev(t, fake, nil)
	verdict, err := jev.Judge(context.Background(), promotion())
	if err != nil {
		t.Fatalf("Judge() error = %v", err)
	}
	kept := decodeJudgment(verdict.Judgment)
	if kept.Jev == nil || kept.Jev.Kind != "promotion" || kept.Jev.Probabilities["promotion"] != 0.96 || kept.Gemini != nil {
		t.Fatalf("judgment = %s, want Jev's classification alone", verdict.Judgment)
	}
}

func TestJevKeepsBothAnswersWhenItEscalates(t *testing.T) {
	fake := &fakeTypeSafe{answers: []string{kindAnswer("routine_notification", 0.6, "booking")}}
	escalate := &recordingJudge{verdict: domain.Verdict{
		Keep: true, Reason: "an order with its details",
		Judgment: judgment{Gemini: &geminiVerdict{Keep: true, Reason: "an order with its details"}}.encode(),
	}}
	jev := testJev(t, fake, escalate)
	verdict, err := jev.Judge(context.Background(), promotion())
	if err != nil {
		t.Fatalf("Judge() error = %v", err)
	}
	kept := decodeJudgment(verdict.Judgment)
	if kept.Jev == nil || kept.Gemini == nil || !kept.Gemini.Keep {
		t.Fatalf("judgment = %s, want both answers", verdict.Judgment)
	}
}

func TestJevDecidesForTheKindsTheSourceIndexes(t *testing.T) {
	fake := &fakeTypeSafe{answers: []string{kindAnswer("event_update", 0.97, "promotion")}}
	jev := testJev(t, fake, nil)
	update := promotion()
	update.Include = []string{"conversation", "event_update"}
	verdict, err := jev.Judge(context.Background(), update)
	if err != nil {
		t.Fatalf("Judge() error = %v", err)
	}
	if !verdict.Keep {
		t.Fatalf("verdict = %+v, want an event update kept where the member chose them", verdict)
	}
}

func TestRedecideDrawsTheVerdictAgainForAChangedChoice(t *testing.T) {
	jev, err := NewJev(JevConfig{APIKey: "ts_test"})
	if err != nil {
		t.Fatal(err)
	}
	sure := judgment{Jev: &Classification{Kind: "event_update", Probabilities: map[string]float64{"event_update": 0.95, "promotion": 0.05}, Model: "jev-1.13.0"}}.encode()
	if verdict, ok := jev.Redecide(sure, nil); !ok || verdict.Keep {
		t.Fatalf("Redecide(default) = %+v, %t; want excluded without asking", verdict, ok)
	}
	if verdict, ok := jev.Redecide(sure, []string{"event_update"}); !ok || !verdict.Keep {
		t.Fatalf("Redecide(event updates) = %+v, %t; want kept without asking", verdict, ok)
	}

	// Unsure under the choice, with the escalation's own answer kept: that
	// answer stands.
	unsure := judgment{
		Jev:    &Classification{Kind: "routine_notification", Probabilities: map[string]float64{"routine_notification": 0.6, "booking": 0.4}},
		Gemini: &geminiVerdict{Keep: true, Reason: "an order"},
	}.encode()
	if verdict, ok := jev.Redecide(unsure, nil); !ok || !verdict.Keep || verdict.Reason != "an order" {
		t.Fatalf("Redecide(unsure, escalated) = %+v, %t; want the escalation's verdict", verdict, ok)
	}

	// Unsure, nothing else kept, and a judge to ask: it must be asked.
	withEscalation, err := NewJev(JevConfig{APIKey: "ts_test", Escalate: &recordingJudge{}})
	if err != nil {
		t.Fatal(err)
	}
	jevOnly := judgment{Jev: &Classification{Kind: "routine_notification", Probabilities: map[string]float64{"routine_notification": 0.6, "booking": 0.4}}}.encode()
	if _, ok := withEscalation.Redecide(jevOnly, nil); ok {
		t.Fatal("Redecide() decided a document Jev was unsure of, with a judge there to ask")
	}
	if _, ok := jev.Redecide(nil, nil); ok {
		t.Fatal("Redecide() decided from no judgment at all")
	}
}

// A document Jev was unsure of is categorized by the judge it escalated to,
// which read it because Jev could not say: Judge and Redecide give the same
// category, and a member's changed choice of kinds, which can make Jev sure
// enough to decide alone, does not change what kind of document it is.
func TestTheEscalationsCategoryIsTheDocuments(t *testing.T) {
	for name, testCase := range map[string]struct {
		escalated string
		want      string
	}{
		"the escalation named one":     {escalated: "booking", want: "booking"},
		"the escalation named nothing": {escalated: "", want: "routine_notification"},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeTypeSafe{answers: []string{kindAnswer("routine_notification", 0.6, "booking")}}
			escalate := &recordingJudge{verdict: domain.Verdict{
				Keep: true, Reason: "an order with its details", Category: testCase.escalated,
				Judgment: judgment{Gemini: &geminiVerdict{Keep: true, Reason: "an order with its details", Category: testCase.escalated}}.encode(),
			}}
			jev := testJev(t, fake, escalate)
			verdict, err := jev.Judge(context.Background(), promotion())
			if err != nil {
				t.Fatal(err)
			}
			if escalate.calls != 1 {
				t.Fatalf("escalations = %d, want 1", escalate.calls)
			}
			if verdict.Category != testCase.want {
				t.Fatalf("Judge() category = %q, want %q", verdict.Category, testCase.want)
			}
			again, ok := jev.Redecide(verdict.Judgment, promotion().Include)
			if !ok || again.Category != verdict.Category {
				t.Fatalf("Redecide() = %q (%t), Judge() = %q: the same judgment gave two categories", again.Category, ok, verdict.Category)
			}
			// Both kinds chosen: Jev is sure it is worth keeping and decides
			// alone, but the document is still what the escalation said.
			chosen, ok := jev.Redecide(verdict.Judgment, []string{"routine_notification", "booking"})
			if !ok || !chosen.Keep || chosen.Category != testCase.want {
				t.Fatalf("Redecide(both kinds) = %+v (%t), want kept as %q", chosen, ok, testCase.want)
			}
		})
	}
}
