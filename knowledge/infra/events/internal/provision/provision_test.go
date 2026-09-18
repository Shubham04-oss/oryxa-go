package provision

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Knowledge hears about a connection's whole life now, not only its end: one
// made is what makes an app's contents searchable.
func TestEventPatternMatchesTheEventsKnowledgeActsOn(t *testing.T) {
	pattern, err := EventPattern()
	if err != nil {
		t.Fatalf("EventPattern() error = %v", err)
	}
	assertJSONEqual(t, pattern, `{
		"$or": [
			{
				"source": ["oryxa.apps"],
				"detail-type": ["apps.connection.connected.v1", "apps.connection.expired.v1", "apps.connection.removed.v1"]
			},
			{
				"source": ["oryxa.auth"],
				"detail-type": ["auth.member.removed.v1", "auth.organization.deleted.v1"]
			}
		]
	}`)
}

func TestQueuePolicyAllowsOnlyTheRule(t *testing.T) {
	policy, err := QueuePolicy("arn:aws:sqs:ap-south-1:123456789012:knowledge-events", "arn:aws:events:ap-south-1:123456789012:rule/knowledge-events")
	if err != nil {
		t.Fatalf("QueuePolicy() error = %v", err)
	}
	assertJSONEqual(t, policy, `{
		"Version": "2012-10-17",
		"Statement": [{
			"Sid": "AllowKnowledgeRule",
			"Effect": "Allow",
			"Principal": {"Service": "events.amazonaws.com"},
			"Action": "sqs:SendMessage",
			"Resource": "arn:aws:sqs:ap-south-1:123456789012:knowledge-events",
			"Condition": {"ArnEquals": {"aws:SourceArn": "arn:aws:events:ap-south-1:123456789012:rule/knowledge-events"}}
		}]
	}`)
}

func TestFromEnvDefaults(t *testing.T) {
	t.Setenv("AWS_REGION", "")
	t.Setenv("EVENT_BUS_NAME", "")
	t.Setenv("KNOWLEDGE_EVENTS_QUEUE_NAME", "")

	if config := FromEnv(); config != (Config{Region: "ap-south-1", EventBusName: "default", QueueName: "knowledge-events", DeadLetterQueueName: "knowledge-events-dlq"}) {
		t.Fatalf("FromEnv() = %+v", config)
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
