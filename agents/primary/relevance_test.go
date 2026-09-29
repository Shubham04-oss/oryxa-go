package primary

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"cloud.google.com/go/auth"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/answerguard"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/serviceconfig"
)

// fixedToken is credentials that always hold the same token.
type fixedToken string

func (token fixedToken) Token(context.Context) (*auth.Token, error) {
	return &auth.Token{Value: string(token), Type: "Bearer"}, nil
}

// clearBedrock is a guardrail that finds nothing in anything.
type clearBedrock struct{}

func (clearBedrock) ApplyGuardrail(context.Context, *bedrockruntime.ApplyGuardrailInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.ApplyGuardrailOutput, error) {
	return &bedrockruntime.ApplyGuardrailOutput{Action: types.GuardrailActionNone}, nil
}

// fakeVertex is a stand-in for Vertex AI that says every answer carries
// health details nobody asked for, and keeps what it was sent.
type fakeVertex struct {
	mu       sync.Mutex
	paths    []string
	bearers  []string
	requests []string
}

func (fake *fakeVertex) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	body, _ := io.ReadAll(request.Body)
	fake.mu.Lock()
	fake.paths = append(fake.paths, request.URL.Path)
	fake.bearers = append(fake.bearers, request.Header.Get("Authorization"))
	fake.requests = append(fake.requests, string(body))
	fake.mu.Unlock()
	answer, _ := json.Marshal(`{"asked":[],"extras":["health"],"certainty":0.9}`)
	writer.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(writer, `{"candidates":[{"content":{"role":"model","parts":[{"text":`+string(answer)+`}]},"finishReason":"STOP"}]}`)
}

func (fake *fakeVertex) asked() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return len(fake.paths)
}

// TestTheGuardIsGivenAJudgeOnTheLiteModelWithTheAgentsCredentials builds the
// judge as Mount does, against a stand-in for Vertex AI the Gen AI client is
// pointed at, and asks it through a guard: the model is the lite one, in the
// agents' project and location, authorized as the platform's Google
// credentials; an answer from a private source is sent and held, in hold mode,
// for what the model said; one from a directory lookup is never sent.
func TestTheGuardIsGivenAJudgeOnTheLiteModelWithTheAgentsCredentials(t *testing.T) {
	vertex := &fakeVertex{}
	server := httptest.NewServer(vertex)
	defer server.Close()
	for _, name := range []string{"GOOGLE_API_KEY", "GEMINI_API_KEY", "GOOGLE_GENAI_USE_VERTEXAI"} {
		t.Setenv(name, "")
	}
	t.Setenv("GOOGLE_VERTEX_BASE_URL", server.URL)

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	plat := platform(map[string]string{
		"GOOGLE_CLOUD_PROJECT": "oryxa-test", "GOOGLE_CLOUD_LOCATION": "global",
		// The two models differ, so that the one the judge runs on is told apart.
		"AGENTS_PRIMARY_MODEL": "gemini-3.5-flash", "AGENTS_PRIMARY_MODEL_LITE": "gemini-3.5-flash-lite",
	}, nil)
	plat.Google = auth.NewCredentials(&auth.CredentialsOptions{TokenProvider: fixedToken("service-account-token")})
	settings, err := serviceconfig.FromEnv(plat.Getenv)
	if err != nil {
		t.Fatal(err)
	}

	options, err := relevanceOptions(context.Background(), settings, plat, logger)
	if err != nil || len(options) != 1 {
		t.Fatalf("relevanceOptions() = %d options, %v, want one and no error", len(options), err)
	}
	guard, err := answerguard.New(clearBedrock{}, answerguard.Config{Identifier: "gr_answers", Version: "1"}, options...)
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{"No, she has a doctor's appointment."}

	directory := guard.CheckWith(context.Background(), texts, &answerguard.RelevanceInput{
		Request: "Is Priya free on Thursday?", Sources: []string{"directory_find_people"}, Mode: answerguard.RelevanceHold,
	})
	if vertex.asked() != 0 || directory.Relevance.Judged || directory.State != answerguard.StateClear {
		t.Fatalf("an answer from a directory lookup reached the model %d times: %+v", vertex.asked(), directory)
	}

	held := guard.CheckWith(context.Background(), texts, &answerguard.RelevanceInput{
		Request: "Is Priya free on Thursday?", Sources: []string{"apps_gmail_search_messages"}, Mode: answerguard.RelevanceHold,
	})
	if vertex.asked() != 1 {
		t.Fatalf("the model was asked %d times, want once", vertex.asked())
	}
	if !held.Relevance.Judged || !held.Relevance.Held || held.State != answerguard.StateHeld ||
		!slices.Equal(held.Kinds, []string{answerguard.OffTopic, "health"}) || held.Shareable(1) != nil {
		t.Fatalf("an off-topic answer from a private source = %+v, want it held for health and nothing shared", held)
	}
	if path := vertex.paths[0]; !strings.Contains(path, "/projects/oryxa-test/") || !strings.Contains(path, "/locations/global/") ||
		!strings.HasSuffix(path, "/models/gemini-3.5-flash-lite:generateContent") {
		t.Errorf("the model was asked at %s, want the lite model in the agents' project and location", path)
	}
	if vertex.bearers[0] != "Bearer service-account-token" {
		t.Errorf("the model was asked with %q, want the platform's Google credentials", vertex.bearers[0])
	}
	if !strings.Contains(vertex.requests[0], "doctor") || !strings.Contains(vertex.requests[0], "Is Priya free on Thursday?") {
		t.Errorf("the model was not shown the answer and the request: %s", vertex.requests[0])
	}

	// The startup line says what judges and how far it may hold, and no text.
	var said map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err == nil && strings.Contains(record["msg"].(string), "private material") {
			said = record
		}
	}
	if said == nil || said["model"] != "gemini-3.5-flash-lite" || said["ceiling"] != "shadow" || said["timeout"] != "6s" || said["level"] != "INFO" {
		t.Errorf("the startup line = %v, want the model, the timeout and the deployment's ceiling", said)
	}
}

