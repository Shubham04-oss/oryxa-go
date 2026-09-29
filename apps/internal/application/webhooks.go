package application

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

// ErrUnverified means a webhook delivery could not be proven to come from
// Composio, so nothing in it was used.
var ErrUnverified = composio.ErrUnverified

// ReceiveWebhook acts on one delivery from Composio, once. Nothing in the
// body is read until its signature is checked. A delivery about something
// apps does not know — a trigger since removed, an account never connected
// here — is acknowledged and dropped, since Composio would otherwise retry
// it forever.
func (s *Service) ReceiveWebhook(ctx context.Context, header http.Header, body []byte) error {
	if err := composio.VerifyWebhook(s.webhookSecret, header, body, s.now()); err != nil {
		return err
	}
	delivery, err := composio.ParseWebhook(body)
	if err != nil {
		return fmt.Errorf("%w: %v", domain.ErrInvalid, err)
	}
	now := s.now().UTC()

	switch delivery.Type {
	case composio.EventTriggerMessage:
		message, err := delivery.TriggerMessage()
		if err != nil {
			return fmt.Errorf("%w: %v", domain.ErrInvalid, err)
		}
		trigger, err := s.store.TriggerByComposioID(ctx, message.TriggerID)
		if errors.Is(err, domain.ErrNotFound) {
			return s.drop(ctx, delivery, "no live trigger")
		}
		if err != nil {
			return err
		}
		if trigger.Status != domain.TriggerActive {
			return s.drop(ctx, delivery, "trigger not active")
		}
		connection, err := s.store.ConnectionByAccount(ctx, message.ConnectedAccountID)
		if err != nil || connection.ID != trigger.ConnectionID || connection.Oryxa() {
			return s.drop(ctx, delivery, "account does not match the trigger")
		}
		if err := s.allowToolkit(ctx, trigger.OrganizationID, trigger.Toolkit); err != nil {
			if errors.Is(err, domain.ErrForbidden) {
				return s.drop(ctx, delivery, "app no longer offered")
			}
			return err
		}
		received := appsv1.TriggerReceived{
			TriggerID: trigger.ID, ConnectionID: trigger.ConnectionID, OrganizationID: trigger.OrganizationID,
			MemberID: trigger.MemberID, Toolkit: trigger.Toolkit, Trigger: trigger.Slug, ReceivedAt: now,
		}
		if len(delivery.Data) > appsv1.MaxTriggerDataBytes {
			received.DataOmitted = true
		} else {
			received.Data = delivery.Data
		}
		event, err := s.event(appsv1.TriggerReceivedDetailType, received)
		if err != nil {
			return err
		}
		// The delivery, its event and the trigger's own tally are written
		// together: a trigger that says it delivered has published what it
		// delivered, and a redelivery counts once.
		recorded, err := s.store.RecordTriggerDelivery(ctx, delivery.ID, delivery.Type, trigger.ID, now, event)
		if err != nil {
			return err
		}
		if !recorded {
			s.logger.InfoContext(ctx, "webhook already handled", "webhook_id", delivery.ID, "type", delivery.Type)
			return nil
		}
		s.logger.InfoContext(ctx, "app delivered an event", "trigger_id", trigger.ID,
			"toolkit", trigger.Toolkit, "trigger", trigger.Slug, "data_omitted", received.DataOmitted)
		return nil

	case composio.EventConnectedAccountExpired:
		account, err := delivery.ExpiredAccount()
		if err != nil {
			return fmt.Errorf("%w: %v", domain.ErrInvalid, err)
		}
		connection, err := s.store.ConnectionByAccount(ctx, account.ID)
		if errors.Is(err, domain.ErrNotFound) {
			return s.drop(ctx, delivery, "no connection")
		}
		if err != nil {
			return err
		}
		if connection.Oryxa() {
			// Composio holds no account of an Oryxa connection's, so it
			// cannot say one expired.
			return s.drop(ctx, delivery, "not a Composio connection")
		}
		event, err := s.event(appsv1.ConnectionExpiredDetailType, appsv1.ConnectionExpired{
			ConnectionID: connection.ID, OrganizationID: connection.OrganizationID, MemberID: connection.MemberID,
			Toolkit: connection.Toolkit, Reason: truncate(account.StatusReason, maxComposioMessage), ExpiredAt: now,
		})
		if err != nil {
			return err
		}
		// Recording the delivery first keeps a redelivery from expiring a
		// connection its member has since reconnected.
		recorded, err := s.store.RecordWebhook(ctx, delivery.ID, delivery.Type, now)
		if err != nil || !recorded {
			return err
		}
		if _, err := s.store.ExpireConnection(ctx, connection.ID, account.StatusReason, now, event); err != nil {
			return err
		}
		s.logger.InfoContext(ctx, "app connection expired", "connection_id", connection.ID)
		return nil

	case composio.EventTriggerDisabled:
		disabled, err := delivery.DisabledTrigger()
		if err != nil {
			return fmt.Errorf("%w: %v", domain.ErrInvalid, err)
		}
		trigger, err := s.store.TriggerByComposioID(ctx, disabled.ID)
		if errors.Is(err, domain.ErrNotFound) {
			return s.drop(ctx, delivery, "no live trigger")
		}
		if err != nil {
			return err
		}
		event, err := s.event(appsv1.TriggerDisabledDetailType, appsv1.TriggerDisabled{
			TriggerID: trigger.ID, ConnectionID: trigger.ConnectionID, OrganizationID: trigger.OrganizationID,
			MemberID: trigger.MemberID, Toolkit: trigger.Toolkit, Trigger: trigger.Slug,
			Reason: truncate(disabled.DisabledReason, maxComposioMessage), DisabledAt: now,
		})
		if err != nil {
			return err
		}
		recorded, err := s.store.RecordWebhook(ctx, delivery.ID, delivery.Type, now)
		if err != nil || !recorded {
			return err
		}
		_, err = s.store.DisableTrigger(ctx, trigger.ID, disabled.DisabledReason, now, event)
		return err

	default:
		// Composio delivers only what its subscription enables, so a type
		// apps never asked for means Composio added one — worth noticing
		// rather than losing in a debug line, since it may be something a
		// member's apps now report and nothing here acts on.
		s.logger.WarnContext(ctx, "composio delivered an event type apps does not handle",
			"webhook_id", delivery.ID, "type", delivery.Type)
		return s.record(ctx, delivery)
	}
}

// record marks a delivery handled together with the event it became, so a
// redelivery publishes nothing twice.
func (s *Service) record(ctx context.Context, delivery composio.WebhookEvent, events ...domain.Event) error {
	recorded, err := s.store.RecordWebhook(ctx, delivery.ID, delivery.Type, s.now().UTC(), events...)
	if err != nil {
		return err
	}
	if !recorded {
		s.logger.InfoContext(ctx, "webhook already handled", "webhook_id", delivery.ID, "type", delivery.Type)
	}
	return nil
}

// drop acknowledges a delivery apps has nothing to do with.
func (s *Service) drop(ctx context.Context, delivery composio.WebhookEvent, reason string) error {
	s.logger.InfoContext(ctx, "webhook dropped", "webhook_id", delivery.ID, "type", delivery.Type, "reason", reason)
	return s.record(ctx, delivery)
}
