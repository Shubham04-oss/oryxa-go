package events

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/email/internal/delivery"
	"github.com/Shubham04-oss/oryxa-go/email/internal/mail"
)

// SESSource is the EventBridge source of every SES event. SES publishes them
// to the default event bus only.
const SESSource = "aws.ses"

// sesStatuses maps the SES sending events the email service records, by
// EventBridge detail type, to the status each one means.
var sesStatuses = map[string]delivery.Status{
	"Email Delivered":          delivery.StatusDelivered,
	"Email Delivery Delayed":   delivery.StatusDelayed,
	"Email Bounced":            delivery.StatusBounced,
	"Email Complaint Received": delivery.StatusComplained,
	"Email Rejected":           delivery.StatusRejected,
}

// SESFeedbackDetailTypes is every SES event the email service consumes.
func SESFeedbackDetailTypes() []string {
	return slices.Sorted(maps.Keys(sesStatuses))
}

// sesRecord is an SES event publishing record: the detail of an SES event.
type sesRecord struct {
	Mail struct {
		MessageID   string              `json:"messageId"`
		Destination []string            `json:"destination"`
		Tags        map[string][]string `json:"tags"`
	} `json:"mail"`
	Bounce *struct {
		BounceType        string         `json:"bounceType"`
		BounceSubType     string         `json:"bounceSubType"`
		BouncedRecipients []sesRecipient `json:"bouncedRecipients"`
		Timestamp         time.Time      `json:"timestamp"`
	} `json:"bounce"`
	Complaint *struct {
		ComplainedRecipients  []sesRecipient `json:"complainedRecipients"`
		ComplaintFeedbackType string         `json:"complaintFeedbackType"`
		ComplaintSubType      string         `json:"complaintSubType"`
		Timestamp             time.Time      `json:"timestamp"`
	} `json:"complaint"`
	Delivery *struct {
		Recipients   []string  `json:"recipients"`
		SMTPResponse string    `json:"smtpResponse"`
		Timestamp    time.Time `json:"timestamp"`
	} `json:"delivery"`
	DeliveryDelay *struct {
		DelayType         string         `json:"delayType"`
		DelayedRecipients []sesRecipient `json:"delayedRecipients"`
		Timestamp         time.Time      `json:"timestamp"`
	} `json:"deliveryDelay"`
	Reject *struct {
		Reason string `json:"reason"`
	} `json:"reject"`
}

type sesRecipient struct {
	EmailAddress   string `json:"emailAddress"`
	DiagnosticCode string `json:"diagnosticCode"`
}

func feedbackFromSES(detailType string, detail json.RawMessage) (delivery.Feedback, error) {
	status, ok := sesStatuses[detailType]
	if !ok {
		return delivery.Feedback{}, fmt.Errorf("unhandled SES event %q", detailType)
	}
	var record sesRecord
	if err := json.Unmarshal(detail, &record); err != nil {
		return delivery.Feedback{}, fmt.Errorf("decode SES event: %w", err)
	}

	feedback := delivery.Feedback{
		SESMessageID: record.Mail.MessageID,
		Status:       status,
		Recipients:   record.Mail.Destination,
	}
	if ids := record.Mail.Tags[mail.DeliveryIDTag]; len(ids) > 0 {
		feedback.DeliveryID = ids[0]
	}

	switch status {
	case delivery.StatusDelivered:
		if record.Delivery == nil {
			return delivery.Feedback{}, missing(detailType, "delivery")
		}
		feedback.Recipients = record.Delivery.Recipients
		feedback.Detail = record.Delivery.SMTPResponse
		feedback.OccurredAt = record.Delivery.Timestamp
	case delivery.StatusDelayed:
		if record.DeliveryDelay == nil {
			return delivery.Feedback{}, missing(detailType, "deliveryDelay")
		}
		feedback.Recipients = addresses(record.DeliveryDelay.DelayedRecipients)
		feedback.Detail = record.DeliveryDelay.DelayType
		feedback.OccurredAt = record.DeliveryDelay.Timestamp
	case delivery.StatusBounced:
		bounce := record.Bounce
		if bounce == nil {
			return delivery.Feedback{}, missing(detailType, "bounce")
		}
		feedback.Recipients = addresses(bounce.BouncedRecipients)
		feedback.Detail = describeBounce(bounce.BounceType, bounce.BounceSubType, bounce.BouncedRecipients)
		feedback.OccurredAt = bounce.Timestamp
		// SES only reports a transient bounce once it has stopped retrying,
		// and the address may well work later. A permanent one never will.
		feedback.Suppress = bounce.BounceType == "Permanent"
		feedback.Reason = delivery.ReasonBounce
	case delivery.StatusComplained:
		complaint := record.Complaint
		if complaint == nil {
			return delivery.Feedback{}, missing(detailType, "complaint")
		}
		feedback.Recipients = addresses(complaint.ComplainedRecipients)
		feedback.Detail = firstNonEmpty(complaint.ComplaintFeedbackType, complaint.ComplaintSubType)
		feedback.OccurredAt = complaint.Timestamp
		// "not-spam" is a mailbox provider taking back a spam verdict.
		feedback.Suppress = complaint.ComplaintFeedbackType != "not-spam"
		feedback.Reason = delivery.ReasonComplaint
	case delivery.StatusRejected:
		if record.Reject != nil {
			feedback.Detail = record.Reject.Reason
		}
	}
	return feedback, nil
}

func describeBounce(bounceType, subType string, recipients []sesRecipient) string {
	description := bounceType + "/" + subType
	for _, recipient := range recipients {
		if recipient.DiagnosticCode != "" {
			return description + ": " + recipient.DiagnosticCode
		}
	}
	return description
}

func addresses(recipients []sesRecipient) []string {
	var result []string
	for _, recipient := range recipients {
		if address := strings.TrimSpace(recipient.EmailAddress); address != "" {
			result = append(result, address)
		}
	}
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func missing(detailType, field string) error {
	return fmt.Errorf("SES %q event has no %s object", detailType, field)
}
