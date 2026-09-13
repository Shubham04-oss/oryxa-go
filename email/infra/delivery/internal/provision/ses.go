package provision

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"

	"github.com/Shubham04-oss/oryxa-go/email/internal/events"
)

const eventDestinationName = "eventbridge"

// sesEventTypes names the SES event type that arrives on EventBridge under
// each detail type the email service consumes.
var sesEventTypes = map[string]types.EventType{
	"Email Bounced":            types.EventTypeBounce,
	"Email Complaint Received": types.EventTypeComplaint,
	"Email Delivered":          types.EventTypeDelivery,
	"Email Delivery Delayed":   types.EventTypeDeliveryDelay,
	"Email Rejected":           types.EventTypeReject,
}

// ensureIdentity returns whether SES may send as identity yet, and for a domain
// the DKIM records that verify it once they are published in DNS.
func ensureIdentity(ctx context.Context, client *sesv2.Client, identity string) (bool, []DNSRecord, error) {
	var verified bool
	var dkim *types.DkimAttributes

	existing, err := client.GetEmailIdentity(ctx, &sesv2.GetEmailIdentityInput{EmailIdentity: aws.String(identity)})
	if err == nil {
		verified = existing.VerifiedForSendingStatus
		dkim = existing.DkimAttributes
	} else {
		var notFound *types.NotFoundException
		if !errors.As(err, &notFound) {
			return false, nil, fmt.Errorf("get SES identity %s: %w", identity, err)
		}
		created, err := client.CreateEmailIdentity(ctx, &sesv2.CreateEmailIdentityInput{
			EmailIdentity: aws.String(identity),
			Tags:          sesTags(),
		})
		if err != nil {
			return false, nil, fmt.Errorf("create SES identity %s: %w", identity, err)
		}
		verified = created.VerifiedForSendingStatus
		dkim = created.DkimAttributes
	}

	var records []DNSRecord
	if dkim != nil {
		for _, token := range dkim.Tokens {
			records = append(records, DNSRecord{
				Type:  "CNAME",
				Name:  token + "._domainkey." + identity,
				Value: token + ".dkim.amazonses.com",
			})
		}
	}
	return verified, records, nil
}

// ensureConfigurationSet has SES itself stop sending to addresses that bounce
// or complain, alongside the service's own suppression list.
func ensureConfigurationSet(ctx context.Context, client *sesv2.Client, name string) error {
	reasons := []types.SuppressionListReason{types.SuppressionListReasonBounce, types.SuppressionListReasonComplaint}
	_, err := client.CreateConfigurationSet(ctx, &sesv2.CreateConfigurationSetInput{
		ConfigurationSetName: aws.String(name),
		SuppressionOptions:   &types.SuppressionOptions{SuppressedReasons: reasons},
		Tags:                 sesTags(),
	})
	if err == nil {
		return nil
	}
	var exists *types.AlreadyExistsException
	if !errors.As(err, &exists) {
		return fmt.Errorf("create SES configuration set %s: %w", name, err)
	}

	_, err = client.PutConfigurationSetSuppressionOptions(ctx, &sesv2.PutConfigurationSetSuppressionOptionsInput{
		ConfigurationSetName: aws.String(name),
		SuppressedReasons:    reasons,
	})
	if err != nil {
		return fmt.Errorf("update SES configuration set %s suppression: %w", name, err)
	}
	return nil
}

func ensureEventDestination(ctx context.Context, client *sesv2.Client, configurationSet, busARN string) error {
	eventTypes, err := feedbackEventTypes()
	if err != nil {
		return err
	}
	destination := &types.EventDestinationDefinition{
		Enabled:                true,
		EventBridgeDestination: &types.EventBridgeDestination{EventBusArn: aws.String(busARN)},
		MatchingEventTypes:     eventTypes,
	}

	_, err = client.CreateConfigurationSetEventDestination(ctx, &sesv2.CreateConfigurationSetEventDestinationInput{
		ConfigurationSetName: aws.String(configurationSet),
		EventDestinationName: aws.String(eventDestinationName),
		EventDestination:     destination,
	})
	if err == nil {
		return nil
	}
	var exists *types.AlreadyExistsException
	if !errors.As(err, &exists) {
		return fmt.Errorf("create SES event destination: %w", err)
	}

	_, err = client.UpdateConfigurationSetEventDestination(ctx, &sesv2.UpdateConfigurationSetEventDestinationInput{
		ConfigurationSetName: aws.String(configurationSet),
		EventDestinationName: aws.String(eventDestinationName),
		EventDestination:     destination,
	})
	if err != nil {
		return fmt.Errorf("update SES event destination: %w", err)
	}
	return nil
}

// feedbackEventTypes is what SES must publish for the consumer to receive
// every feedback event it handles.
func feedbackEventTypes() ([]types.EventType, error) {
	var result []types.EventType
	for _, detailType := range events.SESFeedbackDetailTypes() {
		eventType, ok := sesEventTypes[detailType]
		if !ok {
			return nil, fmt.Errorf("no SES event type publishes %q", detailType)
		}
		result = append(result, eventType)
	}
	return result, nil
}

func sesTags() []types.Tag {
	var result []types.Tag
	for _, key := range sortedTagKeys() {
		result = append(result, types.Tag{Key: aws.String(key), Value: aws.String(tags[key])})
	}
	return result
}
