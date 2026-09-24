package provision

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/realtime/internal/events"
	"github.com/Shubham04-oss/oryxa-go/realtime/internal/purge"
)

func TestEventPatternMatchesTheEndsOfMemberships(t *testing.T) {
	pattern, err := EventPattern()
	if err != nil {
		t.Fatalf("EventPattern() error = %v", err)
	}
	assertJSONEqual(t, pattern, `{
		"source": ["oryxa.auth"],
		"detail-type": ["auth.member.removed.v1", "auth.organization.deleted.v1"]
	}`)
}

func TestQueuePolicyAllowsOnlyTheRule(t *testing.T) {
	policy, err := QueuePolicy("arn:aws:sqs:us-west-2:123456789012:realtime-events", "arn:aws:events:us-west-2:123456789012:rule/oryxa/realtime-auth-memberships")
	if err != nil {
		t.Fatalf("QueuePolicy() error = %v", err)
	}
	assertJSONEqual(t, policy, `{
		"Version": "2012-10-17",
		"Statement": [{
			"Sid": "AllowRealtimeRule",
			"Effect": "Allow",
			"Principal": {"Service": "events.amazonaws.com"},
			"Action": "sqs:SendMessage",
			"Resource": "arn:aws:sqs:us-west-2:123456789012:realtime-events",
			"Condition": {"ArnEquals": {"aws:SourceArn": "arn:aws:events:us-west-2:123456789012:rule/oryxa/realtime-auth-memberships"}}
		}]
	}`)
}

func TestFromEnvDefaults(t *testing.T) {
	t.Setenv("AWS_REGION", "")
	t.Setenv("EVENT_BUS_NAME", "")
	t.Setenv("REALTIME_EVENTS_QUEUE_NAME", "")

	if config := FromEnv(); config != (Config{Region: "ap-south-1", EventBusName: "default", QueueName: "realtime-events", DeadLetterQueueName: "realtime-events-dlq"}) {
		t.Fatalf("FromEnv() = %+v", config)
	}
}

func TestFromEnvNamesTheDeploymentsQueue(t *testing.T) {
	t.Setenv("AWS_REGION", "us-west-2")
	t.Setenv("EVENT_BUS_NAME", "oryxa-production")
	t.Setenv("REALTIME_EVENTS_QUEUE_NAME", "oryxa-production-realtime-events")

	want := Config{Region: "us-west-2", EventBusName: "oryxa-production", QueueName: "oryxa-production-realtime-events", DeadLetterQueueName: "oryxa-production-realtime-events-dlq"}
	if config := FromEnv(); config != want {
		t.Fatalf("FromEnv() = %+v, want %+v", config, want)
	}
}

// The consumer's batch, each call waiting on the Worker in turn, must fit
// inside the visibility timeout, or a Worker that never answers would be sent
// a purge again while the first attempt still waits.
func TestTheVisibilityTimeoutOutlastsABatchTheWorkerNeverAnswers(t *testing.T) {
	visibility := time.Duration(visibilityTimeoutSeconds) * time.Second
	if batch := time.Duration(events.ReceiveBatch) * purge.Timeout; visibility < batch {
		t.Fatalf("visibility timeout %s < %d messages x %s", visibility, events.ReceiveBatch, purge.Timeout)
	}
}

func assertJSONEqual(t *testing.T, got, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal([]byte(got), &gotValue); err != nil {
		t.Fatalf("decode %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("decode %s: %v", want, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON = %s, want %s", got, want)
	}
}
