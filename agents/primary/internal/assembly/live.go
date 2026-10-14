package assembly

// This file builds the sandbox and browser toolsets (live-layer A6): AWS
// AgentCore, Cloudflare's oryxa-edge, or both with each organization sent to
// the one serviceconfig.LiveOnCloudflare picks for it. Both backends' tools
// have the same names and shapes, so the toolboxes, skills and prompts that
// load them don't change.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentcorebrowser"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentcoresandbox"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/cfbrowser"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/cfsandbox"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/edgeclient"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// organizationStateKey is the session state key an organization's id is in
// (agents.OrganizationIDKey).
const organizationStateKey = "organization_id"

// liveToolsets returns the sandbox and browser toolsets the settings ask
// for. AgentCore's sessions, when built, are closed with krude.
func liveToolsets(ctx context.Context, options Options, krude *Krude, logger *slog.Logger) (sandboxTools, browserTools tool.Toolset, err error) {
	settings := options.Settings
	var edgeSandbox, edgeBrowser tool.Toolset
	if settings.LiveBackend == serviceconfig.LiveBackendCloudflare || len(settings.LiveCloudflareOrgs) > 0 {
		if edgeSandbox, edgeBrowser, err = cloudflareToolsets(options); err != nil {
			return nil, nil, err
		}
		logger.InfoContext(ctx, "Cloudflare sandbox and browser configured", "edge", settings.EdgeURL,
			"backend", settings.LiveBackend, "cloudflare_orgs", len(settings.LiveCloudflareOrgs))
	}
	if settings.LiveBackend == serviceconfig.LiveBackendCloudflare {
		return edgeSandbox, edgeBrowser, nil
	}

	agentcoreSandbox, agentcoreBrowser, err := agentcoreToolsets(options, krude)
	if err != nil {
		return nil, nil, err
	}
	logger.InfoContext(ctx, "AWS AgentCore sandbox and browser configured", "region", settings.AWSRegion)
	if edgeSandbox == nil {
		return agentcoreSandbox, agentcoreBrowser, nil
	}
	return byOrganization{name: "sandbox", agentcore: agentcoreSandbox, cloudflare: edgeSandbox, onCloudflare: settings.LiveOnCloudflare},
		byOrganization{name: "browser", agentcore: agentcoreBrowser, cloudflare: edgeBrowser, onCloudflare: settings.LiveOnCloudflare},
		nil
}

func cloudflareToolsets(options Options) (tool.Toolset, tool.Toolset, error) {
	settings := options.Settings
	if settings.ServiceKeyID == "" {
		return nil, nil, errors.New("the Cloudflare sandbox and browser need AGENTS_PRIMARY_SERVICE_KEY_ID to sign edge tokens")
	}
	client, err := edgeclient.New(edgeclient.Config{
		BaseURL: settings.EdgeURL,
		Tokens:  serviceauth.Signer{Client: kms.NewFromConfig(options.AWS), KeyID: settings.ServiceKeyID, Service: "agents"},
		HTTP:    options.HTTP,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("edge client: %w", err)
	}
	sandbox, err := cfsandbox.New(client)
	if err != nil {
		return nil, nil, err
	}
	sandboxTools, err := cfsandbox.NewToolset(sandbox)
	if err != nil {
		return nil, nil, fmt.Errorf("Cloudflare sandbox tools: %w", err)
	}
	browser, err := cfbrowser.New(client)
	if err != nil {
		return nil, nil, err
	}
	browserTools, err := cfbrowser.NewToolset(browser)
	if err != nil {
		return nil, nil, fmt.Errorf("Cloudflare browser tools: %w", err)
	}
	return sandboxTools, browserTools, nil
}

func agentcoreToolsets(options Options, krude *Krude) (tool.Toolset, tool.Toolset, error) {
	settings := options.Settings
	sandbox, err := agentcoresandbox.New(options.AWS, agentcoresandbox.Options{
		CodeInterpreterIdentifier: settings.SandboxCodeInterpreter,
		SessionTimeout:            settings.SandboxSessionTimeout,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("AgentCore sandbox: %w", err)
	}
	krude.closers = append(krude.closers, func(ctx context.Context) error {
		if err := sandbox.Close(ctx); err != nil {
			return fmt.Errorf("stop AgentCore sandboxes: %w", err)
		}
		return nil
	})
	sandboxTools, err := agentcoresandbox.NewToolset(sandbox)
	if err != nil {
		return nil, nil, fmt.Errorf("AgentCore sandbox tools: %w", err)
	}

	browser, err := agentcorebrowser.New(options.AWS, agentcorebrowser.Options{
		BrowserIdentifier: settings.BrowserIdentifier,
		SessionTimeout:    settings.BrowserSessionTimeout,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("AgentCore browser: %w", err)
	}
	krude.closers = append(krude.closers, func(ctx context.Context) error {
		if err := browser.Close(ctx); err != nil {
			return fmt.Errorf("stop AgentCore browsers: %w", err)
		}
		return nil
	})
	browserTools, err := agentcorebrowser.NewToolset(browser)
	if err != nil {
		return nil, nil, fmt.Errorf("AgentCore browser tools: %w", err)
	}
	return sandboxTools, browserTools, nil
}

// byOrganization serves each call the tools of its session's organization's
// backend. A context with no organization gets AgentCore's, whose tools
// themselves refuse a session with no member to act for.
type byOrganization struct {
	name         string
	agentcore    tool.Toolset
	cloudflare   tool.Toolset
	onCloudflare func(organizationID string) bool
}

func (toolset byOrganization) Name() string { return toolset.name }

func (toolset byOrganization) Tools(ctx agent.ReadonlyContext) ([]tool.Tool, error) {
	if ctx != nil && toolset.onCloudflare(organizationOf(ctx)) {
		return toolset.cloudflare.Tools(ctx)
	}
	return toolset.agentcore.Tools(ctx)
}

func organizationOf(ctx agent.ReadonlyContext) string {
	state := ctx.ReadonlyState()
	if state == nil {
		return ""
	}
	value, err := state.Get(organizationStateKey)
	if err != nil {
		return ""
	}
	id, _ := value.(string)
	return id
}
