package primary

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/answerguard"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/relevance"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
)

// relevanceOptions are the options that give the answer guard the relevance
// judge: a Gemini model, the lite one, asked through a Gen AI client on the
// project, location and credentials the agents themselves use, and with no
// retries, so that a judgment that does not come in time holds the answer
// rather than wait for a second try.
//
// With no Google Cloud project there is no model to ask, so there is no judge,
// and the guard runs without the check whatever a member's settings say; that
// is said once, here. A client that cannot be built is an error, and the
// service does not start on it: the models Krude runs on need the same client,
// and a check that quietly does not run is worse than one that says why.
//
// What the check does with what the judge finds is not decided here: it is the
// mode each member's settings come to (guardpolicy), which the deployment's
// ceiling (AGENTS_PRIMARY_GUARD_RELEVANCE_CAP, shadow unless set) holds to
// watching until it has earned holding.
func relevanceOptions(ctx context.Context, settings serviceconfig.Config, platform host.Platform, logger *slog.Logger) ([]answerguard.Option, error) {
	if settings.GoogleCloudProject == "" {
		logger.Warn("GOOGLE_CLOUD_PROJECT is not set: the answer guard has no model to judge relevance with, so its relevance check does not run")
		return nil, nil
	}
	client, err := agents.GeminiClient(ctx, settings.GoogleCloudProject, settings.GoogleCloudLocation, platform.Google)
	if err != nil {
		return nil, fmt.Errorf("relevance judge client: %w", err)
	}
	judge := relevance.NewGemini(client, settings.ModelLite, relevance.DefaultTimeout)
	logger.Info("the answer guard judges whether an answer carries private material its question did not ask for",
		"model", settings.ModelLite, "timeout", relevance.DefaultTimeout.String(), "ceiling", settings.GuardRelevanceCap)
	return []answerguard.Option{answerguard.WithRelevance(judge)}, nil
}
