package host

import (
	"context"
	"log/slog"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/policy"
)

type workspacePolicyRepository interface {
	policy.Repository
	Available(context.Context) (bool, error)
}

// Runtime policy reads the database even in a split host without WORKSPACE_URL.
// An unavailable database is a boot error; a deliberately absent schema is
// reported explicitly so a service cannot silently turn enforcement off.
func workspacePolicyProvider(ctx context.Context, repository workspacePolicyRepository, logger *slog.Logger) (policy.Provider, error) {
	available, err := repository.Available(ctx)
	if err != nil {
		return nil, err
	}
	if !available {
		logger.WarnContext(ctx, "workspace schema is absent: organization policy enforcement is disabled")
		return policy.Noop{}, nil
	}
	return policy.New(repository), nil
}
