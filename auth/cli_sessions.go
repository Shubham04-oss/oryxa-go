package auth

import (
	"context"
	"log/slog"
	"time"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
)

func cliSessionSweeper(store *postgres.Store, logger *slog.Logger) host.Worker {
	return func(ctx context.Context) {
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for {
			cleanupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := store.PurgeCLISessions(cleanupCtx)
			cancel()
			if err != nil && ctx.Err() == nil {
				logger.ErrorContext(ctx, "purge expired CLI session records failed", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}
}
