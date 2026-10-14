package teamspaces

import (
	"strings"
	"testing"
)

// The organization agent has no tools, so its default text must not promise
// delegation or work it cannot do (06-agent-isolation.md, L8).
func TestTheOrganizationAgentPromisesOnlyWhatItCanDo(t *testing.T) {
	agent := defaultOrganizationAgent("org_1", "Acme")
	text := strings.ToLower(agent.Description + " " + agent.Instructions)
	for _, promise := range []string{"delegate", "assign work", "start work for"} {
		if strings.Contains(text, promise) {
			t.Errorf("the default organization agent promises %q: %s", promise, text)
		}
	}
	if !strings.Contains(text, "no tools") {
		t.Errorf("the default organization agent does not say it has no tools: %s", text)
	}
}