// TestWithNoGoogleCloudProjectThereIsNoJudgeAndTheServiceSaysSo: without a
// project there is no model to ask, so the guard is built with no judge and
// runs without the check whatever a member's settings say, and the log says so
// once; it is not an error.
func TestWithNoGoogleCloudProjectThereIsNoJudgeAndTheServiceSaysSo(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	plat := platform(map[string]string{}, nil)
	settings, err := serviceconfig.FromEnv(plat.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	options, err := relevanceOptions(context.Background(), settings, plat, logger)
	if err != nil || len(options) != 0 {
		t.Fatalf("relevanceOptions() = %d options, %v, want none and no error", len(options), err)
	}
	if lines := strings.Count(logs.String(), "GOOGLE_CLOUD_PROJECT is not set"); lines != 1 || !strings.Contains(logs.String(), `"level":"WARN"`) {
		t.Errorf("the log says %d times that there is no model to judge with, want once as a warning:\n%s", lines, logs.String())
	}

	// A guard built without it answers as it always has, in every mode.
	guard, err := answerguard.New(clearBedrock{}, answerguard.Config{Identifier: "gr_answers", Version: "1"}, options...)
	if err != nil {
		t.Fatal(err)
	}
	verdict := guard.CheckWith(context.Background(), []string{"No, she has a doctor's appointment."}, &answerguard.RelevanceInput{
		Request: "Is Priya free on Thursday?", Sources: []string{"apps_gmail_search_messages"}, Mode: answerguard.RelevanceHold,
	})
	if verdict.Relevance.Judged || verdict.State != answerguard.StateClear {
		t.Fatalf("a guard with no judge judged: %+v", verdict)
	}
}

// TestAClientThatCannotBeBuiltStopsTheService: a service that was meant to
// judge relevance and could not reach a model says why instead of starting
// without the check: with no credentials to be found the models cannot be
// reached either.
func TestAClientThatCannotBeBuiltStopsTheService(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "missing.json"))
	for _, name := range []string{"GOOGLE_API_KEY", "GEMINI_API_KEY", "GOOGLE_GENAI_USE_VERTEXAI", "GOOGLE_VERTEX_BASE_URL"} {
		t.Setenv(name, "")
	}
	plat := platform(map[string]string{"GOOGLE_CLOUD_PROJECT": "oryxa-test"}, nil)
	settings, err := serviceconfig.FromEnv(plat.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	options, err := relevanceOptions(context.Background(), settings, plat, slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "relevance judge client") || len(options) != 0 {
		t.Fatalf("relevanceOptions() = %d options, %v, want none and an error naming the judge's client", len(options), err)
	}
}
