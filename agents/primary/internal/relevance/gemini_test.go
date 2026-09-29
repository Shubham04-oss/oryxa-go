package relevance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genai"
)

// vertex stands in for Vertex AI's generateContent: a real Gen AI client
// talks to it over HTTP, so what a judge sends and how it reads what comes
// back are tested on the wire, not through a seam. It keeps every request.
type vertex struct {
	mu       sync.Mutex
	requests []vertexRequest
	// answer decides the reply to one request; it answers with a category
	// list of no findings when nil.
	answer func(n int, r *http.Request) reply
}

// vertexRequest is one request as it arrived.
type vertexRequest struct {
	path string
	body map[string]any
}

// reply is what the fake sends back.
type reply struct {
	status int
	body   string
	// wait holds the reply back until it has passed or the client has gone.
	wait time.Duration
}

func (fake *vertex) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	body := map[string]any{}
	_ = json.Unmarshal(raw, &body)
	fake.mu.Lock()
	n := len(fake.requests)
	fake.requests = append(fake.requests, vertexRequest{path: r.URL.Path, body: body})
	fake.mu.Unlock()

	answer := reply{status: http.StatusOK, body: modelSays(`{"asked":[],"extras":[],"certainty":0.9}`)}
	if fake.answer != nil {
		answer = fake.answer(n, r)
	}
	if answer.wait > 0 {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(answer.wait):
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if answer.status != 0 {
		w.WriteHeader(answer.status)
	}
	fmt.Fprint(w, answer.body)
}

func (fake *vertex) count() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return len(fake.requests)
}

func (fake *vertex) last(t *testing.T) vertexRequest {
	t.Helper()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.requests) == 0 {
		t.Fatal("the model was never asked")
	}
	return fake.requests[len(fake.requests)-1]
}

// modelSays is the response of a model that answered with text.
func modelSays(text string) string {
	encoded, _ := json.Marshal(text)
	return `{"candidates":[{"content":{"role":"model","parts":[{"text":` + string(encoded) + `}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}`
}

