package provision

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	eventbridgetypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

func TestEventPatternMatchesEverythingAgentsConsumes(t *testing.T) {
	pattern, err := EventPattern()
	if err != nil {
		t.Fatalf("EventPattern() error = %v", err)
	}
	assertJSONEqual(t, pattern, `{
		"$or": [
			{
				"source": ["oryxa.agents"],
				"detail-type": ["agents.interaction.completed.v1", "agents.interaction.decided.v1", "agents.interaction.requested.v1", "agents.teamspace.entry.published.v1"]
			},
			{
				"source": ["oryxa.auth"],
				"detail-type": ["auth.member.removed.v1", "auth.organization.deleted.v1"]
			},
			{
				"source": ["oryxa.batching"],
				"detail-type": ["batching.trigger-batch.ready.v1"]
			},
			{
				"source": ["oryxa.teams"],
				"detail-type": ["teams.member.removed.v1", "teams.team.deleted.v1"]
			}
		]
	}`)
}

func TestQueuePolicyAllowsOnlyTheRule(t *testing.T) {
	policy, err := QueuePolicy("arn:aws:sqs:ap-south-1:123456789012:agents-events", "arn:aws:events:ap-south-1:123456789012:rule/agents-events")
	if err != nil {
		t.Fatalf("QueuePolicy() error = %v", err)
	}
	assertJSONEqual(t, policy, `{
		"Version": "2012-10-17",
		"Statement": [{
			"Sid": "AllowAgentsRule",
			"Effect": "Allow",
			"Principal": {"Service": "events.amazonaws.com"},
			"Action": "sqs:SendMessage",
			"Resource": "arn:aws:sqs:ap-south-1:123456789012:agents-events",
			"Condition": {"ArnEquals": {"aws:SourceArn": "arn:aws:events:ap-south-1:123456789012:rule/agents-events"}}
		}]
	}`)
}

func TestFromEnvDefaults(t *testing.T) {
	t.Setenv("AWS_REGION", "")
	t.Setenv("EVENT_BUS_NAME", "")
	t.Setenv("AGENTS_EVENTS_QUEUE_NAME", "")

	if config := FromEnv(); config != (Config{Region: "ap-south-1", EventBusName: "default", QueueName: "agents-events", DeadLetterQueueName: "agents-events-dlq"}) {
		t.Fatalf("FromEnv() = %+v", config)
	}
}

