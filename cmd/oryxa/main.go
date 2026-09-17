// Command oryxa runs Oryxa's services in one process.
//
//	oryxa [serve]      serve the services ORYXA_SERVICES names, all by default
//	oryxa migrate      install those services' schemas, then exit
//	oryxa services     list the services this binary carries
//	oryxa healthcheck  ask the process on ORYXA_ADDRESS whether it is serving
//
// Every service reads its own variables, documented in .env.example at the
// repository root. The process itself reads ORYXA_ADDRESS, ORYXA_SERVICES,
// ORYXA_AUTO_MIGRATE, ORYXA_DATABASE_MAX_CONNS, ORYXA_SHUTDOWN_TIMEOUT,
// DATABASE_URL, POSTGRES_REQUIRED_EXTENSIONS, AWS_REGION and EVENT_BUS_NAME.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	command := "serve"
	if len(args) > 0 {
		command = args[0]
	}
	switch command {
	case "serve", "migrate":
	case "services":
		for _, name := range host.Names(definitions) {
			fmt.Fprintln(stdout, name)
		}
		return 0
	case "healthcheck":
		return healthcheck(ctx, os.Getenv("ORYXA_ADDRESS"), stderr)
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	default:
		usage(stderr)
		return 2
	}

	telemetryShutdown, err := observability.Setup(ctx, observability.Config{ServiceName: "oryxa"})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telemetryShutdown(shutdownContext); err != nil {
			slog.Error("shutdown telemetry", "error", err)
		}
	}()

	config, err := host.ConfigFromEnv(os.Getenv)
	if err == nil {
		if command == "migrate" {
			err = host.Migrate(ctx, config, definitions, slog.Default())
		} else {
			err = host.Run(ctx, config, definitions, slog.Default())
		}
	}
	if err != nil {
		slog.Error("oryxa stopped", "command", command, "error", err)
		return 1
	}
	return 0
}

func usage(writer io.Writer) {
	fmt.Fprintf(writer, `Usage: oryxa [command]

Commands:
  serve        serve the services ORYXA_SERVICES names (the default command)
  migrate      install those services' schemas, then exit
  services     list the services this binary carries: %s
  healthcheck  ask the process on ORYXA_ADDRESS whether it is serving
`, strings.Join(host.Names(definitions), ", "))
}

// healthcheck lets a container without a shell probe the process it runs.
func healthcheck(ctx context.Context, address string, stderr io.Writer) int {
	if strings.TrimSpace(address) == "" {
		address = ":8080"
	}
	hostname, port, err := net.SplitHostPort(address)
	if err != nil {
		fmt.Fprintf(stderr, "ORYXA_ADDRESS %q: %v\n", address, err)
		return 1
	}
	if hostname == "" || net.ParseIP(hostname) != nil && net.ParseIP(hostname).IsUnspecified() {
		hostname = "127.0.0.1"
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(hostname, port)+"/healthz", nil)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		fmt.Fprintf(stderr, "healthz answered %s\n", response.Status)
		return 1
	}
	return 0
}