// clientOn is a real Gen AI client whose Vertex is fake.
func clientOn(t *testing.T, fake *vertex) *genai.Client {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	for _, name := range []string{"GOOGLE_API_KEY", "GEMINI_API_KEY", "GOOGLE_VERTEX_BASE_URL", "GOOGLE_GENAI_USE_VERTEXAI"} {
		t.Setenv(name, "")
	}
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		Backend: genai.BackendVertexAI, Project: "oryxa-test", Location: "global",
		HTTPClient: server.Client(), HTTPOptions: genai.HTTPOptions{BaseURL: server.URL},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

// judgeOn is a Gemini judge on a fake Vertex.
func judgeOn(t *testing.T, fake *vertex, timeout time.Duration) Judge {
	t.Helper()
	return NewGemini(clientOn(t, fake), "gemini-3.5-flash-lite", timeout)
}

// private is an input whose sources may have read private material, so the
// judge asks the model.
func private(request string, answer ...string) Input {
	return Input{Request: request, Answer: answer, Sources: []string{"apps_run_read_tool"}}
}

// find reads a value from a decoded request by its path, or nil.
func find(value any, path ...any) any {
	for _, step := range path {
		switch key := step.(type) {
		case string:
			object, ok := value.(map[string]any)
			if !ok {
				return nil
			}
			value = object[key]
		case int:
			list, ok := value.([]any)
			if !ok || key >= len(list) {
				return nil
			}
			value = list[key]
		}
	}
	return value
}

func TestTheJudgeReadsTheCategoriesTheModelNames(t *testing.T) {
	fake := &vertex{answer: func(int, *http.Request) reply {
		return reply{body: modelSays(`{"asked":["health"],"extras":["hr","credentials"],"certainty":0.8}`)}
	}}
	result := judgeOn(t, fake, time.Second).Judge(context.Background(),
		private("Is Dev out sick tomorrow?", "Yes. Also Arjun is on a plan, and the password is hunter2."))

	if !result.Judged || !result.Hold || result.Err != nil || result.Truncated {
		t.Fatalf("result = %+v, want judged and held for its extras, with no error", result)
	}
	if got := fmt.Sprint(result.Asked, result.Extras); got != "[health] [hr credentials]" {
		t.Fatalf("categories = %s, want asked health and extras hr and credentials", got)
	}
	if result.Certainty != 0.8 || result.Model != "gemini-3.5-flash-lite" || result.Latency <= 0 {
		t.Fatalf("result = %+v, want certainty 0.8, the model named, and a latency", result)
	}
}

// TestTheRequestIsAskedForAsDesigned checks what goes over the wire: one
// call, temperature zero, a JSON answer of a fixed shape whose categories are
// the six, the instruction as the system instruction, and the data in the
// user turn.
func TestTheRequestIsAskedForAsDesigned(t *testing.T) {
	fake := &vertex{}
	judgeOn(t, fake, time.Second).Judge(context.Background(), Input{
		Request: "What is the status of the billing migration?", Context: []string{"Anna: any news?"},
		Answer: []string{"Eighty percent done."}, Sources: []string{"knowledge_search"},
	})
	if fake.count() != 1 {
		t.Fatalf("the model was asked %d times, want once", fake.count())
	}
	request := fake.last(t)
	if !strings.HasSuffix(request.path, "/models/gemini-3.5-flash-lite:generateContent") {
		t.Fatalf("path = %s, want generateContent on the model named", request.path)
	}
	config := find(request.body, "generationConfig")
	if temperature, ok := find(config, "temperature").(float64); !ok || temperature != 0 {
		t.Fatalf("temperature = %v, want 0 sent explicitly", find(config, "temperature"))
	}
	if find(config, "responseMimeType") != "application/json" {
		t.Fatalf("responseMimeType = %v", find(config, "responseMimeType"))
	}
	schema := find(config, "responseSchema")
	if find(schema, "type") != "OBJECT" {
		t.Fatalf("schema = %v, want an object", schema)
	}
	for _, list := range []string{"asked", "extras"} {
		items := find(schema, "properties", list, "items")
		if find(items, "type") != "STRING" {
			t.Fatalf("%s is not a list of strings: %v", list, items)
		}
		enum, _ := find(items, "enum").([]any)
		if len(enum) != len(Categories()) {
			t.Fatalf("%s may name %v, want the six categories", list, enum)
		}
		for index, category := range Categories() {
			if enum[index] != string(category) {
				t.Fatalf("%s enum[%d] = %v, want %s", list, index, enum[index], category)
			}
		}
	}
	if find(schema, "properties", "certainty", "type") != "NUMBER" {
		t.Fatalf("certainty is not a number: %v", find(schema, "properties", "certainty"))
	}
	if required, _ := find(schema, "required").([]any); len(required) != 3 {
		t.Fatalf("required = %v, want all three", required)
	}
	if find(request.body, "systemInstruction", "parts", 0, "text") != instruction {
		t.Fatal("the instruction is not the system instruction")
	}
	user, _ := find(request.body, "contents", 0, "parts", 0, "text").(string)
	for _, want := range []string{"=== BEGIN REQUEST ", "=== BEGIN ANSWER ", "What is the status of the billing migration?", "Eighty percent done.", "knowledge_search", "Anna: any news?"} {
		if !strings.Contains(user, want) {
			t.Fatalf("the model was not shown %q:\n%s", want, user)
		}
	}
	if find(request.body, "contents", 0, "role") != "user" {
		t.Fatalf("the data is not in a user turn: %v", find(request.body, "contents"))
	}
	for _, setting := range find(request.body, "safetySettings").([]any) {
		if find(setting, "threshold") != "OFF" {
			t.Fatalf("safety setting %v: the model must read private material, not refuse it", setting)
		}
	}
}

func TestTheModelThinksAtALowLevelOnlyWhereItTakesOne(t *testing.T) {
	for model, want := range map[string]any{"gemini-3.5-flash-lite": "LOW", "gemini-3.8-flash": "LOW", "gemini-2.5-flash": nil} {
		fake := &vertex{}
		NewGemini(clientOn(t, fake), model, time.Second).Judge(context.Background(), private("q", "a"))
		if got := find(fake.last(t).body, "generationConfig", "thinkingConfig", "thinkingLevel"); got != want {
			t.Fatalf("%s thinks at %v, want %v", model, got, want)
		}
	}
}

// TestCertaintyNeverDecidesTheHold: the same categories hold or do not
// whatever certainty comes with them.
func TestCertaintyNeverDecidesTheHold(t *testing.T) {
	for _, test := range []struct {
		name      string
		answer    string
		regulated bool
		hold      bool
	}{
		{"an extra the model doubts", `{"asked":[],"extras":["health"],"certainty":0.01}`, false, true},
		{"an extra the model is sure of", `{"asked":[],"extras":["health"],"certainty":1}`, false, true},
		{"nothing, though the model doubts it", `{"asked":[],"extras":[],"certainty":0.01}`, false, false},
		{"nothing, and the model is sure", `{"asked":["hr"],"extras":[],"certainty":1}`, false, false},
		{"regulated, asked for HR, sure", `{"asked":["hr"],"extras":[],"certainty":1}`, true, true},
		{"regulated, asked for HR, unsure", `{"asked":["hr"],"extras":[],"certainty":0}`, true, true},
		{"regulated, asked for personal life", `{"asked":["personal_life"],"extras":[],"certainty":0.99}`, true, false},
		{"no certainty at all", `{"asked":[],"extras":["credentials"]}`, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &vertex{answer: func(int, *http.Request) reply { return reply{body: modelSays(test.answer)} }}
			input := private("q", "a")
			input.Regulated = test.regulated
			result := judgeOn(t, fake, time.Second).Judge(context.Background(), input)
			if result.Err != nil || result.Hold != test.hold {
				t.Fatalf("result = %+v, want hold=%v", result, test.hold)
			}
		})
	}
}

