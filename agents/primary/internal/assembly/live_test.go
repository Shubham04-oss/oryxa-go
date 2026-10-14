package assembly

import (
	"iter"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
)

type namedToolset string

func (toolset namedToolset) Name() string { return string(toolset) }
func (toolset namedToolset) Tools(agent.ReadonlyContext) ([]tool.Tool, error) {
	return []tool.Tool{namedTool(toolset)}, nil
}

type namedTool string

func (t namedTool) Name() string        { return string(t) }
func (t namedTool) Description() string { return "" }
func (t namedTool) IsLongRunning() bool { return false }

type orgState string

func (state orgState) Get(key string) (any, error) {
	if key == organizationStateKey && state != "" {
		return string(state), nil
	}
	return nil, session.ErrStateKeyNotExist
}
func (orgState) All() iter.Seq2[string, any] { return func(func(string, any) bool) {} }

type orgContext struct {
	agent.ReadonlyContext
	state orgState
}

func (ctx orgContext) ReadonlyState() session.ReadonlyState { return ctx.state }

// During the rollout each call gets its own organization's backend, and
// anything unplaced stays on AgentCore.
func TestByOrganizationPicksEachOrganizationsBackend(t *testing.T) {
	toolset := byOrganization{
		name: "sandbox", agentcore: namedToolset("agentcore"), cloudflare: namedToolset("cloudflare"),
		onCloudflare: func(org string) bool { return org == "org_ours" },
	}
	for _, test := range []struct {
		ctx  agent.ReadonlyContext
		want string
	}{
		{orgContext{state: "org_ours"}, "cloudflare"},
		{orgContext{state: "org_other"}, "agentcore"},
		{orgContext{state: ""}, "agentcore"},
		{nil, "agentcore"},
	} {
		tools, err := toolset.Tools(test.ctx)
		if err != nil || len(tools) != 1 || tools[0].Name() != test.want {
			t.Errorf("Tools(%v) = %v, %v; want %s", test.ctx, tools, err, test.want)
		}
	}
	if toolset.Name() != "sandbox" {
		t.Errorf("Name = %q", toolset.Name())
	}
}
