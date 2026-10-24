package assembly

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/adkmemory"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/serviceconfig"
)

// memoryBackend is where Krude keeps memory, as settings.MemoryBackend
// says; what it opens is closed with Krude. A nil backend is no memory: the
// default, Memory Bank, without an agent engine to keep it in.
func (krude *Krude) memoryBackend(ctx context.Context, settings serviceconfig.Config, options Options, logger *slog.Logger) (adkmemory.Backend, error) {
	vertex := func() (adkmemory.Backend, error) {
		bank, err := adkmemory.NewMemoryBank(ctx, adkmemory.MemoryBankOptions{
			ProjectID:   settings.GoogleCloudProject,
			Location:    settings.MemoryLocation,
			AgentEngine: settings.MemoryAgentEngine,
			Recalled:    settings.MemoryRecalled,
			Credentials: options.Google,
		})
		if err != nil {
			return nil, fmt.Errorf("memory bank: %w", err)
		}
		krude.closers = append(krude.closers, func(context.Context) error {
			if err := bank.Close(); err != nil {
				return fmt.Errorf("close the memory bank: %w", err)
			}
			return nil
		})
		return bank, nil
	}
	agentCore := func() (adkmemory.Backend, error) {
		store, err := adkmemory.NewAgentCoreMemory(adkmemory.AgentCoreMemoryOptions{
			AWS:      options.AWS,
			MemoryID: settings.MemoryID,
			Recalled: settings.MemoryRecalled,
			Logger:   logger,
		})
		if err != nil {
			return nil, fmt.Errorf("agentcore memory: %w", err)
		}
		return store, nil
	}
	shadowed := func(primary, secondary func() (adkmemory.Backend, error)) (adkmemory.Backend, error) {
		first, err := primary()
		if err != nil {
			return nil, err
		}
		second, err := secondary()
		if err != nil {
			return nil, err
		}
		return adkmemory.Shadow(first, second, logger)
	}

	var (
		backend adkmemory.Backend
		err     error
	)
	switch settings.MemoryBackend {
	case serviceconfig.MemoryBackendAgentCore:
		backend, err = agentCore()
		if err == nil {
			logger.InfoContext(ctx, "memories kept in Bedrock AgentCore Memory", "memory_id", settings.MemoryID, "region", options.AWS.Region)
		}
	case serviceconfig.MemoryBackendVertexShadowAgentCore:
		backend, err = shadowed(vertex, agentCore)
		if err == nil {
			logger.InfoContext(ctx, "memories kept in Vertex AI Memory Bank and also written to Bedrock AgentCore Memory",
				"agent_engine", settings.MemoryAgentEngine, "memory_id", settings.MemoryID)
		}
	case serviceconfig.MemoryBackendAgentCoreShadowVertex:
		backend, err = shadowed(agentCore, vertex)
		if err == nil {
			logger.InfoContext(ctx, "memories kept in Bedrock AgentCore Memory and also written to Vertex AI Memory Bank",
				"memory_id", settings.MemoryID, "agent_engine", settings.MemoryAgentEngine)
		}
	default:
		// MemoryBackendVertex, which an unset setting is: Memory Bank exactly
		// as before there was a choice.
		if settings.MemoryAgentEngine == "" {
			logger.InfoContext(ctx, "Krude remembers nothing between threads; set AGENTS_PRIMARY_MEMORY_AGENT_ENGINE to give it memory")
			return nil, nil
		}
		backend, err = vertex()
		if err == nil {
			logger.InfoContext(ctx, "memories kept in Vertex AI Memory Bank", "agent_engine", settings.MemoryAgentEngine, "location", settings.MemoryLocation)
		}
	}
	if err != nil {
		return nil, err
	}
	return backend, nil
}