func TestTheAnswerIsReadLeniently(t *testing.T) {
	for _, test := range []struct {
		name       string
		answer     string
		certainty  float64
		wantHolds  bool
		wantAsk    string
		wantExtras string
	}{
		{name: "fenced JSON", answer: "```json\n{\"asked\":[],\"extras\":[\"hr\"],\"certainty\":0.5}\n```", wantHolds: true, wantAsk: "[]", wantExtras: "[hr]", certainty: 0.5},
		{name: "labels in any case", answer: `{"asked":["Health"],"extras":[" HR "],"certainty":0.5}`, wantHolds: true, wantAsk: "[health]", wantExtras: "[hr]", certainty: 0.5},
		{name: "duplicates once, in the order of the six", answer: `{"asked":[],"extras":["credentials","health","health","personal_life"],"certainty":0.5}`, wantHolds: true, wantAsk: "[]", wantExtras: "[personal_life health credentials]", certainty: 0.5},
		{name: "certainty above one is one", answer: `{"asked":[],"extras":[],"certainty":7}`, wantAsk: "[]", wantExtras: "[]", certainty: 1},
		{name: "certainty below zero is zero", answer: `{"asked":[],"extras":[],"certainty":-3}`, wantAsk: "[]", wantExtras: "[]", certainty: 0},
		{name: "a field it was not asked for is ignored", answer: `{"asked":[],"extras":[],"certainty":0.5,"reason":"whatever"}`, wantAsk: "[]", wantExtras: "[]", certainty: 0.5},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &vertex{answer: func(int, *http.Request) reply { return reply{body: modelSays(test.answer)} }}
			result := judgeOn(t, fake, time.Second).Judge(context.Background(), private("q", "a"))
			if result.Err != nil {
				t.Fatalf("Err = %v", result.Err)
			}
			if fmt.Sprint(result.Asked) != test.wantAsk || fmt.Sprint(result.Extras) != test.wantExtras || result.Hold != test.wantHolds || result.Certainty != test.certainty {
				t.Fatalf("result = %+v, want asked %s extras %s hold %v certainty %v", result, test.wantAsk, test.wantExtras, test.wantHolds, test.certainty)
			}
		})
	}
}

