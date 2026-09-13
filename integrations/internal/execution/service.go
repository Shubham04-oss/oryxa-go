package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	integrationevents "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/piece"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhooks"
)

var (
	ErrInvalidEnvelope     = errors.New("invalid webhook execution envelope")
	ErrIsolationMismatch   = errors.New("webhook execution isolation mismatch")
	ErrOutputInvalid       = errors.New("piece trigger output is invalid")
	ErrExecutionInProgress = errors.New("webhook trigger execution is already in progress")
)

type Store interface {
	RunnableTriggers(context.Context, string, string, string, string, string, string, string) ([]domain.Trigger, error)
	ConnectionForExecution(context.Context, string, string) (domain.Connection, error)
	ClaimTriggerExecution(context.Context, string, string, string, string, time.Time, time.Time) (domain.TriggerExecution, bool, error)
	CompleteTriggerExecutionWithEvent(context.Context, domain.TriggerExecution, time.Time, domain.Event) error
	FailTriggerExecution(context.Context, domain.TriggerExecution, time.Time, string) error
}

type RuntimeState interface {
	LoadTriggerState(context.Context, string, string) (map[string]any, int64, error)
	ApplyTriggerState(context.Context, string, string, int64, []piece.StoreMutation) error
}

type SecretBox interface {
	Open(context.Context, secrets.Binding, domain.SealedData) ([]byte, error)
}

type CredentialSource interface {
	ForExecution(context.Context, domain.Connection) ([]byte, error)
}

type Mapper interface {
	Map(string, domain.Trigger, webhooks.Envelope, json.RawMessage, string, time.Time) (domain.Event, error)
}

type Service struct {
	Store          Store
	State          RuntimeState
	Secrets        SecretBox
	Credentials    CredentialSource
	Runner         piece.TriggerRunner
	Mapper         Mapper
	Now            func() time.Time
	IDs            func(string) (string, error)
	PublicURL      string
	StaleAfter     time.Duration
	MaxOutputBytes int
}

func (s Service) ProcessWebhook(ctx context.Context, envelope webhooks.Envelope) error {
	if envelope.InvocationID == "" || envelope.OrganizationID == "" || envelope.ProfileID == "" || envelope.Provider == "" || envelope.PieceName == "" || envelope.PieceVersion == "" || envelope.TriggerName == "" {
		return ErrInvalidEnvelope
	}
	if envelope.ConnectionID == "" && envelope.IdentifierValue == "" {
		return ErrInvalidEnvelope
	}
	triggers, err := s.Store.RunnableTriggers(ctx, envelope.OrganizationID, envelope.ProfileID,
		envelope.PieceName, envelope.PieceVersion, envelope.TriggerName,
		envelope.ConnectionID, envelope.IdentifierValue)
	if err != nil {
		return fmt.Errorf("load runnable triggers: %w", err)
	}
	var failures []error
	for _, trigger := range triggers {
		if err := s.processTrigger(ctx, envelope, trigger); err != nil {
			failures = append(failures, fmt.Errorf("trigger %s: %w", trigger.ID, err))
		}
	}
	return errors.Join(failures...)
}

