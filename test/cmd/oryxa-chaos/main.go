// Command oryxa-chaos is cmd/oryxa with a pprof listener, for chaos runs.
//
// It carries exactly the same services, mounted by the same host, and adds
// one thing: net/http/pprof on ORYXA_PPROF_ADDRESS (loopback only), so a
// chaos run can read goroutine and heap profiles while the process is being
// abused and tell a slow request from a leak. cmd/oryxa deliberately has no
// such listener, so this is a test binary and is never deployed.
//
// Keep definitions in step with cmd/oryxa/services.go.
package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof" // registers on http.DefaultServeMux, served below only
	"os"
	"os/signal"
	"syscall"
	"time"

	primary "github.com/Shubham04-oss/oryxa-go/agents/primary"
	"github.com/Shubham04-oss/oryxa-go/apps"
	"github.com/Shubham04-oss/oryxa-go/auth"
	"github.com/Shubham04-oss/oryxa-go/batching"
	"github.com/Shubham04-oss/oryxa-go/billing"
	"github.com/Shubham04-oss/oryxa-go/email"
	"github.com/Shubham04-oss/oryxa-go/files"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/knowledge"
	"github.com/Shubham04-oss/oryxa-go/rag"
	"github.com/Shubham04-oss/oryxa-go/teams"
)

var definitions = []host.Definition{
	auth.Definition,
	teams.Definition,
	billing.Definition,
	files.Definition,
	knowledge.Definition,
	rag.Definition,
	apps.Definition,
	batching.Definition,
	email.Definition,
	primary.Definition,
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if address := os.Getenv("ORYXA_PPROF_ADDRESS"); address != "" {
		hostname, _, err := net.SplitHostPort(address)
		if err != nil || (hostname != "127.0.0.1" && hostname != "localhost" && hostname != "::1") {
			slog.Error("ORYXA_PPROF_ADDRESS must be a loopback host:port", "address", address)
			os.Exit(2)
		}
		go func() {
			if err := http.ListenAndServe(address, http.DefaultServeMux); err != nil {
				slog.Error("pprof listener stopped", "error", err)
			}
		}()
	}

	telemetryShutdown, err := observability.Setup(ctx, observability.Config{ServiceName: "oryxa"})
	if err != nil {
		slog.Error("telemetry", "error", err)
		os.Exit(1)
	}
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = telemetryShutdown(shutdownContext)
	}()

	config, err := host.ConfigFromEnv(os.Getenv)
	if err == nil {
		err = host.Run(ctx, config, definitions, slog.Default())
	}
	if err != nil {
		slog.Error("oryxa stopped", "command", "serve", "error", err)
		os.Exit(1)
	}
}