// TestAnAnswerThatCannotBeCheckedIsHeldWithTheReason: whatever goes wrong, a
// caller gets Hold and an Err naming why, and no categories.
func TestAnAnswerThatCannotBeCheckedIsHeldWithTheReason(t *testing.T) {
	echo := "PRIVATE-ECHO: Priya is on a performance plan"
	for _, test := range []struct {
		name    string
		reply   reply
		timeout time.Duration
		kind    string
		is      error
	}{
		{name: "malformed JSON", reply: reply{body: modelSays("this is not json")}, kind: "unparseable", is: ErrUnparseable},
		{name: "truncated JSON", reply: reply{body: modelSays(`{"asked":[],"extras":["he`)}, kind: "unparseable", is: ErrUnparseable},
		{name: "a list missing", reply: reply{body: modelSays(`{"asked":[],"certainty":1}`)}, kind: "unparseable", is: ErrUnparseable},
		{name: "extras null", reply: reply{body: modelSays(`{"asked":[],"extras":null,"certainty":1}`)}, kind: "unparseable", is: ErrUnparseable},
		{name: "extras not a list", reply: reply{body: modelSays(`{"asked":[],"extras":"health","certainty":1}`)}, kind: "unparseable", is: ErrUnparseable},
		{name: "a JSON array, not an object", reply: reply{body: modelSays(`["health"]`)}, kind: "unparseable", is: ErrUnparseable},
		{name: "an unknown category among extras", reply: reply{body: modelSays(`{"asked":[],"extras":["salary"],"certainty":0.5}`)}, kind: "category", is: ErrUnknownCategory},
		{name: "an unknown category among asked", reply: reply{body: modelSays(`{"asked":["medical"],"extras":[],"certainty":0.5}`)}, kind: "category", is: ErrUnknownCategory},
		{name: "none as a category", reply: reply{body: modelSays(`{"asked":[],"extras":["none"],"certainty":0.5}`)}, kind: "category", is: ErrUnknownCategory},
		{name: "an empty label", reply: reply{body: modelSays(`{"asked":[],"extras":[""],"certainty":0.5}`)}, kind: "category", is: ErrUnknownCategory},
		{name: "no candidates", reply: reply{body: `{"candidates":[]}`}, kind: "empty", is: ErrEmpty},
		{name: "an empty text", reply: reply{body: modelSays("  ")}, kind: "empty", is: ErrEmpty},
		{name: "no body at all", reply: reply{body: `{}`}, kind: "empty", is: ErrEmpty},
		{name: "a prompt the model blocked", reply: reply{body: `{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT"}}`}, kind: "blocked", is: ErrBlocked},
		{name: "an answer the model stopped for safety", reply: reply{body: `{"candidates":[{"finishReason":"SAFETY"}]}`}, kind: "blocked", is: ErrBlocked},
		{name: "a server error", reply: reply{status: http.StatusInternalServerError, body: `{"error":{"code":500,"message":"` + echo + `","status":"INTERNAL"}}`}, kind: "unavailable", is: ErrUnavailable},
		{name: "overloaded", reply: reply{status: http.StatusServiceUnavailable, body: `{"error":{"code":503,"message":"overloaded","status":"UNAVAILABLE"}}`}, kind: "unavailable", is: ErrUnavailable},
		{name: "rate limited", reply: reply{status: http.StatusTooManyRequests, body: `{"error":{"code":429,"message":"quota","status":"RESOURCE_EXHAUSTED"}}`}, kind: "unavailable", is: ErrUnavailable},
		{name: "a request the model refuses", reply: reply{status: http.StatusBadRequest, body: `{"error":{"code":400,"message":"` + echo + `","status":"INVALID_ARGUMENT"}}`}, kind: "unavailable", is: ErrUnavailable},
		{name: "an error that is not JSON", reply: reply{status: http.StatusBadGateway, body: "<html>" + echo + "</html>"}, kind: "unavailable", is: ErrUnavailable},
		{name: "a model that does not answer in time", reply: reply{wait: 3 * time.Second, body: modelSays(`{"asked":[],"extras":[],"certainty":1}`)}, timeout: 50 * time.Millisecond, kind: "timeout", is: ErrTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &vertex{answer: func(int, *http.Request) reply { return test.reply }}
			timeout := test.timeout
			if timeout == 0 {
				timeout = 5 * time.Second
			}
			started := time.Now()
			result := judgeOn(t, fake, timeout).Judge(context.Background(), private("Is she free?", "Sensitive answer text about Priya."))
			if !result.Judged || !result.Hold || result.Err == nil {
				t.Fatalf("result = %+v, want judged, held, with an error", result)
			}
			if fake.count() != 1 {
				t.Fatalf("the model was asked %d times, want once: a check that fails is held, not tried again", fake.count())
			}
			if !errors.Is(result.Err, test.is) || ErrKind(result.Err) != test.kind {
				t.Fatalf("Err = %v (kind %s), want %v (kind %s)", result.Err, ErrKind(result.Err), test.is, test.kind)
			}
			if len(result.Asked) != 0 || len(result.Extras) != 0 || result.Certainty != 0 {
				t.Fatalf("result = %+v, want no categories from an answer that was not read", result)
			}
			if strings.Contains(result.Err.Error(), "PRIVATE-ECHO") || strings.Contains(result.Err.Error(), "Priya") {
				t.Fatalf("Err = %q quotes text", result.Err)
			}
			if test.kind == "timeout" {
				if took := time.Since(started); took > time.Second {
					t.Fatalf("the judge took %s to give up on a %s timeout", took, timeout)
				}
				if result.Latency < timeout/2 {
					t.Fatalf("latency = %s, want about the timeout", result.Latency)
				}
			}
		})
	}
}