// TestRunCreatesTheQueueDeadLetterQueueAndRule drives Run against fakes and
// checks the shape a real AWS account would end up with: the dead-letter
// queue's retention and encryption, the main queue's redrive policy and
// visibility timeout, and a rule whose one target is that queue.
func TestRunCreatesTheQueueDeadLetterQueueAndRule(t *testing.T) {
	eventBridgeClient := newFakeEventBridge()
	sqsClient := newFakeSQS()
	config := Config{Region: "ap-south-1", EventBusName: "default", QueueName: "agents-events", DeadLetterQueueName: "agents-events-dlq"}

	output, err := Run(context.Background(), eventBridgeClient, sqsClient, config)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	deadLetter, ok := sqsClient.queues[config.DeadLetterQueueName]
	if !ok {
		t.Fatalf("dead-letter queue %s was not created", config.DeadLetterQueueName)
	}
	if deadLetter.attributes["MessageRetentionPeriod"] != "1209600" || deadLetter.attributes["SqsManagedSseEnabled"] != "true" {
		t.Fatalf("dead-letter queue attributes = %+v", deadLetter.attributes)
	}

	queue, ok := sqsClient.queues[config.QueueName]
	if !ok {
		t.Fatalf("queue %s was not created", config.QueueName)
	}
	if queue.attributes["SqsManagedSseEnabled"] != "true" || queue.attributes["VisibilityTimeout"] != "60" {
		t.Fatalf("queue attributes = %+v", queue.attributes)
	}
	var redrive struct {
		DeadLetterTargetArn string `json:"deadLetterTargetArn"`
		MaxReceiveCount     int    `json:"maxReceiveCount"`
	}
	if err := json.Unmarshal([]byte(queue.attributes["RedrivePolicy"]), &redrive); err != nil {
		t.Fatalf("decode redrive policy: %v", err)
	}
	if redrive.DeadLetterTargetArn != deadLetter.arn || redrive.MaxReceiveCount != 5 {
		t.Fatalf("redrive policy = %+v, want the dead-letter queue and 5 receives", redrive)
	}
	if !strings.Contains(queue.attributes["Policy"], output.RuleARN) || !strings.Contains(queue.attributes["Policy"], output.QueueARN) {
		t.Fatalf("queue policy = %s, want it naming the rule and the queue", queue.attributes["Policy"])
	}

	rule, ok := eventBridgeClient.rules[ruleName]
	if !ok {
		t.Fatalf("rule %s was not created", ruleName)
	}
	assertJSONEqual(t, rule.pattern, `{"$or":[{"source":["oryxa.agents"],"detail-type":["agents.interaction.completed.v1","agents.interaction.decided.v1","agents.interaction.requested.v1","agents.teamspace.entry.published.v1"]},{"source":["oryxa.auth"],"detail-type":["auth.member.removed.v1","auth.organization.deleted.v1"]},{"source":["oryxa.batching"],"detail-type":["batching.trigger-batch.ready.v1"]},{"source":["oryxa.teams"],"detail-type":["teams.member.removed.v1","teams.team.deleted.v1"]}]}`)
	if rule.state != eventbridgetypes.RuleStateEnabled {
		t.Fatalf("rule state = %s, want ENABLED", rule.state)
	}

	targets, ok := eventBridgeClient.targets[ruleName]
	if !ok || len(targets) != 1 {
		t.Fatalf("targets for %s = %+v, want exactly one", ruleName, targets)
	}
	if aws.ToString(targets[0].Id) != targetID || aws.ToString(targets[0].Arn) != output.QueueARN {
		t.Fatalf("target = %+v, want %s pointed at %s", targets[0], targetID, output.QueueARN)
	}

	if output.Region != config.Region || output.EventBusName != config.EventBusName ||
		output.RuleARN != rule.arn || output.QueueURL != queue.url || output.QueueARN != queue.arn ||
		output.DeadLetterQueueURL != deadLetter.url {
		t.Fatalf("Output = %+v", output)
	}
}

