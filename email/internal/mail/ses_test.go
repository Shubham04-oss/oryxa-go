package mail

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
)

type fakeSES struct {
	input  *sesv2.SendEmailInput
	output *sesv2.SendEmailOutput
	err    error
}

func (f *fakeSES) SendEmail(_ context.Context, input *sesv2.SendEmailInput, _ ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error) {
	f.input = input
	return f.output, f.err
}

func TestSESSenderSendsSimpleContent(t *testing.T) {
	client := &fakeSES{output: &sesv2.SendEmailOutput{MessageId: aws.String("ses-1")}}
	sender := NewSESSender(client, `"Oryxa" <no-reply@example.com>`, "oryxa-email")

	messageID, err := sender.Send(context.Background(), Email{
		To:         "asha@example.com",
		DeliveryID: "delivery-1",
		Message: Message{
			Template: "welcome",
			Subject:  "Welcome to Oryxa",
			HTML:     "<p>Hi Asha,</p>",
			Text:     "Hi Asha,\n",
		},
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if messageID != "ses-1" {
		t.Fatalf("message id = %q, want ses-1", messageID)
	}

	input := client.input
	if got := aws.ToString(input.FromEmailAddress); got != `"Oryxa" <no-reply@example.com>` {
		t.Fatalf("from = %q", got)
	}
	if got := input.Destination.ToAddresses; len(got) != 1 || got[0] != "asha@example.com" {
		t.Fatalf("to = %v, want [asha@example.com]", got)
	}
	if got := aws.ToString(input.ConfigurationSetName); got != "oryxa-email" {
		t.Fatalf("configuration set = %q, want oryxa-email", got)
	}
	simple := input.Content.Simple
	if aws.ToString(simple.Subject.Data) != "Welcome to Oryxa" ||
		aws.ToString(simple.Body.Html.Data) != "<p>Hi Asha,</p>" ||
		aws.ToString(simple.Body.Text.Data) != "Hi Asha,\n" {
		t.Fatalf("unexpected content: %+v", simple)
	}
	if aws.ToString(simple.Subject.Charset) != "UTF-8" {
		t.Fatalf("subject charset = %q, want UTF-8", aws.ToString(simple.Subject.Charset))
	}

	tags := map[string]string{}
	for _, tag := range input.EmailTags {
		tags[aws.ToString(tag.Name)] = aws.ToString(tag.Value)
	}
	if tags["template"] != "welcome" || tags[DeliveryIDTag] != "delivery-1" {
		t.Fatalf("email tags = %v, want template=welcome and %s=delivery-1", tags, DeliveryIDTag)
	}
}

func TestSESSenderOmitsEmptyConfigurationSet(t *testing.T) {
	client := &fakeSES{output: &sesv2.SendEmailOutput{MessageId: aws.String("ses-1")}}
	sender := NewSESSender(client, "no-reply@example.com", "")

	if _, err := sender.Send(context.Background(), Email{To: "asha@example.com", Message: Message{Template: "welcome"}}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if client.input.ConfigurationSetName != nil {
		t.Fatalf("configuration set = %q, want none", aws.ToString(client.input.ConfigurationSetName))
	}
}

func TestSESSenderRequiresRecipient(t *testing.T) {
	sender := NewSESSender(&fakeSES{}, "no-reply@example.com", "")

	if _, err := sender.Send(context.Background(), Email{Message: Message{Template: "welcome"}}); err == nil {
		t.Fatal("Send() error = nil, want missing recipient error")
	}
}

func TestSESSenderReturnsSendError(t *testing.T) {
	sender := NewSESSender(&fakeSES{err: errors.New("throttled")}, "no-reply@example.com", "")

	if _, err := sender.Send(context.Background(), Email{To: "asha@example.com", Message: Message{Template: "welcome"}}); err == nil {
		t.Fatal("Send() error = nil, want SES error")
	}
}