func TestAServerErrorSaysItsStatus(t *testing.T) {
	fake := &vertex{answer: func(int, *http.Request) reply {
		return reply{status: 500, body: `{"error":{"code":500,"message":"x"}}`}
	}}
	result := judgeOn(t, fake, time.Second).Judge(context.Background(), private("q", "a"))
	var failed *failure
	if !errors.As(result.Err, &failed) || failed.status != 500 || !strings.Contains(result.Err.Error(), "status 500") {
		t.Fatalf("Err = %v, want the status 500 in it", result.Err)
	}
}

func TestACancelledCheckIsHeldAsCancelled(t *testing.T) {
	fake := &vertex{answer: func(int, *http.Request) reply { return reply{wait: 3 * time.Second} }}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)
	result := judgeOn(t, fake, 5*time.Second).Judge(ctx, private("q", "a"))
	if !result.Hold || ErrKind(result.Err) != "canceled" || !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("result = %+v (kind %s), want held as cancelled", result, ErrKind(result.Err))
	}
}

func TestACallersDeadlineIsATimeoutToo(t *testing.T) {
	fake := &vertex{answer: func(int, *http.Request) reply { return reply{wait: 3 * time.Second} }}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	result := judgeOn(t, fake, 5*time.Second).Judge(ctx, private("q", "a"))
	if !result.Hold || ErrKind(result.Err) != "timeout" {
		t.Fatalf("result = %+v (kind %s), want held as a timeout", result, ErrKind(result.Err))
	}
}

