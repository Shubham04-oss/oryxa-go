package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
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
			INSERT INTO knowledge_outbox (id, detail_type, detail, occurred_at)
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
			return fmt.Errorf("%s: %w", operation, domain.ErrConflict)
		case "23503":
			return fmt.Errorf("%s: %w", operation, domain.ErrNotFound)
		case "23514":
			return fmt.Errorf("%s breaks a rule: %w", operation, domain.ErrInvalid)
		}
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func nullable(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func text(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// encodeMap stores a string map as jsonb, never as SQL NULL.
func encodeMap(values map[string]string) ([]byte, error) {
	if values == nil {
		values = map[string]string{}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("encode map: %w", err)
	}
	return encoded, nil
}

func decodeMap(raw []byte) (map[string]string, error) {
	values := map[string]string{}
	if len(raw) == 0 {
		return values, nil
	}
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("decode map: %w", err)
	}
	return values, nil
}

// prefixed qualifies each column in a comma-separated list with a table name,
// for a RETURNING clause of an UPDATE that joins another relation.
func prefixed(table, columns string) string {
	fields := strings.Split(columns, ",")
	for index, field := range fields {
		fields[index] = table + "." + strings.TrimSpace(field)
	}
	return strings.Join(fields, ", ")
}
