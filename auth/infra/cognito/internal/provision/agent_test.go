package provision

import (
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
)

func TestAgentAppClientName(t *testing.T) {
	if got, want := agentAppClientName("oryxa-users"), "oryxa-users-agents"; got != want {
		t.Fatalf("agentAppClientName() = %q, want %q", got, want)
	}
}

func TestAgentAppClientSettings(t *testing.T) {
	settings := agentAppClientSettings()

	if !slices.Equal(settings.allowedFlows, []types.OAuthFlowType{types.OAuthFlowTypeCode}) {
		t.Fatalf("allowedFlows = %v, want only the code flow", settings.allowedFlows)
	}
	wantScopes := []string{"openid", "oryxa/agent.actions"}
	if !slices.Equal(settings.allowedScopes, wantScopes) {
		t.Fatalf("allowedScopes = %v, want %v", settings.allowedScopes, wantScopes)
	}
	if settings.refreshTokenRotation == nil || settings.refreshTokenRotation.Feature != types.FeatureTypeEnabled {
		t.Fatalf("refreshTokenRotation = %+v, want enabled", settings.refreshTokenRotation)
	}
	// The agent client is confidential and narrower than the web client: no
	// ExplicitAuthFlows (no direct password auth) and no extra attributes.
	if settings.explicitAuthFlows != nil {
		t.Fatalf("explicitAuthFlows = %v, want none for the agent client", settings.explicitAuthFlows)
	}
}

func TestAgentResourceServerIdentifierAndScope(t *testing.T) {
	if agentResourceServerIdentifier != "oryxa" {
		t.Fatalf("agentResourceServerIdentifier = %q, want oryxa", agentResourceServerIdentifier)
	}
	if agentActionsScopeName != "agent.actions" {
		t.Fatalf("agentActionsScopeName = %q, want agent.actions", agentActionsScopeName)
	}
}