// TestNothingPrivateReadCostsNoCall: the provenance gate keeps the model out
// of a run whose sources read nothing private, and of an answer with nothing
// in it.
func TestNothingPrivateReadCostsNoCall(t *testing.T) {
	for _, test := range []struct {
		name  string
		input Input
	}{
		{"no sources", Input{Request: "q", Answer: []string{"Some answer about Priya's diagnosis."}}},
		{"only harmless sources", Input{Request: "q", Answer: []string{"a"}, Sources: []string{"directory_find_people", "list_tools"}}},
		{"an empty answer, whatever the sources", Input{Request: "q", Answer: []string{"", "  "}, Sources: []string{"apps_run_read_tool"}}},
		{"no answer at all", Input{Request: "q", Sources: []string{"apps_run_read_tool"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &vertex{}
			result := judgeOn(t, fake, time.Second).Judge(context.Background(), test.input)
			if fake.count() != 0 {
				t.Fatalf("the model was asked %d times, want never", fake.count())
			}
			if result.Judged || result.Hold || result.Err != nil || result.Model != "gemini-3.5-flash-lite" || result.Latency != 0 {
				t.Fatalf("result = %+v, want not judged, not held, and the model named", result)
			}
		})
	}
	fake := &vertex{}
	judgeOn(t, fake, time.Second).Judge(context.Background(), Input{Request: "q", Answer: []string{"a"}, Sources: []string{MemoryPreload}})
	if fake.count() != 1 {
		t.Fatalf("memory that was preloaded asked the model %d times, want once", fake.count())
	}
}

func TestAJudgeWithNothingToAskHoldsWhatItIsAskedToCheck(t *testing.T) {
	for name, judge := range map[string]Judge{
		"no client": NewGemini(nil, "gemini-3.5-flash-lite", time.Second),
		"no model":  NewGemini(clientOn(t, &vertex{}), "  ", time.Second),
	} {
		t.Run(name, func(t *testing.T) {
			result := judge.Judge(context.Background(), private("q", "a"))
			if !result.Judged || !result.Hold || ErrKind(result.Err) != "not_configured" {
				t.Fatalf("result = %+v, want held as not configured", result)
			}
			// What needs no judging still needs none.
			if skipped := judge.Judge(context.Background(), Input{Answer: []string{"a"}}); skipped.Judged || skipped.Hold {
				t.Fatalf("result = %+v, want the gate to skip it", skipped)
			}
		})
	}
}

func TestTruncationIsReportedAndTheModelSeesTheMarker(t *testing.T) {
	fake := &vertex{}
	judge := judgeOn(t, fake, time.Second)

	result := judge.Judge(context.Background(), private("q", "a short answer"))
	if result.Truncated {
		t.Fatal("an input within its caps was reported as cut")
	}
	result = judge.Judge(context.Background(), private(strings.Repeat("r", MaxRequestRunes+10), "a"))
	if !result.Truncated {
		t.Fatal("a request past its cap was not reported as cut")
	}
	user, _ := find(fake.last(t).body, "contents", 0, "parts", 0, "text").(string)
	if !strings.Contains(user, "[... 10 characters left out ...]") {
		t.Fatalf("the model was not shown that text was cut:\n%.400s", user)
	}
	result = judge.Judge(context.Background(), private("q", strings.Repeat("a", MaxAnswerRunes+1)))
	if !result.Truncated {
		t.Fatal("an answer past its cap was not reported as cut")
	}
}

func TestTheDefaultTimeoutIsSixSeconds(t *testing.T) {
	if DefaultTimeout != 6*time.Second {
		t.Fatalf("DefaultTimeout = %s", DefaultTimeout)
	}
	for _, timeout := range []time.Duration{0, -time.Second} {
		if got := newGemini(nil, "m", timeout).timeout; got != DefaultTimeout {
			t.Fatalf("a timeout of %s gave %s, want the default", timeout, got)
		}
	}
	if got := newGemini(nil, "m", 2*time.Second).timeout; got != 2*time.Second {
		t.Fatalf("timeout = %s, want the one given", got)
	}
}

// TestJudgesRunAtOnceAndShareNothing is for the race detector: the answers
// of calls that overlap each belong to their own.
func TestJudgesRunAtOnceAndShareNothing(t *testing.T) {
	fake := &vertex{answer: func(_ int, r *http.Request) reply {
		return reply{body: modelSays(`{"asked":[],"extras":["health"],"certainty":0.5}`)}
	}}
	judge := judgeOn(t, fake, 5*time.Second)
	var wait sync.WaitGroup
	for index := range 24 {
		wait.Go(func() {
			result := judge.Judge(context.Background(), private(fmt.Sprintf("q%d", index), fmt.Sprintf("a%d", index)))
			if result.Err != nil || !result.Hold || len(result.Extras) != 1 {
				t.Errorf("call %d: result = %+v", index, result)
			}
		})
	}
	wait.Wait()
	if fake.count() != 24 {
		t.Fatalf("the model was asked %d times, want 24", fake.count())
	}
	// Each call had a code of its own.
	seen := map[string]bool{}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, request := range fake.requests {
		user, _ := find(request.body, "contents", 0, "parts", 0, "text").(string)
		found := markerCode.FindStringSubmatch(user)
		if found == nil {
			t.Fatalf("no code in the prompt:\n%.200s", user)
		}
		if seen[found[1]] {
			t.Fatalf("code %q was used twice", found[1])
		}
		seen[found[1]] = true
	}
}

// markerCode finds the code the markers of a prompt carry.
var markerCode = regexp.MustCompile(`The code on every marker below is (\S+?);`)
