package domain

import (
	"fmt"
	"time"
)

// MaxHold is how long a disconnected connection keeps its credentials for
// services that are still removing remote resources.
const MaxHold = 7 * 24 * time.Hour

// MarkNeedsReconnect records a permanent credential failure. Only an active
// connection moves; one that already needs reconnecting stays as it is, so a
// repeated failure is not a new transition.
func (connection Connection) MarkNeedsReconnect(reason ReconnectReason, at time.Time) (Connection, bool, error) {
	if !reason.Valid() {
		return connection, false, fmt.Errorf("reconnect reason is not allowed: %w", ErrInvalid)
	}
	switch connection.Status {
	case StatusActive:
		connection.Status = StatusNeedsReconnect
		connection.Reason = string(reason)
		connection.StatusChangedAt = at
		connection.UpdatedAt = at
		return connection, true, nil
	case StatusNeedsReconnect:
		return connection, false, nil
	default:
		return connection, false, ErrNotFound
	}
}

// Reconnect replaces a connection's grant in place. The provider account
// cannot change. The connection returns to active with the scopes the new
// grant carries.
func (connection Connection) Reconnect(account ProviderAccount, scopes []string, at time.Time) (Connection, error) {
	if connection.Status == StatusDisconnected {
		return connection, ErrNotFound
	}
	if account.ID == "" {
		return connection, fmt.Errorf("provider account is required: %w", ErrInvalid)
	}
	if account.ID != connection.ProviderAccount.ID {
		return connection, ErrAccountChanged
	}
	if connection.Status != StatusActive {
		connection.Status = StatusActive
		connection.StatusChangedAt = at
	}
	connection.Reason = ""
	if account.Name != "" {
		connection.ProviderAccount.Name = account.Name
	}
	connection.Scopes = NormalizeScopes(scopes)
	connection.UpdatedAt = at
	return connection, nil
}

// Disconnect ends a connection for good and clears its default flag. An
// already disconnected connection stays as it is, so redelivered events are
// harmless.
func (connection Connection) Disconnect(reason DisconnectReason, at time.Time) (Connection, bool, error) {
	if !reason.Valid() {
		return connection, false, fmt.Errorf("disconnect reason is not allowed: %w", ErrInvalid)
	}
	if connection.Status == StatusDisconnected {
		return connection, false, nil
	}
	connection.Status = StatusDisconnected
	connection.Reason = string(reason)
	connection.Default = false
	connection.StatusChangedAt = at
	connection.UpdatedAt = at
	return connection, true, nil
}

// HoldOpen reports whether a disconnected connection still keeps its
// credentials: some service holds it, and MaxHold has not passed.
func HoldOpen(connection Connection, holds int, now time.Time) bool {
	return connection.Status == StatusDisconnected && holds > 0 &&
		now.Before(connection.StatusChangedAt.Add(MaxHold))
}

// Issuable decides whether a token may be issued for a purpose. Cleanup is
// the exception to the usual rules: it still works after a permanent failure,
// while the app is disabled, and after disconnect while a hold is open,
// because remote resources still have to be removed.
func Issuable(connection Connection, app App, purpose Purpose, holdOpen bool) error {
	if !purpose.Valid() {
		return fmt.Errorf("purpose is not allowed: %w", ErrInvalid)
	}
	if app.ID != connection.AppID || app.OrganizationID != connection.OrganizationID || connection.CredentialsDeletedAt != nil {
		return ErrNotFound
	}
	if purpose == PurposeCleanup {
		if connection.Status == StatusDisconnected && !holdOpen {
			return ErrNotFound
		}
		return nil
	}
	switch connection.Status {
	case StatusActive:
	case StatusNeedsReconnect:
		return ErrNeedsReconnect
	default:
		return ErrNotFound
	}
	if app.Status != AppStatusActive {
		return ErrAppDisabled
	}
	return nil
}
