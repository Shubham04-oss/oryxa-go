package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/Shubham04-oss/oryxa-go/apps/infra/composio/internal/provision"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	config := provision.FromEnv()
	client, err := composio.New(composio.Config{APIKey: config.APIKey, BaseURL: config.BaseURL})
	if err != nil {
		log.Fatal(err)
	}
	output, err := provision.Run(ctx, client, config)
	if err != nil {
		log.Fatal(err)
	}
	// The secret is in here: it goes to stdout for an operator to read, and
	// nowhere near a log.
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(output); err != nil {
		log.Fatalf("write provision output: %v", err)
	}
}
