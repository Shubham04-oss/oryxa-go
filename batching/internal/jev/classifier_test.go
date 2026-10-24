package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

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
		if incoming.URL.Path != "/v1/systemone" || incoming.Header.Get("Authorization") != "Bearer secret" {
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
		if len(body) > maxRequestBytes || !strings.Contains(string(body), `"truncated":true`) {
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

const digestResponse = `{"model":"jev-1.13.0","answers":{"batch_lane":{"type":"choice","choice":"digest","confidence":1,"probabilities":{"immediate":0,"digest":1}},"requires_prompt_attention":{"type":"noul","noul":0}}}`

func TestClassifierHTTPContractBoundsTheEncodedRequest(t *testing.T) {
	random := rand.New(rand.NewSource(17))
	randomText := make([]byte, 80<<10)
	for i := range randomText {
		randomText[i] = byte(32 + random.Intn(95))
	}
	for _, test := range []struct {
		name, text, metadata string
	}{
		{"escaped HTML within the old raw bound", strings.Repeat("<>&", 7<<10), "slack"},
		{"quotes and backslashes", strings.Repeat("\"\\", 20<<10), "slack"},
		{"CJK", strings.Repeat("紧急通知", 12<<10), "slack"},
		{"random ASCII", string(randomText), "slack"},
		{"escaped metadata overhead", strings.Repeat("<", 23<<10), strings.Repeat("<", maxMetadataBytes)},
	} {
		t.Run(test.name, func(t *testing.T) {
			var payload bytes.Buffer
			encoder := json.NewEncoder(&payload)
			encoder.SetEscapeHTML(false)
			if err := encoder.Encode(map[string]string{"text": test.text}); err != nil {
				t.Fatal(err)
			}
			data := json.RawMessage(payload.Bytes())
			if test.name == "escaped HTML within the old raw bound" {
				oldBody, err := json.Marshal(request{Model: "jev-latest", State: state{Payload: data}})
				if err != nil || len(data) > 48<<10 || len(oldBody) <= 48<<10 {
					t.Fatalf("fixture must fit the old raw bound but exceed it on the wire: raw %d, encoded %d, error %v", len(data), len(oldBody), err)
				}
			}
			original := append([]byte(nil), data...)
			bodies := make(chan []byte, 1)
			classifier := testClassifier(t, func(writer http.ResponseWriter, incoming *http.Request) {
				body, _ := io.ReadAll(incoming.Body)
				bodies <- body
				if len(body) > maxRequestBytes {
					writer.WriteHeader(http.StatusBadRequest)
					_, _ = writer.Write([]byte(`{"error":"request is too large"}`))
					return
				}
				_, _ = writer.Write([]byte(digestResponse))
			})
			if _, err := classifier.Classify(context.Background(), appsv1.TriggerReceived{
				Toolkit: test.metadata, Trigger: test.metadata, Data: data,
			}); err != nil {
				t.Fatal(err)
			}
			body := <-bodies
			if len(body) > maxRequestBytes || !json.Valid(body) || !utf8.Valid(body) {
				t.Fatalf("invalid or oversized wire body: %d bytes", len(body))
			}
			var input struct {
				Model string `json:"model"`
				State struct {
					Policy, Toolkit, Trigger string
					Payload                  payloadPrefix
				} `json:"state"`
				Questions map[string]question `json:"questions"`
			}
			if err := json.Unmarshal(body, &input); err != nil {
				t.Fatal(err)
			}
			prefix := input.State.Payload
			if !prefix.Truncated || prefix.OriginalBytes != len(data) || prefix.PrefixBytes != len(prefix.TextPrefix) ||
				prefix.PrefixBytes == 0 || !utf8.ValidString(prefix.TextPrefix) || !strings.HasPrefix(string(data), prefix.TextPrefix) {
				t.Fatalf("projection did not preserve a nonempty UTF-8 prefix: %+v", prefix)
			}
			if input.State.Toolkit != test.metadata || input.State.Trigger != test.metadata || input.State.Policy == "" ||
				input.Model != "jev-latest" || len(input.Questions) != 2 || !bytes.Equal(data, original) {
				t.Fatal("metadata, policy, questions, or original stored payload changed")
			}
		})
	}
}

func TestClassifierPreservesStructuredPayloadWhenItFits(t *testing.T) {
	data := json.RawMessage(` { "text": "通知 <>&", "nested": {"count": 2}, "items": [true, null] } `)
	bodies := make(chan []byte, 1)
	classifier := testClassifier(t, func(writer http.ResponseWriter, incoming *http.Request) {
		body, _ := io.ReadAll(incoming.Body)
		bodies <- body
		_, _ = writer.Write([]byte(digestResponse))
	})
	if _, err := classifier.Classify(context.Background(), appsv1.TriggerReceived{Data: data}); err != nil {
		t.Fatal(err)
	}
	var input struct {
		State struct {
			Payload json.RawMessage `json:"payload"`
		} `json:"state"`
	}
	if err := json.Unmarshal(<-bodies, &input); err != nil {
		t.Fatal(err)
	}
	var got, want any
	if json.Unmarshal(input.State.Payload, &got) != nil || json.Unmarshal(data, &want) != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("fitting structured payload was replaced by a text projection")
	}
}

func TestClassifierRejectsOversizedMetadataBeforeHTTP(t *testing.T) {
	classifier := testClassifier(t, func(writer http.ResponseWriter, _ *http.Request) {
		t.Error("invalid metadata reached the provider")
		_, _ = writer.Write([]byte(digestResponse))
	})
	for _, trigger := range []appsv1.TriggerReceived{
		{Toolkit: strings.Repeat("sensitive", maxMetadataBytes)},
		{Trigger: strings.Repeat("sensitive", maxMetadataBytes)},
		{Toolkit: string([]byte{0xff})},
	} {
		if _, err := classifier.Classify(context.Background(), trigger); err == nil || strings.Contains(err.Error(), "sensitive") {
			t.Fatalf("expected a generic local metadata error, got %v", err)
		}
	}
	if _, err := New(http.DefaultClient, Config{APIKey: "secret", BaseURL: "https://api.typesafe.ai", Model: strings.Repeat("sensitive", maxModelLength)}); err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("expected a generic configured model error, got %v", err)
	}
	if _, err := encodeRequest(request{State: state{Policy: strings.Repeat("sensitive", maxRequestBytes)}}, nil); err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("expected a generic total metadata error, got %v", err)
	}
}

