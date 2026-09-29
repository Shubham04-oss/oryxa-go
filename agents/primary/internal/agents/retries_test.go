package agents

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/auth"
	"google.golang.org/genai"

	"google.golang.org/adk/v2/model"
)

// vertexStub stands in for Vertex AI's generateContent and
// streamGenerateContent. It answers request n with statuses[n], and every
// request past the end of statuses with an answer.
type vertexStub struct {
	statuses []int
	// cutStream drops the connection after a stream's first chunk.
	cutStream bool

	mu       sync.Mutex
	requests int
}

func (s *vertexStub) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

func stubChunk(text string, last bool) string {
	finish := ""
	if last {
		finish = `,"finishReason":"STOP"`
	}
	return fmt.Sprintf(`{"candidates":[{"content":{"role":"model","parts":[{"text":%q}]}%s}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`, text, finish)
}

func (s *vertexStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	n := s.requests
	s.requests++
	s.mu.Unlock()

	if n < len(s.statuses) && s.statuses[n] != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.statuses[n])
		fmt.Fprintf(w, `{"error":{"code":%d,"message":"stubbed refusal","status":"REFUSED"}}`, s.statuses[n])
		return
	}
	if strings.Contains(r.URL.Path, ":streamGenerateContent") {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", stubChunk("Hel", false))
		w.(http.Flusher).Flush()
		if s.cutStream {
			panic(http.ErrAbortHandler)
		}
		fmt.Fprintf(w, "data: %s\n\n", stubChunk("lo.", true))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, stubChunk("Hello.", true))
}

// quickRetries keeps the attempts and the doubling but waits milliseconds
// rather than seconds.
func quickRetries(config *geminiConfig) {
	config.retries.initialDelay = time.Millisecond
	config.retries.maxDelay = 8 * time.Millisecond
	config.retries.jitter = 0
}

// stubbedGemini is Gemini with Vertex replaced by stub.
func stubbedGemini(t *testing.T, stub *vertexStub, options ...GeminiOption) model.LLM {
	t.Helper()
	server := httptest.NewServer(stub)
	t.Cleanup(server.Close)
	t.Setenv("GOOGLE_VERTEX_BASE_URL", server.URL)
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	credentials := auth.NewCredentials(&auth.CredentialsOptions{TokenProvider: fixedToken("service-account-token")})
	llm, err := Gemini(context.Background(), "gemini-test", "oryxa-test", "global", credentials, append([]GeminiOption{quickRetries}, options...)...)
	if err != nil {
		t.Fatalf("Gemini() error = %v", err)
	}
	return llm
}

// modelCall is what one call of a model gave back: the text of each partial
// response and of each final one, and the error that ended it, if any.
type modelCall struct {
	partials, finals []string
	err              error
}

func callModel(ctx context.Context, llm model.LLM, stream bool) modelCall {
	var call modelCall
	req := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}}
	for response, err := range llm.GenerateContent(ctx, req, stream) {
		if err != nil {
			call.err = err
			break
		}
		if response.Partial {
			call.partials = append(call.partials, text(response.Content))
		} else {
			call.finals = append(call.finals, text(response.Content))
		}
	}
	return call
}

func TestGeminiRetriesACallVertexRefusesForNow(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			stub := &vertexStub{statuses: []int{status}}
			llm := stubbedGemini(t, stub)

			call := callModel(context.Background(), llm, false)

			if call.err != nil || !slices.Equal(call.finals, []string{"Hello."}) {
				t.Fatalf("call = %+v, want the answer the second attempt got", call)
			}
			if got := stub.requestCount(); got != 2 {
				t.Fatalf("%d requests, want the refused one and its retry", got)
			}
		})
	}
}

func TestGeminiDoesNotRetryARequestVertexWillNotTake(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			stub := &vertexStub{statuses: []int{status}}
			llm := stubbedGemini(t, stub)

			call := callModel(context.Background(), llm, false)

			var refused genai.APIError
			if !errors.As(call.err, &refused) || refused.Code != status {
				t.Fatalf("error = %v, want Vertex's %d", call.err, status)
			}
			if got := stub.requestCount(); got != 1 {
				t.Fatalf("%d requests, want the one Vertex refused for good", got)
			}
		})
	}
}

func TestGeminiGivesUpAfterItsAttempts(t *testing.T) {
	for _, test := range []struct {
		name     string
		options  []GeminiOption
		requests int
	}{
		{"default", nil, defaultRetries.attempts},
		{"three", []GeminiOption{WithAttempts(3)}, 3},
		{"retries off", []GeminiOption{WithAttempts(1)}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := &vertexStub{statuses: slices.Repeat([]int{http.StatusServiceUnavailable}, 20)}
			llm := stubbedGemini(t, stub, test.options...)

			call := callModel(context.Background(), llm, false)

			var refused genai.APIError
			if !errors.As(call.err, &refused) || refused.Code != http.StatusServiceUnavailable {
				t.Fatalf("error = %v, want the last 503", call.err)
			}
			if got := stub.requestCount(); got != test.requests {
				t.Fatalf("%d requests, want %d", got, test.requests)
			}
		})
	}
}

