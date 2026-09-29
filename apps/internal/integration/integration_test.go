package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestIdempotencyKeyTravelsInTheContext(t *testing.T) {
	t.Parallel()
	if key := IdempotencyKey(context.Background()); key != "" {
		t.Fatalf("a read carries the key %q", key)
	}
	ctx := WithIdempotencyKey(context.Background(), "write-0001-retry-safe")
	if key := IdempotencyKey(ctx); key != "write-0001-retry-safe" {
		t.Fatalf("key = %q", key)
	}
}

func TestErrorsAnswerTheirCodes(t *testing.T) {
	t.Parallel()
	var limited *RateLimited
	if err := fmt.Errorf("listing issues: %w", &RateLimited{RetryAfter: 30 * time.Second}); !errors.As(err, &limited) ||
		limited.RetryAfter != 30*time.Second {
		t.Fatalf("a wrapped RateLimited was lost: %v", err)
	}
	var refused *AppError
	if err := fmt.Errorf("creating an issue: %w", &AppError{Message: "Validation Failed"}); !errors.As(err, &refused) ||
		refused.Message != "Validation Failed" {
		t.Fatalf("a wrapped AppError was lost: %v", err)
	}
	for err, want := range map[error]string{
		ErrNeedsReconnect:                       "needs_reconnect",
		ErrNotFound:                             "not_found",
		&RateLimited{}:                          "rate_limited",
		&RateLimited{RetryAfter: time.Minute}:   "rate_limited: retry after 1m0s",
		&AppError{}:                             "app_error",
		&AppError{Message: "Validation Failed"}: "Validation Failed",
	} {
		if err.Error() != want {
			t.Errorf("%#v says %q, want %q", err, err.Error(), want)
		}
	}
}

func TestHookNeverShowsItsSecret(t *testing.T) {
	t.Parallel()
	hook := Hook{URL: "https://api.oryxa.in/v1/apps/hooks/ep_1", Secret: "whsec-do-not-print"}
	var logged bytes.Buffer
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("subscribing", "hook", hook)
	encoded, err := json.Marshal(hook)
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"%v":            fmt.Sprintf("%v", hook),
		"%+v":           fmt.Sprintf("%+v", hook),
		"%#v":           fmt.Sprintf("%#v", hook),
		"inside a %+v":  fmt.Sprintf("%+v", struct{ Hook Hook }{hook}),
		"inside a %#v":  fmt.Sprintf("%#v", struct{ Hook Hook }{hook}),
		"a slog record": logged.String(),
		"JSON":          string(encoded),
	} {
		if strings.Contains(text, hook.Secret) {
			t.Errorf("%s shows the secret: %s", name, text)
		}
		if !strings.Contains(text, "api.oryxa.in") {
			t.Errorf("%s lost the URL: %s", name, text)
		}
	}
}