func (s Service) processTrigger(ctx context.Context, envelope webhooks.Envelope, trigger domain.Trigger) error {
	if trigger.OrganizationID != envelope.OrganizationID || trigger.ProfileID != envelope.ProfileID || trigger.Provider != envelope.Provider ||
		trigger.PieceName != envelope.PieceName || trigger.PieceVersion != envelope.PieceVersion ||
		trigger.TriggerName != envelope.TriggerName || trigger.Status != domain.TriggerStatusActive ||
		(envelope.ConnectionID != "" && trigger.ConnectionID != envelope.ConnectionID) ||
		(envelope.IdentifierValue != "" && trigger.ProviderIdentifier != envelope.IdentifierValue) {
		return ErrIsolationMismatch
	}
	now := s.now()
	executionID, err := s.newID("exe")
	if err != nil {
		return err
	}
	stale := s.StaleAfter
	if stale == 0 {
		stale = 5 * time.Minute
	}
	deliveryID := envelope.DeliveryID
	if deliveryID == "" {
		deliveryID = envelope.InvocationID
	}
	execution, claimed, err := s.Store.ClaimTriggerExecution(ctx, executionID, envelope.OrganizationID,
		trigger.ID, deliveryID, now, now.Add(-stale))
	if err != nil {
		return err
	}
	if !claimed {
		if execution.Status == domain.ExecutionStatusSucceeded {
			return nil
		}
		return ErrExecutionInProgress
	}
	fail := func(code string, cause error) error {
		markErr := s.Store.FailTriggerExecution(ctx, execution, s.now(), code)
		return errors.Join(cause, markErr)
	}
	connection, err := s.Store.ConnectionForExecution(ctx, envelope.OrganizationID, trigger.ConnectionID)
	if err != nil {
		return fail("connection_unavailable", err)
	}
	if connection.ID != trigger.ConnectionID || connection.ProfileID != trigger.ProfileID ||
		connection.Provider != trigger.Provider ||
		connection.OrganizationID != trigger.OrganizationID || connection.Status != domain.ConnectionStatusActive ||
		(envelope.IdentifierValue != "" && connection.Identity.TenantID != envelope.IdentifierValue) {
		return fail("isolation_mismatch", ErrIsolationMismatch)
	}
	if isNativeGraphNotification(trigger, envelope) {
		eventID, err := s.newID("evt")
		if err != nil {
			return fail("id_unavailable", err)
		}
		event, err := s.Mapper.Map(trigger.Provider, trigger, envelope, json.RawMessage(`{}`), eventID, s.now())
		if err != nil {
			return fail("output_mapping_failed", err)
		}
		if err := s.Store.CompleteTriggerExecutionWithEvent(ctx, execution, s.now(), event); err != nil {
			return fail("completion_failed", err)
		}
		return nil
	}
	var auth []byte
	if s.Credentials != nil {
		auth, err = s.Credentials.ForExecution(ctx, connection)
	} else {
		auth, err = s.Secrets.Open(ctx, secrets.Binding{OrganizationID: connection.OrganizationID,
			ResourceType: "connection", ResourceID: connection.ID}, connection.EncryptedCredentials)
	}
	if err != nil {
		return fail("credential_unavailable", err)
	}
	defer wipe(auth)
	if !json.Valid(auth) {
		return fail("credential_invalid", errors.New("decrypted connection credential is invalid"))
	}
	state, stateVersion, err := s.State.LoadTriggerState(ctx, trigger.OrganizationID, trigger.ID)
	if err != nil {
		return fail("state_unavailable", err)
	}
	result, err := s.Runner.RunTrigger(ctx, piece.TriggerInvocation{
		InvocationID: execution.ID, OrganizationID: trigger.OrganizationID,
		PieceName: trigger.PieceName, PieceVersion: trigger.PieceVersion,
		Operation: "trigger.run", TriggerName: trigger.TriggerName,
		Auth: json.RawMessage(auth), Props: trigger.Props, PublicURL: s.PublicURL,
		Payload: envelope, InitialStore: state,
	})
	if err != nil {
		return fail("piece_failed", err)
	}
	limit := s.MaxOutputBytes
	if limit == 0 {
		limit = 1024 * 1024
	}
	if len(result.Output) > limit || !json.Valid(result.Output) {
		return fail("output_invalid", ErrOutputInvalid)
	}
	if err := s.State.ApplyTriggerState(ctx, trigger.OrganizationID, trigger.ID, stateVersion, result.StoreMutations); err != nil {
		return fail("state_conflict", err)
	}
	eventID, err := s.newID("evt")
	if err != nil {
		return fail("id_unavailable", err)
	}
	event, err := s.Mapper.Map(trigger.Provider, trigger, envelope, result.Output, eventID, s.now())
	if err != nil {
		return fail("output_mapping_failed", err)
	}
	if err := s.Store.CompleteTriggerExecutionWithEvent(ctx, execution, s.now(), event); err != nil {
		return fail("completion_failed", err)
	}
	return nil
}

type DomainMapper struct{}

func (DomainMapper) Map(provider string, trigger domain.Trigger, envelope webhooks.Envelope, output json.RawMessage, eventID string, now time.Time) (domain.Event, error) {
	if provider == "microsoft" && trigger.TriggerName == "new-email" && envelope.Event == "change-notification" && len(output) == 2 && string(output) == "{}" {
		return domain.Event{EventID: eventID, Source: integrationevents.Source,
			DetailType: integrationevents.MicrosoftOutlookEmailReceivedDetailType, Version: 1,
			OrganizationID: trigger.OrganizationID, OccurredAt: now,
			Data: integrationevents.MicrosoftOutlookEmailReceived{TriggerID: trigger.ID,
				ConnectionID: trigger.ConnectionID, DeliveryID: envelope.DeliveryID, ReceivedAt: envelope.ReceivedAt}}, nil
	}
	if provider == "microsoft" && trigger.TriggerName == "calendar-event-changed" && envelope.Event == "change-notification" && len(output) == 2 && string(output) == "{}" {
		return domain.Event{EventID: eventID, Source: integrationevents.Source,
			DetailType: integrationevents.MicrosoftCalendarEventChangedDetailType, Version: 1,
			OrganizationID: trigger.OrganizationID, OccurredAt: now,
			Data: integrationevents.MicrosoftCalendarEventChanged{TriggerID: trigger.ID,
				ConnectionID: trigger.ConnectionID, DeliveryID: envelope.DeliveryID, ReceivedAt: envelope.ReceivedAt}}, nil
	}
	if provider != "slack" || trigger.TriggerName != "new-message" {
		return domain.Event{}, fmt.Errorf("%w: unsupported trigger mapping", ErrOutputInvalid)
	}
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

func isNativeGraphNotification(trigger domain.Trigger, envelope webhooks.Envelope) bool {
	return trigger.Provider == "microsoft" && envelope.Event == "change-notification" &&
		(trigger.TriggerName == "new-email" || trigger.TriggerName == "calendar-event-changed")
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func (s Service) newID(prefix string) (string, error) {
	if s.IDs == nil {
		return "", errors.New("ID generator is required")
	}
	return s.IDs(prefix)
}
func wipe(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