// A stream is retried only while Vertex has not started it: the refusal comes
// with the response's headers, before any output.
func TestGeminiRetriesAStreamRefusedBeforeItStarts(t *testing.T) {
	stub := &vertexStub{statuses: []int{http.StatusServiceUnavailable}}
	llm := stubbedGemini(t, stub)

	call := callModel(context.Background(), llm, true)

	if call.err != nil {
		t.Fatalf("error = %v", call.err)
	}
	if !slices.Equal(call.partials, []string{"Hel", "lo."}) || !slices.Equal(call.finals, []string{"Hello."}) {
		t.Fatalf("partials %q, finals %q: want each piece of the answer once", call.partials, call.finals)
	}
	if got := stub.requestCount(); got != 2 {
		t.Fatalf("%d requests, want the refused one and its retry", got)
	}
}

// Once output is out, an error ends the stream: repeating the request would
// yield the first chunk twice.
func TestGeminiDoesNotRestartAStreamThatFailsPartway(t *testing.T) {
	stub := &vertexStub{cutStream: true}
	llm := stubbedGemini(t, stub)

	call := callModel(context.Background(), llm, true)

	if call.err == nil {
		t.Fatal("a stream cut off partway ended without an error")
	}
	if !slices.Equal(call.partials, []string{"Hel"}) || len(call.finals) != 0 {
		t.Fatalf("partials %q, finals %q: want the one chunk that arrived, once, and no answer", call.partials, call.finals)
	}
	if got := stub.requestCount(); got != 1 {
		t.Fatalf("%d requests, want the stream not restarted", got)
	}
}

// A run that ends while a call waits to be made again stops waiting. The waits
// here are a minute long, so anything but stopping shows.
func TestGeminiStopsRetryingWhenItsRunEnds(t *testing.T) {
	stub := &vertexStub{statuses: slices.Repeat([]int{http.StatusServiceUnavailable}, 20)}
	llm := stubbedGemini(t, stub, func(config *geminiConfig) {
		config.retries.initialDelay, config.retries.maxDelay = time.Minute, time.Minute
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for stub.requestCount() == 0 {
			time.Sleep(time.Millisecond)
		}
		// Let the client take the refusal and start its wait.
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	started := time.Now()
	call := callModel(ctx, llm, false)

	if call.err == nil {
		t.Fatal("a call whose run ended succeeded")
	}
	if waited := time.Since(started); waited > 10*time.Second {
		t.Fatalf("waited %v after the run ended", waited)
	}
	if got := stub.requestCount(); got != 1 {
		t.Fatalf("%d requests, want none after the run ended", got)
	}
}

// worstWait is the longest the client waits between the attempts of options,
// with its full jitter each time: min(initial*base^(n-1) + jitter, max).
func worstWait(options *genai.HTTPRetryOptions) time.Duration {
	var seconds float64
	for n := 1; n < int(*options.Attempts); n++ {
		seconds += min(*options.InitialDelay*math.Pow(*options.ExpBase, float64(n-1))+*options.Jitter, *options.MaxDelay)
	}
	return time.Duration(seconds * float64(time.Second))
}

func TestRetryPolicyIsBounded(t *testing.T) {
	options := defaultRetries.options()

	if *options.Attempts != 4 {
		t.Fatalf("attempts = %d, want 4", *options.Attempts)
	}
	want := []int32{429, 500, 502, 503, 504}
	if !slices.Equal(options.HTTPStatusCodes, want) {
		t.Fatalf("statuses = %v, want %v", options.HTTPStatusCodes, want)
	}
	if waited := worstWait(options); waited > 20*time.Second {
		t.Fatalf("a call that keeps failing waits up to %v, want no more than 20s", waited)
	}

	// A larger setting cannot make it unbounded.
	var config geminiConfig
	WithAttempts(1000)(&config)
	config.retries.initialDelay, config.retries.maxDelay, config.retries.jitter = defaultRetries.initialDelay, defaultRetries.maxDelay, defaultRetries.jitter
	if config.retries.attempts != maxAttempts {
		t.Fatalf("attempts = %d, want at most %d", config.retries.attempts, maxAttempts)
	}
	if waited := worstWait(config.retries.options()); waited > time.Minute {
		t.Fatalf("the most attempts wait up to %v, want no more than a minute", waited)
	}
	WithAttempts(-3)(&config)
	if config.retries.attempts != 1 {
		t.Fatalf("attempts = %d, want a nonsense count to mean one attempt", config.retries.attempts)
	}
}
