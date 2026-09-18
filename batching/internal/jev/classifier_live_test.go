package jev

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/batching/internal/domain"
	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"
)

// TestLiveClassifier is an opt-in smoke test for the real TypeSafe API. It is
// skipped during normal test runs and never logs or persists the API key.
func TestLiveClassifier(t *testing.T) {
	apiKey := os.Getenv("TYPESAFE_API_KEY")
	if apiKey == "" {
		t.Skip("TYPESAFE_API_KEY is not set")
	}
	classifier, err := New(&http.Client{Timeout: 3 * time.Second}, Config{
		APIKey: apiKey, BaseURL: "https://api.typesafe.ai", Model: "jev-latest",
		ImmediateProbability: .85, MinimumConfidence: .60,
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		want  domain.Lane
		event appsv1.TriggerReceived
	}{
		{
			name: "critical production incident",
			want: domain.LaneImmediate,
			event: appsv1.TriggerReceived{Toolkit: "pagerduty", Trigger: "incident.triggered", Data: json.RawMessage(`{
				"severity":"critical","status":"triggered","service":"production-api",
				"title":"SEV-1: primary database unavailable",
				"message":"@oncall immediate action required; all customer requests are failing"
			}`)},
		},
		{
			name: "routine repository star",
			want: domain.LaneDigest,
			event: appsv1.TriggerReceived{Toolkit: "github", Trigger: "repository.starred", Data: json.RawMessage(`{
				"repository":"example/project","message":"A user starred the repository"
			}`)},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			classification, err := classifier.Classify(context.Background(), test.event)
			if err != nil {
				t.Fatal(err)
			}
			if err := classification.Validate(); err != nil {
				t.Fatal(err)
			}
			t.Logf("lane=%s priority=%.4f classifier=%s reason=%s", classification.Lane,
				classification.Priority, classification.Classifier, classification.Reason)
			if classification.Lane != test.want {
				t.Errorf("lane = %s, want %s", classification.Lane, test.want)
			}
		})
	}
}
