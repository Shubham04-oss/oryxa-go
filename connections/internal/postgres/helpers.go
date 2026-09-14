package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

func (store *Store) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

func insertEvents(ctx context.Context, tx pgx.Tx, events ...domain.Event) error {
	for _, event := range events {
		if event.ID == "" || event.DetailType == "" || event.OccurredAt.IsZero() {
			return errors.New("event id, detail type and occurrence time are required")
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO connections_outbox (id, detail_type, detail, occurred_at)
			VALUES ($1, $2, $3, $4)`, event.ID, event.DetailType, []byte(event.Data), event.OccurredAt); err != nil {
			return fmt.Errorf("insert outbox event: %w", err)
		}
	}
	return nil
}

func mapWriteError(operation string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			if pgErr.ConstraintName == "connections_live_account_unique" {
				return domain.ErrAlreadyConnected
			}
			return fmt.Errorf("%s: %w", operation, domain.ErrConflict)
		case "23503":
			return fmt.Errorf("%s: %w", operation, domain.ErrNotFound)
		case "23514":
			return fmt.Errorf("%s breaks a rule: %w", operation, domain.ErrInvalid)
		}
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func sealedValue(value *domain.SealedData) ([]byte, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode sealed value: %w", err)
	}
	return encoded, nil
}

func sealedFrom(raw []byte) (*domain.SealedData, error) {
	if raw == nil {
		return nil, nil
	}
	var value domain.SealedData
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("decode sealed value: %w", err)
	}
	return &value, nil
}

func settingsValue(settings domain.AppSettings) ([]byte, error) {
	encoded, err := json.Marshal(settings)
	if err != nil {
		return nil, fmt.Errorf("encode app settings: %w", err)
	}
	return encoded, nil
}

func settingsFrom(raw []byte) (domain.AppSettings, error) {
	var settings domain.AppSettings
	if len(raw) == 0 {
		return settings, nil
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return domain.AppSettings{}, fmt.Errorf("decode app settings: %w", err)
	}
	return settings, nil
}

func nullable(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func stringSlice(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
