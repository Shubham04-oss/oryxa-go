package primary

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/edgeclient"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/edgedispatch"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// edgeAnswerGrace is how long a stopping process gives dispatched runs'
// answers to reach the edge once the runs themselves have drained.
const edgeAnswerGrace = 10 * time.Second

// mountEdgeDispatch sets up the route where the voice agent in a call hands
// real work over, and the client its answers go back on
// (docs/design/live-layer/03, B5). Without AGENTS_PRIMARY_EDGE_TOKEN_PUBLIC_KEYS
// there is none, and the route answers 404.
func mountEdgeDispatch(settings serviceconfig.Config, platform host.Platform, service *threads.Service,
	members edgedispatch.Members, logger *slog.Logger) (*edgedispatch.Service, *httpserver.EdgeDispatch, error) {
	if settings.EdgeTokenPublicKeys == "" {
		return nil, nil, nil
	}
	keys, err := edgedispatch.ParsePublicKeys(settings.EdgeTokenPublicKeys)
	if err != nil {
		return nil, nil, fmt.Errorf("AGENTS_PRIMARY_EDGE_TOKEN_PUBLIC_KEYS: %w", err)
	}
	tokens, err := edgedispatch.NewVerifier(keys, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("AGENTS_PRIMARY_EDGE_TOKEN_PUBLIC_KEYS: %w", err)
	}
	edge, err := edgeclient.New(edgeclient.Config{
		BaseURL: settings.EdgeURL,
		Tokens:  serviceauth.Signer{Client: kms.NewFromConfig(platform.AWS), KeyID: settings.ServiceKeyID, Service: Name},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("the edge client for dispatched answers: %w", err)
	}
	dispatch := edgedispatch.New(edgedispatch.ForThreads(service), members, edgedispatch.EdgeResults{Edge: edge}, logger)
	logger.Info("the edge's voice agents can dispatch work here", "edge", settings.EdgeURL, "edge_keys", len(keys))
	return dispatch, &httpserver.EdgeDispatch{Tokens: tokens, Dispatcher: dispatch}, nil
}
