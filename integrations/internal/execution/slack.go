package execution

import (
	"encoding/json"
	"fmt"
	"time"

	integrationevents "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/catalog"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhooks"
)

func triggerNameMatchesEnvelope(trigger domain.Trigger, envelope webhooks.Envelope) bool {
	if trigger.TriggerName == envelope.TriggerName {
		return true
	}
	return envelope.Provider == "slack" && envelope.TriggerName == domain.SlackEventsRouteTrigger
}

func pieceTriggerName(trigger domain.Trigger) string {
	if trigger.Provider == "slack" {
		return catalog.SlackPieceTriggerName(trigger.TriggerName)
	}
	return trigger.TriggerName
}

func mapSlackTrigger(trigger domain.Trigger, envelope webhooks.Envelope, output json.RawMessage, eventID string, now time.Time) (domain.Event, error) {
	switch trigger.TriggerName {
	case "new-message", "new-message-in-channel", "new-direct-message", "new-mention",
		"new-mention-in-direct-message", "new-command", "new-command-in-direct-message":
		return mapSlackMessageTrigger(trigger, envelope, output, eventID, now)
	case "new-reaction", "reaction-removed":
		return mapSlackReactionTrigger(trigger, envelope, output, eventID, now)
	case "channel-created":
		return mapSlackChannelCreatedTrigger(trigger, envelope, output, eventID, now)
	case "new-user":
		return mapSlackUserJoinedTrigger(trigger, envelope, output, eventID, now)
	case "new-saved-message":
		return mapSlackStarredTrigger(trigger, envelope, output, eventID, now)
	case "new-team-custom-emoji":
		return mapSlackEmojiTrigger(trigger, envelope, output, eventID, now)
	default:
		return domain.Event{}, fmt.Errorf("%w: unsupported Slack trigger mapping", ErrOutputInvalid)
	}
}

func mapSlackMessageTrigger(trigger domain.Trigger, envelope webhooks.Envelope, output json.RawMessage, eventID string, now time.Time) (domain.Event, error) {
	var records []struct {
		Channel   string `json:"channel"`
		User      string `json:"user"`
		Text      string `json:"text"`
		Timestamp string `json:"ts"`
		ThreadTS  string `json:"thread_ts"`
		Subtype   string `json:"subtype"`
	}
	if err := json.Unmarshal(output, &records); err != nil {
		return domain.Event{}, fmt.Errorf("%w: decode Slack messages", ErrOutputInvalid)
	}
	if len(records) == 0 {
		return domain.Event{}, nil
	}
	messages := make([]integrationevents.SlackMessage, 0, len(records))
	for _, record := range records {
		if record.Channel == "" || record.Timestamp == "" {
			return domain.Event{}, fmt.Errorf("%w: Slack message identity is incomplete", ErrOutputInvalid)
		}
		messages = append(messages, integrationevents.SlackMessage{ChannelID: record.Channel,
			UserID: record.User, Text: record.Text, Timestamp: record.Timestamp,
			ThreadTimestamp: record.ThreadTS, Subtype: record.Subtype})
	}
	return domain.Event{EventID: eventID, Source: integrationevents.Source,
		DetailType: integrationevents.SlackMessageReceivedDetailType, Version: 1,
		OrganizationID: trigger.OrganizationID, OccurredAt: now,
		Data: integrationevents.SlackMessageReceived{TriggerID: trigger.ID,
			ConnectionID: trigger.ConnectionID, WorkspaceID: envelope.IdentifierValue,
			Messages: messages, ReceivedAt: envelope.ReceivedAt}}, nil
}

func mapSlackReactionTrigger(trigger domain.Trigger, envelope webhooks.Envelope, output json.RawMessage, eventID string, now time.Time) (domain.Event, error) {
	var records []struct {
		User     string `json:"user"`
		Reaction string `json:"reaction"`
		Item     struct {
			Channel string `json:"channel"`
			TS      string `json:"ts"`
		} `json:"item"`
	}
	if json.Unmarshal(output, &records) != nil {
		return domain.Event{}, fmt.Errorf("%w: decode Slack reactions", ErrOutputInvalid)
	}
	if len(records) == 0 {
		return domain.Event{}, nil
	}
	record := records[0]
	return domain.Event{EventID: eventID, Source: integrationevents.Source,
		DetailType: integrationevents.SlackReactionReceivedDetailType, Version: 1,
		OrganizationID: trigger.OrganizationID, OccurredAt: now,
		Data: integrationevents.SlackReactionReceived{
			TriggerID: trigger.ID, ConnectionID: trigger.ConnectionID, WorkspaceID: envelope.IdentifierValue,
			ChannelID: record.Item.Channel, UserID: record.User, Reaction: record.Reaction,
			ItemTS: record.Item.TS, Removed: trigger.TriggerName == "reaction-removed",
			ReceivedAt: envelope.ReceivedAt}}, nil
}

