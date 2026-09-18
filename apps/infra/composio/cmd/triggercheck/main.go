// Command triggercheck walks Composio's catalog and reports every trigger
// apps could not subscribe a member to, or could not handle. It reads the
// catalog and nothing else: no account is touched and nothing is subscribed.
//
//	(set -a && . ./.env && go run ./apps/infra/composio/cmd/triggercheck)
//
// TRIGGERCHECK_TOOLKITS narrows it to a few apps, comma separated, for a
// quick pass:
//
//	TRIGGERCHECK_TOOLKITS=github,slack,gmail go run ./apps/infra/composio/cmd/triggercheck
//
// It exits non-zero when it finds a trigger apps would refuse, so it can run
// as a check rather than only be read.
package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/Shubham04-oss/oryxa-go/apps/infra/composio/internal/triggercheck"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	config := triggercheck.FromEnv()
	if config.APIKey == "" {
		log.Fatal("COMPOSIO_API_KEY is required")
	}
	client, err := composio.New(composio.Config{APIKey: config.APIKey, BaseURL: config.BaseURL})
	if err != nil {
		log.Fatal(err)
	}
	report, err := triggercheck.Run(ctx, client, config)
	if err != nil {
		log.Fatal(err)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		log.Fatalf("write the report: %v", err)
	}
	if !report.Sound() {
		// Each finding is a trigger some member would be refused, so this is
		// a failure rather than a note.
		os.Exit(1)
	}
}
