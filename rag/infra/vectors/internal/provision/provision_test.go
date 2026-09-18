package provision

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	fileseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/files/v1"
	knowledgeeventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"
	teamseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/events"
)

// TestEventPatternCoversEverythingTheConsumerHandles is the check that matters
// here. A handler the rule does not deliver to is silent: the index simply
// goes on holding what it should have dropped, and nothing says so.
func TestEventPatternCoversEverythingTheConsumerHandles(t *testing.T) {
	t.Parallel()
	pattern, err := EventPattern()
	if err != nil {
		t.Fatalf("EventPattern: %v", err)
	}
	var decoded struct {
		Or []struct {
			Source     []string `json:"source"`
			DetailType []string `json:"detail-type"`
		} `json:"$or"`
	}
	if err := json.Unmarshal([]byte(pattern), &decoded); err != nil {
		t.Fatalf("decode pattern: %v", err)
	}

	bySource := map[string][]string{}
	for _, clause := range decoded.Or {
		if len(clause.Source) != 1 {
			t.Fatalf("a clause names %d sources, want 1: %+v", len(clause.Source), clause)
		}
		bySource[clause.Source[0]] = clause.DetailType
	}
	for source, want := range map[string][]string{
		autheventsv1.Source:      events.AuthSubscriptions(),
		teamseventsv1.Source:     events.TeamSubscriptions(),
		fileseventsv1.Source:     events.FileSubscriptions(),
		knowledgeeventsv1.Source: events.KnowledgeSubscriptions(),
	} {
		got, present := bySource[source]
		if !present {
			t.Fatalf("the rule does not subscribe to %s at all", source)
		}
		for _, detailType := range want {
			if !slices.Contains(got, detailType) {
				t.Fatalf("the rule does not deliver %s, which the consumer handles", detailType)
			}
		}
		if len(got) != len(want) {
			t.Fatalf("%s: rule delivers %v, consumer handles %v", source, got, want)
		}
	}
}

func TestEventPatternIsStable(t *testing.T) {
	t.Parallel()
	// Detail types are sorted, so re-running the provisioner does not rewrite
	// the rule with the same contents in a different order.
	first, err := EventPattern()
	if err != nil {
		t.Fatalf("EventPattern: %v", err)
	}
	for range 20 {
		again, err := EventPattern()
		if err != nil {
			t.Fatalf("EventPattern: %v", err)
		}
		if again != first {
			t.Fatalf("the pattern changed between runs:\n%s\n%s", first, again)
		}
	}
}

func TestQueuePolicyAdmitsTheRuleAndNothingElse(t *testing.T) {
	t.Parallel()
	const (
		queueARN = "arn:aws:sqs:ap-south-1:1:rag-events"
		ruleARN  = "arn:aws:events:ap-south-1:1:rule/rag-lifecycle"
	)
	policy, err := QueuePolicy(queueARN, ruleARN)
	if err != nil {
		t.Fatalf("QueuePolicy: %v", err)
	}
	var decoded struct {
		Statement []struct {
			Effect    string            `json:"Effect"`
			Principal map[string]string `json:"Principal"`
			Action    string            `json:"Action"`
			Resource  string            `json:"Resource"`
			Condition struct {
				ArnEquals map[string]string `json:"ArnEquals"`
			} `json:"Condition"`
		} `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(policy), &decoded); err != nil {
		t.Fatalf("decode policy: %v", err)
	}
	if len(decoded.Statement) != 1 {
		t.Fatalf("policy has %d statements, want 1", len(decoded.Statement))
	}
	statement := decoded.Statement[0]
	if statement.Effect != "Allow" || statement.Action != "sqs:SendMessage" || statement.Resource != queueARN {
		t.Fatalf("statement = %+v", statement)
	}
	if statement.Principal["Service"] != "events.amazonaws.com" {
		t.Fatalf("principal = %v", statement.Principal)
	}
	// Without the condition any EventBridge rule in any account could write
	// to this queue.
	if statement.Condition.ArnEquals["aws:SourceArn"] != ruleARN {
		t.Fatalf("the policy does not restrict the source rule: %+v", statement.Condition)
	}
}

func TestFromEnvDefaults(t *testing.T) {
	config := FromEnv()
	if config.QueueName != "rag-events" || config.DeadLetterQueueName != "rag-events-dlq" {
		t.Fatalf("queues = %q and %q", config.QueueName, config.DeadLetterQueueName)
	}
	if config.EventBusName != "default" {
		t.Fatalf("bus = %q", config.EventBusName)
	}
	// A deployment that only uses pgvector needs no bucket, so an empty name
	// is a valid configuration rather than an error.
	if config.VectorBucketName != "" {
		t.Fatalf("vector bucket = %q, want empty by default", config.VectorBucketName)
	}
}

func TestFromEnvReadsOverrides(t *testing.T) {
	t.Setenv("RAG_EVENTS_QUEUE_NAME", "rag-events-staging")
	t.Setenv("RAG_VECTOR_BUCKET_NAME", "oryxa-vectors-staging")
	t.Setenv("AWS_REGION", "eu-west-1")
	config := FromEnv()
	if config.QueueName != "rag-events-staging" || config.DeadLetterQueueName != "rag-events-staging-dlq" {
		t.Fatalf("queues = %q and %q", config.QueueName, config.DeadLetterQueueName)
	}
	if config.VectorBucketName != "oryxa-vectors-staging" || config.Region != "eu-west-1" {
		t.Fatalf("config = %+v", config)
	}
}

func TestRuleTagsAreStable(t *testing.T) {
	t.Parallel()
	first := ruleTags()
	for range 20 {
		again := ruleTags()
		if len(again) != len(first) {
			t.Fatalf("tag count changed: %d then %d", len(first), len(again))
		}
		for index := range first {
			if *again[index].Key != *first[index].Key {
				t.Fatal("tag order changed between runs")
			}
		}
	}
	var keys []string
	for _, tag := range first {
		keys = append(keys, *tag.Key)
	}
	if !strings.Contains(strings.Join(keys, ","), "service") {
		t.Fatalf("tags = %v, want the service named", keys)
	}
}