func mapSlackChannelCreatedTrigger(trigger domain.Trigger, envelope webhooks.Envelope, output json.RawMessage, eventID string, now time.Time) (domain.Event, error) {
	var records []struct {
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	if json.Unmarshal(output, &records) != nil {
		return domain.Event{}, fmt.Errorf("%w: decode Slack channel", ErrOutputInvalid)
	}
	if len(records) == 0 {
		return domain.Event{}, nil
	}
	if records[0].Channel.ID == "" {
		return domain.Event{}, fmt.Errorf("%w: Slack channel identity is incomplete", ErrOutputInvalid)
	}
	return domain.Event{EventID: eventID, Source: integrationevents.Source,
		DetailType: integrationevents.SlackChannelCreatedReceivedDetailType, Version: 1,
		OrganizationID: trigger.OrganizationID, OccurredAt: now,
		Data: integrationevents.SlackChannelCreatedReceived{TriggerID: trigger.ID,
			ConnectionID: trigger.ConnectionID, WorkspaceID: envelope.IdentifierValue,
			ChannelID: records[0].Channel.ID, ReceivedAt: envelope.ReceivedAt}}, nil
}

func mapSlackUserJoinedTrigger(trigger domain.Trigger, envelope webhooks.Envelope, output json.RawMessage, eventID string, now time.Time) (domain.Event, error) {
	var records []struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(output, &records) != nil {
		return domain.Event{}, fmt.Errorf("%w: decode Slack user", ErrOutputInvalid)
	}
	if len(records) == 0 {
		return domain.Event{}, nil
	}
	if records[0].ID == "" {
		return domain.Event{}, fmt.Errorf("%w: Slack user identity is incomplete", ErrOutputInvalid)
	}
	return domain.Event{EventID: eventID, Source: integrationevents.Source,
		DetailType: integrationevents.SlackUserJoinedDetailType, Version: 1,
		OrganizationID: trigger.OrganizationID, OccurredAt: now,
		Data: integrationevents.SlackUserJoined{TriggerID: trigger.ID, ConnectionID: trigger.ConnectionID,
			WorkspaceID: envelope.IdentifierValue, UserID: records[0].ID, ReceivedAt: envelope.ReceivedAt}}, nil
}

func mapSlackStarredTrigger(trigger domain.Trigger, envelope webhooks.Envelope, output json.RawMessage, eventID string, now time.Time) (domain.Event, error) {
	var records []struct {
		Channel string `json:"channel"`
		Message struct {
			TS string `json:"ts"`
		} `json:"message"`
	}
	if json.Unmarshal(output, &records) != nil {
		return domain.Event{}, fmt.Errorf("%w: decode Slack starred item", ErrOutputInvalid)
	}
	if len(records) == 0 {
		return domain.Event{}, nil
	}
	return domain.Event{EventID: eventID, Source: integrationevents.Source,
		DetailType: integrationevents.SlackItemStarredDetailType, Version: 1,
		OrganizationID: trigger.OrganizationID, OccurredAt: now,
		Data: integrationevents.SlackItemStarred{TriggerID: trigger.ID, ConnectionID: trigger.ConnectionID,
			WorkspaceID: envelope.IdentifierValue, ChannelID: records[0].Channel,
			MessageTS: records[0].Message.TS, ReceivedAt: envelope.ReceivedAt}}, nil
}

func mapSlackEmojiTrigger(trigger domain.Trigger, envelope webhooks.Envelope, output json.RawMessage, eventID string, now time.Time) (domain.Event, error) {
	var records []struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(output, &records) != nil {
		return domain.Event{}, fmt.Errorf("%w: decode Slack emoji", ErrOutputInvalid)
	}
	if len(records) == 0 {
		return domain.Event{}, nil
	}
	if records[0].ID == "" {
		return domain.Event{}, fmt.Errorf("%w: Slack emoji identity is incomplete", ErrOutputInvalid)
	}
	return domain.Event{EventID: eventID, Source: integrationevents.Source,
		DetailType: integrationevents.SlackEmojiChangedDetailType, Version: 1,
		OrganizationID: trigger.OrganizationID, OccurredAt: now,
		Data: integrationevents.SlackEmojiChanged{TriggerID: trigger.ID, ConnectionID: trigger.ConnectionID,
			WorkspaceID: envelope.IdentifierValue, Name: records[0].ID, ReceivedAt: envelope.ReceivedAt}}, nil
}
