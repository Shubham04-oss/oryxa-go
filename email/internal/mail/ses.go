package mail

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

// DeliveryIDTag is the SES message tag carrying the delivery log's ID. SES
// copies message tags into the events it publishes, which is how feedback
// finds its way back to the delivery it is about.
const DeliveryIDTag = "delivery_id"

type sendEmailAPI interface {
	SendEmail(context.Context, *sesv2.SendEmailInput, ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error)
}

// Email is a rendered message addressed to one recipient.
type Email struct {
	To         string
	DeliveryID string
	Message    Message
}

type SESSender struct {
	client           sendEmailAPI
	from             string
	configurationSet string
}

func NewSESSender(client sendEmailAPI, from, configurationSet string) *SESSender {
	return &SESSender{client: client, from: from, configurationSet: configurationSet}
}

// Send returns the SES message ID.
func (s *SESSender) Send(ctx context.Context, email Email) (string, error) {
	if email.To == "" {
		return "", errors.New("recipient is required")
	}

	tags := []types.MessageTag{{
		Name:  aws.String("template"),
		Value: aws.String(email.Message.Template),
	}}
	if email.DeliveryID != "" {
		tags = append(tags, types.MessageTag{Name: aws.String(DeliveryIDTag), Value: aws.String(email.DeliveryID)})
	}
	input := &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(s.from),
		Destination:      &types.Destination{ToAddresses: []string{email.To}},
		Content: &types.EmailContent{Simple: &types.Message{
			Subject: content(email.Message.Subject),
			Body: &types.Body{
				Html: content(email.Message.HTML),
				Text: content(email.Message.Text),
			},
		}},
		EmailTags: tags,
	}
	if s.configurationSet != "" {
		input.ConfigurationSetName = aws.String(s.configurationSet)
	}

	output, err := s.client.SendEmail(ctx, input)
	if err != nil {
		return "", fmt.Errorf("send %s email: %w", email.Message.Template, err)
	}
	return aws.ToString(output.MessageId), nil
}

func content(value string) *types.Content {
	return &types.Content{Charset: aws.String("UTF-8"), Data: aws.String(value)}
}
