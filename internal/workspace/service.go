// Package workspace assembles the workspace configuration API in the one
// Oryxa binary. Its applier reaches the existing service APIs as the admin.
package workspace

import (
	"context"
	"fmt"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/httpserver"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/store"
)

const Name = "workspace"

var Definition = host.Definition{Name: Name, URLVariable: "WORKSPACE_URL", Migrate: Migrate, Mount: Mount}

func Migrate(ctx context.Context, platform host.Platform) error {
	if platform.Database == nil {
		return host.ErrDatabaseRequired
	}
	return store.New(platform.Database).Migrate(ctx)
}

func Mount(_ context.Context, platform host.Platform) (host.Service, error) {
	if platform.Database == nil {
		return host.Service{}, host.ErrDatabaseRequired
	}
	apiURL := platform.Getenv("WORKSPACE_API_URL")
	if apiURL == "" {
		apiURL = platform.Getenv("AUTH_URL")
	}
	if apiURL == "" {
		return host.Service{}, fmt.Errorf("WORKSPACE_API_URL or AUTH_URL is required")
	}
	server, err := httpserver.New(store.New(platform.Database), apiURL)
	if err != nil {
		return host.Service{}, err
	}
	return host.Service{
		Handler: server, Routes: server.Patterns(),
		Timeouts: host.Timeouts{Read: 15 * time.Second, Write: 2 * time.Minute},
	}, nil
}