func TestClassifierProviderErrorDiagnosticsAreBoundedAndSelective(t *testing.T) {
	for _, test := range []struct {
		name, body, want string
	}{
		{"string", `{"error":"state exceeds limit","content":"PRIVATE_PAYLOAD","detail":"PRIVATE_DETAIL"}`, "call Jev: HTTP 400: state exceeds limit"},
		{"structured", `{"error":{"code":"too_large","type":"validation","message":"state exceeds limit","content":"PRIVATE_PAYLOAD","detail":"PRIVATE_DETAIL"}}`, "call Jev: HTTP 400: too_large validation state exceeds limit"},
		{"numeric code with string message", `{"error":{"code":400,"message":"state exceeds limit"}}`, "call Jev: HTTP 400: state exceeds limit"},
		{"nested message with string code", `{"error":{"code":"invalid_request","message":{"content":"PRIVATE_PAYLOAD","detail":"PRIVATE_DETAIL"}}}`, "call Jev: HTTP 400: invalid_request"},
		{"sanitized", `{"error":"bad\nrequest\u0000\u001b\u202e state"}`, "call Jev: HTTP 400: bad request state"},
		{"bounded", `{"error":"` + strings.Repeat("界", maxDiagnosticRunes+10) + `"}`, "call Jev: HTTP 400: " + strings.Repeat("界", maxDiagnosticRunes-1) + "…"},
		{"absent", `{"content":"PRIVATE_PAYLOAD","detail":"PRIVATE_DETAIL"}`, "call Jev: HTTP 400"},
		{"malformed", `PRIVATE_PAYLOAD`, "call Jev: HTTP 400"},
		{"oversized", `{"error":"unsafe partial error","content":"` + strings.Repeat("x", maxErrorBytes) + `"}`, "call Jev: HTTP 400"},
		{"unsupported structured error", `{"error":{"message":{"content":"PRIVATE_PAYLOAD"}}}`, "call Jev: HTTP 400"},
		{"array error", `{"error":["PRIVATE_PAYLOAD"]}`, "call Jev: HTTP 400"},
		{"null error", `{"error":null}`, "call Jev: HTTP 400"},
	} {
		t.Run(test.name, func(t *testing.T) {
			classifier := testClassifier(t, func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusBadRequest)
				_, _ = writer.Write([]byte(test.body))
			})
			_, err := classifier.Classify(context.Background(), appsv1.TriggerReceived{Data: json.RawMessage(`{"text":"PRIVATE_PAYLOAD"}`)})
			if err == nil || err.Error() != test.want {
				t.Fatalf("provider error = %v, want %q", err, test.want)
			}
			if strings.Contains(err.Error(), "PRIVATE_") {
				t.Fatal("provider content or detail leaked into diagnostics")
			}
		})
	}
}

func TestClassifierInvalidUTF8UsesAValidPrefix(t *testing.T) {
	data := json.RawMessage(append([]byte(`{"text":"before`), 0xff))
	data = append(data, []byte(`after"}`)...)
	if !json.Valid(data) {
		t.Fatal("fixture must demonstrate json.Valid accepting malformed UTF-8")
	}
	bodies := make(chan []byte, 1)
	classifier := testClassifier(t, func(writer http.ResponseWriter, incoming *http.Request) {
		body, _ := io.ReadAll(incoming.Body)
		bodies <- body
		_, _ = writer.Write([]byte(digestResponse))
	})
	if _, err := classifier.Classify(context.Background(), appsv1.TriggerReceived{Data: data}); err != nil {
		t.Fatal(err)
	}
	body := <-bodies
	if !utf8.Valid(body) || !json.Valid(body) || len(body) > maxRequestBytes {
		t.Fatal("malformed UTF-8 escaped into the provider request")
	}
	var input struct {
		State struct {
			Payload payloadPrefix `json:"payload"`
		} `json:"state"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		t.Fatal(err)
	}
	if input.State.Payload.TextPrefix != `{"text":"before` || !input.State.Payload.Truncated || input.State.Payload.OriginalBytes != len(data) {
		t.Fatalf("invalid UTF-8 projection = %+v", input.State.Payload)
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