// TestRunConvergesWhenRunAgain re-runs Run against the same fakes, the way an
// operator re-running the provisioner would, and checks it settles rather
// than duplicating or drifting: the queues are created once, their
// attributes are reconciled on the second pass, and both runs report the
// same resources.
func TestRunConvergesWhenRunAgain(t *testing.T) {
	eventBridgeClient := newFakeEventBridge()
	sqsClient := newFakeSQS()
	config := Config{Region: "ap-south-1", EventBusName: "default", QueueName: "agents-events", DeadLetterQueueName: "agents-events-dlq"}

	first, err := Run(context.Background(), eventBridgeClient, sqsClient, config)
	if err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	second, err := Run(context.Background(), eventBridgeClient, sqsClient, config)
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}

	if first != second {
		t.Fatalf("Run() is not idempotent: first = %+v, second = %+v", first, second)
	}
	if sqsClient.createCalls[config.QueueName] != 1 {
		t.Fatalf("queue %s was created %d times, want 1", config.QueueName, sqsClient.createCalls[config.QueueName])
	}
	if sqsClient.createCalls[config.DeadLetterQueueName] != 1 {
		t.Fatalf("dead-letter queue %s was created %d times, want 1", config.DeadLetterQueueName, sqsClient.createCalls[config.DeadLetterQueueName])
	}
	if sqsClient.setAttributesCalls[config.QueueName] == 0 {
		t.Fatal("re-running never reconciled the existing queue's attributes")
	}
	if len(eventBridgeClient.targets[ruleName]) != 1 {
		t.Fatalf("targets for %s = %+v after re-running, want exactly one", ruleName, eventBridgeClient.targets[ruleName])
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

// fakeQueue is one SQS queue as fakeSQS remembers it: enough to answer the
// four calls ensureQueue makes without ever reaching AWS.
type fakeQueue struct {
	url        string
	arn        string
	attributes map[string]string
}

// fakeSQS is an in-memory sqsAPI. It keeps state across calls, so a test can
// run the provisioner twice and check the second run converges instead of
// duplicating or erroring.
type fakeSQS struct {
	queues             map[string]*fakeQueue
	createCalls        map[string]int
	setAttributesCalls map[string]int
}

func newFakeSQS() *fakeSQS {
	return &fakeSQS{
		queues:             map[string]*fakeQueue{},
		createCalls:        map[string]int{},
		setAttributesCalls: map[string]int{},
	}
}

func (fake *fakeSQS) GetQueueUrl(_ context.Context, params *sqs.GetQueueUrlInput, _ ...func(*sqs.Options)) (*sqs.GetQueueUrlOutput, error) {
	queue, ok := fake.queues[aws.ToString(params.QueueName)]
	if !ok {
		return nil, &sqstypes.QueueDoesNotExist{}
	}
	return &sqs.GetQueueUrlOutput{QueueUrl: aws.String(queue.url)}, nil
}

func (fake *fakeSQS) CreateQueue(_ context.Context, params *sqs.CreateQueueInput, _ ...func(*sqs.Options)) (*sqs.CreateQueueOutput, error) {
	name := aws.ToString(params.QueueName)
	if _, exists := fake.queues[name]; exists {
		return nil, fmt.Errorf("fake: queue %s already exists", name)
	}
	fake.createCalls[name]++
	queue := &fakeQueue{
		url:        "https://sqs.ap-south-1.amazonaws.com/000000000000/" + name,
		arn:        "arn:aws:sqs:ap-south-1:000000000000:" + name,
		attributes: maps.Clone(params.Attributes),
	}
	if queue.attributes == nil {
		queue.attributes = map[string]string{}
	}
	fake.queues[name] = queue
	return &sqs.CreateQueueOutput{QueueUrl: aws.String(queue.url)}, nil
}

func (fake *fakeSQS) SetQueueAttributes(_ context.Context, params *sqs.SetQueueAttributesInput, _ ...func(*sqs.Options)) (*sqs.SetQueueAttributesOutput, error) {
	url := aws.ToString(params.QueueUrl)
	for name, queue := range fake.queues {
		if queue.url != url {
			continue
		}
		fake.setAttributesCalls[name]++
		maps.Copy(queue.attributes, params.Attributes)
		return &sqs.SetQueueAttributesOutput{}, nil
	}
	return nil, fmt.Errorf("fake: no queue at %s", url)
}

func (fake *fakeSQS) GetQueueAttributes(_ context.Context, params *sqs.GetQueueAttributesInput, _ ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error) {
	url := aws.ToString(params.QueueUrl)
	for _, queue := range fake.queues {
		if queue.url == url {
			return &sqs.GetQueueAttributesOutput{
				Attributes: map[string]string{string(sqstypes.QueueAttributeNameQueueArn): queue.arn},
			}, nil
		}
	}
	return nil, fmt.Errorf("fake: no queue at %s", url)
}

// fakeRule is one EventBridge rule as fakeEventBridge remembers it.
type fakeRule struct {
	arn     string
	pattern string
	state   eventbridgetypes.RuleState
}

// fakeEventBridge is an in-memory eventBridgeAPI, keeping state across calls
// the same way fakeSQS does.
type fakeEventBridge struct {
	rules   map[string]*fakeRule
	targets map[string][]eventbridgetypes.Target
}

func newFakeEventBridge() *fakeEventBridge {
	return &fakeEventBridge{rules: map[string]*fakeRule{}, targets: map[string][]eventbridgetypes.Target{}}
}

func (fake *fakeEventBridge) PutRule(_ context.Context, params *eventbridge.PutRuleInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutRuleOutput, error) {
	name := aws.ToString(params.Name)
	arn := "arn:aws:events:ap-south-1:000000000000:rule/" + name
	fake.rules[name] = &fakeRule{arn: arn, pattern: aws.ToString(params.EventPattern), state: params.State}
	return &eventbridge.PutRuleOutput{RuleArn: aws.String(arn)}, nil
}

func (fake *fakeEventBridge) PutTargets(_ context.Context, params *eventbridge.PutTargetsInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutTargetsOutput, error) {
	fake.targets[aws.ToString(params.Rule)] = params.Targets
	return &eventbridge.PutTargetsOutput{FailedEntryCount: 0}, nil
}
