package provision

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/Shubham04-oss/oryxa-go/realtime/internal/events"
	"github.com/Shubham04-oss/oryxa-go/realtime/internal/purge"
)

// The rule matches each source's own endings, and only from that source: an
// agents detail type claimed by auth, or the other way round, matches
// nothing.
func TestEventPatternMatchesTheEndsOfMembershipsAndTeamspaces(t *testing.T) {
	pattern, err := EventPattern()
	if err != nil {
		t.Fatalf("EventPattern() error = %v", err)
	}
	assertJSONEqual(t, pattern, `{
		"$or": [
			{
				"source": ["oryxa.agents"],
				"detail-type": ["agents.teamspace.deleted.v1", "agents.teamspace.member.removed.v1"]
			},
			{
				"source": ["oryxa.auth"],
				"detail-type": ["auth.member.removed.v1", "auth.organization.deleted.v1"]
			}
		]
	}`)
}

func TestQueuePolicyAllowsOnlyTheRule(t *testing.T) {
	const ruleARN = "arn:aws:events:us-west-2:123456789012:rule/oryxa/realtime-auth-memberships"
	for queueARN, sid := range map[string]string{
		"arn:aws:sqs:us-west-2:123456789012:realtime-events":     "AllowRealtimeRule",
		"arn:aws:sqs:us-west-2:123456789012:realtime-events-dlq": "AllowRealtimeRuleFailures",
	} {
		policy, err := QueuePolicy(queueARN, ruleARN, sid)
		if err != nil {
			t.Fatalf("QueuePolicy() error = %v", err)
		}
		assertJSONEqual(t, policy, `{
			"Version": "2012-10-17",
			"Statement": [{
				"Sid": "`+sid+`",
				"Effect": "Allow",
				"Principal": {"Service": "events.amazonaws.com"},
				"Action": "sqs:SendMessage",
				"Resource": "`+queueARN+`",
				"Condition": {"ArnEquals": {"aws:SourceArn": "`+ruleARN+`"}}
			}]
		}`)
	}
}

// What EventBridge cannot deliver to the queue, after its own retries, goes
// to the dead-letter queue rather than being lost, and its purge with it.
func TestTheRuleSendsWhatItCannotDeliverToTheDeadLetterQueue(t *testing.T) {
	target := Target("arn:aws:sqs:us-west-2:123456789012:realtime-events", "arn:aws:sqs:us-west-2:123456789012:realtime-events-dlq")
	if aws.ToString(target.Id) != "realtime-queue" || aws.ToString(target.Arn) != "arn:aws:sqs:us-west-2:123456789012:realtime-events" ||
		target.DeadLetterConfig == nil || aws.ToString(target.DeadLetterConfig.Arn) != "arn:aws:sqs:us-west-2:123456789012:realtime-events-dlq" {
		t.Fatalf("Target() = %+v, dead letters to %+v", target, target.DeadLetterConfig)
	}
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
// a purge again while the first attempt still waits. The longest call is an
// organization's.
func TestTheVisibilityTimeoutOutlastsABatchTheWorkerNeverAnswers(t *testing.T) {
	visibility := time.Duration(visibilityTimeoutSeconds) * time.Second
	longest := max(purge.MemberTimeout, purge.OrganizationTimeout)
	if batch := time.Duration(events.ReceiveBatch) * longest; visibility < batch {
		t.Fatalf("visibility timeout %s < %d messages x %s", visibility, events.ReceiveBatch, longest)
	}
}

// Enforcing signatures, a purge must still verify for as long as it can wait
// on the queue or its dead-letter queue, and then for the week the bus's
// archive keeps it to be replayed (deploy/production/events.tf).
func TestSignaturesOutlastEveryWayAPurgeCanWait(t *testing.T) {
	const archiveRetention = 7 * 24 * time.Hour
	if wait := time.Duration(retentionSeconds)*time.Second + archiveRetention; events.SignatureMaxAge < wait {
		t.Fatalf("realtime accepts signatures up to %s old, but a purge can wait %s", events.SignatureMaxAge, wait)
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
