package serviceconfig

import (
	"strings"
	"testing"
)

// Unset, memory is Memory Bank exactly as before the setting existed, and an
// AgentCore memory id on its own changes nothing.
func TestMemoryBackendDefaultsToVertex(t *testing.T) {
	for name, values := range map[string]map[string]string{
		"unset":           {},
		"unset, engine":   {"AGENTS_PRIMARY_MEMORY_AGENT_ENGINE": "123"},
		"vertex":          {"AGENTS_PRIMARY_MEMORY_BACKEND": " vertex "},
		"stray memory id": {"AGENTS_PRIMARY_MEMORY_ID": "krude_memory-abc"},
	} {
		config, err := FromEnv(env(values))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if config.MemoryBackend != MemoryBackendVertex || config.MemoryID != "" || config.MemoryAgentEngine != values["AGENTS_PRIMARY_MEMORY_AGENT_ENGINE"] {
			t.Errorf("%s: backend %q, memory id %q, engine %q", name, config.MemoryBackend, config.MemoryID, config.MemoryAgentEngine)
		}
	}
}

func TestMemoryBackendReadsAgentCoreAndBothShadows(t *testing.T) {
	for _, backend := range []string{MemoryBackendAgentCore, MemoryBackendVertexShadowAgentCore, MemoryBackendAgentCoreShadowVertex} {
		config, err := FromEnv(env(map[string]string{
			"AGENTS_PRIMARY_MEMORY_BACKEND":      backend,
			"AGENTS_PRIMARY_MEMORY_ID":           " krude_memory-abc ",
			"AGENTS_PRIMARY_MEMORY_AGENT_ENGINE": "123",
			"AWS_REGION":                         "ap-south-1",
		}))
		if err != nil {
			t.Fatalf("%s: %v", backend, err)
		}
		if config.MemoryBackend != backend || config.MemoryID != "krude_memory-abc" || config.AWSRegion != "ap-south-1" {
			t.Errorf("%s: backend %q, memory id %q, region %q", backend, config.MemoryBackend, config.MemoryID, config.AWSRegion)
		}
	}
	// AgentCore alone needs no Memory Bank engine.
	if _, err := FromEnv(env(map[string]string{"AGENTS_PRIMARY_MEMORY_BACKEND": "agentcore", "AGENTS_PRIMARY_MEMORY_ID": "m"})); err != nil {
		t.Fatalf("agentcore without an engine: %v", err)
	}
}

func TestMemoryBackendRefusesWhatCouldNotWork(t *testing.T) {
	for name, test := range map[string]struct {
		values map[string]string
		says   string
	}{
		"unknown":                   {map[string]string{"AGENTS_PRIMARY_MEMORY_BACKEND": "mem0"}, "AGENTS_PRIMARY_MEMORY_BACKEND must be"},
		"agentcore, no id":          {map[string]string{"AGENTS_PRIMARY_MEMORY_BACKEND": "agentcore"}, "AGENTS_PRIMARY_MEMORY_ID is required"},
		"shadow, no id":             {map[string]string{"AGENTS_PRIMARY_MEMORY_BACKEND": "vertex+agentcore", "AGENTS_PRIMARY_MEMORY_AGENT_ENGINE": "123"}, "AGENTS_PRIMARY_MEMORY_ID is required"},
		"vertex+agentcore, no bank": {map[string]string{"AGENTS_PRIMARY_MEMORY_BACKEND": "vertex+agentcore", "AGENTS_PRIMARY_MEMORY_ID": "m"}, "AGENTS_PRIMARY_MEMORY_AGENT_ENGINE is required"},
		"agentcore+vertex, no bank": {map[string]string{"AGENTS_PRIMARY_MEMORY_BACKEND": "agentcore+vertex", "AGENTS_PRIMARY_MEMORY_ID": "m"}, "AGENTS_PRIMARY_MEMORY_AGENT_ENGINE is required"},
		"wrong case":                {map[string]string{"AGENTS_PRIMARY_MEMORY_BACKEND": "AgentCore", "AGENTS_PRIMARY_MEMORY_ID": "m"}, "AGENTS_PRIMARY_MEMORY_BACKEND must be"},
	} {
		_, err := FromEnv(env(test.values))
		if err == nil || !strings.Contains(err.Error(), test.says) {
			t.Errorf("%s: %v, want an error saying %q", name, err, test.says)
		}
	}
}
