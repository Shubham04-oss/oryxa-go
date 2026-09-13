package provision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge/types"

	"github.com/Shubham04-oss/oryxa-go/email/internal/events"
)

const (
	subscriptionsRuleName = "email-subscriptions"
	feedbackRuleName      = "email-ses-feedback"
	targetID              = "email-queue"
)

type rule struct {
	name        string
	bus         string
	description string
	pattern     string
}

// EventPattern matches exactly the subscribed events. Each source keeps its own
// detail types, so one publisher's event name never matches under another's.
func EventPattern(subscriptions []events.Subscription) (string, error) {
	if len(subscriptions) == 0 {
		return "", errors.New("the email service has no event subscriptions")
	}
	detailTypes := map[string][]string{}
	for _, subscription := range subscriptions {
		detailTypes[subscription.Source] = append(detailTypes[subscription.Source], subscription.DetailType)
	}

	var patterns []map[string][]string
	for _, source := range slices.Sorted(maps.Keys(detailTypes)) {
		patterns = append(patterns, map[string][]string{
			"source":      {source},
			"detail-type": slices.Sorted(slices.Values(detailTypes[source])),
		})
	}
	var pattern any = patterns[0]
	if len(patterns) > 1 {
		pattern = map[string]any{"$or": patterns}
	}
	return encodePattern(pattern)
}

// FeedbackPattern matches the SES events the email service consumes, for mail
// sent through its own configuration set only: the default bus carries SES
// events for everything else in the account too.
func FeedbackPattern(configurationSet string) (string, error) {
	return encodePattern(map[string]any{
		"source":      []string{events.SESSource},
		"detail-type": events.SESFeedbackDetailTypes(),
		"detail": map[string]any{
			"mail": map[string]any{
				"tags": map[string][]string{"ses:configuration-set": {configurationSet}},
			},
		},
	})
}

func encodePattern(pattern any) (string, error) {
	encoded, err := json.Marshal(pattern)
	if err != nil {
		return "", fmt.Errorf("encode event pattern: %w", err)
	}
	return string(encoded), nil
}

func ensureRule(ctx context.Context, client *eventbridge.Client, eventRule rule) (string, error) {
	var ruleTags []types.Tag
	for _, key := range sortedTagKeys() {
		ruleTags = append(ruleTags, types.Tag{Key: aws.String(key), Value: aws.String(tags[key])})
	}

	output, err := client.PutRule(ctx, &eventbridge.PutRuleInput{
		Name:         aws.String(eventRule.name),
		Description:  aws.String(eventRule.description),
		EventBusName: aws.String(eventRule.bus),
		EventPattern: aws.String(eventRule.pattern),
		State:        types.RuleStateEnabled,
		Tags:         ruleTags,
	})
	if err != nil {
		return "", fmt.Errorf("put EventBridge rule %s: %w", eventRule.name, err)
	}
	return aws.ToString(output.RuleArn), nil
}

func ensureTarget(ctx context.Context, client *eventbridge.Client, eventRule rule, queueARN string) error {
	output, err := client.PutTargets(ctx, &eventbridge.PutTargetsInput{
		Rule:         aws.String(eventRule.name),
		EventBusName: aws.String(eventRule.bus),
		Targets: []types.Target{{
			Id:  aws.String(targetID),
			Arn: aws.String(queueARN),
		}},
	})
	if err != nil {
		return fmt.Errorf("put EventBridge target for %s: %w", eventRule.name, err)
	}
	if output.FailedEntryCount > 0 {
		entry := output.FailedEntries[0]
		return fmt.Errorf("put EventBridge target for %s failed: %s: %s", eventRule.name, aws.ToString(entry.ErrorCode), aws.ToString(entry.ErrorMessage))
	}
	return nil
}

func busARN(ctx context.Context, client *eventbridge.Client, name string) (string, error) {
	output, err := client.DescribeEventBus(ctx, &eventbridge.DescribeEventBusInput{Name: aws.String(name)})
	if err != nil {
		return "", fmt.Errorf("describe EventBridge bus %s: %w", name, err)
	}
	return aws.ToString(output.Arn), nil
}
