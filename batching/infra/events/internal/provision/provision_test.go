package provision

import (
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/batching/internal/serviceconfig"
)

func TestEventPatternMatchesAppsAndAuthEvents(t *testing.T) {
	pattern, err := EventPattern()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"source":["oryxa.apps"]`, `"apps.trigger.received.v1"`,
		`"apps.connection.removed.v1"`, `"apps.trigger.unsubscribed.v1"`,
		`"source":["oryxa.auth"]`, `"auth.member.removed.v1"`, `"auth.organization.deleted.v1"`,
	} {
		if !strings.Contains(pattern, want) {
			t.Fatalf("pattern %s does not contain %s", pattern, want)
		}
	}
}

func TestQueuePolicyScopesRuleAndAccount(t *testing.T) {
	policy, err := QueuePolicy("arn:aws:sqs:ap-south-1:123456789012:batching-events",
		"arn:aws:events:ap-south-1:123456789012:rule/batching-events", "AllowBatchingRule")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"events.amazonaws.com", "aws:SourceArn", "aws:SourceAccount", "123456789012"} {
		if !strings.Contains(policy, want) {
			t.Fatalf("policy %s does not contain %s", policy, want)
		}
	}
}

func TestQueuePolicyRejectsARuleWithoutAnAccount(t *testing.T) {
	if _, err := QueuePolicy("queue", "not-an-arn", "sid"); err == nil {
		t.Fatal("QueuePolicy() error = nil")
	}
}

// TestTheQueueHidesAMessageLongerThanItsHandlingCanTake: a message is
// handled within one classification, at most serviceconfig.MaxJevTimeout,
// and a few statements. Were it visible again sooner, another replica would
// handle it as well.
func TestTheQueueHidesAMessageLongerThanItsHandlingCanTake(t *testing.T) {
	visibility := time.Duration(visibilityTimeoutSeconds) * time.Second
	if headroom := visibility - serviceconfig.MaxJevTimeout; headroom < 30*time.Second {
		t.Fatalf("a %s visibility timeout leaves %s after the longest classification, want at least 30s for the database",
			visibility, headroom)
	}
}
