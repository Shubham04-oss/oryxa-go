package jev

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/batching/internal/domain"
	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"
)

func testClassifier(t *testing.T, handle http.HandlerFunc) *Classifier {
	t.Helper()
	server := httptest.NewServer(handle)
	t.Cleanup(server.Close)
	classifier, err := New(server.Client(), Config{
		APIKey: "secret", BaseURL: server.URL, Model: "jev-latest",
		ImmediateProbability: .85, MinimumConfidence: .60,
	})
	if err != nil {
		t.Fatal(err)
	}
	return classifier
}

func TestClassifierAsksBothQuestionsAndReturnsAnImmediateDecision(t *testing.T) {
	classifier := testClassifier(t, func(writer http.ResponseWriter, incoming *http.Request) {
		if incoming.URL.Path != systemOnePath || incoming.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("request = %s, authorization = %q", incoming.URL.Path, incoming.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(incoming.Body)
		if strings.Contains(string(body), "org_secret") || !strings.Contains(string(body), laneQuestionID) || !strings.Contains(string(body), urgencyQuestionID) {
			t.Fatalf("request body = %s", body)
		}
		var input request
		if err := json.Unmarshal(body, &input); err != nil {
			t.Fatal(err)
		}
		if input.Model != "jev-latest" || input.Questions[laneQuestionID].Type != "choice" || input.Questions[urgencyQuestionID].Type != "noul" {
			t.Fatalf("input = %+v", input)
		}
		_, _ = writer.Write([]byte(`{
			"model":"jev-1.13.0",
			"answers":{
				"batch_lane":{"type":"choice","choice":"immediate","confidence":0.91,"probabilities":{"immediate":0.94,"digest":0.06}},
				"requires_prompt_attention":{"type":"noul","noul":0.89}
			}
		}`))
	})
	classification, err := classifier.Classify(context.Background(), appsv1.TriggerReceived{
		OrganizationID: "org_secret", Toolkit: "slack", Trigger: "message",
		Data: json.RawMessage(`{"text":"@Asha production is down"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if classification.Lane != domain.LaneImmediate || classification.Priority != .94 ||
		classification.Classifier != "jev:jev-1.13.0/batching-urgency.v1" {
		t.Fatalf("classification = %+v", classification)
	}
}

func TestLowConfidenceImmediateChoiceStaysDigestButKeepsItsPriority(t *testing.T) {
	classifier := testClassifier(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{
			"model":"jev-1.13.0",
			"answers":{
				"batch_lane":{"type":"choice","choice":"immediate","confidence":0.30,"probabilities":{"immediate":0.70,"digest":0.30}},
				"requires_prompt_attention":{"type":"noul","noul":0.80}
			}
		}`))
	})
	classification, err := classifier.Classify(context.Background(), appsv1.TriggerReceived{Toolkit: "github", Trigger: "issue"})
	if err != nil {
		t.Fatal(err)
	}
	if classification.Lane != domain.LaneDigest || classification.Priority != .80 {
		t.Fatalf("classification = %+v", classification)
	}
}

func TestClassifierRejectsProviderAndMalformedResponses(t *testing.T) {
	for name, handle := range map[string]http.HandlerFunc{
		"provider": func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusTooManyRequests) },
		"malformed": func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(`{"model":"jev-1.13.0","answers":{}}`))
		},
		"oversized": func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(strings.Repeat(" ", maxResponseBytes+1)))
		},
	} {
		t.Run(name, func(t *testing.T) {
			classifier := testClassifier(t, handle)
			if _, err := classifier.Classify(context.Background(), appsv1.TriggerReceived{}); err == nil {
				t.Fatal("Classify() error = nil")
			}
		})
	}
}

func TestLargePayloadIsProjectedWithoutChangingTheStoredTrigger(t *testing.T) {
	classifier := testClassifier(t, func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		if len(body) > maxPayloadBytes+(8<<10) || !strings.Contains(string(body), `"truncated":true`) {
			t.Fatalf("request bytes = %d, body prefix = %.200s", len(body), body)
		}
		_, _ = writer.Write([]byte(`{
			"model":"jev-1.13.0",
			"answers":{
				"batch_lane":{"type":"choice","choice":"digest","confidence":1,"probabilities":{"immediate":0,"digest":1}},
				"requires_prompt_attention":{"type":"noul","noul":0}
			}
		}`))
	})
	data := json.RawMessage(`{"text":"` + strings.Repeat("x", 80<<10) + `"}`)
	if _, err := classifier.Classify(context.Background(), appsv1.TriggerReceived{Data: data}); err != nil {
		t.Fatal(err)
	}
	if len(data) < 80<<10 {
		t.Fatal("source payload was changed")
	}
}

func TestNewRefusesToSendTheKeyInCleartext(t *testing.T) {
	config := Config{APIKey: "secret", Model: "jev-latest", ImmediateProbability: .85, MinimumConfidence: .60}
	for baseURL, allowed := range map[string]bool{
		"https://api.typesafe.ai": true, "http://localhost:8080": true, "http://127.0.0.1:9000": true,
		"http://api.typesafe.ai": false, "ftp://api.typesafe.ai": false,
	} {
		config.BaseURL = baseURL
		if _, err := New(http.DefaultClient, config); (err == nil) != allowed {
			t.Fatalf("New(%s) error = %v, want allowed %t", baseURL, err, allowed)
		}
	}
}

// TestAModelNameIsBounded: the model TypeSafe names is stored on every
// trigger it classifies, so an unbounded one is not accepted as an answer.
func TestAModelNameIsBounded(t *testing.T) {
	for length, allowed := range map[int]bool{maxModelLength: true, maxModelLength + 1: false} {
		model := strings.Repeat("j", length)
		classifier := testClassifier(t, func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(`{
				"model":"` + model + `",
				"answers":{
					"batch_lane":{"type":"choice","choice":"digest","confidence":1,"probabilities":{"immediate":0,"digest":1}},
					"requires_prompt_attention":{"type":"noul","noul":0}
				}
			}`))
		})
		classification, err := classifier.Classify(context.Background(), appsv1.TriggerReceived{Toolkit: "github", Trigger: "issue"})
		if (err == nil) != allowed {
			t.Fatalf("a %d character model: classification %+v, error %v", length, classification, err)
		}
	}
}
