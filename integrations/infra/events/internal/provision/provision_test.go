package provision

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestEventPatternMatchesAuthOrganizationEvents(t *testing.T) {
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
	policy, err := QueuePolicy("arn:aws:sqs:ap-south-1:123456789012:integrations-events", "arn:aws:events:ap-south-1:123456789012:rule/integrations-auth-organizations")
	if err != nil {
		t.Fatalf("QueuePolicy() error = %v", err)
	}
	assertJSONEqual(t, policy, `{
		"Version": "2012-10-17",
		"Statement": [{
			"Sid": "AllowIntegrationsRule",
			"Effect": "Allow",
			"Principal": {"Service": "events.amazonaws.com"},
			"Action": "sqs:SendMessage",
			"Resource": "arn:aws:sqs:ap-south-1:123456789012:integrations-events",
			"Condition": {"ArnEquals": {"aws:SourceArn": "arn:aws:events:ap-south-1:123456789012:rule/integrations-auth-organizations"}}
		}]
	}`)
}

func TestFromEnvDefaults(t *testing.T) {
	t.Setenv("AWS_REGION", "")
	t.Setenv("EVENT_BUS_NAME", "")
	t.Setenv("INTEGRATIONS_EVENTS_QUEUE_NAME", "")

	if config := FromEnv(); config != (Config{Region: "ap-south-1", EventBusName: "default", QueueName: "integrations-events", DeadLetterQueueName: "integrations-events-dlq"}) {
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
